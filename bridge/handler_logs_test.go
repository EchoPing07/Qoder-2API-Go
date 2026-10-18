package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qoder2api/logs"
)

// mkChatReq builds an authenticated chat request against the handler.
func mkChatReq(body string) *http.Request {
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-test")
	return req
}

// A successful request must leave exactly one log entry carrying the key
// metadata, the resolved model, the stream flag and the usage frame.
func TestChatHandlerLogsSuccessfulRequest(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "ok"}}}}),
		sseFrame(t, map[string]interface{}{"choices": []interface{}{}, "usage": map[string]interface{}{
			"prompt_tokens": 5, "completion_tokens": 7, "total_tokens": 12, "credits": 0.5}}),
		"data: [DONE]",
	}}
	b := newFakeBridge(t, gw)
	lr := logs.NewRecorder(nil, 90, 2000)

	handler := MakeChatHandler(
		func(ResolveRequest) *ResolvedBridge {
			return &ResolvedBridge{Bridge: b, KeyID: "abc123", KeyNote: "dev key"}
		},
		nil, lr, nil, nil)
	handler(httptest.NewRecorder(), mkChatReq(`{"model":"Fake-Model","stream":true,"messages":[{"role":"user","content":"hi"}]}`))

	if lr.Len() != 1 {
		t.Fatalf("log entries = %d, want 1", lr.Len())
	}
	e := lr.Query(logs.Filter{PageSize: 1}).Logs[0]
	if e.KeyID != "abc123" || e.KeyNote != "dev key" {
		t.Errorf("key metadata = %q/%q, want abc123/dev key", e.KeyID, e.KeyNote)
	}
	if e.Model != "Fake-Model" {
		t.Errorf("model = %q, want Fake-Model", e.Model)
	}
	if !e.Stream {
		t.Error("stream flag lost")
	}
	if e.StatusCode != 200 || e.ErrorMsg != "" {
		t.Errorf("status/msg = %d/%q, want 200/\"\"", e.StatusCode, e.ErrorMsg)
	}
	if e.PromptTokens != 5 || e.CompletionTokens != 7 || e.TotalTokens != 12 {
		t.Errorf("tokens = %d/%d/%d, want 5/7/12", e.PromptTokens, e.CompletionTokens, e.TotalTokens)
	}
	if e.Credits != 0.5 {
		t.Errorf("credits = %v, want 0.5", e.Credits)
	}
	if e.DurationMs < 0 {
		t.Errorf("duration = %d, want >= 0", e.DurationMs)
	}
}

// Auth failures happen before the log entry exists and must not be logged
// (same policy as the reference implementation).
func TestChatHandlerDoesNotLogAuthFailures(t *testing.T) {
	lr := logs.NewRecorder(nil, 90, 2000)
	handler := MakeChatHandler(nil, nil, lr, nil, nil)

	// No bearer token.
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`)))
	if w.Code != 401 {
		t.Fatalf("no-token status = %d, want 401", w.Code)
	}

	// Invalid key (resolver returns nil).
	handler = MakeChatHandler(func(ResolveRequest) *ResolvedBridge { return nil }, nil, lr, nil, nil)
	w = httptest.NewRecorder()
	handler(w, mkChatReq(`{}`))
	if w.Code != 401 {
		t.Fatalf("invalid-key status = %d, want 401", w.Code)
	}

	if lr.Len() != 0 {
		t.Errorf("auth failures must not be logged, got %d entries", lr.Len())
	}
}

// Malformed JSON on an authenticated request is logged as a 400.
func TestChatHandlerLogsInvalidJSON(t *testing.T) {
	b := &OpenAiBridge{}
	lr := logs.NewRecorder(nil, 90, 2000)
	handler := MakeChatHandler(func(ResolveRequest) *ResolvedBridge { return &ResolvedBridge{Bridge: b} }, nil, lr, nil, nil)

	w := httptest.NewRecorder()
	handler(w, mkChatReq(`{"model": `))

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if lr.Len() != 1 {
		t.Fatalf("log entries = %d, want 1", lr.Len())
	}
	e := lr.Query(logs.Filter{PageSize: 1}).Logs[0]
	if e.StatusCode != 400 {
		t.Errorf("logged status = %d, want 400", e.StatusCode)
	}
	if !strings.Contains(e.ErrorMsg, "Invalid JSON body") {
		t.Errorf("logged error = %q, want JSON body error", e.ErrorMsg)
	}
}

// The log records the status the CLIENT saw: a 500 from the gateway is
// mapped to 502 for the client and 502 in the log.
func TestChatHandlerLogsClientStatusForUpstreamErrors(t *testing.T) {
	gw := &rejectingGateway{status: 500, body: `{"code":"internal_error","message":"boom"}`}
	b := newRejectingBridge(t, gw)
	lr := logs.NewRecorder(nil, 90, 2000)
	handler := MakeChatHandler(func(ResolveRequest) *ResolvedBridge { return &ResolvedBridge{Bridge: b} }, nil, lr, nil, nil)

	w := httptest.NewRecorder()
	handler(w, mkChatReq(`{"model":"Fake-Model","messages":[{"role":"user","content":"hi"}]}`))

	if w.Code != 502 {
		t.Fatalf("client status = %d, want 502", w.Code)
	}
	e := lr.Query(logs.Filter{PageSize: 1}).Logs[0]
	if e.StatusCode != 502 {
		t.Errorf("logged status = %d, want 502 (the client status, not the raw 500)", e.StatusCode)
	}
	if !strings.Contains(e.ErrorMsg, "boom") {
		t.Errorf("logged error = %q, want the upstream body", e.ErrorMsg)
	}
}

// A long error message must be truncated so one pathological request cannot
// bloat the persisted log slice.
func TestChatHandlerTruncatesLoggedError(t *testing.T) {
	gw := &rejectingGateway{status: 400, body: strings.Repeat("x", 2000)}
	b := newRejectingBridge(t, gw)
	lr := logs.NewRecorder(nil, 90, 2000)
	handler := MakeChatHandler(func(ResolveRequest) *ResolvedBridge { return &ResolvedBridge{Bridge: b} }, nil, lr, nil, nil)

	w := httptest.NewRecorder()
	handler(w, mkChatReq(`{"model":"Fake-Model","messages":[{"role":"user","content":"hi"}]}`))
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	e := lr.Query(logs.Filter{PageSize: 1}).Logs[0]
	if n := len([]rune(e.ErrorMsg)); n > 503 {
		t.Errorf("logged error length = %d runes, want <= 503", n)
	}
	if !strings.HasPrefix(e.ErrorMsg, "upstream: ") {
		t.Errorf("logged error must carry the upstream body: %q", e.ErrorMsg[:min(30, len(e.ErrorMsg))])
	}
}

// A bridge with no session fails the request before any streaming starts;
// the handler maps it to 502 and the log entry must carry that status.
func TestChatHandlerLogsBridgeBootstrapFailure(t *testing.T) {
	b := &OpenAiBridge{}
	lr := logs.NewRecorder(nil, 90, 2000)
	handler := MakeChatHandler(func(ResolveRequest) *ResolvedBridge { return &ResolvedBridge{Bridge: b} }, nil, lr, nil, nil)
	w := httptest.NewRecorder()
	handler(w, mkChatReq(`{"model":"Fake-Model","messages":[]}`))
	if w.Code != 502 {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	e := lr.Query(logs.Filter{PageSize: 1}).Logs[0]
	if e.StatusCode != 502 {
		t.Errorf("logged status = %d, want 502", e.StatusCode)
	}
	if !strings.Contains(e.ErrorMsg, "upstream auth") {
		t.Errorf("logged error = %q, want upstream auth detail", e.ErrorMsg)
	}
	if e.CreatedAt < time.Now().Unix()-60 {
		t.Errorf("CreatedAt not set: %d", e.CreatedAt)
	}
}

// 请求日志必须记录服务该请求的账号与区域。
func TestChatHandlerLogsAccountAndRegion(t *testing.T) {
	gw := &fakeGateway{chatLines: []string{
		sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "ok"}}}}),
		"data: [DONE]",
	}}
	b := newFakeBridge(t, gw)
	lr := logs.NewRecorder(nil, 90, 2000)
	handler := MakeChatHandler(func(ResolveRequest) *ResolvedBridge {
		return &ResolvedBridge{Bridge: b, KeyID: "k", AccountID: "acc-1", AccountNote: "账号A", Region: "global"}
	}, nil, lr, nil, nil)
	handler(httptest.NewRecorder(), mkChatReq(`{"model":"M","messages":[{"role":"user","content":"hi"}]}`))

	e := lr.Query(logs.Filter{PageSize: 1}).Logs[0]
	if e.AccountID != "acc-1" || e.AccountNote != "账号A" || e.Region != "global" {
		t.Errorf("log account = %q/%q/%q", e.AccountID, e.AccountNote, e.Region)
	}
}

// conversationSeed：同一对话的不同轮次 seed 相同；不同首句/模型/系统前缀/图片不同。
func TestConversationSeed(t *testing.T) {
	base := `{"model":"M","messages":[{"role":"system","content":"sys"},{"role":"user","content":"你好"}]}`
	dec := func(s string) map[string]interface{} {
		var m map[string]interface{}
		_ = json.Unmarshal([]byte(s), &m)
		return m
	}
	seed := func(s string) string {
		m := dec(s)
		model, _ := m["model"].(string)
		return conversationSeed(m, model)
	}

	a := seed(base)
	// 追加后续轮次，seed 不变。
	turn2 := `{"model":"M","messages":[{"role":"system","content":"sys"},{"role":"user","content":"你好"},{"role":"assistant","content":"在"},{"role":"user","content":"再说点"}]}`
	if seed(turn2) != a {
		t.Error("appending turns must not change the seed")
	}
	// 不同首句 -> 不同 seed。
	if seed(`{"model":"M","messages":[{"role":"user","content":"再见"}]}`) == a {
		t.Error("different first user text must differ")
	}
	// 相同首句、不同 system 前缀 -> 不同 seed（避免不同对话误判同一会话）。
	if seed(`{"model":"M","messages":[{"role":"system","content":"别的系统提示"},{"role":"user","content":"你好"}]}`) == a {
		t.Error("different system prefix must differ")
	}
	// 不同模型 -> 不同 seed。
	if seed(`{"model":"M2","messages":[{"role":"system","content":"sys"},{"role":"user","content":"你好"}]}`) == a {
		t.Error("different model must differ")
	}
	// 没有 user 消息 -> 空 seed。
	if seed(`{"model":"M","messages":[{"role":"system","content":"sys"}]}`) != "" {
		t.Error("no user message must yield empty seed")
	}
}
