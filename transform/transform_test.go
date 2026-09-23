package transform

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildMessagesUsesOnlyIncomingOpenAIMessages(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "system", "content": "Sys"},
		{"role": "user", "content": "Hi"},
		{"role": "assistant", "content": "Hello!"},
		{"role": "user", "content": "Ok"},
	}
	converted := BuildQoderMessages(messages, "Ok", true)
	if len(converted) == 0 {
		t.Fatal("expected non-empty messages")
	}
	b, _ := json.Marshal(converted)
	if strings.Contains(string(b), "Skill") {
		t.Error("converted messages should not contain 'Skill'")
	}
}

func TestBuildQoderMessagesNormalizesDeveloperRoleToSystem(t *testing.T) {
	converted := BuildQoderMessages([]map[string]interface{}{
		{"role": "developer", "content": "Agent instruction"},
		{"role": "user", "content": "Hi"},
	}, "Hi", false)
	if len(converted) != 2 {
		t.Fatalf("converted message count = %d, want 2", len(converted))
	}
	if converted[0].Role != "system" {
		t.Errorf("developer role = %q, want system", converted[0].Role)
	}
}

func TestApplyOpenAIToolConfigRemovesTemplateToolsWhenAbsent(t *testing.T) {
	body := map[string]interface{}{
		"tools":               []interface{}{map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "Skill"}}},
		"tool_choice":         "auto",
		"parallel_tool_calls": true,
	}
	reqBody := map[string]interface{}{"messages": []interface{}{}}
	toolsEnabled := ApplyOpenAIToolConfig(body, reqBody)
	if toolsEnabled {
		t.Error("tools should be disabled")
	}
	if _, ok := body["tools"]; ok {
		t.Error("tools should be removed")
	}
	if _, ok := body["parallel_tool_calls"]; ok {
		t.Error("parallel_tool_calls should be removed")
	}
}

func TestApplyOpenAIToolConfigKeepsOnlyRequestTools(t *testing.T) {
	reqTool := map[string]interface{}{
		"type":     "function",
		"function": map[string]interface{}{"name": "MyTool", "parameters": map[string]interface{}{}},
	}
	body := map[string]interface{}{
		"tools":       []interface{}{map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "Skill"}}},
		"tool_choice": "auto",
	}
	reqBody := map[string]interface{}{
		"tools":       []interface{}{reqTool},
		"tool_choice": "required",
		"messages":    []interface{}{},
	}
	toolsEnabled := ApplyOpenAIToolConfig(body, reqBody)
	if !toolsEnabled {
		t.Error("tools should be enabled")
	}
	tools, _ := body["tools"].([]interface{})
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	fn := tools[0].(map[string]interface{})["function"].(map[string]interface{})
	if fn["name"] != "MyTool" {
		t.Errorf("expected MyTool, got %v", fn["name"])
	}
	if body["tool_choice"] != "required" {
		t.Errorf("expected required, got %v", body["tool_choice"])
	}
}

func TestToolHistoryFlattenedWhenRequestToolsAbsent(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "user", "content": "Hi"},
		{
			"role":       "assistant",
			"content":    nil,
			"tool_calls": []interface{}{map[string]interface{}{"id": "1", "function": map[string]interface{}{"name": "do", "arguments": "{}"}}},
		},
		{"role": "tool", "content": "result!", "tool_call_id": "1"},
	}
	converted := BuildQoderMessages(messages, "Hi", false)
	if len(converted) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(converted))
	}
	if converted[1].Content == "" {
		t.Error("expected non-empty content for assistant message")
	}
	if !strings.Contains(converted[1].Content, "do") {
		t.Error("expected 'do' in assistant content")
	}
}

func TestExtractDeltaCapturesReasoningContent(t *testing.T) {
	inner, _ := json.Marshal(map[string]interface{}{
		"choices": []map[string]interface{}{
			{"delta": map[string]interface{}{"content": "", "reasoning_content": "thinking..."}},
		},
	})
	wrapper := map[string]interface{}{"body": string(inner)}
	line, _ := json.Marshal(wrapper)
	delta := ExtractDelta(string(line))
	if delta.ReasoningContent != "thinking..." {
		t.Errorf("expected reasoning_content 'thinking...', got %q", delta.ReasoningContent)
	}
	if delta.IsEmpty() {
		t.Error("delta should not be empty")
	}
}

// The gateway sends a final frame with empty choices + usage right before
// [DONE]. ExtractDelta must surface it as a Usage-only delta.
func TestExtractDeltaCapturesUsageFrame(t *testing.T) {
	inner, _ := json.Marshal(map[string]interface{}{
		"choices": []interface{}{},
		"usage": map[string]interface{}{
			"billable":                  true,
			"prompt_tokens":             17,
			"completion_tokens":         70,
			"total_tokens":              87,
			"credits":                   0.005279472,
			"original_credits":          0.01319868,
			"prompt_tokens_details":     map[string]interface{}{"cached_tokens": 3},
			"completion_tokens_details": map[string]interface{}{"reasoning_tokens": 64},
		},
	})
	wrapper := map[string]interface{}{"body": string(inner)}
	line, _ := json.Marshal(wrapper)
	delta := ExtractDelta(string(line))
	if delta.Usage == nil {
		t.Fatal("expected usage on final frame")
	}
	u := delta.Usage
	if u.PromptTokens != 17 || u.CompletionTokens != 70 || u.TotalTokens != 87 {
		t.Errorf("unexpected token counts: %+v", u)
	}
	if u.CachedPromptTokens() != 3 {
		t.Errorf("expected 3 cached tokens, got %d", u.CachedPromptTokens())
	}
	if u.ReasoningTokens() != 64 {
		t.Errorf("expected 64 reasoning tokens, got %d", u.ReasoningTokens())
	}
	if u.Credits < 0.005279 || u.Credits > 0.005280 {
		t.Errorf("unexpected credits: %v", u.Credits)
	}
	if delta.Role != "" || delta.Content != "" {
		t.Errorf("usage frame must not carry content: %+v", delta)
	}
	if delta.IsEmpty() {
		t.Error("usage delta should not be empty")
	}
}

// extractUsage must keep the gateway's charge flag: an explicit false marks
// the frame non-billable, while an absent key defaults to billable so frames
// from models that predate the flag keep contributing credits.
func TestExtractUsageBillableFlag(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]interface{}
		want bool
	}{
		{"explicit false", map[string]interface{}{"billable": false, "prompt_tokens": 5}, false},
		{"explicit true", map[string]interface{}{"billable": true, "prompt_tokens": 5}, true},
		{"absent defaults to billable", map[string]interface{}{"prompt_tokens": 5}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := extractUsage(tt.raw)
			if u == nil {
				t.Fatal("expected usage")
			}
			if u.Billable != tt.want {
				t.Errorf("Billable = %v, want %v", u.Billable, tt.want)
			}
		})
	}
}

// §26: Billable is internal-only and must never serialize into the usage
// chunk sent to OpenAI clients, while the real fields keep serializing.
func TestUsageMarshalOmitsBillableFlag(t *testing.T) {
	u := &Usage{
		PromptTokens:     17,
		CompletionTokens: 70,
		TotalTokens:      87,
		Credits:          0.005,
		OriginalCredits:  0.013,
		Billable:         false,
	}
	// Same shape the bridge writes: the usage object nested in a chunk.
	chunk := map[string]interface{}{"choices": []interface{}{}, "usage": u}
	raw, err := json.Marshal(chunk)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if strings.Contains(out, "billable") {
		t.Errorf("billable must not be serialized to clients: %s", out)
	}
	for _, key := range []string{
		`"prompt_tokens":17`,
		`"completion_tokens":70`,
		`"total_tokens":87`,
		`"credits":0.005`,
		`"original_credits":0.013`,
	} {
		if !strings.Contains(out, key) {
			t.Errorf("expected %s in serialized usage: %s", key, out)
		}
	}
}

// [DONE] and frames without usage must not produce a Usage delta.
func TestExtractDeltaDoneAndPlainFrames(t *testing.T) {
	wrapper := map[string]interface{}{"body": "[DONE]"}
	line, _ := json.Marshal(wrapper)
	if d := ExtractDelta(string(line)); d.Usage != nil {
		t.Error("[DONE] must not yield usage")
	}

	inner, _ := json.Marshal(map[string]interface{}{
		"choices": []map[string]interface{}{
			{"delta": map[string]interface{}{"content": "hi"}, "finish_reason": nil},
		},
	})
	wrapper = map[string]interface{}{"body": string(inner)}
	line, _ = json.Marshal(wrapper)
	d := ExtractDelta(string(line))
	if d.Usage != nil {
		t.Error("content frame must not yield usage")
	}
	if d.Content != "hi" {
		t.Errorf("expected content 'hi', got %q", d.Content)
	}
}

func TestStreamAccumulatorForwardsReasoningAndNonReasoningDeltas(t *testing.T) {
	acc := NewStreamAccumulator("r1", 0, "m", false, nil)
	acc.Accept(&BridgeDelta{Content: "hello"})
	acc.Accept(&BridgeDelta{ReasoningContent: "think"})
	acc.Accept(&BridgeDelta{Content: " world"})
	acc.Flush()
	chunks := acc.GetChunks()
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	if !strings.Contains(chunks[0], "hello") {
		t.Error("first chunk should contain 'hello'")
	}
	if !strings.Contains(chunks[len(chunks)-1], " world") {
		t.Error("last chunk should contain ' world'")
	}
}

func TestMakeSSEChunkOutputsReasoningContentWhenPresent(t *testing.T) {
	chunk := makeSSEChunk("r1", 0, "Qwen3.7-Max", "assistant", "hi", "thinking...", nil)
	if !strings.Contains(chunk, "thinking...") {
		t.Error("chunk should contain reasoning_content")
	}
}

func TestExtractMessageImagesAndBuildUserMessage(t *testing.T) {
	dataURL := "data:image/png;base64,iVBORw0KGgo="
	msg := map[string]interface{}{
		"role": "user",
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": "what is this?"},
			map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": dataURL}},
			map[string]interface{}{"type": "input_image", "image_url": map[string]interface{}{"url": "https://x/a.jpg"}},
		},
	}
	urls := ExtractMessageImages(msg)
	if len(urls) != 2 {
		t.Fatalf("expected 2 images, got %d", len(urls))
	}
	if urls[0] != dataURL {
		t.Errorf("first URL mismatch: got %q", urls[0])
	}
	if urls[1] != "https://x/a.jpg" {
		t.Errorf("second URL mismatch: got %q", urls[1])
	}

	built := buildUserMessage("what is this?", urls)
	if len(built.Contents) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(built.Contents))
	}
	if built.Contents[0].Type != "image_url" {
		t.Error("first part should be image_url")
	}
	if built.Contents[2].Text != "what is this?" {
		t.Error("last part should be text")
	}
}

func TestBuildUserMessageImageOnly(t *testing.T) {
	built := buildUserMessage("", []string{"data:image/png;base64,AAA"})
	if len(built.Contents) != 1 {
		t.Fatalf("expected 1 part, got %d", len(built.Contents))
	}
	if built.Contents[0].Type != "image_url" {
		t.Error("part should be image_url")
	}
}

func TestConvertIncomingUserMessageWithImage(t *testing.T) {
	msg := map[string]interface{}{
		"role": "user",
		"content": []interface{}{
			map[string]interface{}{
				"type":      "image_url",
				"image_url": map[string]interface{}{"url": "data:image/png;base64,AAA"},
			},
			map[string]interface{}{"type": "text", "text": "describe"},
		},
	}
	out := convertIncomingMessage(msg, false, false)
	if out == nil {
		t.Fatal("expected non-nil message")
	}
	if out.Role != "user" {
		t.Errorf("expected role 'user', got %q", out.Role)
	}
	if len(out.Contents) == 0 || out.Contents[0].Type != "image_url" {
		t.Error("first content part should be image_url")
	}
	if out.Contents[len(out.Contents)-1].Text != "describe" {
		t.Error("last content part should be text 'describe'")
	}
}

func TestParseToolCallsText(t *testing.T) {
	// Standard format
	calls := ParseToolCallsText("Tool calls: [{\"id\":\"1\",\"type\":\"function\",\"function\":{\"name\":\"do\",\"arguments\":\"{}\"}}]")
	if calls == nil {
		t.Fatal("expected non-nil tool calls")
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Function.Name != "do" {
		t.Errorf("expected name 'do', got %q", calls[0].Function.Name)
	}

	// Non-tool-call text
	calls = ParseToolCallsText("just some text")
	if calls != nil {
		t.Error("expected nil for non-tool-call text")
	}

	// Empty
	calls = ParseToolCallsText("")
	if calls != nil {
		t.Error("expected nil for empty text")
	}
}

func TestExtractLatestUserPrompt(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "system", "content": "Sys"},
		{"role": "user", "content": "first"},
		{"role": "assistant", "content": "reply"},
		{"role": "user", "content": "second"},
	}
	prompt := ExtractLatestUserPrompt(messages)
	if prompt != "second" {
		t.Errorf("expected 'second', got %q", prompt)
	}
}

// A malformed negative or huge tool-call index must not panic or trigger
// unbounded slice growth; it falls back to appending at the end.
func TestToolCallAccumulatorRejectsBadIndex(t *testing.T) {
	fn := func(name string) map[string]interface{} {
		return map[string]interface{}{
			"function": map[string]interface{}{"name": name, "arguments": "{}"},
			"type":     "function",
		}
	}

	// Negative index.
	a := NewToolCallAccumulator()
	a.Append([]map[string]interface{}{withIndex(fn("neg"), -1)})
	if a.IsEmpty() {
		t.Fatal("expected one call appended after negative index")
	}
	snap := a.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 call, got %d", len(snap))
	}

	// Huge index.
	b := NewToolCallAccumulator()
	b.Append([]map[string]interface{}{withIndex(fn("huge"), 1000000000)})
	if b.IsEmpty() {
		t.Fatal("expected one call appended after huge index")
	}
	if len(b.Snapshot()) != 1 {
		t.Fatalf("expected 1 call after huge index, got %d", len(b.Snapshot()))
	}
}

func withIndex(m map[string]interface{}, idx int) map[string]interface{} {
	m["index"] = float64(idx)
	return m
}

// --- Regression tests for review fixes ---

// Deltas without an index (or with malformed ones) must not grow the
// accumulator beyond maxToolCalls: the fallback i = len(calls) used to
// bypass the cap entirely.
func TestToolCallAccumulatorCapNotBypassedByMissingIndex(t *testing.T) {
	a := NewToolCallAccumulator()
	fn := func() map[string]interface{} {
		return map[string]interface{}{
			"function": map[string]interface{}{"name": "x", "arguments": "{}"},
			"type":     "function",
			// no "index" key at all
		}
	}
	for i := 0; i < maxToolCalls+100; i++ {
		a.Append([]map[string]interface{}{fn()})
	}
	if got := len(a.Snapshot()); got != maxToolCalls {
		t.Errorf("cap bypassed: got %d calls, want exactly %d", got, maxToolCalls)
	}
}

// An upstream float64 index must be preserved in the emitted chunk so
// clients can reassemble parallel tool calls correctly.
func TestWithToolCallIndicesPreservesUpstreamIndex(t *testing.T) {
	delta := []map[string]interface{}{
		{"id": "call_2", "type": "function", "index": float64(2),
			"function": map[string]interface{}{"name": "f", "arguments": ""}},
	}
	out := withToolCallIndices(delta)
	if got, _ := out[0]["index"].(float64); got != 2 {
		t.Errorf("upstream index clobbered: got %v, want 2", out[0]["index"])
	}
	// Missing index still gets a sequential one.
	out = withToolCallIndices([]map[string]interface{}{{"id": "a"}})
	if got, _ := out[0]["index"].(float64); got != 0 {
		t.Errorf("expected sequential index 0, got %v", out[0]["index"])
	}
}

// A frame may carry BOTH choices (content) and usage (GLM-style final
// frame). Both must be surfaced; usage must not swallow content and
// content must not drop usage.
func TestExtractDeltaContentFrameWithUsageIsNotSwallowed(t *testing.T) {
	inner, _ := json.Marshal(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index":         float64(0),
				"delta":         map[string]interface{}{"content": "hello"},
				"finish_reason": nil,
			},
		},
		"usage": map[string]interface{}{"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6},
	})
	wrapper, _ := json.Marshal(map[string]interface{}{"body": string(inner)})
	delta := ExtractDelta(string(wrapper))
	if delta.Usage == nil {
		t.Error("usage attached to a content frame must be captured")
	} else if delta.Usage.PromptTokens != 5 {
		t.Errorf("unexpected usage: %+v", delta.Usage)
	}
	if delta.Content != "hello" {
		t.Errorf("content swallowed: %+v", delta)
	}
}

// Regression (real gateway, GLM-5.3 / key "gmodel"): the terminal frame
// carries finish_reason=stop together with usage and an empty delta. The
// old code treated any frame with choices as content-only and silently
// dropped the usage, so token stats stayed zero for GLM models.
func TestExtractDeltaGLMStyleFinalFrame(t *testing.T) {
	inner, _ := json.Marshal(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index":         float64(0),
				"delta":         map[string]interface{}{"content": "", "role": "assistant"},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":             19,
			"completion_tokens":         90,
			"total_tokens":              109,
			"credits":                   0.059,
			"prompt_tokens_details":     map[string]interface{}{"cached_tokens": 0},
			"completion_tokens_details": map[string]interface{}{"reasoning_tokens": 86},
		},
	})
	wrapper, _ := json.Marshal(map[string]interface{}{"body": string(inner)})
	delta := ExtractDelta(string(wrapper))
	if delta.Usage == nil {
		t.Fatal("usage on GLM-style final frame must be captured")
	}
	if delta.Usage.PromptTokens != 19 || delta.Usage.CompletionTokens != 90 {
		t.Errorf("unexpected usage: %+v", delta.Usage)
	}
	if delta.FinishReason != "stop" {
		t.Errorf("finish_reason lost: %+v", delta)
	}
}

// Upstream finish_reason (e.g. "length") must flow through to the client.
func TestExtractDeltaCapturesFinishReason(t *testing.T) {
	inner, _ := json.Marshal(map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index":         float64(0),
				"delta":         map[string]interface{}{},
				"finish_reason": "length",
			},
		},
	})
	wrapper, _ := json.Marshal(map[string]interface{}{"body": string(inner)})
	delta := ExtractDelta(string(wrapper))
	if delta.FinishReason != "length" {
		t.Errorf("expected finish_reason=length, got %q", delta.FinishReason)
	}
	if delta.IsEmpty() {
		t.Error("finish_reason-only delta must not be empty")
	}
}

func TestStreamAccumulatorUpstreamFinishReason(t *testing.T) {
	var out []string
	acc := NewStreamAccumulator("r", 1, "m", false, func(c string) { out = append(out, c) })
	acc.Accept(&BridgeDelta{Content: "hi"})
	acc.Accept(&BridgeDelta{FinishReason: "length"})
	acc.Flush()
	if got := acc.FinishReason(); got != "length" {
		t.Errorf("expected upstream finish_reason=length to win, got %q", got)
	}
}

// Images carried in the Qoder-style "contents" array must be extracted just
// like "content" ones (the text fallback path already reads "contents").
func TestExtractMessageImagesFallsBackToContents(t *testing.T) {
	msg := map[string]interface{}{
		"role": "user",
		"contents": []interface{}{
			map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": "https://x/1.png"}},
			map[string]interface{}{"type": "text", "text": "hi"},
		},
	}
	urls := ExtractMessageImages(msg)
	if len(urls) != 1 || urls[0] != "https://x/1.png" {
		t.Errorf("expected contents image fallback, got %v", urls)
	}
}
