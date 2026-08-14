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
	"testing"
	"time"
	"unicode/utf8"

	"qoder2api/auth"
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
	b.applyJobToken(map[string]interface{}{
		"name": "u", "id": "1", "userType": "personal_standard",
		"refreshToken": "r", "securityOauthToken": "s",
		"expireTime": nil, // missing expiry
	})
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
	b.applyJobToken(map[string]interface{}{"name": "u", "id": "1", "expireTime": float64(1e15)})
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
		io.WriteString(w, `{"chat":[{"key":"qmodel_latest","display_name":"Fake-Model","enable":true,"is_vl":false}]}`)
	})
	mux.HandleFunc("/algo/api/v2/service/pro/sse/agent_chat_generation", func(w http.ResponseWriter, r *http.Request) {
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
