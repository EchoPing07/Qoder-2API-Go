package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qoder2api/auth"
	"qoder2api/logs"
	"qoder2api/models"
	"qoder2api/stats"
	"qoder2api/store"
)

func tempStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/data.json"
	s, err := store.New(path)
	if err != nil {
		t.Fatalf("store.New failed: %v", err)
	}
	return s
}

func newAdmin(t *testing.T) (*Admin, *store.Store) {
	s := tempStore(t)
	mf := func(ctx context.Context) []string {
		return models.DefaultCatalog().Keys()
	}
	return New(s, mf, nil, nil), s
}

func doRequest(t *testing.T, handler http.HandlerFunc, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var bodyStr string
	if body != nil {
		b, _ := json.Marshal(body)
		bodyStr = string(b)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(bodyStr))
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

// -- API Keys --

func TestAddKey(t *testing.T) {
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleKeys, "POST", "/admin/api/keys", map[string]string{"key": "sk-test", "note": "unit test"})
	if w.Code != 201 {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListKeys(t *testing.T) {
	a, _ := newAdmin(t)
	doRequest(t, a.handleKeys, "POST", "/admin/api/keys", map[string]string{"key": "sk-list-test", "note": "list test"})
	w := doRequest(t, a.handleKeys, "GET", "/admin/api/keys", nil)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	keys, ok := resp["keys"].([]interface{})
	if !ok {
		t.Fatal("expected keys array in response")
	}
	if len(keys) != 1 {
		t.Errorf("expected 1 key, got %d", len(keys))
	}
}

func TestDeleteKey(t *testing.T) {
	a, s := newAdmin(t)
	entry, _ := s.AddKey("sk-delete-me", "delete test")
	w := doRequest(t, a.handleKeys, "DELETE", "/admin/api/keys?id="+entry.ID, nil)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(s.ListKeys()) != 0 {
		t.Error("expected 0 keys after delete")
	}
}

func TestDeleteKeyMissingID(t *testing.T) {
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleKeys, "DELETE", "/admin/api/keys", nil)
	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// -- PAT --

func TestPATSetAndGet(t *testing.T) {
	a, _ := newAdmin(t)
	w := doRequest(t, a.handlePAT, "POST", "/admin/api/pat", map[string]string{"pat": "pt-test123"})
	if w.Code != 200 {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	w = doRequest(t, a.handlePAT, "GET", "/admin/api/pat", nil)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	// The plaintext PAT must never appear in the response; only the masked form.
	if strings.Contains(w.Body.String(), "pt-test123") {
		t.Error("response must not leak the plaintext PAT")
	}
	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if _, ok := resp["pat"]; ok {
		t.Error("response must not contain a 'pat' field")
	}
	if resp["pat_masked"] != "pt-t****t123" {
		t.Errorf("expected masked PAT, got %q", resp["pat_masked"])
	}
}

func TestMaskPAT(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"short", "****"},
		{"pt-12345678", "pt-1****5678"},
		{"pt-very-long-token-here", "pt-v****here"},
	}
	for _, tt := range tests {
		got := maskPAT(tt.input)
		if got != tt.expected {
			t.Errorf("maskPAT(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// -- Models --

func TestModels(t *testing.T) {
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleModels, "GET", "/admin/api/models", nil)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	models, ok := resp["models"].([]interface{})
	if !ok {
		t.Fatal("expected models array in response")
	}
	if len(models) == 0 {
		t.Error("expected non-empty models list")
	}
}

// -- Config --

func TestConfigGet(t *testing.T) {
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleConfig, "GET", "/admin/api/config", nil)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["host"] != store.DefaultHost {
		t.Errorf("expected default host %q, got %v", store.DefaultHost, resp["host"])
	}
	if int(resp["port"].(float64)) != store.DefaultPort {
		t.Errorf("expected default port %d, got %v", store.DefaultPort, resp["port"])
	}
}

func TestConfigSet(t *testing.T) {
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleConfig, "POST", "/admin/api/config", map[string]interface{}{"host": "127.0.0.1", "port": 8080})
	if w.Code != 200 {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// Verify it persisted
	w = doRequest(t, a.handleConfig, "GET", "/admin/api/config", nil)
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["host"] != "127.0.0.1" {
		t.Errorf("expected host '127.0.0.1', got %v", resp["host"])
	}
	if int(resp["port"].(float64)) != 8080 {
		t.Errorf("expected port 8080, got %v", resp["port"])
	}
}

func TestConfigSetDefaults(t *testing.T) {
	a, _ := newAdmin(t)
	// Empty values should fall back to defaults
	w := doRequest(t, a.handleConfig, "POST", "/admin/api/config", map[string]interface{}{"host": "", "port": 0})
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["host"] != store.DefaultHost {
		t.Errorf("expected default host, got %v", resp["host"])
	}
	if int(resp["port"].(float64)) != store.DefaultPort {
		t.Errorf("expected default port, got %v", resp["port"])
	}
}

// -- Stats --

func TestStatsEndpoint(t *testing.T) {
	s := tempStore(t)
	mf := func(ctx context.Context) []string {
		return models.DefaultCatalog().Keys()
	}
	rec := stats.NewRecorder(nil)
	rec.Record("Qwen3.7-Max", true)
	rec.Record("Qwen3.7-Max", false)
	rec.Record("DeepSeek-V4-Pro", true)
	a := New(s, mf, rec, nil)

	w := doRequest(t, a.handleStats, "GET", "/admin/api/stats", nil)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Total   int64 `json:"total"`
		Success int64 `json:"success"`
		Failed  int64 `json:"failed"`
		ByModel []struct {
			Model string `json:"model"`
			Total int64  `json:"total"`
		} `json:"by_model"`
		Hourly []struct {
			Total int64 `json:"total"`
		} `json:"hourly"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response JSON: %v", err)
	}
	if resp.Total != 3 || resp.Success != 2 || resp.Failed != 1 {
		t.Errorf("unexpected totals: %+v", resp)
	}
	if len(resp.ByModel) != 2 {
		t.Errorf("expected 2 model rows, got %d", len(resp.ByModel))
	}
	if resp.ByModel[0].Model != "Qwen3.7-Max" || resp.ByModel[0].Total != 2 {
		t.Errorf("expected Qwen3.7-Max first (sorted by total desc), got %+v", resp.ByModel[0])
	}
	if len(resp.Hourly) != 24 {
		t.Errorf("expected 24 hourly buckets, got %d", len(resp.Hourly))
	}
}

func TestStatsEndpointNilRecorder(t *testing.T) {
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleStats, "GET", "/admin/api/stats", nil)
	if w.Code != 200 {
		t.Errorf("expected 200 with nil recorder, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if v, ok := resp["total"].(float64); !ok || v != 0 {
		t.Errorf("expected zero total, got %v", resp["total"])
	}
}

func TestStatsEndpointMethodNotAllowed(t *testing.T) {
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleStats, "POST", "/admin/api/stats", nil)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// -- WebUI --
// 页面 HTML 与前端脚本的断言（含额度池展示回归）见 web 包的 web_test.go：
// 面板 UI 已从 admin 包内嵌单页迁移为 web 包的多页前端（/admin、/admin/keys、…）。

// Repeated wrong passwords trigger per-IP rate limiting (HTTP 429).
func TestLoginRateLimit(t *testing.T) {
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleLogin, "POST", "/admin/api/login", map[string]string{"password": "wrong"})
	if w.Code != 401 {
		t.Fatalf("expected 401 on first wrong login, got %d", w.Code)
	}
	// Immediately retry: should be locked out.
	w = doRequest(t, a.handleLogin, "POST", "/admin/api/login", map[string]string{"password": "wrong"})
	if w.Code != 429 {
		t.Errorf("expected 429 on second wrong login (rate limited), got %d", w.Code)
	}
}

// Out-of-range ports must be rejected before persisting.
func TestConfigSetInvalidPort(t *testing.T) {
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleConfig, "POST", "/admin/api/config",
		map[string]interface{}{"host": "0.0.0.0", "port": 99999})
	if w.Code != 400 {
		t.Errorf("expected 400 for out-of-range port, got %d", w.Code)
	}
}

// Duplicate keys are reported as 409, not 500.
func TestAddKeyDuplicateReturns409(t *testing.T) {
	a, _ := newAdmin(t)
	doRequest(t, a.handleKeys, "POST", "/admin/api/keys", map[string]string{"key": "sk-dup", "note": "first"})
	w := doRequest(t, a.handleKeys, "POST", "/admin/api/keys", map[string]string{"key": "sk-dup", "note": "second"})
	if w.Code != 409 {
		t.Errorf("expected 409 for duplicate key, got %d", w.Code)
	}
}

// --- Regression tests for review fixes ---

// Sustained brute-force attempts must stay locked out: the old shift-based
// backoff overflowed int64 nanoseconds after ~34 consecutive failures and
// silently disabled the lockout entirely.
func TestLoginLimiterStaysLockedAfterManyFailures(t *testing.T) {
	l := newLoginLimiter()
	for i := 0; i < 100; i++ {
		l.fail("1.2.3.4")
	}
	if locked, _ := l.check("1.2.3.4"); !locked {
		t.Fatal("attacker unlocked after 100 consecutive failures")
	}
	// And a capped wait is reported, not a bogus negative one.
	_, retry := l.check("1.2.3.4")
	if retry > 31*time.Second {
		t.Errorf("retry hint exceeds cap: %v", retry)
	}
}

// Changing the password must invalidate all pre-existing sessions.
func TestPasswordChangeInvalidatesSessions(t *testing.T) {
	a, s := newAdmin(t)
	if err := s.SetPassword("oldpass"); err != nil {
		t.Fatal(err)
	}
	token, err := a.createSession()
	if err != nil {
		t.Fatal(err)
	}
	if !a.isValidSession(token) {
		t.Fatal("session should be valid before password change")
	}
	w := doRequest(t, a.handlePassword, "POST", "/admin/api/password",
		map[string]string{"current_password": "oldpass", "new_password": "newpass"})
	if w.Code != 200 {
		t.Fatalf("password change failed: %d %s", w.Code, w.Body.String())
	}
	if a.isValidSession(token) {
		t.Error("old session must be invalidated after password change")
	}
}

func TestClientIPv6(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[::1]:8080"
	if got := clientIP(r); got != "::1" {
		t.Errorf("clientIP([::1]:8080) = %q, want ::1", got)
	}
	r.RemoteAddr = "1.2.3.4:5678"
	if got := clientIP(r); got != "1.2.3.4" {
		t.Errorf("clientIP(1.2.3.4:5678) = %q, want 1.2.3.4", got)
	}
}

// -- Config: stream timeouts --

func TestConfigGetIncludesTimeouts(t *testing.T) {
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleConfig, "GET", "/admin/api/config", nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if int(resp["chat_timeout_seconds"].(float64)) != store.DefaultChatTimeoutSeconds {
		t.Errorf("expected default chat timeout %d, got %v", store.DefaultChatTimeoutSeconds, resp["chat_timeout_seconds"])
	}
	if int(resp["idle_timeout_seconds"].(float64)) != store.DefaultIdleTimeoutSeconds {
		t.Errorf("expected default idle timeout %d, got %v", store.DefaultIdleTimeoutSeconds, resp["idle_timeout_seconds"])
	}
}

// Saving timeouts must persist to the store and hot-apply to the auth
// package without a restart.
func TestConfigSetTimeoutsHotApplied(t *testing.T) {
	defer auth.SetStreamTimeouts(auth.DefaultChatHeaderTimeout, auth.DefaultChatIdleTimeout)
	a, s := newAdmin(t)
	w := doRequest(t, a.handleConfig, "POST", "/admin/api/config", map[string]interface{}{
		"host": "0.0.0.0", "port": 10081,
		"chat_timeout_seconds": 90, "idle_timeout_seconds": 45,
	})
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if s.GetChatTimeoutSeconds() != 90 || s.GetIdleTimeoutSeconds() != 45 {
		t.Errorf("timeouts not persisted: chat=%d idle=%d", s.GetChatTimeoutSeconds(), s.GetIdleTimeoutSeconds())
	}
	timeouts := auth.CurrentStreamTimeouts()
	if timeouts.Header != 90*time.Second || timeouts.Idle != 45*time.Second {
		t.Errorf("timeouts not hot-applied: %+v", timeouts)
	}
	// GET reflects the effective values.
	w = doRequest(t, a.handleConfig, "GET", "/admin/api/config", nil)
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if int(resp["chat_timeout_seconds"].(float64)) != 90 {
		t.Errorf("GET should report 90, got %v", resp["chat_timeout_seconds"])
	}
}

// Omitted timeout fields (0) must keep current values so legacy callers
// that only send host/port keep working; out-of-range values are rejected.
func TestConfigSetTimeoutValidation(t *testing.T) {
	defer auth.SetStreamTimeouts(auth.DefaultChatHeaderTimeout, auth.DefaultChatIdleTimeout)
	a, s := newAdmin(t)
	s.SetChatTimeoutSeconds(90)
	w := doRequest(t, a.handleConfig, "POST", "/admin/api/config", map[string]interface{}{"host": "0.0.0.0", "port": 10081})
	if w.Code != 200 {
		t.Fatalf("legacy body should be accepted, got %d: %s", w.Code, w.Body.String())
	}
	if s.GetChatTimeoutSeconds() != 90 {
		t.Errorf("legacy body must not touch timeouts, got %d", s.GetChatTimeoutSeconds())
	}
	for _, body := range []map[string]interface{}{
		{"host": "0.0.0.0", "port": 10081, "chat_timeout_seconds": 5000},
		{"host": "0.0.0.0", "port": 10081, "idle_timeout_seconds": -3},
	} {
		w := doRequest(t, a.handleConfig, "POST", "/admin/api/config", body)
		if w.Code != 400 {
			t.Errorf("expected 400 for %+v, got %d: %s", body, w.Code, w.Body.String())
		}
	}
}

// When timeouts are pinned by env vars the panel must refuse changes (the
// env would silently win again after the next restart).
func TestConfigTimeoutsEnvManaged(t *testing.T) {
	t.Setenv("QODER_CHAT_TIMEOUT_SECONDS", "90")
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleConfig, "POST", "/admin/api/config", map[string]interface{}{
		"host": "0.0.0.0", "port": 10081, "chat_timeout_seconds": 60,
	})
	if w.Code != 400 {
		t.Errorf("expected 400 when env-managed, got %d: %s", w.Code, w.Body.String())
	}
}

// A legacy host/port-only save must not clobber env-pinned timeouts with the
// persisted (file) values.
func TestConfigLegacyBodyKeepsEnvTimeouts(t *testing.T) {
	auth.SetStreamTimeouts(200*time.Second, 300*time.Second)
	defer auth.SetStreamTimeouts(auth.DefaultChatHeaderTimeout, auth.DefaultChatIdleTimeout)
	t.Setenv("QODER_CHAT_TIMEOUT_SECONDS", "200")

	a, s := newAdmin(t)
	w := doRequest(t, a.handleConfig, "POST", "/admin/api/config", map[string]interface{}{"host": "0.0.0.0", "port": 10081})
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	timeouts := auth.CurrentStreamTimeouts()
	if timeouts.Header != 200*time.Second {
		t.Errorf("legacy save clobbered env timeout, got %s", timeouts.Header)
	}
	if s.GetChatTimeoutSeconds() != store.DefaultChatTimeoutSeconds {
		t.Errorf("legacy save must not touch stored timeouts, got %d", s.GetChatTimeoutSeconds())
	}
}

// A request mixing a valid and an invalid timeout must apply neither.
func TestConfigPartialInvalidTimeoutAppliesNothing(t *testing.T) {
	defer auth.SetStreamTimeouts(auth.DefaultChatHeaderTimeout, auth.DefaultChatIdleTimeout)
	a, s := newAdmin(t)
	w := doRequest(t, a.handleConfig, "POST", "/admin/api/config", map[string]interface{}{
		"host": "0.0.0.0", "port": 10081,
		"chat_timeout_seconds": 90, "idle_timeout_seconds": 9999,
	})
	if w.Code != 400 {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if s.GetChatTimeoutSeconds() != store.DefaultChatTimeoutSeconds {
		t.Errorf("valid field must not be applied when another field is invalid, got %d", s.GetChatTimeoutSeconds())
	}
	timeouts := auth.CurrentStreamTimeouts()
	if timeouts.Header != auth.DefaultChatHeaderTimeout {
		t.Errorf("hot-apply must not run on rejected input, got %s", timeouts.Header)
	}
}

// The billing-cycle fields and account metadata must reach the panel through
// the stats endpoint, so the UI can render cycle-relative credits without any
// network call of its own.
func TestStatsEndpointExposesBillingCycle(t *testing.T) {
	// The anchor is derived from the wall clock, not hardcoded: SetBillingCycle
	// rolls a boundary that has already passed forward to the next period, so a
	// fixed instant starts disagreeing with the response the moment the calendar
	// reaches it. Day 15 of a month two ahead is always in the future and never
	// lands on a month-end, so no day clamping is involved.
	now := time.Now()
	reset := time.Date(now.Year(), now.Month()+2, 15, 0, 0, 0, 0, time.Local)
	rec := stats.NewRecorder(nil)
	rec.SetBillingCycle(reset.UnixMilli())
	rec.SetAccount(&stats.Account{
		Plan: "PLAN_TIER_TEAM", Tag: "Teams", OrgName: "Example Org",
		TotalUsagePercentage: 0.98,
		UserQuota:            &stats.Quota{Total: 3000, Used: 2939, Remaining: 61, Percentage: 0.98, Unit: "credits"},
		OrgResourcePackage:   &stats.OrgResourcePackage{Cap: 4000, Available: false, Unit: "credits"},
	})
	rec.RecordUsage("Qwen3.7-Max", &stats.Usage{PromptTokens: 5, Credits: 1.25})

	a := New(tempStore(t), func(ctx context.Context) []string { return nil }, rec, nil)
	w := doRequest(t, a.handleStats, "GET", "/admin/api/stats", nil)

	var resp struct {
		Credits      float64 `json:"credits"`
		CycleCredits float64 `json:"cycle_credits"`
		CycleStartMs int64   `json:"cycle_start_ms"`
		NextResetMs  int64   `json:"next_reset_ms"`
		Account      *struct {
			Plan                 string  `json:"plan"`
			Tag                  string  `json:"tag"`
			OrgName              string  `json:"org_name"`
			TotalUsagePercentage float64 `json:"total_usage_percentage"`
			UserQuota            *struct {
				Total     float64 `json:"total"`
				Used      float64 `json:"used"`
				Remaining float64 `json:"remaining"`
			} `json:"user_quota"`
			OrgResourcePackage *struct {
				Cap       float64 `json:"cap"`
				Available bool    `json:"available"`
			} `json:"org_resource_package"`
		} `json:"account"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response JSON: %v", err)
	}
	if resp.NextResetMs != reset.UnixMilli() {
		t.Errorf("expected next_reset_ms %d, got %d", reset.UnixMilli(), resp.NextResetMs)
	}
	wantStart := time.Date(reset.Year(), reset.Month()-1, 15, 0, 0, 0, 0, time.Local).UnixMilli()
	if resp.CycleStartMs != wantStart {
		t.Errorf("expected cycle_start_ms %d (one calendar month back), got %d", wantStart, resp.CycleStartMs)
	}
	if resp.CycleCredits < 1.25-1e-9 || resp.CycleCredits > 1.25+1e-9 {
		t.Errorf("expected cycle_credits 1.25, got %v", resp.CycleCredits)
	}
	if resp.Account == nil || resp.Account.Tag != "Teams" || resp.Account.Plan != "PLAN_TIER_TEAM" {
		t.Errorf("expected account metadata in the response, got %+v", resp.Account)
	}
	if resp.Account.UserQuota == nil || resp.Account.UserQuota.Total != 3000 || resp.Account.UserQuota.Used != 2939 || resp.Account.UserQuota.Remaining != 61 {
		t.Errorf("expected authoritative user quota in the response, got %+v", resp.Account.UserQuota)
	}
	if resp.Account.OrgResourcePackage == nil || resp.Account.OrgResourcePackage.Cap != 4000 || resp.Account.OrgResourcePackage.Available {
		t.Errorf("expected organization resource package in the response, got %+v", resp.Account.OrgResourcePackage)
	}
}

// The per-model and per-hour credit breakdown must reach the panel through the
// stats endpoint; the UI renders it directly and has no other channel for it.
func TestStatsEndpointExposesCreditBreakdown(t *testing.T) {
	rec := stats.NewRecorder(nil)
	rec.RecordUsage("Qwen3.7-Max", &stats.Usage{Credits: 1.5})
	rec.RecordUsage("Qwen3.7-Max", &stats.Usage{Credits: 0.5})
	rec.RecordUsage("Kimi-K2.7-Code", &stats.Usage{Credits: 3})
	rec.RecordUsage("Kimi-K2.7-Code", &stats.Usage{Credits: 9, NonBillable: true})

	a := New(tempStore(t), func(ctx context.Context) []string { return nil }, rec, nil)
	w := doRequest(t, a.handleStats, "GET", "/admin/api/stats", nil)

	var resp struct {
		Credits        float64 `json:"credits"`
		BilledRequests int64   `json:"billed_requests"`
		AvgCredits     float64 `json:"avg_credits"`
		ByModel        []struct {
			Model      string  `json:"model"`
			Credits    float64 `json:"credits"`
			Billed     int64   `json:"billed"`
			AvgCredits float64 `json:"avg_credits"`
		} `json:"by_model"`
		Hourly []struct {
			Hour    string  `json:"hour"`
			Credits float64 `json:"credits"`
			Billed  int64   `json:"billed"`
		} `json:"hourly"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response JSON: %v", err)
	}
	if resp.BilledRequests != 3 {
		t.Errorf("expected 3 billed requests from the response, got %d", resp.BilledRequests)
	}
	// 5 credits over 3 billed frames: the non-billable 9 must be excluded from
	// both the numerator and the denominator.
	if want := 5.0 / 3; resp.Credits < 5-1e-9 || resp.AvgCredits < want-1e-9 || resp.AvgCredits > want+1e-9 {
		t.Errorf("expected 5 credits at %v average, got credits=%v avg=%v", want, resp.Credits, resp.AvgCredits)
	}

	rows := map[string]float64{}
	for _, m := range resp.ByModel {
		rows[m.Model] = m.Credits
		if m.Model == "Qwen3.7-Max" {
			if m.Credits < 2-1e-9 || m.Billed != 2 {
				t.Errorf("expected Qwen3.7-Max 2 credits over 2 billed, got %+v", m)
			}
			if m.AvgCredits < 1-1e-9 || m.AvgCredits > 1+1e-9 {
				t.Errorf("expected Qwen3.7-Max average 1, got %v", m.AvgCredits)
			}
		}
		if m.Model == "Kimi-K2.7-Code" && (m.Credits < 3-1e-9 || m.Billed != 1) {
			t.Errorf("expected Kimi-K2.7-Code 3 credits over 1 billed (non-billable dropped), got %+v", m)
		}
	}
	if _, ok := rows["Kimi-K2.7-Code"]; !ok {
		t.Errorf("Kimi-K2.7-Code row missing from by_model: %+v", resp.ByModel)
	}

	// The current hour bucket must carry the attributed spend for the chart tip.
	key := time.Now().Format(stats.HourKeyFormat)
	found := false
	for _, h := range resp.Hourly {
		if h.Hour != key {
			continue
		}
		found = true
		if h.Credits < 5-1e-9 || h.Billed != 3 {
			t.Errorf("expected the current hour to carry 5 credits over 3 billed, got %+v", h)
		}
	}
	if !found {
		t.Errorf("the current hour %s is missing from hourly: %+v", key, resp.Hourly)
	}
}

// With no account status ever resolved the cycle fields stay zero and the
// account is omitted, which is what makes the UI fall back to the lifetime
// total instead of rendering a bogus "resetting soon".
func TestStatsEndpointBillingCycleUnknown(t *testing.T) {
	rec := stats.NewRecorder(nil)
	rec.RecordUsage("Qwen3.7-Max", &stats.Usage{Credits: 2})

	a := New(tempStore(t), func(ctx context.Context) []string { return nil }, rec, nil)
	w := doRequest(t, a.handleStats, "GET", "/admin/api/stats", nil)

	var resp struct {
		Credits      float64        `json:"credits"`
		NextResetMs  int64          `json:"next_reset_ms"`
		CycleCredits float64        `json:"cycle_credits"`
		Account      map[string]any `json:"account"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response JSON: %v", err)
	}
	if resp.NextResetMs != 0 {
		t.Errorf("expected no reset boundary, got %d", resp.NextResetMs)
	}
	if resp.Credits < 2-1e-9 || resp.Credits > 2+1e-9 {
		t.Errorf("lifetime credits must still accrue with no cycle known, got %v", resp.Credits)
	}
	if resp.Account != nil {
		t.Errorf("expected account to be omitted, got %+v", resp.Account)
	}
}

// -- Logs --

func TestLogsEndpoint(t *testing.T) {
	s := tempStore(t)
	rec := logs.NewRecorder(nil, 90, 2000)
	rec.Record(&logs.LogEntry{KeyID: "k1", KeyNote: "dev", Model: "Qwen3.7-Max", StatusCode: 200, TotalTokens: 42})
	rec.Record(&logs.LogEntry{KeyID: "k2", Model: "DeepSeek-V4-Pro", StatusCode: 502, ErrorMsg: "upstream: boom"})
	a := New(s, func(ctx context.Context) []string { return nil }, nil, rec)

	w := doRequest(t, a.handleLogs, "GET", "/admin/api/logs", nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Logs     []logs.LogEntry `json:"logs"`
		Total    int             `json:"total"`
		Page     int             `json:"page"`
		PageSize int             `json:"page_size"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response JSON: %v", err)
	}
	if resp.Total != 2 || len(resp.Logs) != 2 {
		t.Fatalf("total=%d len=%d, want 2/2", resp.Total, len(resp.Logs))
	}
	// Newest first
	if resp.Logs[0].Model != "DeepSeek-V4-Pro" {
		t.Errorf("newest entry = %s, want DeepSeek-V4-Pro", resp.Logs[0].Model)
	}
	if resp.Logs[1].KeyNote != "dev" || resp.Logs[1].TotalTokens != 42 {
		t.Errorf("entry metadata lost: %+v", resp.Logs[1])
	}
	if resp.Page != 1 || resp.PageSize != 20 {
		t.Errorf("page=%d page_size=%d, want 1/20", resp.Page, resp.PageSize)
	}
}

func TestLogsEndpointFilters(t *testing.T) {
	s := tempStore(t)
	rec := logs.NewRecorder(nil, 90, 2000)
	rec.Record(&logs.LogEntry{KeyID: "k1", Model: "Qwen3.7-Max", StatusCode: 200})
	rec.Record(&logs.LogEntry{KeyID: "k2", Model: "Qwen3.7-Max", StatusCode: 429, ErrorMsg: "busy"})
	rec.Record(&logs.LogEntry{KeyID: "k1", Model: "DeepSeek-V4-Pro", StatusCode: 200})
	a := New(s, func(ctx context.Context) []string { return nil }, nil, rec)

	get := func(qs string) int {
		w := doRequest(t, a.handleLogs, "GET", "/admin/api/logs"+qs, nil)
		if w.Code != 200 {
			t.Fatalf("GET %s: code %d: %s", qs, w.Code, w.Body.String())
		}
		var resp struct {
			Logs  []logs.LogEntry `json:"logs"`
			Total int             `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("GET %s: bad JSON: %v", qs, err)
		}
		return resp.Total
	}
	if got := get("?model=deepseek"); got != 1 {
		t.Errorf("model filter: total = %d, want 1", got)
	}
	if got := get("?key_id=k1"); got != 2 {
		t.Errorf("key filter: total = %d, want 2", got)
	}
	if got := get("?status=error"); got != 1 {
		t.Errorf("error filter: total = %d, want 1", got)
	}
	if got := get("?status=429"); got != 1 {
		t.Errorf("status filter: total = %d, want 1", got)
	}
	if got := get("?status=bogus"); got != 3 {
		t.Errorf("invalid status filter must be ignored: total = %d, want 3", got)
	}
	if got := get("?page=9&page_size=1"); got != 3 {
		t.Errorf("out-of-range page still reports full total: got %d, want 3", got)
	}
}

func TestLogsEndpointNilRecorder(t *testing.T) {
	a, _ := newAdmin(t) // log recorder is nil
	w := doRequest(t, a.handleLogs, "GET", "/admin/api/logs", nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	// An empty list must serialize as [] (not null) for the frontend.
	if !strings.Contains(w.Body.String(), `"logs":[]`) {
		t.Errorf("expected empty logs array, got: %s", w.Body.String())
	}
}

func TestLogsEndpointMethodNotAllowed(t *testing.T) {
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleLogs, "POST", "/admin/api/logs", nil)
	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestLogsRequireAuth(t *testing.T) {
	s := tempStore(t)
	a := New(s, nil, nil, nil)
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/admin/api/logs", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("expected 401 without a session, got %d", w.Code)
	}
}

// -- Config: log limits --

func TestConfigLogLimitsRoundTrip(t *testing.T) {
	a, s := newAdmin(t)
	w := doRequest(t, a.handleConfig, "POST", "/admin/api/config", map[string]interface{}{
		"host": "", "port": 0, "log_retention_days": 30, "log_max_entries": 500,
	})
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := s.GetLogRetentionDays(); got != 30 {
		t.Errorf("retention = %d, want 30", got)
	}
	if got := s.GetLogMaxEntries(); got != 500 {
		t.Errorf("max entries = %d, want 500", got)
	}

	// GET reports the persisted limits.
	w = doRequest(t, a.handleConfig, "GET", "/admin/api/config", nil)
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["log_retention_days"] != float64(30) || resp["log_max_entries"] != float64(500) {
		t.Errorf("GET config log fields = %v/%v, want 30/500", resp["log_retention_days"], resp["log_max_entries"])
	}

	// Legacy callers that omit the fields keep the current values.
	w = doRequest(t, a.handleConfig, "POST", "/admin/api/config", map[string]interface{}{"host": "", "port": 0})
	if w.Code != 200 {
		t.Fatalf("legacy save: code %d: %s", w.Code, w.Body.String())
	}
	if got := s.GetLogRetentionDays(); got != 30 {
		t.Errorf("retention after legacy save = %d, want 30", got)
	}
}

func TestConfigLogLimitsHotApplyToRecorder(t *testing.T) {
	s := tempStore(t)
	rec := logs.NewRecorder(nil, 90, 2000)
	for i := 0; i < logs.MinMaxEntries+20; i++ {
		rec.Record(&logs.LogEntry{Model: "m", CreatedAt: time.Now().Unix()})
	}
	a := New(s, func(ctx context.Context) []string { return nil }, nil, rec)
	w := doRequest(t, a.handleConfig, "POST", "/admin/api/config", map[string]interface{}{
		"host": "", "port": 0, "log_max_entries": logs.MinMaxEntries,
	})
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// The lowered cap must prune the live recorder immediately.
	if got := rec.Len(); got != logs.MinMaxEntries {
		t.Errorf("recorder len after lowering cap = %d, want %d", got, logs.MinMaxEntries)
	}
}

func TestConfigLogLimitsValidation(t *testing.T) {
	a, _ := newAdmin(t)
	for _, body := range []map[string]interface{}{
		{"host": "", "port": 0, "log_retention_days": 0}, // 0 is fine (keep current)
		{"host": "", "port": 0, "log_retention_days": -5},
		{"host": "", "port": 0, "log_retention_days": 99999},
		{"host": "", "port": 0, "log_max_entries": 10},
		{"host": "", "port": 0, "log_max_entries": 999999},
	} {
		w := doRequest(t, a.handleConfig, "POST", "/admin/api/config", body)
		// 0 means "keep current" and must be accepted; other out-of-range
		// values must be rejected with 400.
		if body["log_retention_days"] == 0 && body["log_max_entries"] == nil {
			if w.Code != 200 {
				t.Errorf("body %v: code %d, want 200", body, w.Code)
			}
			continue
		}
		if w.Code != 400 {
			t.Errorf("body %v: code %d, want 400 (%s)", body, w.Code, w.Body.String())
		}
	}
}

func TestConfigLogLimitsEnvPinned(t *testing.T) {
	t.Setenv("QODER_LOG_RETENTION_DAYS", "7")
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleConfig, "POST", "/admin/api/config", map[string]interface{}{
		"host": "", "port": 0, "log_retention_days": 30,
	})
	if w.Code != 400 {
		t.Errorf("expected 400 when env pins log limits, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "QODER_LOG_RETENTION_DAYS") {
		t.Errorf("error should name the pinning env var: %s", w.Body.String())
	}
}

// -- Accounts --

// TestAccountsCRUDFlow 添加（首个自动激活）→ 列表（掩码回显）→ 切换 → 删除守卫的完整链路。
func TestAccountsCRUDFlow(t *testing.T) {
	a, s := newAdmin(t)
	// 首个账号：创建即激活
	w := doRequest(t, a.handleAccounts, "POST", "/admin/api/accounts", map[string]string{"pat": "pt-one-12345678", "note": "一号"})
	if w.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if s.GetPAT() != "pt-one-12345678" {
		t.Errorf("first account must activate, pat=%q", s.GetPAT())
	}
	// 第二个账号：不改变激活
	w = doRequest(t, a.handleAccounts, "POST", "/admin/api/accounts", map[string]string{"pat": "pt-two-12345678", "note": "二号"})
	if w.Code != 201 {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	if s.GetPAT() != "pt-one-12345678" {
		t.Errorf("adding must not move activation, pat=%q", s.GetPAT())
	}
	// 列表：掩码 + active 标记，明文永不回传
	w = doRequest(t, a.handleAccounts, "GET", "/admin/api/accounts", nil)
	var list struct {
		Accounts []struct {
			ID        string `json:"id"`
			PATMasked string `json:"pat_masked"`
			Note      string `json:"note"`
			Active    bool   `json:"active"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(list.Accounts))
	}
	if strings.Contains(w.Body.String(), "pt-one-12345678") || strings.Contains(w.Body.String(), "pt-two-12345678") {
		t.Error("response must not leak plaintext PATs")
	}
	var inactiveID string
	for _, acc := range list.Accounts {
		if acc.Active && acc.PATMasked != "pt-o****5678" {
			t.Errorf("active masked = %q", acc.PATMasked)
		}
		if !acc.Active {
			inactiveID = acc.ID
		}
	}
	// 切换
	w = doRequest(t, a.handleAccountSwitch, "POST", "/admin/api/accounts/switch", map[string]string{"id": inactiveID})
	if w.Code != 200 {
		t.Fatalf("switch failed: %d %s", w.Code, w.Body.String())
	}
	if s.GetPAT() != "pt-two-12345678" {
		t.Errorf("after switch pat=%q, want pt-two-12345678", s.GetPAT())
	}
	// 删除激活中的账号 → 400
	w = doRequest(t, a.handleAccounts, "DELETE", "/admin/api/accounts?id="+inactiveID, nil)
	if w.Code != 400 {
		t.Errorf("deleting active should 400, got %d", w.Code)
	}
	// 切回去再删
	otherID := list.Accounts[0].ID
	if otherID == inactiveID {
		otherID = list.Accounts[1].ID
	}
	doRequest(t, a.handleAccountSwitch, "POST", "/admin/api/accounts/switch", map[string]string{"id": otherID})
	w = doRequest(t, a.handleAccounts, "DELETE", "/admin/api/accounts?id="+inactiveID, nil)
	if w.Code != 200 {
		t.Errorf("delete failed: %d", w.Code)
	}
	if len(s.ListAccounts()) != 1 {
		t.Errorf("expected 1 account after delete")
	}
}

// TestAccountsPostDuplicateActivates 相同 PAT 重复提交视为切换（幂等），返回 200 而非 409。
func TestAccountsPostDuplicateActivates(t *testing.T) {
	a, s := newAdmin(t)
	doRequest(t, a.handleAccounts, "POST", "/admin/api/accounts", map[string]string{"pat": "pt-x", "note": "a"})
	doRequest(t, a.handleAccounts, "POST", "/admin/api/accounts", map[string]string{"pat": "pt-y", "note": "b"})
	w := doRequest(t, a.handleAccounts, "POST", "/admin/api/accounts", map[string]string{"pat": "pt-x", "note": "again"})
	if w.Code != 200 {
		t.Fatalf("duplicate PAT should switch (200), got %d", w.Code)
	}
	if s.GetPAT() != "pt-x" {
		t.Errorf("pat=%q, want pt-x", s.GetPAT())
	}
	if len(s.ListAccounts()) != 2 {
		t.Errorf("duplicate must not add, got %d accounts", len(s.ListAccounts()))
	}
}

// TestAccountsPostDuplicateConflict 不同的管理语义下，重复 PAT 走切换而非报错；
// 这里确认返回体不含明文。
func TestAccountsPostDuplicateNoPlaintextLeak(t *testing.T) {
	a, _ := newAdmin(t)
	doRequest(t, a.handleAccounts, "POST", "/admin/api/accounts", map[string]string{"pat": "pt-secret-value"})
	w := doRequest(t, a.handleAccounts, "POST", "/admin/api/accounts", map[string]string{"pat": "pt-secret-value"})
	if strings.Contains(w.Body.String(), "pt-secret-value") {
		t.Error("response must not leak the plaintext PAT")
	}
}

// TestAccountSwitchUnknown 未知 id 切换 → 404。
func TestAccountSwitchUnknown(t *testing.T) {
	a, _ := newAdmin(t)
	w := doRequest(t, a.handleAccountSwitch, "POST", "/admin/api/accounts/switch", map[string]string{"id": "nope"})
	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// TestAccountNoteEndpoint 备注更新端点。
func TestAccountNoteEndpoint(t *testing.T) {
	a, s := newAdmin(t)
	acc, _ := s.AddAccount("pt-note", "")
	w := doRequest(t, a.handleAccountNote, "POST", "/admin/api/accounts/note", map[string]string{"id": acc.ID, "note": "测试备注"})
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if s.ListAccounts()[0].Note != "测试备注" {
		t.Errorf("note = %q", s.ListAccounts()[0].Note)
	}
	w = doRequest(t, a.handleAccountNote, "POST", "/admin/api/accounts/note", map[string]string{"id": "nope", "note": "x"})
	if w.Code != 404 {
		t.Errorf("unknown id should 404, got %d", w.Code)
	}
}

// TestAccountCheckEndpoint 检测端点：成功时快照落库回传；失败时错误进快照（LastError）。
func TestAccountCheckEndpoint(t *testing.T) {
	a, s := newAdmin(t)
	acc, _ := s.AddAccount("pt-check", "")
	calls := 0
	a.SetAccountChecker(func(ctx context.Context, pat, region string) (*store.AccountSnapshot, error) {
		calls++
		if pat != "pt-check" {
			t.Errorf("checker pat = %q", pat)
		}
		return &store.AccountSnapshot{Remaining: 10, Total: 100, CheckedAt: 1700000000}, nil
	})
	w := doRequest(t, a.handleAccountCheck, "POST", "/admin/api/accounts/check", map[string]string{"id": acc.ID})
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if calls != 1 {
		t.Errorf("checker calls = %d", calls)
	}
	snap := s.ListAccounts()[0].Snapshot
	if snap == nil || snap.Remaining != 10 {
		t.Errorf("snapshot not persisted: %+v", snap)
	}
	// checker 失败：502 且 LastError 记入快照
	a.SetAccountChecker(func(ctx context.Context, pat, region string) (*store.AccountSnapshot, error) {
		return nil, errors.New("会话失败")
	})
	w = doRequest(t, a.handleAccountCheck, "POST", "/admin/api/accounts/check", map[string]string{"id": acc.ID})
	if w.Code != 502 {
		t.Errorf("expected 502, got %d", w.Code)
	}
	snap = s.ListAccounts()[0].Snapshot
	if snap == nil || snap.LastError != "会话失败" {
		t.Errorf("LastError not recorded: %+v", snap)
	}
	// 未知账号 → 404
	w = doRequest(t, a.handleAccountCheck, "POST", "/admin/api/accounts/check", map[string]string{"id": "nope"})
	if w.Code != 404 {
		t.Errorf("unknown id should 404, got %d", w.Code)
	}
	// 未装 checker → 503
	a2, s2 := newAdmin(t)
	acc2, _ := s2.AddAccount("pt-x", "")
	w = doRequest(t, a2.handleAccountCheck, "POST", "/admin/api/accounts/check", map[string]string{"id": acc2.ID})
	if w.Code != 503 {
		t.Errorf("nil checker should 503, got %d", w.Code)
	}
}

// TestPATPostStillMirrorsPool 旧 PAT 端点的兼容语义：新 PAT 写入后自动进池并激活；
// 清空 PAT 时若池还有账号则仅取消激活（不动池），镜像随 SetPAT 落空。
func TestPATPostStillMirrorsPool(t *testing.T) {
	a, s := newAdmin(t)
	// 新 PAT（池为空）→ 添加并激活
	w := doRequest(t, a.handlePAT, "POST", "/admin/api/pat", map[string]string{"pat": "pt-via-pat"})
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(s.ListAccounts()) != 1 || s.GetActiveAccountID() == "" {
		t.Fatalf("PAT write must sync the pool: %+v", s.ListAccounts())
	}
	// 换成池外新 PAT → 追加账号并激活
	doRequest(t, a.handlePAT, "POST", "/admin/api/pat", map[string]string{"pat": "pt-via-pat-2"})
	if len(s.ListAccounts()) != 2 || s.GetPAT() != "pt-via-pat-2" {
		t.Fatalf("second PAT write must add+activate: %+v pat=%q", s.ListAccounts(), s.GetPAT())
	}
	// 清空 PAT → 取消激活，池保留
	doRequest(t, a.handlePAT, "POST", "/admin/api/pat", map[string]string{"pat": ""})
	if s.GetActiveAccountID() != "" || len(s.ListAccounts()) != 2 {
		t.Errorf("clearing PAT must deactivate but keep the pool: active=%q n=%d",
			s.GetActiveAccountID(), len(s.ListAccounts()))
	}
}

// TestAutoSwitchEndpoint 自动切换设置的读写与校验。
func TestAutoSwitchEndpoint(t *testing.T) {
	a, s := newAdmin(t)
	// 默认值
	w := doRequest(t, a.handleAutoSwitch, "GET", "/admin/api/accounts/auto", nil)
	var resp struct {
		Enabled   bool    `json:"enabled"`
		Threshold float64 `json:"threshold"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Enabled || resp.Threshold != store.DefaultAutoSwitchThreshold {
		t.Errorf("default = %+v", resp)
	}
	// 保存
	w = doRequest(t, a.handleAutoSwitch, "POST", "/admin/api/accounts/auto",
		map[string]interface{}{"enabled": true, "threshold": 30})
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// 回读
	w = doRequest(t, a.handleAutoSwitch, "GET", "/admin/api/accounts/auto", nil)
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Enabled || resp.Threshold != 30 {
		t.Errorf("after save = %+v", resp)
	}
	if as := s.GetAutoSwitch(); !as.Enabled || as.Threshold != 30 {
		t.Errorf("store = %+v", as)
	}
	// 非法阈值
	w = doRequest(t, a.handleAutoSwitch, "POST", "/admin/api/accounts/auto",
		map[string]interface{}{"enabled": true, "threshold": 0})
	if w.Code != 400 {
		t.Errorf("threshold 0 should 400, got %d", w.Code)
	}
	// 方法不允许
	w = doRequest(t, a.handleAutoSwitch, "DELETE", "/admin/api/accounts/auto", nil)
	if w.Code != 405 {
		t.Errorf("DELETE should 405, got %d", w.Code)
	}
}

// TestAccountsRegionRoundTrip 添加时携带区域，列表回传区域；默认国内。
func TestAccountsRegionRoundTrip(t *testing.T) {
	a, s := newAdmin(t)
	w := doRequest(t, a.handleAccounts, "POST", "/admin/api/accounts", map[string]string{"pat": "pt-global-x", "note": "海外", "region": "global"})
	if w.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if got := s.AccountRegionByPAT("pt-global-x"); got != "global" {
		t.Errorf("stored region = %q, want global", got)
	}
	w = doRequest(t, a.handleAccounts, "POST", "/admin/api/accounts", map[string]string{"pat": "pt-cn-account", "note": "国内"})
	if w.Code != 201 {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	w = doRequest(t, a.handleAccounts, "GET", "/admin/api/accounts", nil)
	var list struct {
		Accounts []struct {
			PATMasked string `json:"pat_masked"`
			Region    string `json:"region"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	regions := map[string]string{}
	for _, acc := range list.Accounts {
		regions[acc.PATMasked] = acc.Region
	}
	if regions["pt-g****al-x"] != "global" {
		t.Errorf("global account region view = %q", regions["pt-g****al-x"])
	}
	if regions["pt-c****ount"] != "cn" {
		t.Errorf("cn account region view = %q", regions["pt-c****ount"])
	}
}

// TestAccountCheckPassesRegion 检测端点必须把账号区域透传给 checker。
func TestAccountCheckPassesRegion(t *testing.T) {
	a, s := newAdmin(t)
	acc, _ := s.AddAccountWithRegion("pt-global-check", "", "global")
	var gotRegion string
	a.SetAccountChecker(func(ctx context.Context, pat, region string) (*store.AccountSnapshot, error) {
		gotRegion = region
		return &store.AccountSnapshot{Remaining: 5, Total: 10, CheckedAt: 1700000000}, nil
	})
	w := doRequest(t, a.handleAccountCheck, "POST", "/admin/api/accounts/check", map[string]string{"id": acc.ID})
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if gotRegion != "global" {
		t.Errorf("checker region = %q, want global", gotRegion)
	}
}
