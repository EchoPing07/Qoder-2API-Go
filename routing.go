package main

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"qoder2api/bridge"
	"qoder2api/models"
	"qoder2api/store"
)

// ═══ Request router ═══
//
// The router spreads chat requests across the healthy account pool instead of
// pinning every request to the single "active" account. It is deliberately
// process-local: bindings and cursors are routing cache, not durable state, so
// a restart simply re-balances from scratch (mirroring cli2api's design).
//
// Three strategies:
//   sticky          — keep a conversation on one account until it has served
//                     StickyLimit requests, then rotate (default; avoids
//                     hammering one account into the gateway's rate limiter);
//   round-robin     — rotate every request;
//   least-inflight  — prefer the account with the fewest in-flight requests.

const (
	stickyTTL      = time.Hour
	stickyCapacity = 10_000
	// routerFailureCooldown is how long an account is skipped after an upstream
	// auth failure. Short enough that a transient blip recovers on its own.
	routerFailureCooldown = 5 * time.Minute
)

type binding struct {
	accountID string
	count     int
	expiresAt time.Time
}

// routerDeps is the read-only view of the provider the router needs. Keeping it
// as callbacks avoids a lock-order tangle between the router and bridgeProvider.
type routerDeps struct {
	accounts    func() []store.Account
	snapshot    func(id string) *store.AccountSnapshot
	bridge      func(acc store.Account) *bridge.OpenAiBridge
	config      func() store.AutoSwitch
	markFailure func(accountID string, cooldownUntil int64)
}

type router struct {
	deps routerDeps

	mu     sync.Mutex
	sticky map[string]*binding
	cursor map[string]string // routeKey -> last picked account ID (stable rotation)
	// wrr holds the smooth-weighted-round-robin running weights per route.
	wrr map[string]map[string]int
}

func newRouter(deps routerDeps) *router {
	return &router{
		deps:   deps,
		sticky: make(map[string]*binding),
		cursor: make(map[string]string),
		wrr:    make(map[string]map[string]int),
	}
}

func accountRegion(acc store.Account) string {
	if acc.Region == "global" {
		return "global"
	}
	return "cn"
}

// regionBase returns the accounts allowed by the filter. RegionDefault prefers
// CN accounts and only falls back to every account when the pool has no CN one.
func regionBase(accounts []store.Account, filter models.RegionFilter) []store.Account {
	switch filter {
	case models.RegionCN:
		return filterRegion(accounts, "cn")
	case models.RegionGlobal:
		return filterRegion(accounts, "global")
	case models.RegionAuto:
		return accounts
	default: // RegionDefault
		if cn := filterRegion(accounts, "cn"); len(cn) > 0 {
			return cn
		}
		return accounts
	}
}

func filterRegion(accounts []store.Account, region string) []store.Account {
	out := make([]store.Account, 0, len(accounts))
	for _, acc := range accounts {
		if accountRegion(acc) == region {
			out = append(out, acc)
		}
	}
	return out
}

// usable reports whether acc may receive a new request: not excluded, not
// cooling down, not hard-exhausted, and — when auto-switch is enabled — not
// below the credit threshold. Saturation is checked separately so the router
// can still queue onto a busy account rather than fail.
func (r *router) usable(acc store.Account, exclude map[string]bool, cfg store.AutoSwitch, now time.Time) bool {
	if exclude[acc.ID] {
		return false
	}
	snap := r.deps.snapshot(acc.ID)
	if snap == nil {
		return true // no data: do not exclude on speculation
	}
	if snap.CooldownUntil > now.Unix() {
		return false
	}
	if snap.IsExceeded {
		return false
	}
	if cfg.Enabled && snap.Total > 0 && snap.Remaining <= cfg.Threshold {
		return false
	}
	return true
}

// notSaturated reports whether the account still has a free upstream slot.
func (r *router) notSaturated(acc store.Account) bool {
	b := r.deps.bridge(acc)
	if b == nil {
		return true
	}
	c := b.Capacity()
	if c <= 0 {
		return true
	}
	return b.InFlight() < c
}

// supportsModel is a soft preference: when at least one candidate has a cached
// catalog that knows the model, restrict to those; otherwise leave the set
// alone (the catalog may simply not have been fetched yet).
func (r *router) supportsModel(candidates []store.Account, bareModel string) []store.Account {
	if bareModel == "" {
		return candidates
	}
	supported := make([]store.Account, 0, len(candidates))
	for _, acc := range candidates {
		if b := r.deps.bridge(acc); b != nil {
			if cat := b.CachedCatalog(); cat != nil {
				if cat.GetKey(bareModel) != "" {
					supported = append(supported, acc)
				}
				continue
			}
		}
		supported = append(supported, acc) // unknown catalog: keep it
	}
	if len(supported) == 0 {
		return candidates
	}
	return supported
}

// pick selects an account for one chat request. It returns the chosen account,
// the sticky key (for the success binding) and whether a route exists at all.
func (r *router) pick(apiKeyID, bareModel, seed string, filter models.RegionFilter, exclude map[string]bool) (store.Account, string, bool) {
	accounts := r.deps.accounts()
	if len(accounts) == 0 {
		return store.Account{}, "", false
	}
	cfg := r.deps.config()
	stickyKey := stickyKeyFor(apiKeyID, seed)

	base := regionBase(accounts, filter)
	now := time.Now()

	candidates := make([]store.Account, 0, len(base))
	for _, acc := range base {
		if r.usable(acc, exclude, cfg, now) {
			candidates = append(candidates, acc)
		}
	}
	candidates = r.supportsModel(candidates, bareModel)

	// Free-slot candidates first; if every usable account is saturated, fall
	// back to the usable set and let the per-account chatSlots queue the request
	// instead of rejecting it outright.
	free := make([]store.Account, 0, len(candidates))
	for _, acc := range candidates {
		if r.notSaturated(acc) {
			free = append(free, acc)
		}
	}
	pickFrom := free
	if len(pickFrom) == 0 {
		pickFrom = candidates
	}
	if len(pickFrom) == 0 {
		return store.Account{}, "", false
	}

	// Sticky reuse (skipped for round-robin).
	if cfg.Strategy != store.StrategyRoundRobin {
		r.mu.Lock()
		if b, ok := r.sticky[stickyKey]; ok {
			if now.After(b.expiresAt) {
				delete(r.sticky, stickyKey)
			} else if containsAccount(pickFrom, b.accountID) {
				b.count++
				if cfg.StickyLimit > 0 && b.count > cfg.StickyLimit {
					// Served its quota: rotate to the next account and rebind.
					delete(r.sticky, stickyKey)
				} else {
					acc, _ := accountByID(pickFrom, b.accountID)
					r.mu.Unlock()
					return acc, stickyKey, true
				}
			} else {
				// Bound account escaped (cooled/excluded/saturated): re-route.
				delete(r.sticky, stickyKey)
			}
		}
		r.mu.Unlock()
	}

	acc := r.rotate(pickFrom, filter, bareModel, cfg.Strategy)
	return acc, stickyKey, true
}

// rotate advances the stable ID cursor and returns the next account. Sorting by
// ID and resuming after the last-picked ID keeps rotation stable even as the
// candidate set changes (cooling/exclusion), which an index cursor cannot do.
//
// When the candidates carry differing weights, it uses smooth weighted
// round-robin instead, so a heavier account receives proportionally more
// requests without bursting; equal weights degrade to the plain ID cursor.
func (r *router) rotate(candidates []store.Account, filter models.RegionFilter, bareModel, strategy string) store.Account {
	routeKey := filter.String() + "|" + bareModel

	if strategy == store.StrategyLeastInFlight {
		best := candidates[0]
		bestInFlight := r.inFlight(best)
		for _, acc := range candidates[1:] {
			if n := r.inFlight(acc); n < bestInFlight {
				best, bestInFlight = acc, n
			}
		}
		r.mu.Lock()
		r.cursor[routeKey] = best.ID
		r.mu.Unlock()
		return best
	}

	if acc, ok := r.weightedRotate(routeKey, candidates); ok {
		return acc
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	r.mu.Lock()
	last := r.cursor[routeKey]
	r.mu.Unlock()

	idx := 0
	if last != "" {
		for i, acc := range candidates {
			if acc.ID > last {
				idx = i
				break
			}
			if i == len(candidates)-1 {
				idx = 0 // wrapped around
			}
		}
	}
	chosen := candidates[idx]
	r.mu.Lock()
	r.cursor[routeKey] = chosen.ID
	r.mu.Unlock()
	return chosen
}

// weightedRotate performs one step of smooth weighted round-robin. It returns
// ok=false when every candidate has the same weight (caller falls back to the
// plain ID cursor, which is cheaper and gives identical fairness).
func (r *router) weightedRotate(routeKey string, candidates []store.Account) (store.Account, bool) {
	if len(candidates) == 0 {
		return store.Account{}, false
	}
	total := 0
	uniform := true
	first := store.AccountWeight(candidates[0])
	for _, acc := range candidates {
		w := store.AccountWeight(acc)
		total += w
		if w != first {
			uniform = false
		}
	}
	if uniform {
		// Drop any stale running weights so a later weight change starts clean.
		r.mu.Lock()
		delete(r.wrr, routeKey)
		r.mu.Unlock()
		return store.Account{}, false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.wrr[routeKey]
	if state == nil {
		state = make(map[string]int, len(candidates))
		r.wrr[routeKey] = state
	}
	// Drop entries for accounts that left the candidate set (cooling/excluded),
	// so a returning account does not inherit a huge accumulated weight.
	live := make(map[string]struct{}, len(candidates))
	for _, acc := range candidates {
		live[acc.ID] = struct{}{}
	}
	for id := range state {
		if _, ok := live[id]; !ok {
			delete(state, id)
		}
	}

	best := candidates[0]
	bestWeight := 0
	for _, acc := range candidates {
		w := store.AccountWeight(acc)
		state[acc.ID] += w
		if state[acc.ID] > bestWeight {
			best, bestWeight = acc, state[acc.ID]
		}
	}
	state[best.ID] -= total
	r.cursor[routeKey] = best.ID
	return best, true
}

func (r *router) inFlight(acc store.Account) int {
	if b := r.deps.bridge(acc); b != nil {
		return b.InFlight()
	}
	return 0
}

// bind records that accountID successfully served the sticky key. Only
// successful requests bind, so a failing account is never pinned.
func (r *router) bind(stickyKey, accountID string) {
	if stickyKey == "" || accountID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if b, ok := r.sticky[stickyKey]; ok && now.Before(b.expiresAt) && b.accountID == accountID {
		// Refresh the TTL; count is managed by pick (incremented on reuse).
		b.expiresAt = now.Add(stickyTTL)
		return
	}
	// New binding (first success or a rotation target). Prune when full.
	if len(r.sticky) >= stickyCapacity {
		r.pruneSticky(now)
	}
	r.sticky[stickyKey] = &binding{accountID: accountID, count: 1, expiresAt: now.Add(stickyTTL)}
}

func (r *router) pruneSticky(now time.Time) {
	for k, b := range r.sticky {
		if now.After(b.expiresAt) {
			delete(r.sticky, k)
		}
	}
	// Still full: drop arbitrary entries (oldest TTLs first is overkill here).
	for k := range r.sticky {
		if len(r.sticky) < stickyCapacity {
			break
		}
		delete(r.sticky, k)
	}
}

// markFailure records an upstream auth failure so the account is skipped for a
// short window. The snapshot is memory-only and rides the 30s flush.
func (r *router) markFailure(accountID string) {
	if accountID == "" {
		return
	}
	r.deps.markFailure(accountID, time.Now().Add(routerFailureCooldown).Unix())
}

func stickyKeyFor(apiKeyID, seed string) string {
	if apiKeyID == "" && seed == "" {
		return ""
	}
	h := sha256.Sum256([]byte(apiKeyID + "\x00" + seed))
	return hex.EncodeToString(h[:])
}

func containsAccount(accounts []store.Account, id string) bool {
	_, ok := accountByID(accounts, id)
	return ok
}

func accountByID(accounts []store.Account, id string) (store.Account, bool) {
	for _, acc := range accounts {
		if acc.ID == id {
			return acc, true
		}
	}
	return store.Account{}, false
}
