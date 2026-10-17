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

// bridgeProvider manages a single OpenAiBridge for the current PAT.
// When the PAT changes (via admin UI), the bridge is recreated on next access.
type bridgeProvider struct {
	mu       sync.Mutex
	bridge   *bridge.OpenAiBridge
	pat      string
	store    *store.Store
	switcher *autoSwitcher // nil in tests that build a provider directly
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
	return &bridgeProvider{store: st, snapshots: map[string]*store.AccountSnapshot{}}
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

// resolveBridge validates the API key and returns the bridge for the current
// PAT plus the key's metadata for request logging. Returns nil if the key is
// invalid or no PAT is configured.
func (p *bridgeProvider) resolveBridge(apiKey string) *bridge.ResolvedBridge {
	key, ok := p.store.LookupKey(apiKey)
	if !ok {
		return nil
	}
	b := p.currentBridge()
	if b == nil {
		return nil
	}
	return &bridge.ResolvedBridge{Bridge: b, KeyID: key.ID, KeyNote: key.Note, AccountID: p.store.GetActiveAccountID()}
}

// currentBridge returns the bridge for the current PAT, creating or recreating
// the bridge when the PAT changes. A PAT change (including clearing it) also
// fires onPatChange after the provider lock is released, so the cached account
// snapshot of the previous PAT can be dropped.
func (p *bridgeProvider) currentBridge() *bridge.OpenAiBridge {
	pat := p.store.GetPAT()
	p.mu.Lock()
	changed := p.pat != "" && p.pat != pat
	var b *bridge.OpenAiBridge
	if pat == "" {
		if changed {
			// The old PAT/session must not linger once it is cleared.
			p.bridge = nil
			p.pat = ""
		}
	} else if p.bridge == nil || p.pat != pat {
		region := auth.RegionByName(p.store.AccountRegionByPAT(pat))
		p.bridge = bridge.NewOpenAiBridge(pat, region)
		p.pat = pat
		b = p.bridge
	} else {
		b = p.bridge
	}
	hook := p.onPatChange
	p.mu.Unlock()

	if changed && hook != nil {
		hook()
	}
	return b
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

// ═══ Automatic account switching ═══
//
// Three signals feed the pool snapshots, in decreasing trust order, and none
// of them adds a polling loop:
//
//  1. authoritative correction — the existing 60s account loop already fetches
//     the active account's quota; its result is written back into the snapshot
//     and hard-exhaustion sets a cooldown until the next reset;
//  2. failure fallback — a chat request dying on an upstream 401/403 before any
//     byte reached the client puts the failed account into a short cooldown and
//     moves to the next candidate; the request retries once on the new bridge;
//  3. passive deduction — the usage frame of every billed request carries
//     credits, which are subtracted from the current account's remaining
//     snapshot; a threshold breach schedules a switch.
//
// Switching itself is still SetPAT(): the bridge rebuilds lazily, the stats
// snapshot is cleared by the existing PAT-change hook, and patWriteMu keeps
// account-loop writes from landing across a switch.

const (
	// failureCooldown is the exclusion window applied after a chat-path auth
	// failure: long enough to stop hammering a dead token, short enough that
	// a fixed credential recovers without admin action.
	failureCooldown = 5 * time.Minute
	// minResidence is the minimum time an account stays active before
	// automatic switching may move away from it again (anti-oscillation).
	minResidence = 60 * time.Second
	// switchHysteresis requires a candidate to beat the threshold by this much
	// margin (credits) so an account hovering at the threshold cannot
	// flip-flop.
	switchHysteresis = 5.0
)

// autoSwitcher owns the switching state machine. It mutates the pool through
// the store (ActivateAccount) and never holds locks across upstream calls.
type autoSwitcher struct {
	mu       sync.Mutex
	store    *store.Store
	since    time.Time // when the current account became active
	lastAsk  time.Time // last "no candidate" log instant (throttle for the stay-put warning)
	onSwitch func()    // fired after a successful automatic switch (stats snapshot clear)
}

func newAutoSwitcher(st *store.Store) *autoSwitcher {
	return &autoSwitcher{store: st, since: time.Now()}
}

// setOnSwitch installs the post-switch hook (must happen before serving).
func (s *autoSwitcher) setOnSwitch(fn func()) { s.onSwitch = fn }

// pickCandidate returns the pool entry to switch to: skipping cooling-down
// accounts, preferring the one with the most remaining credits that sits
// comfortably above the threshold (+ hysteresis), falling back to the highest
// remaining when every candidate sits below it — a partially charged account
// still beats a dead one. Returns "" when nobody qualifies.
func pickCandidate(accounts []store.Account, activeID string, threshold float64) string {
	type cand struct {
		id        string
		remaining float64
		ready     bool // comfortably above the threshold
	}
	var pool []cand
	for _, acc := range accounts {
		if acc.ID == activeID || acc.Snapshot == nil {
			continue
		}
		if acc.Snapshot.CooldownUntil > time.Now().Unix() {
			continue
		}
		remaining := acc.Snapshot.Remaining
		pool = append(pool, cand{acc.ID, remaining, remaining > threshold+switchHysteresis && !acc.Snapshot.IsExceeded})
	}
	// Prefer ready candidates (most credits first), then any candidate
	// (still most credits first). Accounts without a snapshot never qualify:
	// no data beats guessing.
	for _, wantReady := range []bool{true, false} {
		best := ""
		bestRemaining := -1.0
		for _, c := range pool {
			if c.ready != wantReady || c.remaining <= bestRemaining {
				continue
			}
			best, bestRemaining = c.id, c.remaining
		}
		if best != "" {
			return best
		}
	}
	return ""
}

// maybeSwitch evaluates the auto-switch conditions for reason ("threshold" or
// "failure") and switches when a candidate exists. Serializes against itself;
// concurrent failures dedupe because the second caller re-reads the store and
// finds itself already on the account the first one switched to.
func (s *autoSwitcher) maybeSwitch(reason, failedID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.store.GetAutoSwitch()
	if !cfg.Enabled {
		return
	}
	activeID := s.store.GetActiveAccountID()
	if failedID != "" && failedID != activeID {
		// Someone already switched away from the failed account.
		return
	}
	if reason == "failure" && failedID != "" {
		// Cool the failed account down first so the candidate pick below skips
		// it (failures are rare; one save is fine).
		for _, acc := range s.store.ListAccounts() {
			if acc.ID != failedID {
				continue
			}
			snap := &store.AccountSnapshot{CooldownUntil: time.Now().Add(failureCooldown).Unix()}
			if acc.Snapshot != nil {
				cp := *acc.Snapshot
				snap = &cp
				snap.CooldownUntil = time.Now().Add(failureCooldown).Unix()
			}
			_ = s.store.SetAccountSnapshot(failedID, snap)
			break
		}
	}
	if reason == "threshold" && time.Since(s.since) < minResidence {
		return // anti-oscillation: honor the current account's residence
	}
	id := pickCandidate(s.store.ListAccounts(), activeID, cfg.Threshold)
	if id == "" {
		// Keep serving and say so once per account loop.
		if time.Since(s.lastAsk) > accountRefreshCadence {
			s.lastAsk = time.Now()
			log.Printf("[bridge] WARN auto-switch: no eligible candidate above %.0f credits; staying on the current account", cfg.Threshold)
		}
		return
	}
	if err := s.store.ActivateAccount(id); err != nil {
		log.Printf("[bridge] WARN auto-switch failed: %v", err)
		return
	}
	s.since = time.Now()
	note := id
	for _, acc := range s.store.ListAccounts() {
		if acc.ID == id {
			note = acc.Note
			if note == "" {
				note = acc.PAT[:4] + "****"
			}
		}
	}
	log.Printf("[bridge] auto-switch (%s): moved to account %s", reason, note)
	if s.onSwitch != nil {
		s.onSwitch()
	}
}

// handleChatFailover implements bridge.FailoverFunc: report the auth failure
// of failedAccountID, cool it down, switch to a candidate, and hand back the
// replacement bridge — or nil to give up and surface the original error.
func (p *bridgeProvider) handleChatFailover(failedAccountID string) *bridge.ResolvedBridge {
	activeID := p.store.GetActiveAccountID()
	p.switcher.maybeSwitch("failure", failedAccountID)
	if p.store.GetActiveAccountID() == activeID {
		return nil // no switch happened: no point retrying on the same bridge
	}
	// The PAT mirror moved, so currentBridge rebuilds lazily on the new PAT.
	b := p.currentBridge()
	if b == nil {
		return nil
	}
	return &bridge.ResolvedBridge{Bridge: b, AccountID: p.store.GetActiveAccountID()}
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
	remaining := snap.Remaining
	p.snapMu.Unlock()

	if cfg := p.store.GetAutoSwitch(); cfg.Enabled && remaining <= cfg.Threshold {
		p.switcher.maybeSwitch("threshold", accountID)
	}
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
			snap.CooldownUntil = time.Now().Add(failureCooldown).Unix()
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
	// Automatic switching state machine: ownership of the pool switch, fed by
	// the quota loop (authoritative), the failover path (failure) and the
	// usage sink (threshold). The post-switch hook reuses the PAT-change
	// clear so the panel drops the previous account's snapshot.
	switcher := newAutoSwitcher(st)
	provider.switcher = switcher
	switcher.setOnSwitch(func() { clearAccountOnPatChange(rec) })
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

	// Model fetcher for admin UI
	modelFetcher := func(ctx context.Context) []string {
		b := provider.currentBridge()
		if b == nil {
			return models.DefaultCatalog().Keys()
		}
		catalog := b.GetCatalog(ctx)
		return catalog.Keys()
	}

	// Subscription account loop: refreshes identity metadata from /user/status
	// and authoritative allowance totals from OpenAPI /api/v2/quota/usage out
	// of band. The admin panel's 15-second poll remains memory-only.
	//
	// The loop has its own stop/done pair so shutdown can join it before the
	// final stats flush: its last SetAccount/SetBillingCycle would otherwise
	// land after that flush and be lost on exit.
	accountStop := make(chan struct{})
	accountDone := make(chan struct{})
	applyAccount := func(ctx context.Context) {
		// Capture the PAT this refresh belongs to: if it changes while the
		// fetch is in flight, the snapshot must be dropped rather than written
		// after the PAT-change hook cleared the previous account.
		pat := provider.store.GetPAT()
		b := provider.currentBridge()
		if b == nil {
			return // no PAT configured yet
		}
		st := b.EnsureAccountStatus(ctx)
		if st == nil {
			return // upstream unreachable: keep the last known state
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
		// The write section runs under patWriteMu and re-checks the PAT inside it:
		// the PAT-change hook clears the recorder under the same lock, so a
		// snapshot fetched for the previous PAT can no longer land after that
		// clear. The fetch above stays outside the lock (it can take 30s).
		applyAccountWrite(rec, provider, pat, st.NextResetAtMs, account)
		// Authoritative correction (signal 1): merge the OpenAPI result into the
		// account snapshot and arm the hard-exhaustion cooldown. Runs after the
		// recorder write so a switch racing this write evaluates fresh data.
		provider.applyQuotaToSnapshot(provider.store.GetActiveAccountID(), st)
	}
	go func() {
		defer close(accountDone)
		// Fetch once immediately so the panel is populated from the start
		// rather than after the first tick.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		applyAccount(ctx)
		cancel()

		ticker := time.NewTicker(accountRefreshCadence)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				applyAccount(ctx)
				cancel()
			case <-accountStop:
				return
			}
		}
	}()

	// Initialize admin
	adminInst := admin.New(st, modelFetcher, rec, logRec)
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
	// failover hands a chat-path auth failure to the auto-switcher and, when
	// the active account moved, returns the rebuilt bridge for a single retry.
	failover := func(failedAccountID string) *bridge.ResolvedBridge {
		return provider.handleChatFailover(failedAccountID)
	}
	// usageHook feeds the usage sink with the account context: billed credits
	// passively deduct the current account's snapshot (signal 3) and a
	// threshold breach schedules an automatic switch.
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
