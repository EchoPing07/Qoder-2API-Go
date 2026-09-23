// qoder2api: OpenAI-compatible API bridge for QoderWork.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"qoder2api/admin"
	"qoder2api/auth"
	"qoder2api/bridge"
	"qoder2api/models"
	"qoder2api/stats"
	"qoder2api/store"

	"sync"
	"time"
)

// bridgeProvider manages a single OpenAiBridge for the current PAT.
// When the PAT changes (via admin UI), the bridge is recreated on next access.
type bridgeProvider struct {
	mu     sync.Mutex
	bridge *bridge.OpenAiBridge
	pat    string
	store  *store.Store
	// onPatChange is fired after a different non-empty PAT was replaced or the
	// PAT was cleared. It runs without the provider lock held: the callback
	// takes the recorder lock, and nesting the two risks a lock-order stall.
	onPatChange func()
}

func newBridgeProvider(st *store.Store) *bridgeProvider {
	return &bridgeProvider{store: st}
}

// setOnPatChange installs the callback fired when the configured PAT changes.
// Wiring happens before the provider is used by any goroutine.
func (p *bridgeProvider) setOnPatChange(fn func()) {
	p.mu.Lock()
	p.onPatChange = fn
	p.mu.Unlock()
}

// resolveBridge validates the API key and returns the bridge for the current PAT.
// Returns nil if the key is invalid or no PAT is configured.
func (p *bridgeProvider) resolveBridge(apiKey string) *bridge.OpenAiBridge {
	if !p.store.ValidateKey(apiKey) {
		return nil
	}
	return p.currentBridge()
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
		realPAT, region := auth.Resolve(pat)
		p.bridge = bridge.NewOpenAiBridge(realPAT, region)
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

	provider := newBridgeProvider(st)

	// Request statistics recorder with periodic persistence (30s cadence).
	// The loop also drains a stop channel so graceful shutdown can trigger a
	// final flush, avoiding loss of the last <=30s of stats.
	rec := stats.NewRecorder(st)
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
		for {
			select {
			case <-ticker.C:
				if err := rec.Flush(); err != nil {
					log.Printf("[stats] WARN flush failed: %v", err)
				}
			case <-statsStop:
				if err := rec.Flush(); err != nil {
					log.Printf("[stats] WARN final flush failed: %v", err)
				}
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
	adminInst := admin.New(st, modelFetcher, rec)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", bridge.MakeChatHandler(provider.resolveBridge, rec))
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

	// Register admin routes
	adminInst.RegisterRoutes(mux)

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
