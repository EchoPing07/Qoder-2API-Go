package bridge

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"qoder2api/stats"
)

func TestChatRouteReturns401WithoutBearerToken(t *testing.T) {
	handler := MakeChatHandler(nil, nil)

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen3.7-Max","messages":[]}`))
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid_request_error") {
		t.Error("expected invalid_request_error type")
	}
}

func TestChatRouteRejectsInvalidApiKey(t *testing.T) {
	resolver := func(apiKey string) *OpenAiBridge { return nil }
	handler := MakeChatHandler(resolver, nil)

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen3.7-Max","messages":[]}`))
	req.Header.Set("Authorization", "Bearer sk-invalid")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestModelsRouteReturnsWithNilResolver(t *testing.T) {
	handler := MakeModelsHandler(nil)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Qwen3.7-Max") {
		t.Error("response should contain Qwen3.7-Max")
	}
}

func TestModelsRouteReturnsAllModels(t *testing.T) {
	// With a nil resolver, models handler returns the default catalog
	handler := MakeModelsHandler(nil)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Qwen3.7-Max") {
		t.Error("response should contain Qwen3.7-Max")
	}
	if !strings.Contains(body, "Qwen3.7-Plus") {
		t.Error("response should contain Qwen3.7-Plus")
	}
}

// Unauthorized requests (missing/invalid key) must not be counted.
func TestChatRouteDoesNotRecordUnauthorized(t *testing.T) {
	rec := stats.NewRecorder(nil)
	handler := MakeChatHandler(nil, rec)

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen3.7-Max","messages":[]}`))
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401, got %d", w.Code)
	}
	if rec.Report().Total != 0 {
		t.Error("unauthorized requests must not be counted")
	}
}

// Malformed JSON with a valid key must be rejected and not counted.
func TestChatRouteDoesNotRecordInvalidJSON(t *testing.T) {
	rec := stats.NewRecorder(nil)
	resolver := func(apiKey string) *OpenAiBridge { return &OpenAiBridge{} }
	handler := MakeChatHandler(resolver, rec)

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model": `))
	req.Header.Set("Authorization", "Bearer sk-valid")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if rec.Report().Total != 0 {
		t.Error("malformed JSON requests must not be counted")
	}
}

// Model labels for stats must be capped and have sane fallbacks.
func TestStatsModelLabel(t *testing.T) {
	ctx := context.Background()
	long := strings.Repeat("A", 5000)
	got := statsModelLabel(map[string]interface{}{"model": long}, nil, ctx)
	if len(got) != 64 {
		t.Errorf("expected label capped to 64 chars, got %d", len(got))
	}
	got = statsModelLabel(map[string]interface{}{}, nil, ctx)
	if got != "(default)" {
		t.Errorf("expected fallback label '(default)', got %q", got)
	}
	got = statsModelLabel(map[string]interface{}{"model": 42}, nil, ctx)
	if got != "(default)" {
		t.Errorf("non-string model should fall back to '(default)', got %q", got)
	}
	got = statsModelLabel(map[string]interface{}{"model": "Qwen3.7-Max"}, nil, ctx)
	if got != "Qwen3.7-Max" {
		t.Errorf("expected model name preserved, got %q", got)
	}
}

func TestNewRequestBody(t *testing.T) {
	body := newRequestBody()
	if body.Stream != true {
		t.Error("Stream should be true")
	}
	if body.ChatTask != "FREE_INPUT" {
		t.Error("ChatTask should be FREE_INPUT")
	}
	if body.ModelConfig.Key != "lite" {
		t.Error("ModelConfig.Key should be lite")
	}
	if body.ModelConfig.Source != "system" {
		t.Error("ModelConfig.Source should be system")
	}
	if body.Source != 1 {
		t.Error("Source should be 1")
	}
	if body.Version != "3" {
		t.Error("Version should be 3")
	}
}
