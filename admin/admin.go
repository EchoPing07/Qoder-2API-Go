// Package admin provides WebUI management for the API bridge:
// PAT configuration, API key CRUD, model listing, and password-protected access.
package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"

	"qoder2api/models"
	"qoder2api/store"
)

const sessionDuration = 24 * time.Hour

// Admin manages the web UI and admin API endpoints.
type Admin struct {
	store        *store.Store
	modelFetcher ModelFetcher
	sessions     map[string]time.Time
	sessionMu    sync.Mutex
}

// ModelFetcher returns the current model catalog (dynamic or fallback).
type ModelFetcher func(ctx context.Context) []string

// New creates an Admin instance.
func New(s *store.Store, mf ModelFetcher) *Admin {
	return &Admin{
		store:        s,
		modelFetcher: mf,
		sessions:     make(map[string]time.Time),
	}
}

// RegisterRoutes registers all admin routes on the given mux.
func (a *Admin) RegisterRoutes(mux *http.ServeMux) {
	// WebUI (no auth — the page handles auth client-side)
	mux.HandleFunc("/admin", a.serveIndex)
	mux.HandleFunc("/admin/", a.serveIndex)

	// Auth endpoints (no auth required)
	mux.HandleFunc("/admin/api/login", a.handleLogin)
	mux.HandleFunc("/admin/api/logout", a.handleLogout)
	mux.HandleFunc("/admin/api/auth", a.handleAuth)

	// Protected API endpoints (require auth)
	mux.HandleFunc("/admin/api/keys", a.requireAuth(a.handleKeys))
	mux.HandleFunc("/admin/api/pat", a.requireAuth(a.handlePAT))
	mux.HandleFunc("/admin/api/models", a.requireAuth(a.handleModels))
	mux.HandleFunc("/admin/api/config", a.requireAuth(a.handleConfig))
	mux.HandleFunc("/admin/api/password", a.requireAuth(a.handlePassword))
}

// --- Session & Auth ---

func generateSessionToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *Admin) createSession() string {
	token := generateSessionToken()
	a.sessionMu.Lock()
	a.sessions[token] = time.Now().Add(sessionDuration)
	// Opportunistic cleanup: remove expired sessions
	now := time.Now()
	for k, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, k)
		}
	}
	a.sessionMu.Unlock()
	return token
}

func (a *Admin) isValidSession(token string) bool {
	if token == "" {
		return false
	}
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	expiry, ok := a.sessions[token]
	if !ok || time.Now().After(expiry) {
		delete(a.sessions, token)
		return false
	}
	return true
}

func (a *Admin) removeSession(token string) {
	a.sessionMu.Lock()
	delete(a.sessions, token)
	a.sessionMu.Unlock()
}

func (a *Admin) getPassword() string {
	if env := os.Getenv("QODER_ADMIN_PASSWORD"); env != "" {
		return env
	}
	return a.store.GetPassword()
}

// requireAuth wraps a handler with session authentication.
func (a *Admin) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("admin_session")
		if err != nil || !a.isValidSession(cookie.Value) {
			writeJSONError(w, 401, "未登录或会话已过期")
			return
		}
		next(w, r)
	}
}

func (a *Admin) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, 405, "方法不允许")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, 400, "请求格式错误")
		return
	}
	if body.Password != a.getPassword() {
		writeJSONError(w, 401, "密码错误")
		return
	}
	token := a.createSession()
	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   int(sessionDuration.Seconds()),
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *Admin) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, 405, "方法不允许")
		return
	}
	if cookie, err := r.Cookie("admin_session"); err == nil {
		a.removeSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *Admin) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, 405, "方法不允许")
		return
	}
	cookie, err := r.Cookie("admin_session")
	authed := err == nil && a.isValidSession(cookie.Value)
	writeJSON(w, 200, map[string]interface{}{"authenticated": authed})
}

func (a *Admin) handlePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, 405, "方法不允许")
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, 400, "请求格式错误")
		return
	}
	if body.CurrentPassword != a.getPassword() {
		writeJSONError(w, 401, "当前密码错误")
		return
	}
	if len(body.NewPassword) < 1 {
		writeJSONError(w, 400, "新密码不能为空")
		return
	}
	if err := a.store.SetPassword(body.NewPassword); err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// --- HTTP Helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// --- API Key endpoints ---

func (a *Admin) handleKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		keys := a.store.ListKeys()
		writeJSON(w, 200, map[string]interface{}{"keys": keys})
	case http.MethodPost:
		var body struct {
			Key  string `json:"key"`
			Note string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, 400, "请求格式错误")
			return
		}
		entry, err := a.store.AddKey(body.Key, body.Note)
		if err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, entry)
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			writeJSONError(w, 400, "缺少 id 参数")
			return
		}
		if err := a.store.DeleteKey(id); err != nil {
			writeJSONError(w, 404, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "deleted"})
	default:
		writeJSONError(w, 405, "方法不允许")
	}
}

// --- Config endpoint ---

func (a *Admin) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, map[string]interface{}{
			"host": a.store.GetHost(),
			"port": a.store.GetPort(),
		})
	case http.MethodPost:
		var body struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, 400, "请求格式错误")
			return
		}
		if body.Host == "" {
			body.Host = store.DefaultHost
		}
		if body.Port == 0 {
			body.Port = store.DefaultPort
		}
		if err := a.store.SetHostPort(body.Host, body.Port); err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]interface{}{
			"host":    body.Host,
			"port":    body.Port,
			"restart": "修改主机或端口后需要重启服务才能生效",
		})
	default:
		writeJSONError(w, 405, "方法不允许")
	}
}

// --- PAT endpoint ---

func (a *Admin) handlePAT(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		pat := a.store.GetPAT()
		masked := maskPAT(pat)
		writeJSON(w, 200, map[string]string{"pat": pat, "pat_masked": masked})
	case http.MethodPost:
		var body struct {
			PAT string `json:"pat"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, 400, "请求格式错误")
			return
		}
		if err := a.store.SetPAT(body.PAT); err != nil {
			writeJSONError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	default:
		writeJSONError(w, 405, "方法不允许")
	}
}

// --- Models endpoint ---

func (a *Admin) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, 405, "方法不允许")
		return
	}
	var modelList []string
	if a.modelFetcher != nil {
		modelList = a.modelFetcher(r.Context())
	}
	if modelList == nil {
		catalog := models.DefaultCatalog()
		modelList = catalog.Keys()
	}
	writeJSON(w, 200, map[string]interface{}{"models": modelList})
}

// --- WebUI ---

func (a *Admin) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.WriteHeader(200)
	w.Write([]byte(indexHTML))
}

// maskPAT returns a masked version of the PAT for display.
func maskPAT(pat string) string {
	if len(pat) <= 8 {
		if pat == "" {
			return ""
		}
		return "****"
	}
	return pat[:4] + "****" + pat[len(pat)-4:]
}
