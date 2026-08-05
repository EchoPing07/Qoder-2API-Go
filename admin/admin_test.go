package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["pat"] != "pt-test123" {
		t.Errorf("expected 'pt-test123', got %q", resp["pat"])
	}
	if resp["pat_masked"] == "" {
		t.Error("expected non-empty pat_masked")
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
	rec.Record("Qwen3.7-Max", true, true)
	rec.Record("Qwen3.7-Max", false, true)
	rec.Record("DeepSeek-V4-Pro", true, false)
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
	if !strings.Contains(w.Body.String(), "<!DOCTYPE html>") {
		t.Error("expected HTML doctype")
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
