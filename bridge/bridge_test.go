package bridge

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatRouteReturns401WithoutBearerToken(t *testing.T) {
	handler := MakeChatHandler(nil)

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
	handler := MakeChatHandler(resolver)

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
