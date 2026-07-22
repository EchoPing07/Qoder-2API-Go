package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qoder2api/models"
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
	return New(s, mf), s
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
