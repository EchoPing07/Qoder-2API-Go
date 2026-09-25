package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qoder2api/auth"
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
	return New(s, mf, nil), s
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
	a := New(s, mf, rec)

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

func TestServeIndex(t *testing.T) {
	a, _ := newAdmin(t)
	req := httptest.NewRequest("GET", "/admin", nil)
	w := httptest.NewRecorder()
	a.serveIndex(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("expected HTML doctype")
	}
	for _, want := range []string{`id="quotaCard"`, `id="quotaTable"`, "renderQuotaDetails(d)", "组织资源包"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected quota detail UI marker %q", want)
		}
	}
}

// The panel's headline must reflect the allowance the account actually holds.
// A free account reports userQuota.total=0 while the usable budget sits in
// addOnQuota; keying the headline off userQuota rendered "套餐 8 / 0 · 剩 0"
// for an account that still had credits, and hid the pool that really mattered.
func TestQuotaSourceSelectionIgnoresEmptyPools(t *testing.T) {
	start := strings.Index(indexHTML, "function quotaPools(acct) {")
	if start < 0 {
		t.Fatal("quotaPools not found in the embedded UI")
	}
	end := strings.Index(indexHTML[start:], "\n/*\n * Prefer the authoritative")
	if end < 0 {
		t.Fatal("could not delimit quotaPools")
	}
	pools := indexHTML[start : start+end]

	// A zero-total pool carries no allowance and must not be offered as one.
	for _, guard := range []string{
		"acct.user_quota && Number(acct.user_quota.total || 0) > 0",
		"acct.add_on_quota && Number(acct.add_on_quota.total || 0) > 0",
		"acct.org_resource_package && Number(acct.org_resource_package.cap || 0) > 0",
	} {
		if !strings.Contains(pools, guard) {
			t.Errorf("quotaPools must skip zero-capacity pools; missing guard %q", guard)
		}
	}

	// Both renderers must share that selection instead of hand-rolling their
	// own, otherwise the headline and the detail card can disagree.
	renderCredits := indexHTML[strings.Index(indexHTML, "function renderCredits(d) {"):]
	renderCredits = renderCredits[:strings.Index(renderCredits, "function renderQuotaDetails(d) {")]
	if !strings.Contains(renderCredits, "quotaPools(acct)") {
		t.Error("renderCredits must select its pools via quotaPools(acct)")
	}
	if strings.Contains(renderCredits, "acct.user_quota ||") {
		t.Error("renderCredits must not hardcode user_quota as the headline source")
	}
}

// quotaDetailsSource returns the renderQuotaDetails body from the embedded UI,
// delimited by the next top-level function.
func quotaDetailsSource(t *testing.T) string {
	t.Helper()
	start := strings.Index(indexHTML, "function renderQuotaDetails(d) {")
	if start < 0 {
		t.Fatal("renderQuotaDetails not found in the embedded UI")
	}
	end := strings.Index(indexHTML[start:], "function makeBadge(")
	if end < 0 {
		t.Fatal("could not delimit renderQuotaDetails")
	}
	return indexHTML[start : start+end]
}

// The 合计剩余 summary must not absorb credits the account cannot spend: an
// unavailable org_resource_package still gets a row marked 不可用, so counting
// its remaining credits made the summary contradict the row right above it.
func TestQuotaSummaryOnlyCountsAvailablePools(t *testing.T) {
	details := quotaDetailsSource(t)

	guard := "if (pool.available) totalRemaining += remaining;"
	if !strings.Contains(details, guard) {
		t.Errorf("the summary must accumulate only available pools; missing %q", guard)
	}
	// Any other accumulation must carry the same availability guard.
	for _, line := range strings.Split(details, "\n") {
		if strings.Contains(line, "totalRemaining += remaining") && !strings.Contains(line, "pool.available") {
			t.Errorf("totalRemaining accumulated without the availability guard: %q", strings.TrimSpace(line))
		}
	}
}

// fmtCredits never returns a negative string and total comes from
// Number(pool.quota[...] || 0), so the '—' branch of the total ternary was
// unreachable. The cell must render fmtCredits(total) directly.
func TestQuotaDetailsHasNoDeadTotalBranch(t *testing.T) {
	details := quotaDetailsSource(t)

	for _, dead := range []string{"total >= 0 ?", "? fmtCredits(total) : '—'"} {
		if strings.Contains(details, dead) {
			t.Errorf("dead total branch %q must be removed from renderQuotaDetails", dead)
		}
	}
	if !strings.Contains(details, "' / ' + fmtCredits(total) + '</td>'") {
		t.Error("renderQuotaDetails must render the total via fmtCredits(total)")
	}
}

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
	rec.RecordUsage(&stats.Usage{PromptTokens: 5, Credits: 1.25})

	a := New(tempStore(t), func(ctx context.Context) []string { return nil }, rec)
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

// With no account status ever resolved the cycle fields stay zero and the
// account is omitted, which is what makes the UI fall back to the lifetime
// total instead of rendering a bogus "resetting soon".
func TestStatsEndpointBillingCycleUnknown(t *testing.T) {
	rec := stats.NewRecorder(nil)
	rec.RecordUsage(&stats.Usage{Credits: 2})

	a := New(tempStore(t), func(ctx context.Context) []string { return nil }, rec)
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
