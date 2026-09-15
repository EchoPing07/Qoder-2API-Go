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
}

func newBridgeProvider(st *store.Store) *bridgeProvider {
	return &bridgeProvider{store: st}
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
// the bridge when the PAT changes.
func (p *bridgeProvider) currentBridge() *bridge.OpenAiBridge {
	pat := p.store.GetPAT()
	if pat == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bridge == nil || p.pat != pat {
		realPAT, region := auth.Resolve(pat)
		p.bridge = bridge.NewOpenAiBridge(realPAT, region)
		p.pat = pat
	}
	return p.bridge
}

// accountRefreshCadence is how often the subscription state is re-checked. The
// bridge caches the upstream result for an hour, so this only bounds how quickly
// a billing rollover is noticed, not how often the gateway is called.
const accountRefreshCadence = 10 * time.Minute

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

	// Subscription account loop: refreshes the plan and billing-cycle boundary
	// from the gateway's /user/status endpoint out of band, so the admin panel
	// can render cycle-relative credits straight from the recorder instead of
	// making an upstream call on every 15s poll.
	//
	// The cadence is intentionally shorter than the bridge's 1h account TTL:
	// most ticks are served from cache, but a tick landing just after a reset
	// boundary picks up the new cycle within minutes.
	applyAccount := func(ctx context.Context) {
		b := provider.currentBridge()
		if b == nil {
			return // no PAT configured yet
		}
		st := b.EnsureAccountStatus(ctx)
		if st == nil {
			return // upstream unreachable: keep the last known state
		}
		rec.SetBillingCycle(st.NextResetAtMs)
		rec.SetAccount(&stats.Account{
			Plan:            st.Plan,
			Tag:             st.UserTag,
			IsQuotaExceeded: st.IsQuotaExceeded,
		})
	}
	go func() {
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
			case <-statsStop:
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

	// Graceful shutdown on SIGINT/SIGTERM: stop the stats ticker (final
	// flush) and drain in-flight requests with a bounded deadline.
	shutdownErr := make(chan error, 1)
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("[server] received %s, shutting down...", sig)
		close(statsStop)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		shutdownErr <- srv.Shutdown(ctx)
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
