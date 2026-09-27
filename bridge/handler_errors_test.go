// HTTP-layer error mapping tests for the streaming/sync rejection paths:
// upstream status passthrough, Retry-After forwarding, the wrote boundary
// (committed SSE vs uncommitted JSON error), upstream auth faults, verbatim
// number decoding, and max_thinking_tokens forwarding.
package bridge

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"qoder2api/auth"
)

// The same rejection mapping must hold for stream=true requests as long as no
// content was committed: the client (and every SDK retry policy) reads the HTTP
// status, and a 200 SSE stream whose first frame is an error reads as success.
// This is the streaming counterpart of TestMakeChatHandlerMapsUpstreamErrors.
func TestChatRouteStreamUpstreamRejectionReturnsRealStatus(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode int
	}{
		{"400 parameter rejection", 400, `{"code":"invalid_parameter_error","message":"Range of max_tokens validation failed"}`, 400},
		{"404 unknown model", 404, `{"code":"not_found","message":"model not found"}`, 400},
		{"429 rate limit with Retry-After", 429, `{"code":"too_many_requests","message":"slow down"}`, 429},
		{"500 gateway fault", 500, `{"code":"internal_error","message":"boom"}`, 502},
		{"503 gateway unavailable", 503, "service unavailable", 502},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retryAfter := ""
			if tc.status == 429 {
				retryAfter = "7"
			}
			gw := &rejectingGateway{status: tc.status, body: tc.body, retryAfter: retryAfter}
			b := newRejectingBridge(t, gw)
			handler := MakeChatHandler(func(string) *ResolvedBridge { return &ResolvedBridge{Bridge: b} }, nil, nil)

			req := httptest.NewRequest("POST", "/v1/chat/completions",
				strings.NewReader(`{"model":"Fake-Model","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer sk-test")
			w := httptest.NewRecorder()
			handler(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("streaming status = %d, want %d (no content committed yet)\n%s", w.Code, tc.wantCode, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
				t.Errorf("Content-Type = %q, want a JSON error body, not SSE", ct)
			}
			if tc.status == 429 {
				if ra := w.Header().Get("Retry-After"); ra != "7" {
					t.Errorf("Retry-After = %q, want 7", ra)
				}
			}
		})
	}
}

// Once content has been committed to the streaming client, the status can no
// longer change: the remaining failure is delivered as an SSE error chunk and
// the HTTP status stays 200. Guards the wrote boundary against regressing to
// raw errors that would attempt a second response on a committed writer.
func TestChatRouteStreamErrorAfterContentKeepsSSE(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "partial"}}}}),
	}}
	b := newFakeBridge(t, gw)
	handler := MakeChatHandler(func(string) *ResolvedBridge { return &ResolvedBridge{Bridge: b} }, nil, nil)

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"Fake-Model","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (content already committed)\n%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "partial") {
		t.Errorf("delivered content must remain in the stream:\n%s", body)
	}
	if !strings.Contains(body, "error") {
		t.Error("SSE error chunk must be present")
	}
	if strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Error("failed stream must not terminate with [DONE]")
	}
}

// A gateway 401/403 means OUR session/PAT credentials were rejected, not the
// client's API key (which the resolver already validated). It must surface as
// 502 bad-gateway, never as a client-facing 401 that would make every client
// believe its own key is invalid.
func TestChatRouteMapsUpstreamAuthErrorTo502(t *testing.T) {
	gw := &rejectingGateway{status: 401, body: `{"code":"401","message":"session token expired"}`}
	b := newRejectingBridge(t, gw)
	handler := MakeChatHandler(func(string) *ResolvedBridge { return &ResolvedBridge{Bridge: b} }, nil, nil)

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"Fake-Model","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != 502 {
		t.Fatalf("status = %d, want 502 (upstream auth fault)\n%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Error("no content was delivered; the response must be a JSON error, not SSE")
	}
	if !strings.Contains(w.Body.String(), "upstream auth") {
		t.Errorf("body should name the auth fault:\n%s", w.Body.String())
	}
}

// Verbatim numbers must survive the production decode path, not just maps
// built directly in tests: the handler decodes with UseNumber, so a value
// beyond float64 precision (2^53+1) and a long fraction reach the gateway
// request with their exact JSON token intact.
func TestChatRouteForwardsNumbersVerbatimThroughDecoder(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{"data: [DONE]"}}
	b := newFakeBridge(t, gw)
	handler := MakeChatHandler(func(string) *ResolvedBridge { return &ResolvedBridge{Bridge: b} }, nil, nil)

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
		`{"model":"Fake-Model","messages":[{"role":"user","content":"hi"}],`+
			`"max_tokens":9007199254740993,"temperature":0.1234567890123456789,"n":1}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d\n%s", w.Code, w.Body.String())
	}

	plain, err := auth.Decode(string(gw.lastChatBody()))
	if err != nil {
		t.Fatalf("decode signed gateway request: %v", err)
	}
	// Compare against the raw JSON text: re-decoding into interface{} would
	// itself go through float64 and hide the very rounding this guards against.
	var payload struct {
		Parameters struct {
			MaxTokens   json.Number `json:"max_tokens"`
			Temperature json.Number `json:"temperature"`
			N           json.Number `json:"n"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("decode gateway request: %v", err)
	}
	if got := payload.Parameters.MaxTokens.String(); got != "9007199254740993" {
		t.Errorf("parameters.max_tokens = %s, want the exact token 9007199254740993 (2^53+1 must not round through float64)", got)
	}
	if got := payload.Parameters.Temperature.String(); got != "0.1234567890123456789" {
		t.Errorf("parameters.temperature = %s, want the exact token 0.1234567890123456789", got)
	}
	if got := payload.Parameters.N.String(); got != "1" {
		t.Errorf("parameters.n = %s, want 1", got)
	}
}

// max_thinking_tokens is a gateway-defined parameter like any other: a
// client-supplied value is forwarded verbatim (even alongside effort=none,
// where it overrides the synthesized off-switch — the gateway judges), and a
// wrong JSON type is a local 400.
func TestHandleChatForwardsMaxThinkingTokens(t *testing.T) {
	cases := []struct {
		name         string
		request      string
		wantThinking string // exact JSON token expected in parameters
	}{
		{"explicit budget is forwarded verbatim",
			`{"model":"Fake-Model","reasoning_effort":"high","max_thinking_tokens":2048,` +
				`"messages":[{"role":"user","content":"hi"}]}`, "2048"},
		{"client budget overrides the none off-switch",
			`{"model":"Fake-Model","reasoning_effort":"none","max_thinking_tokens":4096,` +
				`"messages":[{"role":"user","content":"hi"}]}`, "4096"},
		{"huge magnitude keeps its exact token",
			`{"model":"Fake-Model","max_thinking_tokens":1e18,` +
				`"messages":[{"role":"user","content":"hi"}]}`, "1e18"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gw := &fakeGateway{chatLines: []string{"data: [DONE]"}}
			b := newFakeBridge(t, gw)
			var reqBody map[string]interface{}
			dec := json.NewDecoder(strings.NewReader(tc.request))
			dec.UseNumber()
			if err := dec.Decode(&reqBody); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if err := b.HandleChat(context.Background(), httptest.NewRecorder(), reqBody, nil); err != nil {
				t.Fatalf("HandleChat: %v", err)
			}
			plain, err := auth.Decode(string(gw.lastChatBody()))
			if err != nil {
				t.Fatalf("decode signed gateway request: %v", err)
			}
			var payload struct {
				Parameters struct {
					MaxThinkingTokens json.Number `json:"max_thinking_tokens"`
				} `json:"parameters"`
			}
			if err := json.Unmarshal(plain, &payload); err != nil {
				t.Fatalf("decode gateway request: %v", err)
			}
			if got := payload.Parameters.MaxThinkingTokens.String(); got != tc.wantThinking {
				t.Errorf("parameters.max_thinking_tokens = %q, want %q", got, tc.wantThinking)
			}
		})
	}

	// Type errors are a local 400, not a silent drop.
	gw := &fakeGateway{chatLines: []string{"data: [DONE]"}}
	b := newFakeBridge(t, gw)
	handler := MakeChatHandler(func(string) *ResolvedBridge { return &ResolvedBridge{Bridge: b} }, nil, nil)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
		`{"model":"Fake-Model","max_thinking_tokens":"2048","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != 400 {
		t.Fatalf("type error status = %d, want 400\n%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "max_thinking_tokens") {
		t.Errorf("error must name the field:\n%s", w.Body.String())
	}
}
