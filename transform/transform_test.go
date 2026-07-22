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
