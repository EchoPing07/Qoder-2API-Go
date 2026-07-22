// qoder2api: OpenAI-compatible API bridge for QoderWork.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"

	"qoder2api/admin"
	"qoder2api/auth"
	"qoder2api/bridge"
	"qoder2api/models"
	"qoder2api/store"

	"sync"
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

	provider := newBridgeProvider(st)

	// Model fetcher for admin UI
	modelFetcher := func(ctx context.Context) []string {
		b := provider.currentBridge()
		if b == nil {
			return models.DefaultCatalog().Keys()
		}
		catalog := b.GetCatalog(ctx)
		return catalog.Keys()
	}

	// Initialize admin
	adminInst := admin.New(st, modelFetcher)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", bridge.MakeChatHandler(provider.resolveBridge))
	mux.HandleFunc("/v1/models", bridge.MakeModelsHandler(provider.resolveBridge))

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
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("[server] failed to listen on %s: %v\n[server] If port %d is in use, change it in the admin UI or data.json", addr, err, port)
	}
}
