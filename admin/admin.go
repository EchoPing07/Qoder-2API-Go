// Package admin provides WebUI management for the API bridge:
// PAT configuration, API key CRUD, model listing, and password-protected access.
package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"qoder2api/models"
	"qoder2api/stats"
	"qoder2api/store"
)

const sessionDuration = 24 * time.Hour

// adminMaxBody caps JSON request bodies handled by the admin API.
const adminMaxBody = 1 << 16 // 64 KiB

// Admin manages the web UI and admin API endpoints.
type Admin struct {
	store        *store.Store
	modelFetcher ModelFetcher
	stats        *stats.Recorder
	sessions     map[string]time.Time
	sessionMu    sync.Mutex
	limiter      *loginLimiter
}

// ModelFetcher returns the current model catalog (dynamic or fallback).
type ModelFetcher func(ctx context.Context) []string

// New creates an Admin instance. rec may be nil.
func New(s *store.Store, mf ModelFetcher, rec *stats.Recorder) *Admin {
	return &Admin{
		store:        s,
		modelFetcher: mf,
		stats:        rec,
		sessions:     make(map[string]time.Time),
		limiter:      newLoginLimiter(),
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
	mux.HandleFunc("/admin/api/stats", a.requireAuth(a.handleStats))
	mux.HandleFunc("/admin/api/config", a.requireAuth(a.handleConfig))
	mux.HandleFunc("/admin/api/password", a.requireAuth(a.handlePassword))
}

// --- Session & Auth ---

func generateSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *Admin) createSession() (string, error) {
	token, err := generateSessionToken()
	if err != nil {
		return "", err
	}
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
	return token, nil
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

// --- Login rate limiting (per client IP, exponential backoff) ---

type loginAttempt struct {
	failures    int
	lockedUntil time.Time
	lastFail    time.Time
}

type loginLimiter struct {
	mu    sync.Mutex
	state map[string]*loginAttempt
}

const (
	loginMaxBackoff = 30 * time.Second
	loginPruneAge   = time.Hour
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{state: make(map[string]*loginAttempt)}
}

// check reports whether the IP is currently locked out and, if so, how long
// until it may retry.
func (l *loginLimiter) check(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked()
	st := l.state[ip]
	if st == nil {
		return false, 0
	}
	now := time.Now()
	if now.Before(st.lockedUntil) {
		return true, st.lockedUntil.Sub(now)
	}
	return false, 0
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.state[ip]
	if st == nil {
		st = &loginAttempt{}
		l.state[ip] = st
	}
	st.failures++
	st.lastFail = time.Now()
	// Compute the backoff in plain integers and clamp before converting to
	// time.Duration: 1<<63 seconds overflows int64 nanoseconds and yields a
	// negative duration, which would silently disable the lockout after
	// enough consecutive failures.
	shift := st.failures - 1
	if shift > 5 { // 1<<5 s == 32s > loginMaxBackoff
		shift = 5
	}
	backoff := time.Second << uint(shift)
	if backoff > loginMaxBackoff {
		backoff = loginMaxBackoff
	}
	st.lockedUntil = st.lastFail.Add(backoff)
}

func (l *loginLimiter) success(ip string) {
	l.mu.Lock()
	delete(l.state, ip)
	l.mu.Unlock()
}

func (l *loginLimiter) pruneLocked() {
	cutoff := time.Now().Add(-loginPruneAge)
	for ip, st := range l.state {
		if st.lastFail.Before(cutoff) {
			delete(l.state, ip)
		}
	}
}

// --- Request helpers ---

// readJSON decodes a JSON request body capped at adminMaxBody. The size cap
// bounds memory use against oversized payloads.
func readJSON(w http.ResponseWriter, r *http.Request, v interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, adminMaxBody)
	return json.NewDecoder(r.Body).Decode(v)
}

// passwordsEqual compares two secrets in constant time to avoid timing leaks.
func passwordsEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	// Trim the port, handling both "1.2.3.4:5678" and "[::1]:5678".
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return host
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if xfp := r.Header.Get("X-Forwarded-Proto"); xfp == "https" {
		return true
	}
	return false
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		MaxAge:   int(sessionDuration.Seconds()),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		MaxAge:   -1,
	})
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
	ip := clientIP(r)
	if locked, retry := a.limiter.check(ip); locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		writeJSONError(w, 429, "登录尝试过于频繁，请稍后再试")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeJSONError(w, 400, "请求格式错误")
		return
	}
	if !passwordsEqual(body.Password, a.getPassword()) {
		a.limiter.fail(ip)
		writeJSONError(w, 401, "密码错误")
		return
	}
	a.limiter.success(ip)
	token, err := a.createSession()
	if err != nil {
		writeJSONError(w, 500, "无法创建会话")
		return
	}
	setSessionCookie(w, r, token)
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
	clearSessionCookie(w, r)
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
	if err := readJSON(w, r, &body); err != nil {
		writeJSONError(w, 400, "请求格式错误")
		return
	}
	if os.Getenv("QODER_ADMIN_PASSWORD") != "" {
		writeJSONError(w, 400, "密码由环境变量 QODER_ADMIN_PASSWORD 管理，无法在面板中修改")
		return
	}
	if !passwordsEqual(body.CurrentPassword, a.getPassword()) {
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
	// Invalidate every existing session so a (possibly stolen) cookie from
	// before the password change cannot be replayed.
	a.sessionMu.Lock()
	a.sessions = make(map[string]time.Time)
	a.sessionMu.Unlock()
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
		if err := readJSON(w, r, &body); err != nil {
			writeJSONError(w, 400, "请求格式错误")
			return
		}
		entry, err := a.store.AddKey(body.Key, body.Note)
		if err != nil {
			if errors.Is(err, store.ErrDuplicateKey) {
				writeJSONError(w, 409, err.Error())
			} else {
				writeJSONError(w, 500, err.Error())
			}
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
		if err := readJSON(w, r, &body); err != nil {
			writeJSONError(w, 400, "请求格式错误")
			return
		}
		if body.Host == "" {
			body.Host = store.DefaultHost
		}
		if body.Port == 0 {
			body.Port = store.DefaultPort
		}
		if body.Port < 1 || body.Port > 65535 {
			writeJSONError(w, 400, "端口必须在 1-65535 范围内")
			return
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
		// Only the masked form is exposed; the plaintext PAT never leaves
		// the server. The UI edits it as a whole new value, never in place.
		masked := maskPAT(a.store.GetPAT())
		writeJSON(w, 200, map[string]string{"pat_masked": masked})
	case http.MethodPost:
		var body struct {
			PAT string `json:"pat"`
		}
		if err := readJSON(w, r, &body); err != nil {
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

// --- Stats endpoint ---

func (a *Admin) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, 405, "方法不允许")
		return
	}
	if a.stats == nil {
		writeJSON(w, 200, stats.Report{})
		return
	}
	writeJSON(w, 200, a.stats.Report())
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
