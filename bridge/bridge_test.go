package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"qoder2api/auth"
	"qoder2api/models"
	"qoder2api/stats"
	"qoder2api/transform"
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

// --- Regression tests for review fixes ---

func TestTruncateRunesNeverSplitsUTF8(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"你好世界", 3, "你好世"},
		{"你好世界", 10, "你好世界"},
		{"abc", 0, ""},
		{"", 5, ""},
		{"a你b", 2, "a你"},
	}
	for _, c := range cases {
		if got := truncateRunes(c.in, c.n); got != c.want {
			t.Errorf("truncateRunes(%q,%d)=%q want %q", c.in, c.n, got, c.want)
		}
	}
	// The result must always be valid UTF-8.
	for _, in := range []string{"你好世界abcdefg", "日本語テキスト"} {
		got := truncateRunes(in, 4)
		if !utf8.ValidString(got) {
			t.Errorf("truncateRunes(%q,4) produced invalid UTF-8: %q", in, got)
		}
	}
}

// expireTimeMs == 0 (gateway did not report an expiry) must not force a
// renewal network call on every request: after applyJobToken the bridge
// trusts the session for unknownExpiryTTL.
func TestNeedsRefreshWithUnknownExpiry(t *testing.T) {
	b := NewOpenAiBridge("pat", nil)
	if !b.needsRefresh() {
		t.Fatal("fresh bridge must need refresh (no session)")
	}
	// An empty resolved tier falls back to the jobToken value, matching the
	// pre-user/status behaviour.
	b.applyJobToken(map[string]interface{}{
		"name": "u", "id": "1", "userType": "personal_standard",
		"refreshToken": "r", "securityOauthToken": "s",
		"expireTime": nil, // missing expiry
	}, "")
	if b.needsRefresh() {
		t.Error("missing expireTime must fall back to TTL-based freshness, not always-refresh")
	}
	b.mu.Lock()
	b.refreshedAtMs -= (unknownExpiryTTL + time.Minute).Milliseconds()
	b.mu.Unlock()
	if !b.needsRefresh() {
		t.Error("stale unknown-expiry session must need refresh")
	}
}

// currentIdentity must return the post-refresh identity snapshot taken
// under the lock (unlocked b.identity reads were a data race).
func TestCurrentIdentitySnapshot(t *testing.T) {
	b := NewOpenAiBridge("pat", nil)
	if b.currentIdentity() != nil {
		t.Fatal("expected nil identity before bootstrap")
	}
	b.applyJobToken(map[string]interface{}{"name": "u", "id": "1", "expireTime": float64(1e15)}, "")
	id := b.currentIdentity()
	if id == nil || id.Name != "u" {
		t.Fatalf("expected identity snapshot, got %+v", id)
	}
}

// --- SSE end-to-end: fake gateway wired through RegionConfig ---

// fakeGateway serves the auth + chat endpoints with canned SSE frames
// (frames are pre-encoded: {"body": "<json string>"}, matching the real
// gateway's Encode=1 wire format which OpenStreamLines decodes upstream —
// here the chat path is driven through OpenAiBridge.HandleChat with a
// region pointing at this test server).
type fakeGateway struct {
	chatLines []string // raw SSE lines ("data: {...}")

	mu       sync.Mutex
	chatBody []byte
}

func (g *fakeGateway) lastChatBody() []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]byte(nil), g.chatBody...)
}

func newFakeBridge(t *testing.T, gw *fakeGateway) *OpenAiBridge {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/algo/api/v3/user/jobToken", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"name":"tester","id":"uid1","userType":"personal_standard","refreshToken":"rt","securityOauthToken":"sot","expireTime":%d}`, time.Now().Add(6*time.Hour).UnixMilli())
	})
	mux.HandleFunc("/algo/api/v2/model/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Fixture deliberately mirrors the live catalog shape: thinking metadata
		// is nested under thinking_config, not flattened onto the entry. A flat
		// fixture is what let the original parser pass tests while reading a
		// field path the gateway never emits.
		io.WriteString(w, `{"chat":[{"key":"qmodel_latest","display_name":"Fake-Model","enable":true,"is_vl":false,"is_reasoning":true,"max_output_tokens":4096,"thinking_config":{"disabled":{},"enabled":{"efforts":{"low":{},"medium":{"is_default":true},"xhigh":{}},"is_default":true}}}]}`)
	})
	mux.HandleFunc("/algo/api/v2/service/pro/sse/agent_chat_generation", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gw.mu.Lock()
		gw.chatBody = body
		gw.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, l := range gw.chatLines {
			fmt.Fprintf(w, "%s\n\n", l)
			flusher.Flush()
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	region := &auth.RegionConfig{Name: "test", AuthBase: srv.URL, ChatBase: srv.URL}
	return NewOpenAiBridge("pt-test", region)
}

func sseFrame(t *testing.T, inner map[string]interface{}) string {
	t.Helper()
	b, _ := json.Marshal(inner)
	line, _ := json.Marshal(map[string]interface{}{"body": string(b)})
	return "data: " + string(line)
}

// Streaming path: content deltas flow as OpenAI chunks; usage frame is
// emitted before [DONE]; tool calls keep upstream indices.
func TestHandleStreamEndToEnd(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"role": "assistant"}}}}),
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "Hel"}}}}),
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "lo"}}}}),
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"tool_calls": []interface{}{map[string]interface{}{"index": float64(7), "function": map[string]interface{}{"name": "f", "arguments": "{}"}}}}}}}),
		sseFrame(t, map[string]interface{}{"choices": []interface{}{}, "usage": map[string]interface{}{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5, "credits": 0.1}}),
		"data: [DONE]",
	}}
	b := newFakeBridge(t, gw)

	var usageSeen *stats.Usage
	w := httptest.NewRecorder()
	err := b.HandleChat(context.Background(), w, map[string]interface{}{
		"model": "Fake-Model",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "hi"},
		},
		"stream": true,
	}, func(u *transform.Usage) { usageSeen = &stats.Usage{PromptTokens: u.PromptTokens, Credits: u.Credits} })
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}
	body := w.Body.String()
	for _, want := range []string{"Hel", "lo", "f", "\"index\":7"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream body missing %q\n%s", want, body)
		}
	}
	if !strings.Contains(body, "\"prompt_tokens\":3") {
		t.Errorf("usage frame missing from stream:\n%s", body)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Error("stream must end with [DONE]")
	}
	if !strings.Contains(body, "\"finish_reason\":\"tool_calls\"") {
		t.Error("tool call stream must finish with tool_calls")
	}
	if usageSeen == nil || usageSeen.PromptTokens != 3 {
		t.Errorf("usage sink not invoked correctly: %+v", usageSeen)
	}
}

// Non-streaming path: content + tool calls + usage are assembled into a
// single chat.completion response.
func TestHandleSyncEndToEnd(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"reasoning_content": "thinking"}}}}),
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "answer"}}}}),
		sseFrame(t, map[string]interface{}{"choices": []interface{}{}, "usage": map[string]interface{}{"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14, "credits": 0.2}}),
		"data: [DONE]",
	}}
	b := newFakeBridge(t, gw)

	w := httptest.NewRecorder()
	err := b.HandleChat(context.Background(), w, map[string]interface{}{
		"model":    "Fake-Model",
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "q"}},
	}, nil)
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}
	body := w.Body.String()
	for _, want := range []string{`"content":"answer"`, `"reasoning_content":"thinking"`, `"total_tokens":14`, `"finish_reason":"stop"`} {
		if !strings.Contains(body, want) {
			t.Errorf("sync body missing %s\n%s", want, body)
		}
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
}

// An upstream error mid-stream (after content) is delivered as an SSE error
// chunk and reported as StreamReportedError, so the outer handler does not
// write a second error response.
func TestHandleStreamUpstreamErrorAfterContent(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "partial"}}}}),
	}}
	b := newFakeBridge(t, gw)

	w := httptest.NewRecorder()
	err := b.HandleChat(context.Background(), w, map[string]interface{}{
		"model":    "Fake-Model",
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		"stream":   true,
	}, nil)
	if err == nil {
		t.Fatal("expected error from truncated stream")
	}
	var repErr *StreamReportedError
	if !errors.As(err, &repErr) {
		t.Fatalf("expected StreamReportedError, got %T: %v", err, err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "partial") {
		t.Error("delivered content must remain in the stream")
	}
	if !strings.Contains(body, "error") {
		t.Error("error chunk must be present")
	}
	if strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Error("failed stream must not terminate with [DONE]")
	}
}

// Regression: the real Qoder gateway terminates its SSE stream with [DONE]
// wrapped inside the Encode-layer envelope (data: {"body":"[DONE]",...}),
// followed by an "event:finish" line and a telemetry data frame. The bare
// "data: [DONE]" check previously used by OpenStreamLines never matched this
// shape, so every real request failed with "stream ended without [DONE]".
// This test replays the real wire format end-to-end.
func TestHandleStreamRealGatewayDoneFormat(t *testing.T) {
	doneFrame, _ := json.Marshal(map[string]interface{}{"body": "[DONE]"})
	telemetryFrame, _ := json.Marshal(map[string]interface{}{
		"firstTokenDuration": float64(728),
		"serverDuration":     float64(66),
		"totalDuration":      float64(1230),
	})
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "blue"}}}}),
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{}, "finish_reason": "stop"}}}),
		sseFrame(t, map[string]interface{}{"choices": []interface{}{}, "usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}),
		"data: " + string(doneFrame),      // wrapped [DONE] — the real gateway form
		"event:finish",                    // SSE event field, no data: prefix
		"data: " + string(telemetryFrame), // trailing telemetry, no body/choices
	}}
	b := newFakeBridge(t, gw)

	w := httptest.NewRecorder()
	err := b.HandleChat(context.Background(), w, map[string]interface{}{
		"model":    "Fake-Model",
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "sky color"}},
		"stream":   true,
	}, nil)
	if err != nil {
		t.Fatalf("real-format stream must not error, got %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "blue") {
		t.Error("content must be delivered")
	}
	if !strings.Contains(body, "\"finish_reason\":\"stop\"") {
		t.Error("finish_reason stop must be emitted")
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Error("client-facing stream must end with our own bare [DONE]")
	}
}

// Regression (real gateway, GLM-5.3): the final SSE frame carries BOTH
// finish_reason=stop and usage on the same frame (with an empty delta).
// The old mutually-exclusive classification dropped the usage, leaving
// token stats at zero for GLM models while Qwen models (standalone usage
// frame) worked fine. The stream must deliver content, finish_reason and
// the usage chunk, and the usage sink must fire.
func TestHandleStreamGLMStyleUsageOnFinalContentFrame(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "蓝", "role": "assistant"}}}}),
		sseFrame(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{
				"delta":         map[string]interface{}{"content": ""},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{
				"prompt_tokens":             19,
				"completion_tokens":         90,
				"total_tokens":              109,
				"credits":                   0.059,
				"prompt_tokens_details":     map[string]interface{}{"cached_tokens": 3},
				"completion_tokens_details": map[string]interface{}{"reasoning_tokens": 86},
			},
		}),
		"data: [DONE]",
	}}
	b := newFakeBridge(t, gw)

	var usageSeen *stats.Usage
	w := httptest.NewRecorder()
	err := b.HandleChat(context.Background(), w, map[string]interface{}{
		"model": "Fake-Model",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "sky color"},
		},
		"stream": true,
	}, func(u *transform.Usage) {
		usageSeen = &stats.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, CachedTokens: u.CachedPromptTokens(), Credits: u.Credits}
	})
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "蓝") {
		t.Errorf("content missing:\n%s", body)
	}
	if !strings.Contains(body, "\"finish_reason\":\"stop\"") {
		t.Errorf("finish_reason stop missing:\n%s", body)
	}
	if !strings.Contains(body, "\"prompt_tokens\":19") {
		t.Errorf("usage chunk missing:\n%s", body)
	}
	if usageSeen == nil {
		t.Fatal("usage sink not invoked")
	}
	if usageSeen.PromptTokens != 19 || usageSeen.CompletionTokens != 90 || usageSeen.CachedTokens != 3 {
		t.Errorf("usage sink wrong: %+v", usageSeen)
	}
}

// Non-streaming variant of the same GLM-style regression.
func TestHandleSyncGLMStyleUsageOnFinalContentFrame(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "蓝"}}}}),
		sseFrame(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{
				"delta":         map[string]interface{}{},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{"prompt_tokens": 19, "completion_tokens": 90, "total_tokens": 109, "credits": 0.059},
		}),
		"data: [DONE]",
	}}
	b := newFakeBridge(t, gw)

	var usageSeen *stats.Usage
	w := httptest.NewRecorder()
	err := b.HandleChat(context.Background(), w, map[string]interface{}{
		"model":    "Fake-Model",
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "sky color"}},
	}, func(u *transform.Usage) {
		usageSeen = &stats.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens}
	})
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if resp["usage"] == nil {
		t.Fatal("usage missing from sync response")
	}
	u, _ := resp["usage"].(map[string]interface{})
	if u["prompt_tokens"].(float64) != 19 {
		t.Errorf("unexpected usage: %v", u)
	}
	if usageSeen == nil || usageSeen.PromptTokens != 19 {
		t.Errorf("usage sink not invoked correctly: %+v", usageSeen)
	}
}

// Regression (real gateway, qmodel_preview provider outage): stream opens
// with HTTP 200 but carries a 429 envelope (provider_error "All backends
// failed"), event:error, a Java stack frame and a bare success:false frame,
// then EOF without [DONE]. The client must receive the real gateway error
// in the SSE error chunk — not the misleading "connection truncated".
func TestHandleStreamProviderErrorSurfacesRealCause(t *testing.T) {
	providerErr, _ := json.Marshal(map[string]interface{}{
		"body": `{"code":"provider_error","message":"All backends failed","request_id":"x","type":"provider_error","param":{"retries":2}}
`,
		"statusCode": "TOO_MANY_REQUESTS", "statusCodeValue": float64(429),
	})
	stackTrace, _ := json.Marshal(map[string]interface{}{
		"localizedMessage": "Unknown sse issue",
		"stackTrace":       []interface{}{},
	})
	bareFail, _ := json.Marshal(map[string]interface{}{
		"success": false, "msgCode": float64(500),
		"msgInfo": "Internal Server Error", "message": "Internal Server Error",
	})
	gw := &fakeGateway{chatLines: []string{
		"data: " + string(providerErr),
		"event:error",
		"data: " + string(stackTrace),
		"data: " + string(bareFail),
	}}
	b := newFakeBridge(t, gw)

	w := httptest.NewRecorder()
	err := b.HandleChat(context.Background(), w, map[string]interface{}{
		"model":    "Fake-Model",
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		"stream":   true,
	}, nil)
	if err == nil {
		t.Fatal("provider outage stream must fail")
	}
	var repErr *StreamReportedError
	if !errors.As(err, &repErr) {
		t.Fatalf("expected StreamReportedError, got %T: %v", err, err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "HTTP 429") || !strings.Contains(body, "provider_error: All backends failed") {
		t.Errorf("error chunk must surface the real gateway cause, got:\n%s", body)
	}
	if strings.Contains(body, "connection truncated") {
		t.Errorf("misleading truncated message must be gone, got:\n%s", body)
	}
	if strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Error("failed stream must not terminate with [DONE]")
	}
}

// Regression (real gateway, MiniMax / key "mmodel"): the stream terminates
// with "event:finish" + a telemetry frame instead of a [DONE] marker, and
// interleaves body:"null" keep-alive frames. The old sawDone check flagged
// these streams as truncated ("stream ended without [DONE]"), failing the
// request even though content and usage had fully arrived.
func TestHandleStreamMiniMaxStyleFinishEvent(t *testing.T) {
	nullFrame, _ := json.Marshal(map[string]interface{}{"body": "null"})
	telemetryFrame, _ := json.Marshal(map[string]interface{}{
		"firstTokenDuration": float64(669),
		"serverDuration":     float64(59),
		"totalDuration":      float64(5590),
	})
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "蓝", "role": "assistant"}}}}),
		"data: " + string(nullFrame),
		sseFrame(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "色"}, "finish_reason": "stop"}},
		}),
		"data: " + string(nullFrame),
		sseFrame(t, map[string]interface{}{"choices": []interface{}{}, "usage": map[string]interface{}{"prompt_tokens": 48, "completion_tokens": 146, "total_tokens": 194, "credits": 0.029}}),
		"data: " + string(nullFrame),
		"event:finish", // MiniMax termination: no [DONE] frame
		"data: " + string(telemetryFrame),
	}}
	b := newFakeBridge(t, gw)

	var usageSeen *stats.Usage
	w := httptest.NewRecorder()
	err := b.HandleChat(context.Background(), w, map[string]interface{}{
		"model": "Fake-Model",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "sky color"},
		},
		"stream": true,
	}, func(u *transform.Usage) {
		usageSeen = &stats.Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, Credits: u.Credits}
	})
	if err != nil {
		t.Fatalf("MiniMax-style stream must not error, got %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "蓝") || !strings.Contains(body, "色") {
		t.Errorf("content missing:\n%s", body)
	}
	if !strings.Contains(body, "\"prompt_tokens\":48") {
		t.Errorf("usage chunk missing:\n%s", body)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Error("client-facing stream must still end with our own [DONE]")
	}
	if usageSeen == nil || usageSeen.PromptTokens != 48 {
		t.Errorf("usage sink not invoked correctly: %+v", usageSeen)
	}
}

// busyGateway is a fake upstream that rejects the first N chat requests with
// a 10605 admission-control frame and counts jobToken exchanges, so tests can
// verify that busy retries do NOT trigger a token refresh.
type busyGateway struct {
	busyFirstN int
	chatHits   int
	tokenHits  int
}

func newBusyBridge(t *testing.T, gw *busyGateway) *OpenAiBridge {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/algo/api/v3/user/jobToken", func(w http.ResponseWriter, r *http.Request) {
		gw.tokenHits++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"name":"tester","id":"uid1","userType":"personal_standard","refreshToken":"rt","securityOauthToken":"sot","expireTime":%d}`, time.Now().Add(6*time.Hour).UnixMilli())
	})
	mux.HandleFunc("/algo/api/v2/model/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"chat":[{"key":"qmodel_latest","display_name":"Fake-Model","enable":true,"is_vl":false}]}`)
	})
	mux.HandleFunc("/algo/api/v2/service/pro/sse/agent_chat_generation", func(w http.ResponseWriter, r *http.Request) {
		gw.chatHits++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		if gw.chatHits <= gw.busyFirstN {
			io.WriteString(w, `data: {"body":"{\"code\":\"10605\",\"message\":\"{\\\"isQueued\\\":false,\\\"modelKey\\\":\\\"qmodel_38max\\\",\\\"queueCount\\\":0,\\\"queueType\\\":\\\"slow\\\",\\\"retryAfterSeconds\\\":1,\\\"serviceAvailable\\\":true,\\\"waitTime\\\":0}\"}","statusCode":"FORBIDDEN","statusCodeValue":403}`+"\n\n")
			flusher.Flush()
			return
		}
		for _, l := range []string{
			sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "ok"}}}}),
			"data: [DONE]",
		} {
			fmt.Fprintf(w, "%s\n\n", l)
			flusher.Flush()
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	region := &auth.RegionConfig{Name: "test", AuthBase: srv.URL, ChatBase: srv.URL}
	b := NewOpenAiBridge("pt-test", region)
	orig := busyFallbackBackoff
	busyFallbackBackoff = 5 * time.Millisecond
	t.Cleanup(func() { busyFallbackBackoff = orig })
	return b
}

func chatRequest(model string) map[string]interface{} {
	return map[string]interface{}{
		"model":    model,
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		"stream":   true,
	}
}

// A 10605 rejection before any content must be retried once after back-off,
// and the retry must NOT refresh the job token (the session is still valid).
func TestHandleChatBusyRetriesWithoutTokenRefresh(t *testing.T) {
	gw := &busyGateway{busyFirstN: 1}
	b := newBusyBridge(t, gw)

	w := httptest.NewRecorder()
	if err := b.HandleChat(context.Background(), w, chatRequest("Fake-Model"), nil); err != nil {
		t.Fatalf("HandleChat after busy retry: %v", err)
	}
	if !strings.Contains(w.Body.String(), `"content":"ok"`) {
		t.Errorf("expected successful content after retry, got:\n%s", w.Body.String())
	}
	if gw.chatHits != 2 {
		t.Errorf("chat hits = %d, want 2 (one rejection + one retry)", gw.chatHits)
	}
	if gw.tokenHits != 1 {
		t.Errorf("jobToken exchanges = %d, want 1 (bootstrap only; busy must not force a refresh)", gw.tokenHits)
	}
}

// When both attempts are rejected the busy error surfaces; via the HTTP
// handler it maps to 429 (not 500), carrying Retry-After for sync callers.
func TestMakeChatHandlerMapsBusyTo429(t *testing.T) {
	gw := &busyGateway{busyFirstN: 99}
	b := newBusyBridge(t, gw)

	handler := MakeChatHandler(func(string) *OpenAiBridge { return b }, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"Fake-Model","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	handler(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429\n%s", w.Code, w.Body.String())
	}
	if ra := w.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Errorf("Retry-After header missing/invalid: %q", ra)
	}

	// Streaming path: the error chunk carries the busy message and no second
	// HTTP-level response is attempted.
	w2 := httptest.NewRecorder()
	body := `{"model":"Fake-Model","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req2 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req2.Header.Set("Authorization", "Bearer sk-test")
	handler(w2, req2)
	if w2.Code != 200 {
		t.Errorf("streaming status = %d, want 200 (SSE headers already sent)", w2.Code)
	}
	if !strings.Contains(w2.Body.String(), "10605") {
		t.Errorf("stream body should carry the busy code:\n%s", w2.Body.String())
	}
}

// Parallel requests queue on the per-PAT slot instead of all hitting the
// gateway at once: with one slot, gateway concurrency never exceeds 1.
func TestChatSlotsSerializeUpstreamCalls(t *testing.T) {
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/algo/api/v3/user/jobToken", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"name":"tester","id":"uid1","userType":"personal_standard","refreshToken":"rt","securityOauthToken":"sot","expireTime":%d}`, time.Now().Add(6*time.Hour).UnixMilli())
	})
	mux.HandleFunc("/algo/api/v2/model/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"chat":[{"key":"qmodel_latest","display_name":"Fake-Model","enable":true,"is_vl":false}]}`)
	})
	mux.HandleFunc("/algo/api/v2/service/pro/sse/agent_chat_generation", func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			old := maxInFlight.Load()
			if n <= old || maxInFlight.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, l := range []string{
			sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "ok"}}}}),
			"data: [DONE]",
		} {
			fmt.Fprintf(w, "%s\n\n", l)
			flusher.Flush()
		}
		inFlight.Add(-1)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	region := &auth.RegionConfig{Name: "test", AuthBase: srv.URL, ChatBase: srv.URL}
	b := NewOpenAiBridge("pt-test", region)

	const parallel = 4
	done := make(chan error, parallel)
	for i := 0; i < parallel; i++ {
		go func() {
			w := httptest.NewRecorder()
			done <- b.HandleChat(context.Background(), w, chatRequest("Fake-Model"), nil)
		}()
	}
	for i := 0; i < parallel; i++ {
		if err := <-done; err != nil {
			t.Fatalf("parallel HandleChat: %v", err)
		}
	}
	if got := maxInFlight.Load(); got != 1 {
		t.Errorf("max concurrent upstream calls = %d, want 1 (default slot limit)", got)
	}
}

func TestEnsureAccountStatusMergesAuthoritativeQuota(t *testing.T) {
	var quotaCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/algo/api/v3/user/jobToken", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"name":"tester","id":"uid1","refreshToken":"rt","securityOauthToken":"sot","expireTime":%d}`, time.Now().Add(6*time.Hour).UnixMilli())
	})
	mux.HandleFunc("/algo/api/v3/user/status", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"uid1","userType":"teams","plan":"PLAN_TIER_TEAM","userTag":"Teams","orgName":"Example Org","nextResetAt":1790265600000,"isQuotaExceeded":false}`)
	})
	mux.HandleFunc("/api/v2/quota/usage", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sot" {
			t.Errorf("quota Authorization = %q", got)
		}
		if quotaCalls.Add(1) > 1 {
			http.Error(w, "temporary outage", http.StatusServiceUnavailable)
			return
		}
		// expiresAt is deliberately a different (and implausibly distant) value:
		// the gateway's nextResetAt must win over it.
		io.WriteString(w, `{"userId":"uid1","userType":"teams","totalUsagePercentage":0.98,"isQuotaExceeded":true,"expiresAt":253402214400000,"userQuota":{"total":3000,"used":2939,"remaining":61,"percentage":0.98,"unit":"credits"},"orgResourcePackage":{"used":0,"cap":4000,"remaining":0,"percentage":0,"available":false,"unit":"credits"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	region := &auth.RegionConfig{Name: "test", AuthBase: srv.URL, ChatBase: srv.URL, OpenAPIBase: srv.URL}
	b := NewOpenAiBridge("pt-test", region)
	st := b.EnsureAccountStatus(context.Background())
	if st == nil || st.UserQuota == nil {
		t.Fatalf("expected authoritative quota, got %+v", st)
	}
	if st.UserQuota.Used != 2939 || st.UserQuota.Remaining != 61 {
		t.Errorf("unexpected quota merge: %+v", st)
	}
	// The gateway boundary wins; the far-future expiresAt sentinel is ignored.
	if st.NextResetAtMs != 1790265600000 {
		t.Errorf("NextResetAtMs = %d, want the /user/status nextResetAt (1790265600000)", st.NextResetAtMs)
	}
	if !st.IsQuotaExceeded || st.Plan != "PLAN_TIER_TEAM" || st.OrgName != "Example Org" {
		t.Errorf("gateway metadata and OpenAPI verdict were not merged: %+v", st)
	}
	if st.OrgResourcePackage == nil || st.OrgResourcePackage.Cap != 4000 {
		t.Errorf("organization package missing: %+v", st.OrgResourcePackage)
	}

	// Force the identity cache stale, then fail the next quota request. A fresh
	// reduced /user/status response must not erase the last official snapshot.
	b.accountMu.Lock()
	b.accountTs = 0
	b.accountMu.Unlock()
	st = b.EnsureAccountStatus(context.Background())
	if st == nil || st.UserQuota == nil || st.UserQuota.Used != 2939 || !st.IsQuotaExceeded {
		t.Errorf("temporary quota outage erased the last authoritative snapshot: %+v", st)
	}
}

func TestHandleChatSendsReasoningEffortToSignedGateway(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "ok"}}}}),
		"data: [DONE]",
	}}
	bridge := newFakeBridge(t, gw)

	err := bridge.HandleChat(context.Background(), httptest.NewRecorder(), map[string]interface{}{
		"model":            "Fake-Model",
		"messages":         []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		"stream":           true,
		"reasoning_effort": "xhigh",
	}, nil)
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}

	plain, err := auth.Decode(string(gw.lastChatBody()))
	if err != nil {
		t.Fatalf("decode signed gateway request: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("decode JSON gateway request: %v", err)
	}
	if got := payload["reasoning_effort"]; got != nil {
		t.Errorf("reasoning_effort must not be a top-level body field, got %#v", got)
	}
	params, ok := payload["parameters"].(map[string]interface{})
	if !ok {
		t.Fatalf("parameters object missing from gateway request: %s", plain)
	}
	if got := params["reasoning_effort"]; got != "xhigh" {
		t.Errorf("parameters.reasoning_effort = %#v, want xhigh", got)
	}
	if got, ok := params["max_tokens"].(float64); !ok || got <= 0 {
		t.Errorf("parameters.max_tokens = %#v, want a positive cap", params["max_tokens"])
	}
	if _, present := params["max_thinking_tokens"]; present {
		t.Errorf("max_thinking_tokens must be omitted for a non-none tier, got %#v", params["max_thinking_tokens"])
	}
	if got := payload["model_config"].(map[string]interface{})["key"]; got != "qmodel_latest" {
		t.Errorf("model_config.key = %#v, want qmodel_latest", got)
	}
}

// A "none" tier must disable thinking the way the official client does: set
// is_reasoning=false on both model_config copies AND send an explicit
// max_thinking_tokens of 0. Serializing 0 (rather than omitting it) is the
// whole point, so the pointer field is asserted here.
func TestHandleChatDisablesReasoningForNoneTier(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "ok"}}}}),
		"data: [DONE]",
	}}
	bridge := newFakeBridge(t, gw)

	err := bridge.HandleChat(context.Background(), httptest.NewRecorder(), map[string]interface{}{
		"model":            "Fake-Model",
		"messages":         []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		"stream":           true,
		"reasoning_effort": "none",
	}, nil)
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}

	plain, err := auth.Decode(string(gw.lastChatBody()))
	if err != nil {
		t.Fatalf("decode signed gateway request: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("decode JSON gateway request: %v", err)
	}

	params := payload["parameters"].(map[string]interface{})
	if got := params["reasoning_effort"]; got != "none" {
		t.Errorf("parameters.reasoning_effort = %#v, want none", got)
	}
	if got, ok := params["max_thinking_tokens"].(float64); !ok || got != 0 {
		t.Errorf("parameters.max_thinking_tokens = %#v, want explicit 0", params["max_thinking_tokens"])
	}
	if got := payload["model_config"].(map[string]interface{})["is_reasoning"]; got != false {
		t.Errorf("model_config.is_reasoning = %#v, want false", got)
	}
	extra := payload["chat_context"].(map[string]interface{})["extra"].(map[string]interface{})
	if got := extra["modelConfig"].(map[string]interface{})["is_reasoning"]; got != false {
		t.Errorf("chat_context.extra.modelConfig.is_reasoning = %#v, want false", got)
	}
}

// A tier the model does not support is dropped entirely, and the model keeps
// whatever reasoning capability the catalog advertised for it.
func TestHandleChatUnsupportedTierKeepsCatalogReasoning(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "ok"}}}}),
		"data: [DONE]",
	}}
	bridge := newFakeBridge(t, gw)

	err := bridge.HandleChat(context.Background(), httptest.NewRecorder(), map[string]interface{}{
		"model":            "Fake-Model",
		"messages":         []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		"stream":           true,
		"reasoning_effort": "high",
	}, nil)
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}

	plain, err := auth.Decode(string(gw.lastChatBody()))
	if err != nil {
		t.Fatalf("decode signed gateway request: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("decode JSON gateway request: %v", err)
	}

	params := payload["parameters"].(map[string]interface{})
	if got, present := params["reasoning_effort"]; present {
		t.Errorf("unsupported tier must be omitted, got %#v", got)
	}
	if got := payload["model_config"].(map[string]interface{})["is_reasoning"]; got != true {
		t.Errorf("model_config.is_reasoning = %#v, want true (catalog capability preserved)", got)
	}
}

func TestResolveReasoningEffortRejectsUnsupportedModelTier(t *testing.T) {
	qwen := &models.ModelReasoning{Efforts: []string{"low", "medium", "xhigh"}, SupportsDisabled: true, Known: true}
	if got := resolveReasoningEffort(map[string]interface{}{"reasoning_effort": "high"}, qwen); got != "" {
		t.Errorf("unsupported tier = %q, want omitted", got)
	}
	if got := resolveReasoningEffort(map[string]interface{}{"reasoning_effort": "none"}, qwen); got != "none" {
		t.Errorf("supported disabled tier = %q, want none", got)
	}
}

// Forwarding "none" to a model that does not declare thinking_config.disabled
// makes the upstream reject the whole request with HTTP 400. When the catalog
// carries no usable metadata for the key the tier cannot be validated, so it
// must be omitted rather than passed through.
func TestResolveReasoningEffortOmitsUnverifiedNone(t *testing.T) {
	if got := resolveReasoningEffort(map[string]interface{}{"reasoning_effort": "none"}, nil); got != "" {
		t.Errorf("none without catalog metadata = %q, want omitted", got)
	}
	unknown := &models.ModelReasoning{}
	if got := resolveReasoningEffort(map[string]interface{}{"reasoning_effort": "none"}, unknown); got != "" {
		t.Errorf("none with unknown metadata = %q, want omitted", got)
	}
	// ...while other tiers keep their historical pass-through.
	if got := resolveReasoningEffort(map[string]interface{}{"reasoning_effort": "high"}, nil); got != "high" {
		t.Errorf("high without catalog metadata = %q, want high", got)
	}
	// A model that declares the switch but no tiers still accepts "none".
	switchOnly := &models.ModelReasoning{SupportsDisabled: true, Known: true}
	if got := resolveReasoningEffort(map[string]interface{}{"reasoning_effort": "none"}, switchOnly); got != "none" {
		t.Errorf("none for a model declaring disabled = %q, want none", got)
	}
}

// A model whose catalog entry has no thinking_config.disabled must not receive
// "none" on the wire, and must keep its reasoning enabled.
func TestHandleChatOmitsNoneForModelWithoutDisabled(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "ok"}}}}),
		"data: [DONE]",
	}}
	bridge := newFakeBridge(t, gw)
	// Replace the fixture catalog with one that has no thinking_config.disabled,
	// mirroring the models that reject "none" upstream (gmodel/gfmodel). The
	// timestamp must be refreshed too, otherwise the cache is considered stale
	// and the fake gateway's own catalog is fetched over it.
	bridge.catalogMu.Lock()
	bridge.catalog = models.ExtractCatalog(map[string]interface{}{
		"chat": []interface{}{
			map[string]interface{}{"key": "qmodel_latest", "display_name": "Fake-Model", "enable": true,
				"is_reasoning": true,
				"thinking_config": map[string]interface{}{
					"enabled": map[string]interface{}{
						"efforts": map[string]interface{}{"low": map[string]interface{}{}, "high": map[string]interface{}{}},
					},
				}},
		},
	})
	bridge.catalogTs = float64(time.Now().Unix())
	bridge.catalogMu.Unlock()

	err := bridge.HandleChat(context.Background(), httptest.NewRecorder(), map[string]interface{}{
		"model":            "Fake-Model",
		"messages":         []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		"stream":           true,
		"reasoning_effort": "none",
	}, nil)
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}

	plain, err := auth.Decode(string(gw.lastChatBody()))
	if err != nil {
		t.Fatalf("decode signed gateway request: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("decode JSON gateway request: %v", err)
	}
	params := payload["parameters"].(map[string]interface{})
	if got, present := params["reasoning_effort"]; present {
		t.Errorf("none must be omitted for a model without thinking_config.disabled, got %#v", got)
	}
	if _, present := params["max_thinking_tokens"]; present {
		t.Error("max_thinking_tokens must not be sent when the tier is omitted")
	}
	if got := payload["model_config"].(map[string]interface{})["is_reasoning"]; got != true {
		t.Errorf("model_config.is_reasoning = %#v, want true (thinking stays on)", got)
	}
}

// A far-future expiresAt sentinel (9999-12-31, used by plans without an
// expiry) must never override the gateway's real nextResetAt, and must never
// be adopted as the boundary when nextResetAt is missing.
func TestEnsureAccountStatusRejectsSentinelResetBoundary(t *testing.T) {
	const sentinel = int64(253402214400000)
	const real = int64(1790265600000)

	newBridge := func(t *testing.T, statusBody, quotaBody string) *OpenAiBridge {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc("/algo/api/v3/user/jobToken", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"name":"tester","id":"uid1","refreshToken":"rt","securityOauthToken":"sot","expireTime":%d}`, time.Now().Add(6*time.Hour).UnixMilli())
		})
		mux.HandleFunc("/algo/api/v3/user/status", func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, statusBody)
		})
		mux.HandleFunc("/api/v2/quota/usage", func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, quotaBody)
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return NewOpenAiBridge("pt-test", &auth.RegionConfig{Name: "test", AuthBase: srv.URL, ChatBase: srv.URL, OpenAPIBase: srv.URL})
	}

	quota := func(expiresAt int64) string {
		return fmt.Sprintf(`{"userId":"uid1","userType":"personal_standard","expiresAt":%d,"userQuota":{"total":0,"used":8,"remaining":0,"unit":"credits"},"addOnQuota":{"total":300,"used":36,"remaining":264,"unit":"credits"}}`, expiresAt)
	}

	// The gateway boundary wins over an OpenAPI sentinel.
	b := newBridge(t,
		fmt.Sprintf(`{"id":"uid1","userType":"personal_standard","plan":"PLAN_FREE","nextResetAt":%d}`, real),
		quota(sentinel))
	if st := b.EnsureAccountStatus(context.Background()); st == nil || st.NextResetAtMs != real {
		t.Errorf("NextResetAtMs = %v, want the gateway boundary %d", st, real)
	}

	// With no gateway boundary, the sentinel must be dropped rather than adopted.
	b = newBridge(t, `{"id":"uid1","userType":"personal_standard","plan":"PLAN_FREE"}`, quota(sentinel))
	if st := b.EnsureAccountStatus(context.Background()); st == nil || st.NextResetAtMs != 0 {
		t.Errorf("NextResetAtMs = %v, want 0 (sentinel rejected)", st)
	}

	// A plausible expiresAt still serves as the fallback.
	b = newBridge(t, `{"id":"uid1","userType":"personal_standard","plan":"PLAN_FREE"}`, quota(real))
	if st := b.EnsureAccountStatus(context.Background()); st == nil || st.NextResetAtMs != real {
		t.Errorf("NextResetAtMs = %v, want the expiresAt fallback %d", st, real)
	}
}

// The caller's cap must win over the catalog default, otherwise models with a
// small advertised cap (the built-in BYOK table lists one at 2048) would have
// their responses silently truncated.
func TestResolveMaxTokensPrefersCallerValue(t *testing.T) {
	const catalogDefault = 2048

	cases := []struct {
		name    string
		reqBody map[string]interface{}
		want    int
	}{
		{"caller cap wins over smaller catalog cap",
			map[string]interface{}{"max_tokens": float64(16000)}, 16000},
		{"newer max_completion_tokens is honored",
			map[string]interface{}{"max_completion_tokens": float64(9000)}, 9000},
		{"max_tokens takes precedence over max_completion_tokens",
			map[string]interface{}{"max_tokens": float64(4096), "max_completion_tokens": float64(9000)}, 4096},
		{"absent falls back to catalog", map[string]interface{}{}, catalogDefault},
		{"zero is not a usable cap", map[string]interface{}{"max_tokens": float64(0)}, catalogDefault},
		{"negative is not a usable cap", map[string]interface{}{"max_tokens": float64(-5)}, catalogDefault},
		{"fractional is not a usable cap", map[string]interface{}{"max_tokens": 100.5}, catalogDefault},
		{"string is not a usable cap", map[string]interface{}{"max_tokens": "8000"}, catalogDefault},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveMaxTokens(tc.reqBody, catalogDefault); got != tc.want {
				t.Errorf("resolveMaxTokens(%v, %d) = %d, want %d", tc.reqBody, catalogDefault, got, tc.want)
			}
		})
	}
}

// The resolved cap must reach the signed gateway body inside "parameters".
func TestHandleChatForwardsCallerMaxTokens(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "ok"}}}}),
		"data: [DONE]",
	}}
	bridge := newFakeBridge(t, gw)

	err := bridge.HandleChat(context.Background(), httptest.NewRecorder(), map[string]interface{}{
		"model":      "Fake-Model",
		"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		"stream":     true,
		"max_tokens": float64(16000),
	}, nil)
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}

	plain, err := auth.Decode(string(gw.lastChatBody()))
	if err != nil {
		t.Fatalf("decode signed gateway request: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("decode JSON gateway request: %v", err)
	}
	params := payload["parameters"].(map[string]interface{})
	if got := params["max_tokens"].(float64); got != 16000 {
		t.Errorf("parameters.max_tokens = %v, want 16000 (catalog cap is 4096)", got)
	}
}
