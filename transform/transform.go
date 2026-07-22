// Package transform handles OpenAI ↔ Qoder message conversion and stream accumulation.
package transform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// --- BridgeDelta ---

type BridgeDelta struct {
	Role             string
	Content          string
	ReasoningContent string
	ToolCalls        []map[string]interface{}
}

func (d *BridgeDelta) IsEmpty() bool {
	return d.Role == "" && d.Content == "" && d.ReasoningContent == "" &&
		(d.ToolCalls == nil || len(d.ToolCalls) == 0)
}

// --- Qoder message types ---

type responseMetaUsage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	TotalTokens             int `json:"total_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type responseMeta struct {
	ID    string            `json:"id"`
	Usage responseMetaUsage `json:"usage"`
}

func blankResponseMeta() responseMeta {
	return responseMeta{ID: "", Usage: responseMetaUsage{}}
}

type imageURLPart struct {
	URL string `json:"url"`
}

// ContentPart is a part in the contents array of a user message.
// ImageURL is nil for text parts; Text is "" for image parts.
type ContentPart struct {
	Type     string        `json:"type"`
	ImageURL *imageURLPart `json:"image_url,omitempty"`
	Text     string        `json:"text,omitempty"`
}

// NormalizedToolCall is a tool call with normalized structure.
type NormalizedToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// QoderMessage is a message in the Qoder chat request body.
// Field order MUST match Python dict insertion order for signature compatibility.
type QoderMessage struct {
	Role                string               `json:"role"`
	Content             string               `json:"content"`
	ResponseMeta        responseMeta         `json:"response_meta"`
	ReasoningContentSig string               `json:"reasoning_content_signature"`
	Contents            []ContentPart        `json:"contents,omitempty"`
	ToolCalls           []NormalizedToolCall `json:"tool_calls,omitempty"`
	Name                string               `json:"name,omitempty"`
	ToolCallID          string               `json:"tool_call_id,omitempty"`
}

func buildUserMessage(text string, images []string) QoderMessage {
	parts := []ContentPart{}
	for _, url := range images {
		parts = append(parts, ContentPart{
			Type:     "image_url",
			ImageURL: &imageURLPart{URL: url},
		})
	}
	if strings.TrimSpace(text) != "" {
		parts = append(parts, ContentPart{Type: "text", Text: text})
	}
	if len(parts) == 0 {
		parts = append(parts, ContentPart{Type: "text", Text: ""})
	}
	return QoderMessage{
		Role:                "user",
		Content:             "",
		ResponseMeta:        blankResponseMeta(),
		ReasoningContentSig: "",
		Contents:            parts,
	}
}

func buildStructuredMessage(role, text string) QoderMessage {
	return QoderMessage{
		Role:                role,
		Content:             text,
		ResponseMeta:        blankResponseMeta(),
		ReasoningContentSig: "",
	}
}

func buildAssistantToolCallMessage(text string, toolCalls []NormalizedToolCall) QoderMessage {
	content := text
	if ParseToolCallsText(content) != nil {
		content = ""
	}
	msg := buildStructuredMessage("assistant", content)
	msg.ToolCalls = toolCalls
	return msg
}

func buildToolMessage(name, toolCallID, text string) QoderMessage {
	msg := buildStructuredMessage("tool", text)
	msg.Name = name
	msg.ToolCallID = toolCallID
	return msg
}

// --- Tool call text parsing ---

func renderToolCalls(toolCalls []map[string]interface{}) string {
	return "Tool calls:\n" + mustJSON(toolCalls)
}

func renderToolResult(name, toolCallID, text string) string {
	parts := []string{"Tool result"}
	if name != "" {
		parts = append(parts, fmt.Sprintf(" (%s)", name))
	}
	if toolCallID != "" {
		parts = append(parts, fmt.Sprintf(" [%s]", toolCallID))
	}
	if strings.TrimSpace(text) != "" {
		parts = append(parts, fmt.Sprintf(":\n%s", text))
	}
	return strings.Join(parts, "")
}

func summarizeUnresolvedToolCalls(toolCalls []map[string]interface{}) string {
	sb := []string{"Previously planned but unexecuted tool calls"}
	limit := len(toolCalls)
	if limit > 6 {
		limit = 6
	}
	names := []string{}
	for i := 0; i < limit; i++ {
		name := ""
		if fn, ok := toolCalls[i]["function"].(map[string]interface{}); ok {
			if n, ok := fn["name"].(string); ok {
				name = n
			}
		}
		if name == "" {
			name = "unknown"
		}
		names = append(names, name)
	}
	if len(names) > 0 {
		sb = append(sb, ": ")
		sb = append(sb, strings.Join(names, ", "))
	}
	if len(toolCalls) > limit {
		sb = append(sb, fmt.Sprintf(" and %d more", len(toolCalls)-limit))
	}
	sb = append(sb, ".")
	return strings.Join(sb, "")
}

func joinSections(first, second string) string {
	if strings.TrimSpace(first) == "" {
		return second
	}
	if strings.TrimSpace(second) == "" {
		return first
	}
	return first + "\n\n" + second
}

// hasResolvedToolResponse checks if an assistant message at index i has a
// following tool response.
func hasResolvedToolResponse(messages []map[string]interface{}, assistantIndex int) bool {
	message := messages[assistantIndex]
	role, _ := message["role"].(string)
	if role != "assistant" {
		return false
	}
	tc := message["tool_calls"]
	hasToolCalls := false
	if tcList, ok := tc.([]interface{}); ok && len(tcList) > 0 {
		hasToolCalls = true
	}
	if !hasToolCalls {
		text := normalizeMessageText(message)
		if ParseToolCallsText(text) != nil {
			hasToolCalls = true
		}
	}
	if !hasToolCalls {
		return false
	}
	for i := assistantIndex + 1; i < len(messages); i++ {
		nextRole, _ := messages[i]["role"].(string)
		if nextRole == "tool" {
			return true
		}
		if nextRole == "assistant" || nextRole == "user" || nextRole == "system" {
			return false
		}
	}
	return false
}

// extractAnyToolCalls extracts tool calls from a message.
func extractAnyToolCalls(message map[string]interface{}, text string, toolsEnabled bool) []NormalizedToolCall {
	if !toolsEnabled {
		return nil
	}
	tc, ok := message["tool_calls"].([]interface{})
	if ok && len(tc) > 0 {
		return normalizeToolCalls(tc)
	}
	parsed := ParseToolCallsText(text)
	if parsed != nil {
		return parsed
	}
	return nil
}

// convertIncomingMessage converts an OpenAI message to a Qoder message.
func convertIncomingMessage(message map[string]interface{}, toolsEnabled, allowStructuredToolCalls bool) *QoderMessage {
	role, _ := message["role"].(string)
	if role == "" {
		role = "user"
	}
	text := normalizeMessageText(message)
	anyToolCalls := extractAnyToolCalls(message, text, toolsEnabled)
	var structuredToolCalls []NormalizedToolCall
	if toolsEnabled && allowStructuredToolCalls {
		structuredToolCalls = extractAnyToolCalls(message, text, true)
	}

	if role == "assistant" && structuredToolCalls != nil {
		msg := buildAssistantToolCallMessage(text, structuredToolCalls)
		return &msg
	}
	if role == "assistant" && anyToolCalls != nil && !allowStructuredToolCalls {
		msg := buildStructuredMessage("assistant", summarizeUnresolvedToolCalls(toolsCallsToMaps(anyToolCalls)))
		return &msg
	}

	// Handle tool_calls when tools are not enabled
	tc, ok := message["tool_calls"].([]interface{})
	if !toolsEnabled && ok && len(tc) > 0 {
		text = joinSections(text, renderToolCalls(interfaceSliceToMapSlice(tc)))
	}

	if role == "tool" {
		if toolsEnabled {
			name, _ := message["name"].(string)
			toolCallID, _ := message["tool_call_id"].(string)
			msg := buildToolMessage(name, toolCallID, text)
			return &msg
		}
		role = "user"
		name, _ := message["name"].(string)
		toolCallID, _ := message["tool_call_id"].(string)
		text = renderToolResult(name, toolCallID, text)
	}

	// Collect inline images (only for user messages)
	var images []string
	if role == "user" {
		images = ExtractMessageImages(message)
	}
	// When images are carried as real image_url parts, drop the "[image] <url>"
	// textual fallback that normalizeMessageText injected.
	if len(images) > 0 {
		segments := strings.Split(text, "\n\n")
		filtered := []string{}
		for _, seg := range segments {
			s := strings.TrimSpace(seg)
			if s != "" && !strings.HasPrefix(s, "[image] ") {
				filtered = append(filtered, s)
			}
		}
		text = strings.Join(filtered, "\n\n")
	}

	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return nil
	}

	if role == "user" {
		msg := buildUserMessage(text, images)
		return &msg
	}

	msg := buildStructuredMessage(role, text)
	return &msg
}

// BuildQoderMessages converts OpenAI messages to Qoder format.
func BuildQoderMessages(incomingMessages []map[string]interface{}, prompt string, toolsEnabled bool) []QoderMessage {
	rebuilt := []QoderMessage{}

	if len(incomingMessages) > 0 {
		for i, message := range incomingMessages {
			allowStructured := toolsEnabled && hasResolvedToolResponse(incomingMessages, i)
			converted := convertIncomingMessage(message, toolsEnabled, allowStructured)
			if converted != nil {
				rebuilt = append(rebuilt, *converted)
			}
		}
	}

	if len(rebuilt) == 0 && strings.TrimSpace(prompt) != "" {
		rebuilt = append(rebuilt, buildUserMessage(prompt, nil))
	}

	return rebuilt
}

// ApplyOpenAIToolConfig applies tool configuration from the request to the body.
// Returns true if tools are enabled.
func ApplyOpenAIToolConfig(body map[string]interface{}, reqBody map[string]interface{}) bool {
	incomingTools, ok := reqBody["tools"].([]interface{})
	toolsEnabled := ok && len(incomingTools) > 0
	if toolsEnabled {
		body["tools"] = incomingTools
	} else {
		delete(body, "tools")
	}
	if tc, ok := reqBody["tool_choice"]; ok {
		body["tool_choice"] = tc
	} else {
		delete(body, "tool_choice")
	}
	if ptc, ok := reqBody["parallel_tool_calls"]; ok {
		body["parallel_tool_calls"] = ptc
	} else {
		delete(body, "parallel_tool_calls")
	}
	return toolsEnabled
}

// ExtractLatestUserPrompt extracts the latest user message text.
func ExtractLatestUserPrompt(messages []map[string]interface{}) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if role, _ := messages[i]["role"].(string); role == "user" {
			text := normalizeMessageText(messages[i])
			if strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return ""
}

// --- Content normalization ---

func normalizeContent(content interface{}) string {
	if content == nil {
		return ""
	}
	switch c := content.(type) {
	case string:
		return c
	case []interface{}:
		parts := []string{}
		for _, item := range c {
			part := normalizeContentPart(item)
			if part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, "\n\n")
	case map[string]interface{}:
		return normalizeContentPart(c)
	default:
		return fmt.Sprintf("%v", content)
	}
}

func normalizeContentPart(item interface{}) string {
	if item == nil {
		return ""
	}
	switch v := item.(type) {
	case string:
		return v
	case map[string]interface{}:
		t, _ := v["type"].(string)
		if text, ok := v["text"].(string); ok {
			return text
		}
		if t == "image_url" || t == "input_image" {
			iu, ok := v["image_url"].(map[string]interface{})
			if ok {
				url, _ := iu["url"].(string)
				if url != "" {
					return fmt.Sprintf("[image] %s", url)
				}
			}
			if url, ok := v["url"].(string); ok && url != "" {
				return fmt.Sprintf("[image] %s", url)
			}
		}
		if content, ok := v["content"]; ok {
			if _, isList := content.([]interface{}); isList {
				return normalizeContent(content)
			}
			if _, isMap := content.(map[string]interface{}); isMap {
				return normalizeContent(content)
			}
		}
		b, _ := json.Marshal(v)
		return string(b)
	default:
		return fmt.Sprintf("%v", item)
	}
}

func extractImageDataURL(part map[string]interface{}) string {
	t, _ := part["type"].(string)
	if t != "image_url" && t != "input_image" {
		return ""
	}
	iu := part["image_url"]
	switch v := iu.(type) {
	case map[string]interface{}:
		url, _ := v["url"].(string)
		if url != "" {
			return url
		}
	case string:
		return v
	}
	url, _ := part["url"].(string)
	return url
}

// ExtractMessageImages returns all image URLs found in an OpenAI message.
func ExtractMessageImages(message map[string]interface{}) []string {
	content, ok := message["content"].([]interface{})
	if !ok {
		return nil
	}
	urls := []string{}
	for _, item := range content {
		if part, ok := item.(map[string]interface{}); ok {
			url := extractImageDataURL(part)
			if url != "" {
				urls = append(urls, url)
			}
		}
	}
	return urls
}

func normalizeMessageText(message map[string]interface{}) string {
	text := normalizeContent(message["content"])
	if strings.TrimSpace(text) == "" {
		text = normalizeContent(message["contents"])
	}
	return text
}

// --- Tool calls normalization ---

func normalizeToolArguments(arguments interface{}) string {
	if arguments == nil {
		return ""
	}
	switch v := arguments.(type) {
	case string:
		return v
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func normalizeToolCalls(rawToolCalls []interface{}) []NormalizedToolCall {
	normalized := []NormalizedToolCall{}
	for _, rtc := range rawToolCalls {
		m, ok := rtc.(map[string]interface{})
		if !ok {
			continue
		}
		funcMap, _ := m["function"].(map[string]interface{})
		name, _ := funcMap["name"].(string)
		arguments := normalizeToolArguments(funcMap["arguments"])
		if name == "" && arguments == "" {
			continue
		}
		tc := NormalizedToolCall{}
		tc.ID, _ = m["id"].(string)
		tc.Type, _ = m["type"].(string)
		if tc.Type == "" {
			tc.Type = "function"
		}
		tc.Function.Name = name
		tc.Function.Arguments = arguments
		normalized = append(normalized, tc)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

// ParseToolCallsText tries to parse "Tool calls: [...]" format from text.
func ParseToolCallsText(text string) []NormalizedToolCall {
	if text == "" {
		return nil
	}
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "Tool calls:") {
		return nil
	}
	payload := strings.TrimSpace(trimmed[len("Tool calls:"):])
	if strings.HasPrefix(payload, "```") && strings.HasSuffix(payload, "```") {
		newline := strings.Index(payload, "\n")
		if newline >= 0 {
			payload = strings.TrimSpace(payload[newline+1 : len(payload)-3])
		}
	}
	if !strings.HasPrefix(payload, "[") {
		return nil
	}
	var parsed []interface{}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return nil
	}
	return normalizeToolCalls(parsed)
}

// --- ToolCallAccumulator ---

type ToolCallAccumulator struct {
	calls []map[string]interface{}
}

func NewToolCallAccumulator() *ToolCallAccumulator {
	return &ToolCallAccumulator{calls: []map[string]interface{}{}}
}

func (a *ToolCallAccumulator) Append(deltaCalls []map[string]interface{}) {
	for _, dc := range deltaCalls {
		idx, ok := dc["index"].(float64)
		if !ok {
			idx = float64(len(a.calls))
		}
		i := int(idx)
		for len(a.calls) <= i {
			a.calls = append(a.calls, map[string]interface{}{
				"id":       "",
				"type":     "function",
				"function": map[string]interface{}{"name": "", "arguments": ""},
			})
		}
		existing := a.calls[i]
		if id, ok := dc["id"].(string); ok {
			existing["id"] = id
		}
		if t, ok := dc["type"].(string); ok {
			existing["type"] = t
		}
		df, _ := dc["function"].(map[string]interface{})
		ef, _ := existing["function"].(map[string]interface{})
		if name, ok := df["name"].(string); ok {
			ef["name"] = name
		}
		if args, ok := df["arguments"].(string); ok {
			cur, _ := ef["arguments"].(string)
			ef["arguments"] = cur + args
		}
	}
}

func (a *ToolCallAccumulator) IsEmpty() bool {
	return len(a.calls) == 0
}

func (a *ToolCallAccumulator) Snapshot() []map[string]interface{} {
	result := make([]map[string]interface{}, len(a.calls))
	for i, c := range a.calls {
		b, _ := json.Marshal(c)
		var copy map[string]interface{}
		json.Unmarshal(b, &copy)
		result[i] = copy
	}
	return result
}

// --- StreamAccumulator ---

type StreamAccumulator struct {
	reqID            string
	created          int64
	model            string
	toolCallFallback bool
	toolCalls        *ToolCallAccumulator
	pendingContent   []string
	pendingRole      string
	emitted          bool
	streamingText    bool
	emitFn           func(string)
	chunks           []string
}

func NewStreamAccumulator(reqID string, created int64, model string, toolCallFallback bool, emitFn func(string)) *StreamAccumulator {
	return &StreamAccumulator{
		reqID:            reqID,
		created:          created,
		model:            model,
		toolCallFallback: toolCallFallback,
		toolCalls:        NewToolCallAccumulator(),
		pendingContent:   []string{},
		pendingRole:      "assistant",
		emitFn:           emitFn,
		chunks:           []string{},
	}
}

func (a *StreamAccumulator) Accept(delta *BridgeDelta) {
	if delta.Role != "" {
		a.pendingRole = delta.Role
	}

	if delta.ReasoningContent != "" {
		a.emit("", delta.ReasoningContent, nil)
	}

	if len(delta.ToolCalls) > 0 {
		a.discardBufferedToolCallText()
		a.toolCalls.Append(delta.ToolCalls)
		a.emit("", "", withToolCallIndices(delta.ToolCalls))
		return
	}

	if delta.Content == "" {
		return
	}

	if !a.toolCallFallback || a.streamingText {
		a.streamingText = true
		a.emit(delta.Content, "", nil)
		return
	}

	a.pendingContent = append(a.pendingContent, delta.Content)
	text := strings.Join(a.pendingContent, "")
	if isPotentialToolCallText(text) {
		return
	}
	a.streamingText = true
	a.emitBufferedText()
}

func (a *StreamAccumulator) Flush() {
	if len(a.pendingContent) == 0 {
		return
	}
	buffered := strings.Join(a.pendingContent, "")
	a.pendingContent = nil
	var parsed []NormalizedToolCall
	if a.toolCallFallback {
		parsed = ParseToolCallsText(buffered)
	}
	if parsed != nil {
		a.toolCalls.Append(toolsCallsToMaps(parsed))
		a.emit("", "", withToolCallIndices(toolsCallsToMaps(parsed)))
		return
	}
	a.streamingText = true
	a.emit(buffered, "", nil)
}

func (a *StreamAccumulator) FinishReason() string {
	if !a.toolCalls.IsEmpty() {
		return "tool_calls"
	}
	return "stop"
}

func (a *StreamAccumulator) GetChunks() []string {
	return a.chunks
}

func (a *StreamAccumulator) emitBufferedText() {
	if len(a.pendingContent) == 0 {
		return
	}
	buffered := strings.Join(a.pendingContent, "")
	a.pendingContent = nil
	a.emit(buffered, "", nil)
}

func (a *StreamAccumulator) discardBufferedToolCallText() {
	if len(a.pendingContent) == 0 {
		return
	}
	buffered := strings.Join(a.pendingContent, "")
	a.pendingContent = nil
	if a.toolCallFallback && isPotentialToolCallText(buffered) {
		return
	}
	a.streamingText = true
	a.emit(buffered, "", nil)
}

func (a *StreamAccumulator) emit(content, reasoningContent string, toolCalls []map[string]interface{}) {
	role := ""
	if !a.emitted {
		role = a.pendingRole
		if role == "" {
			role = "assistant"
		}
	}
	chunk := makeSSEChunk(a.reqID, a.created, a.model, role, content, reasoningContent, toolCalls)
	if a.emitFn != nil {
		a.emitFn(chunk)
	} else {
		a.chunks = append(a.chunks, chunk)
	}
	a.emitted = true
}

func isPotentialToolCallText(text string) bool {
	candidate := strings.TrimLeft(text, " \t\n\r")
	if candidate == "" {
		return true
	}
	return strings.HasPrefix("Tool calls:", candidate) || strings.HasPrefix(candidate, "Tool calls:")
}

func withToolCallIndices(rawToolCalls []map[string]interface{}) []map[string]interface{} {
	indexed := []map[string]interface{}{}
	for i, tc := range rawToolCalls {
		copy := deepCopyMap(tc)
		if _, ok := copy["index"].(int); !ok {
			copy["index"] = i
		}
		indexed = append(indexed, copy)
	}
	return indexed
}

// --- SSE chunk construction ---

type chunkChoice struct {
	Index        int                    `json:"index"`
	Delta        map[string]interface{} `json:"delta"`
	FinishReason interface{}            `json:"finish_reason"`
}

type chatChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
}

func MakeChunk(reqID string, created int64, model string) map[string]interface{} {
	return map[string]interface{}{
		"id":      reqID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         map[string]interface{}{},
				"finish_reason": nil,
			},
		},
	}
}

func makeSSEChunk(reqID string, created int64, model, role, content, reasoningContent string, toolCalls []map[string]interface{}) string {
	chunk := MakeChunk(reqID, created, model)
	choices, _ := chunk["choices"].([]map[string]interface{})
	if len(choices) == 0 {
		return ""
	}
	delta := choices[0]["delta"].(map[string]interface{})
	if role != "" {
		delta["role"] = role
	}
	if content != "" {
		delta["content"] = content
	}
	if reasoningContent != "" {
		delta["reasoning_content"] = reasoningContent
	}
	if len(toolCalls) > 0 {
		delta["tool_calls"] = toolCalls
	}
	b, _ := marshalNoEscapeChunk(chunk)
	return "data: " + string(b) + "\n\n"
}

// ExtractDelta parses a Qoder SSE data line into a BridgeDelta.
func ExtractDelta(dataLine string) *BridgeDelta {
	delta := &BridgeDelta{}
	var wrapper map[string]interface{}
	if err := json.Unmarshal([]byte(dataLine), &wrapper); err != nil {
		return delta
	}
	innerStr, ok := wrapper["body"].(string)
	if !ok || innerStr == "" {
		return delta
	}
	var innerJSON map[string]interface{}
	if err := json.Unmarshal([]byte(innerStr), &innerJSON); err != nil {
		return delta
	}
	choices, ok := innerJSON["choices"].([]interface{})
	if !ok {
		return delta
	}
	for _, ch := range choices {
		choice, ok := ch.(map[string]interface{})
		if !ok {
			continue
		}
		deltaMap, _ := choice["delta"].(map[string]interface{})
		role, _ := deltaMap["role"].(string)
		content, _ := deltaMap["content"].(string)
		reasoning, _ := deltaMap["reasoning_content"].(string)
		tc, hasTC := deltaMap["tool_calls"].([]interface{})
		var toolCalls []map[string]interface{}
		if hasTC && len(tc) > 0 {
			toolCalls = interfaceSliceToMapSlice(tc)
		}
		if role != "" || content != "" || reasoning != "" || toolCalls != nil {
			return &BridgeDelta{
				Role:             role,
				Content:          content,
				ReasoningContent: reasoning,
				ToolCalls:        toolCalls,
			}
		}
	}
	return delta
}

// --- Helpers ---

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func marshalNoEscapeChunk(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func deepCopyMap(m map[string]interface{}) map[string]interface{} {
	b, _ := json.Marshal(m)
	var copy map[string]interface{}
	json.Unmarshal(b, &copy)
	return copy
}

func interfaceSliceToMapSlice(slice []interface{}) []map[string]interface{} {
	result := []map[string]interface{}{}
	for _, item := range slice {
		if m, ok := item.(map[string]interface{}); ok {
			result = append(result, deepCopyMap(m))
		}
	}
	return result
}

func toolsCallsToMaps(calls []NormalizedToolCall) []map[string]interface{} {
	result := []map[string]interface{}{}
	for _, tc := range calls {
		m := map[string]interface{}{
			"id":   tc.ID,
			"type": tc.Type,
			"function": map[string]interface{}{
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			},
		}
		result = append(result, m)
	}
	return result
}
