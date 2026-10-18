// qoder2api: OpenAI-compatible API bridge for QoderWork.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"syscall"

	"qoder2api/admin"
	"qoder2api/auth"
	"qoder2api/bridge"
	"qoder2api/logs"
	"qoder2api/models"
	"qoder2api/stats"
	"qoder2api/store"
	"qoder2api/transform"
	"qoder2api/web"

	"sync"
	"time"
)

// Version is the build version, overridden at build time via
// -ldflags "-X main.Version=v1.2.3". It is only used for display (login page).
var Version = "dev"

// bridgeProvider owns the per-account bridges and routes requests across them.
// It keeps one OpenAiBridge per account (lazily built and invalidated when the
// account's PAT/region changes) so switching accounts no longer discards a
// warmed-up session and model catalog.
type bridgeProvider struct {
	mu        sync.Mutex
	bridges   map[string]*bridge.OpenAiBridge // accountID -> bridge
	bridgeSig map[string]string               // accountID -> "pat|region" the bridge was built for
	store     *store.Store
	router    *router
	// lastPAT tracks the PAT currentBridge last observed, so a PAT change can
	// fire the recorder-clearing hook (the panel's account snapshot belongs to
	// the previous account).
	lastPAT string
	// snapshots are the in-memory quota snapshots maintained by passive
	// deduction and authoritative correction; flushed to disk periodically.
	snapMu    sync.Mutex
	snapshots map[string]*store.AccountSnapshot
	// onPatChange is fired after a different non-empty PAT was replaced or the
	// PAT was cleared. It runs without the provider lock held: the callback
	// takes the recorder lock, and nesting the two risks a lock-order stall.
	onPatChange func()
}

func newBridgeProvider(st *store.Store) *bridgeProvider {
	p := &bridgeProvider{
		store:     st,
		bridges:   map[string]*bridge.OpenAiBridge{},
		bridgeSig: map[string]string{},
		snapshots: map[string]*store.AccountSnapshot{},
	}
	p.router = newRouter(routerDeps{
		accounts:    func() []store.Account { return st.ListAccounts() },
		snapshot:    p.snapshotFor,
		bridge:      p.bridgeForAccount,
		config:      func() store.AutoSwitch { return st.GetAutoSwitch() },
		markFailure: p.markAccountFailure,
	})
	return p
}

// snapshotFor returns the freshest snapshot for an account: the in-memory one
// (updated by passive deduction / authoritative correction) or the persisted
// one when no in-memory entry exists yet.
func (p *bridgeProvider) snapshotFor(id string) *store.AccountSnapshot {
	p.snapMu.Lock()
	snap := p.snapshots[id]
	p.snapMu.Unlock()
	if snap != nil {
		return snap
	}
	if acc, ok := p.store.GetAccount(id); ok {
		return acc.Snapshot
	}
	return nil
}

// markAccountFailure writes a cooldown onto an account's in-memory snapshot so
// the router skips it for a while. Memory-only; flushed by the 30s loop.
func (p *bridgeProvider) markAccountFailure(accountID string, cooldownUntil int64) {
	p.snapMu.Lock()
	snap := p.snapshots[accountID]
	if snap == nil {
		if acc, ok := p.store.GetAccount(accountID); ok && acc.Snapshot != nil {
			cp := *acc.Snapshot
			snap = &cp
		} else {
			snap = &store.AccountSnapshot{}
		}
	}
	snap.CooldownUntil = cooldownUntil
	p.snapshots[accountID] = snap
	p.snapMu.Unlock()
}

// AccountIDByPAT resolves a PAT to its pool entry ID ("" when absent).
func AccountIDByPAT(s *store.Store, pat string) string {
	for _, acc := range s.ListAccounts() {
		if acc.PAT == pat {
			return acc.ID
		}
	}
	return ""
}

// setOnPatChange installs the callback fired when the configured PAT changes.
// Wiring happens before the provider is used by any goroutine.
func (p *bridgeProvider) setOnPatChange(fn func()) {
	p.mu.Lock()
	p.onPatChange = fn
	p.mu.Unlock()
}

// bridgeForAccount returns the bridge for one account, building it on first use
// or rebuilding it when the account's PAT or region changed. Safe for
// concurrent use; never holds the lock across upstream work.
func (p *bridgeProvider) bridgeForAccount(acc store.Account) *bridge.OpenAiBridge {
	sig := acc.PAT + "|" + acc.Region + "|" + acc.Note
	p.mu.Lock()
	defer p.mu.Unlock()
	if b, ok := p.bridges[acc.ID]; ok && p.bridgeSig[acc.ID] == sig {
		return b
	}
	b := bridge.NewOpenAiBridge(acc.PAT, auth.RegionByName(acc.Region))
	b.SetLabel(accountLabel(acc))
	p.bridges[acc.ID] = b
	p.bridgeSig[acc.ID] = sig
	return b
}

// accountLabel renders the account's log label: its note when set, otherwise a
// masked PAT, always tagged with the region.
func accountLabel(acc store.Account) string {
	name := acc.Note
	if name == "" {
		if len(acc.PAT) > 8 {
			name = acc.PAT[:4] + "****" + acc.PAT[len(acc.PAT)-4:]
		} else if acc.PAT != "" {
			name = "pt-****"
		} else {
			name = "账号"
		}
	}
	region := "cn"
	if acc.Region == "global" {
		region = "global"
	}
	return name + "(" + region + ")"
}

// pruneBridges drops bridges for accounts that no longer exist (or whose
// signature changed) so a deleted/replaced account cannot leak a stale session.
func (p *bridgeProvider) pruneBridges(accounts []store.Account) {
	live := make(map[string]struct{}, len(accounts))
	for _, acc := range accounts {
		live[acc.ID] = struct{}{}
	}
	p.mu.Lock()
	for id := range p.bridges {
		if _, ok := live[id]; !ok {
			delete(p.bridges, id)
			delete(p.bridgeSig, id)
		}
	}
	p.mu.Unlock()
}

// resolveBridge validates the API key and routes the request to an account,
// returning that account's bridge plus the key's metadata for request logging.
// Returns nil if the key is invalid or no account can serve the request.
func (p *bridgeProvider) resolveBridge(req bridge.ResolveRequest) *bridge.ResolvedBridge {
	key, ok := p.store.LookupKey(req.APIKey)
	if !ok {
		return nil
	}
	acc, stickyKey, ok := p.router.pick(key.ID, req.Model, req.StickySeed, req.Region, req.Exclude)
	if !ok {
		return nil
	}
	b := p.bridgeForAccount(acc)
	if b == nil {
		return nil
	}
	rb := &bridge.ResolvedBridge{Bridge: b, KeyID: key.ID, KeyNote: key.Note,
		AccountID: acc.ID, AccountNote: acc.Note, Region: auth.RegionByName(acc.Region).Name}
	if stickyKey != "" {
		accountID := acc.ID
		rb.OnSuccess = func() { p.router.bind(stickyKey, accountID) }
	}
	return rb
}

// currentBridge returns the bridge for the active account (used by the account
// loop, model fetcher, and legacy paths). Returns nil when no PAT is set. A PAT
// change fires onPatChange so the panel drops the previous account's snapshot.
func (p *bridgeProvider) currentBridge() *bridge.OpenAiBridge {
	pat := p.store.GetPAT()

	p.mu.Lock()
	changed := p.lastPAT != "" && p.lastPAT != pat
	p.lastPAT = pat
	hook := p.onPatChange
	p.mu.Unlock()
	if changed && hook != nil {
		hook()
	}

	activeID := p.store.GetActiveAccountID()
	if activeID != "" {
		if acc, ok := p.store.GetAccount(activeID); ok && acc.PAT != "" {
			return p.bridgeForAccount(acc)
		}
	}
	// Legacy: a bare PAT with no pool entry.
	if pat == "" {
		return nil
	}
	acc := store.Account{PAT: pat, Region: p.store.AccountRegionByPAT(pat)}
	return p.bridgeForAccount(acc)
}

// accountRefreshCadence keeps the authoritative allowance reasonably fresh
// without coupling the admin panel's 15-second poll to an upstream request.
const accountRefreshCadence = time.Minute

// patWriteMu serializes the recorder writes of the account loop against the
// PAT-change clear. Without it, an in-flight refresh could pass its PAT
// re-check, be preempted by an admin PAT change, and then write the previous
// account's snapshot after the hook had already cleared it. The lock is never
// held across the upstream fetch.
var patWriteMu sync.Mutex

// applyAccountWrite persists one account snapshot under patWriteMu, re-checking
// the PAT inside the lock so a snapshot belonging to a replaced or cleared PAT
// can never be written. It returns false when the snapshot was dropped.
func applyAccountWrite(rec *stats.Recorder, provider *bridgeProvider, pat string, nextResetMs int64, account *stats.Account) bool {
	patWriteMu.Lock()
	defer patWriteMu.Unlock()
	if provider.store.GetPAT() != pat {
		return false // the PAT changed while the fetch was in flight
	}
	rec.SetBillingCycle(nextResetMs)
	rec.SetAccount(account)
	return true
}

// clearAccountOnPatChange drops the cached account snapshot when the configured
// PAT changed. It shares patWriteMu with applyAccountWrite so the clear and the
// writes are mutually exclusive.
func clearAccountOnPatChange(rec *stats.Recorder) {
	patWriteMu.Lock()
	defer patWriteMu.Unlock()
	rec.ClearAccount()
}

// ═══ Account health signals ═══
//
// Routing itself lives in routing.go (the router spreads requests across the
// pool). These helpers keep the per-account snapshots the router reads fresh:
//
//  1. authoritative correction — the account loop fetches each account's quota
//     in rotation; the result is written back into the snapshot and hard
//     exhaustion arms a cooldown until the next reset;
//  2. failure fallback — a chat request dying on an upstream 401/403 before any
//     byte reached the client marks that account cooling and retries on another;
//  3. passive deduction — the usage frame of every billed request carries
//     credits, which are subtracted from the serving account's snapshot. The
//     threshold is enforced by the router (low-credit accounts stop receiving
//     new requests), not by switching the single active account.

// handleChatFailover implements bridge.FailoverFunc: mark the failed account
// cooling, exclude it, and route the request to another account. Returns nil
// when no other account can serve it (the original error surfaces).
func (p *bridgeProvider) handleChatFailover(failedAccountID string, req bridge.ResolveRequest, excluded map[string]bool) *bridge.ResolvedBridge {
	p.router.markFailure(failedAccountID)
	if excluded == nil {
		excluded = map[string]bool{}
	}
	excluded[failedAccountID] = true
	key, ok := p.store.LookupKey(req.APIKey)
	if !ok {
		return nil
	}
	acc, stickyKey, ok := p.router.pick(key.ID, req.Model, req.StickySeed, req.Region, excluded)
	if !ok {
		return nil
	}
	b := p.bridgeForAccount(acc)
	if b == nil {
		return nil
	}
	rb := &bridge.ResolvedBridge{Bridge: b, KeyID: key.ID, KeyNote: key.Note,
		AccountID: acc.ID, AccountNote: acc.Note, Region: auth.RegionByName(acc.Region).Name}
	if stickyKey != "" {
		accountID := acc.ID
		rb.OnSuccess = func() { p.router.bind(stickyKey, accountID) }
	}
	return rb
}

// currentBridge returns the bridge for the active account

// modelsUnion merges the model names from every account's catalog so the admin
// panel (and /v1/models via the default account) reflects models available in
// any region. Falls back to the built-in catalog when no bridge exists.
func (p *bridgeProvider) modelsUnion(ctx context.Context) []string {
	accounts := p.store.ListAccounts()
	if len(accounts) == 0 {
		b := p.currentBridge()
		if b == nil {
			return models.DefaultCatalog().Keys()
		}
		return b.GetCatalog(ctx).Keys()
	}
	seen := map[string]struct{}{}
	var out []string
	for _, acc := range accounts {
		b := p.bridgeForAccount(acc)
		if b == nil {
			continue
		}
		for _, name := range b.GetCatalog(ctx).Keys() {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return models.DefaultCatalog().Keys()
	}
	sort.Strings(out)
	return out
}

// deductCredits passively applies one usage frame to the current account's
// snapshot and evaluates the threshold. Memory-only: the snapshot is flushed
// to disk by the 30s stats loop (flushAccountSnapshots).
func (p *bridgeProvider) deductCredits(accountID string, credits float64) {
	if accountID == "" || credits <= 0 {
		return
	}
	p.snapMu.Lock()
	snap := p.snapshots[accountID]
	if snap == nil {
		// Seed from the persisted snapshot so passive deduction continues
		// where the last check left off.
		for _, acc := range p.store.ListAccounts() {
			if acc.ID == accountID {
				if acc.Snapshot != nil {
					cp := *acc.Snapshot
					snap = &cp
				}
				break
			}
		}
	}
	if snap == nil || snap.Total <= 0 {
		p.snapMu.Unlock()
		return // no authoritative baseline: passive deduction would guess
	}
	snap.Remaining -= credits
	if snap.Remaining < 0 {
		snap.Remaining = 0
	}
	p.snapshots[accountID] = snap
	p.snapMu.Unlock()
	// The router excludes low-credit accounts on its own (see routing.go), so
	// no explicit switch is needed here — the next request simply routes away.
}

// flushAccountSnapshots persists the passively deducted snapshots. Runs on the
// existing 30s stats flush loop; a single save covers every touched account.
func (p *bridgeProvider) flushAccountSnapshots() {
	p.snapMu.Lock()
	if len(p.snapshots) == 0 {
		p.snapMu.Unlock()
		return
	}
	batch := make(map[string]*store.AccountSnapshot, len(p.snapshots))
	for id, snap := range p.snapshots {
		cp := *snap
		batch[id] = &cp
	}
	p.snapMu.Unlock()
	if err := p.store.SaveSnapshots(batch); err != nil {
		log.Printf("[stats] WARN account snapshot flush failed: %v", err)
	}
}

// sumSpendablePools sums the spendable quota pools (personal + add-on +
// available org package) with the same policy as the panel's quota list:
// zero-total pools are absent; the org pool counts only when it is available
// to this member. Org-only accounts (Teams) report a 0/0 userQuota upstream,
// so adopting it verbatim would zero the snapshot. The totals are clamped at
// zero: an over-drawn pool (remaining < 0) must never poison the snapshot.
// NOTE: the cooldown/reset model assumes a single billing cycle; if add-on or
// org pools ever reset on a different cadence than the subscription, that
// assumption needs revisiting.
func sumSpendablePools(st *auth.AccountStatus) (total, remaining float64) {
	if st.UserQuota != nil && st.UserQuota.Total > 0 {
		total += st.UserQuota.Total
		remaining += st.UserQuota.Remaining
	}
	if st.AddOnQuota != nil && st.AddOnQuota.Total > 0 {
		total += st.AddOnQuota.Total
		remaining += st.AddOnQuota.Remaining
	}
	if st.OrgResourcePackage != nil && st.OrgResourcePackage.Cap > 0 && st.OrgResourcePackage.Available {
		total += st.OrgResourcePackage.Cap
		remaining += st.OrgResourcePackage.Remaining
	}
	return total, max(remaining, 0)
}

// applyQuotaToSnapshot merges an authoritative OpenAPI result into the pool
// snapshot of the given account: it repairs drift from passive deduction and
// records hard exhaustion (cooldown until the next reset).
func (p *bridgeProvider) applyQuotaToSnapshot(accountID string, st *auth.AccountStatus) {
	if accountID == "" || st == nil {
		return
	}
	p.snapMu.Lock()
	snap := p.snapshots[accountID]
	if snap == nil {
		snap = &store.AccountSnapshot{}
	}
	snap.Total, snap.Remaining = sumSpendablePools(st)
	snap.Plan = st.UserTag
	snap.IsExceeded = st.IsQuotaExceeded
	snap.NextResetAtMs = st.NextResetAtMs
	snap.CheckedAt = time.Now().Unix()
	snap.LastError = ""
	if st.IsQuotaExceeded {
		if st.NextResetAtMs > time.Now().UnixMilli() {
			snap.CooldownUntil = st.NextResetAtMs / 1000
		} else {
			snap.CooldownUntil = time.Now().Add(routerFailureCooldown).Unix()
		}
	} else {
		snap.CooldownUntil = 0
	}
	p.snapshots[accountID] = snap
	p.snapMu.Unlock()
}

// healthHandler serves an unauthenticated liveness/readiness probe. It is
// deliberately cheap (no upstream calls, no session bootstrap): the body
// carries readiness details (PAT configured, effective stream timeouts)
// while the status stays 200 as long as the process is serving.
func healthHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		timeouts := auth.CurrentStreamTimeouts()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":               "ok",
			"has_pat":              st.GetPAT() != "",
			"chat_timeout_seconds": int(timeouts.Header.Seconds()),
			"idle_timeout_seconds": int(timeouts.Idle.Seconds()),
		})
	}
}

func main() {
	// Initialize store
	dataPath := os.Getenv("QODER_DATA_PATH")
	if dataPath == "" {
		dataPath = "data.json"
	}
	st, err := store.New(dataPath)
	if err != nil {
		log.Fatalf("[store] failed to init: %v", err)
	}

	// Host/port: env vars override config file; config file overrides defaults
	host := os.Getenv("QODER_HOST")
	if host == "" {
		host = st.GetHost()
	}
	port := st.GetPort()
	if envPort := os.Getenv("QODER_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			port = p
		} else {
			log.Printf("[bridge] WARN invalid QODER_PORT=%q; using %d", envPort, port)
		}
	}

	// Chat stream timeouts: env vars override the persisted config, the
	// config file overrides the built-in defaults (same precedence as
	// host/port). Applied to new requests immediately.
	chatTimeout := st.GetChatTimeoutSeconds()
	if v := os.Getenv("QODER_CHAT_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= store.MaxChatTimeoutSeconds {
			chatTimeout = n
		} else {
			log.Printf("[bridge] WARN invalid QODER_CHAT_TIMEOUT_SECONDS=%q; using %d", v, chatTimeout)
		}
	}
	idleTimeout := st.GetIdleTimeoutSeconds()
	if v := os.Getenv("QODER_IDLE_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= store.MaxIdleTimeoutSeconds {
			idleTimeout = n
		} else {
			log.Printf("[bridge] WARN invalid QODER_IDLE_TIMEOUT_SECONDS=%q; using %d", v, idleTimeout)
		}
	}
	auth.SetStreamTimeouts(time.Duration(chatTimeout)*time.Second, time.Duration(idleTimeout)*time.Second)

	// Request-log limits: env vars override the persisted config, which
	// overrides the built-in defaults (same precedence as the timeouts above).
	logRetention := st.GetLogRetentionDays()
	if v := os.Getenv("QODER_LOG_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= store.MaxLogRetentionDays {
			logRetention = n
		} else {
			log.Printf("[logs] WARN invalid QODER_LOG_RETENTION_DAYS=%q; using %d", v, logRetention)
		}
	}
	logMaxEntries := st.GetLogMaxEntries()
	if v := os.Getenv("QODER_LOG_MAX_ENTRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= store.MinLogMaxEntries && n <= store.MaxLogEntriesHardCap {
			logMaxEntries = n
		} else {
			log.Printf("[logs] WARN invalid QODER_LOG_MAX_ENTRIES=%q; using %d", v, logMaxEntries)
		}
	}

	provider := newBridgeProvider(st)
	// Request statistics recorder with periodic persistence (30s cadence).
	// The loop also drains a stop channel so graceful shutdown can trigger a
	// final flush, avoiding loss of the last <=30s of stats.
	rec := stats.NewRecorder(st)
	// Request-log recorder: same persistence model as stats (in-memory on the
	// request path, flushed out of band). Entries are metadata only.
	logRec := logs.NewRecorder(st, logRetention, logMaxEntries)
	// A PAT change invalidates the previous account's plan and allowance; drop
	// the cached snapshot so the panel cannot keep showing it. The clear shares
	// patWriteMu with the account loop's writes.
	provider.setOnPatChange(func() { clearAccountOnPatChange(rec) })
	// The snapshot is persisted, so a restart with a different or cleared PAT
	// would otherwise keep showing the previous account (the hook only fires on
	// an in-process change). Drop it once here; the account loop repopulates the
	// panel from the first successful refresh.
	rec.ClearAccount()
	statsStop := make(chan struct{})
	statsDone := make(chan struct{})
	go func() {
		defer close(statsDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		flushAll := func(final bool) {
			if err := rec.Flush(); err != nil {
				log.Printf("[stats] WARN %sflush failed: %v", finalStr(final), err)
			}
			if err := logRec.Flush(); err != nil {
				log.Printf("[logs] WARN %sflush failed: %v", finalStr(final), err)
			}
			// Passively deducted account snapshots ride the same cadence (a
			// single batched save; a no-op when nothing was touched).
			provider.flushAccountSnapshots()
		}
		for {
			select {
			case <-ticker.C:
				flushAll(false)
			case <-statsStop:
				flushAll(true)
				return
			}
		}
	}()

	// Model fetcher for admin UI: union of every account's catalog so the panel
	// shows models available across all regions/accounts.
	modelFetcher := func(ctx context.Context) []string {
		return provider.modelsUnion(ctx)
	}

	// Subscription account loop: refreshes each account's identity metadata
	// (/user/status) and authoritative allowance (OpenAPI quota/usage) out of
	// band. It probes ONE account per tick in rotation, so N accounts are all
	// refreshed within N minutes while upstream load stays at ~1 call/minute.
	// The stats recorder (panel headline) is only updated when the probed
	// account is the active one.
	//
	// The loop has its own stop/done pair so shutdown can join it before the
	// final stats flush: its last SetAccount/SetBillingCycle would otherwise
	// land after that flush and be lost on exit.
	accountStop := make(chan struct{})
	accountDone := make(chan struct{})

	// applyAccountFor probes one account and folds the result into its pool
	// snapshot. When it is the active account, the panel recorder is updated
	// too. Returns true when a status was obtained.
	applyAccountFor := func(ctx context.Context, acc store.Account, activeID string) bool {
		b := provider.bridgeForAccount(acc)
		if b == nil {
			return false
		}
		st := b.EnsureAccountStatus(ctx)
		if st == nil {
			return false // upstream unreachable: keep the last known state
		}
		// Authoritative correction: merge into that account's snapshot and arm
		// the hard-exhaustion cooldown.
		provider.applyQuotaToSnapshot(acc.ID, st)
		if acc.ID != activeID {
			return true
		}
		account := &stats.Account{
			Plan:                 st.Plan,
			Tag:                  st.UserTag,
			OrgName:              st.OrgName,
			IsQuotaExceeded:      st.IsQuotaExceeded,
			TotalUsagePercentage: st.TotalUsagePercentage,
		}
		if st.UserQuota != nil {
			account.UserQuota = &stats.Quota{
				Total: st.UserQuota.Total, Used: st.UserQuota.Used,
				Remaining: st.UserQuota.Remaining, Percentage: st.UserQuota.Percentage,
				Unit: st.UserQuota.Unit, DetailURL: st.UserQuota.DetailURL,
			}
		}
		if st.AddOnQuota != nil {
			account.AddOnQuota = &stats.Quota{
				Total: st.AddOnQuota.Total, Used: st.AddOnQuota.Used,
				Remaining: st.AddOnQuota.Remaining, Percentage: st.AddOnQuota.Percentage,
				Unit: st.AddOnQuota.Unit, DetailURL: st.AddOnQuota.DetailURL,
			}
		}
		if st.OrgResourcePackage != nil {
			account.OrgResourcePackage = &stats.OrgResourcePackage{
				Used: st.OrgResourcePackage.Used, Cap: st.OrgResourcePackage.Cap,
				Remaining: st.OrgResourcePackage.Remaining, Percentage: st.OrgResourcePackage.Percentage,
				Available: st.OrgResourcePackage.Available, Unit: st.OrgResourcePackage.Unit,
			}
		}
		// The write section runs under patWriteMu and re-checks the PAT inside
		// it: the PAT-change hook clears the recorder under the same lock, so a
		// snapshot fetched for the previous PAT can no longer land after that
		// clear. The fetch above stays outside the lock (it can take 30s).
		applyAccountWrite(rec, provider, provider.store.GetPAT(), st.NextResetAtMs, account)
		return true
	}

	// probeAccounts returns the rotation order: the active account first, then
	// the rest by ID. Empty pool (legacy bare PAT) yields a synthetic account.
	probeAccounts := func() []store.Account {
		accounts := provider.store.ListAccounts()
		if len(accounts) == 0 {
			pat := provider.store.GetPAT()
			if pat == "" {
				return nil
			}
			return []store.Account{{ID: "", PAT: pat, Region: provider.store.AccountRegionByPAT(pat)}}
		}
		activeID := provider.store.GetActiveAccountID()
		out := make([]store.Account, 0, len(accounts))
		for _, acc := range accounts {
			if acc.ID == activeID {
				out = append(out, acc)
			}
		}
		for _, acc := range accounts {
			if acc.ID != activeID {
				out = append(out, acc)
			}
		}
		return out
	}

	go func() {
		defer close(accountDone)
		// Fetch the active account once immediately so the panel is populated
		// from the start rather than after the first tick.
		activeID := provider.store.GetActiveAccountID()
		for _, acc := range probeAccounts() {
			if acc.ID == activeID || activeID == "" {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				applyAccountFor(ctx, acc, activeID)
				cancel()
				break
			}
		}

		ticker := time.NewTicker(accountRefreshCadence)
		defer ticker.Stop()
		cursor := 0
		for {
			select {
			case <-ticker.C:
				list := probeAccounts()
				if len(list) == 0 {
					continue
				}
				provider.pruneBridges(provider.store.ListAccounts())
				acc := list[cursor%len(list)]
				cursor++
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				applyAccountFor(ctx, acc, provider.store.GetActiveAccountID())
				cancel()
			case <-accountStop:
				return
			}
		}
	}()

	// Initialize admin
	adminInst := admin.New(st, modelFetcher, rec, logRec)
	// Per-account in-flight count for the panel: shows how the router spreads
	// load. Reads only the in-memory bridge counters.
	adminInst.SetAccountRuntime(func(accountID string) (int, int) {
		if acc, ok := st.GetAccount(accountID); ok {
			if b := provider.bridgeForAccount(acc); b != nil {
				return b.InFlight(), b.Capacity()
			}
		}
		return 0, 0
	})
	// Account「检测」runs a throwaway bridge through a full session bootstrap
	// and returns the authoritative quota snapshot; the admin layer only owns
	// the endpoint. On-demand only — every check costs a session round-trip.
	// The result also seeds the auto-switch snapshot for that account.
	adminInst.SetAccountChecker(func(ctx context.Context, pat, regionName string) (*store.AccountSnapshot, error) {
		region := auth.RegionByName(regionName)
		b := bridge.NewOpenAiBridge(pat, region)
		acct := b.EnsureAccountStatus(ctx)
		if acct == nil {
			return nil, fmt.Errorf("无法获取账号状态（会话建立失败）")
		}
		snap := &store.AccountSnapshot{
			Plan:          acct.UserTag,
			IsExceeded:    acct.IsQuotaExceeded,
			NextResetAtMs: acct.NextResetAtMs,
			CheckedAt:     time.Now().Unix(),
		}
		// Same spendable-pool summation as applyQuotaToSnapshot (see
		// sumSpendablePools): numbers must stay comparable across accounts and
		// consistent with the stats page.
		snap.Total, snap.Remaining = sumSpendablePools(acct)
		// Seed the pool snapshot so passive deduction and candidate picking
		// have an authoritative baseline even before the loop reaches it.
		if id := AccountIDByPAT(st, pat); id != "" {
			cp := *snap
			provider.snapMu.Lock()
			provider.snapshots[id] = &cp
			provider.snapMu.Unlock()
		}
		return snap, nil
	})

	// WebUI: pre-render all pages at startup (multi-page Alpine.js frontend,
	// no build step, no external dependencies).
	webHandler, err := web.New(Version)
	if err != nil {
		log.Fatalf("[web] failed to init: %v", err)
	}

	mux := http.NewServeMux()
	// failover routes a chat-path auth failure to another account (excluding
	// every account already tried) and returns its bridge for the retry.
	failover := func(failedAccountID string, req bridge.ResolveRequest, excluded map[string]bool) *bridge.ResolvedBridge {
		return provider.handleChatFailover(failedAccountID, req, excluded)
	}
	// usageHook feeds the usage sink with the serving account: billed credits
	// passively deduct that account's snapshot (the router then avoids it once
	// it drops below the threshold).
	usageHook := func(accountID string, u *transform.Usage) {
		if u != nil && u.Billable {
			provider.deductCredits(accountID, u.Credits)
		}
	}
	mux.HandleFunc("/v1/chat/completions", bridge.MakeChatHandler(provider.resolveBridge, rec, logRec, failover, usageHook))
	mux.HandleFunc("/v1/models", bridge.MakeModelsHandler(provider.resolveBridge))
	mux.HandleFunc("/health", healthHandler(st))

	// Root redirect to admin UI
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	// Register admin API routes (/admin/api/*) and WebUI routes
	// (/admin, /admin/<page>, /admin/assets/*)
	adminInst.RegisterRoutes(mux)
	webHandler.Mount(mux)

	addr := host + ":" + strconv.Itoa(port)
	log.Printf("[bridge] listening http://%s/v1/chat/completions", addr)
	log.Printf("[admin]  http://%s/admin", addr)

	// Timeouts: ReadHeaderTimeout/IdleTimeout harden against slowloris-style
	// resource exhaustion. WriteTimeout is intentionally unset so that
	// long-lived SSE streaming responses are not prematurely cancelled.
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM: stop the account refresher and join
	// it, trigger the final stats flush, and drain in-flight requests with a
	// bounded deadline.
	//
	// stopOnce is shared by every caller of shutdown/stopBackgroundLoops so a
	// second signal (or a future forced-exit path) cannot panic on a double
	// close of the stop channels.
	stopOnce := &sync.Once{}
	shutdownErr := make(chan error, 1)
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		// Unregister before tearing down: restoring the default disposition
		// means a second signal terminates the process immediately instead of
		// being queued into a handler that has already run. The stop channels
		// themselves are guarded by sync.Once, so a duplicate teardown is safe.
		signal.Stop(sigCh)
		log.Printf("[server] received %s, shutting down...", sig)
		shutdown(shutdownErr, srv, stopOnce, accountStop, statsStop, accountDone, statsDone)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[server] failed to listen on %s: %v\n[server] If port %d is in use, change it in the admin UI or data.json", addr, err, port)
	}
	if err := <-shutdownErr; err != nil {
		log.Printf("[server] shutdown error: %v", err)
	}
	// Wait for the stats goroutine to finish its final flush before exiting.
	<-statsDone
	log.Printf("[server] stopped")
}

// finalStr labels a flush log line ("final " on shutdown) so periodic and
// shutdown flushes stay distinguishable.
func finalStr(final bool) string {
	if final {
		return "final "
	}
	return ""
}

// shutdown stops the background loops and drains in-flight requests. The drain
// runs concurrently with the join so a slow account refresh cannot delay it, but
// it is still awaited before this returns: cancelling the drain's context early
// would abort every in-flight streaming response on SIGTERM, which is what a
// previous version of this function did. The stop channels are closed through
// stopOnce, so a repeated shutdown call cannot panic.
func shutdown(shutdownErr chan<- error, srv *http.Server, stopOnce *sync.Once, accountStop, statsStop chan<- struct{}, accountDone, statsDone <-chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	drain := make(chan error, 1)
	go func() { drain <- srv.Shutdown(ctx) }()
	stopBackgroundLoops(stopOnce, accountStop, statsStop, accountDone, statsDone)
	shutdownErr <- <-drain
	cancel()
}

// stopBackgroundLoops joins the account refresher before triggering the stats
// loop's final flush: the account loop may be mid-fetch for up to 30s, and a
// SetAccount/SetBillingCycle landing after that flush would be lost on exit.
//
// once guards every stop-channel close, so a second caller (a future
// "second signal forces exit" path, or a duplicate call) blocks until the
// first stop completes and then returns instead of panicking on a double
// close. Callers supply the Once so independent instances stay independent.
func stopBackgroundLoops(once *sync.Once, accountStop, statsStop chan<- struct{}, accountDone, statsDone <-chan struct{}) {
	once.Do(func() {
		close(accountStop)
		<-accountDone
		close(statsStop)
		<-statsDone
	})
}
