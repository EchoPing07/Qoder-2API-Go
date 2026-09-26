// Streaming and non-streaming response delivery: SSE chunk forwarding for
// stream=true requests and the accumulated JSON response for stream=false.
// Extracted from bridge.go; behaviour is unchanged.
package bridge

import (
	"context"
	"log"
	"net/http"
	"strings"

	"qoder2api/transform"
)

// StreamReportedError wraps an upstream stream error that has already been
// delivered to the client as an SSE error chunk. Handlers must not write a
// second error response when this error is returned.
type StreamReportedError struct {
	Err error
}

func (e *StreamReportedError) Error() string { return e.Err.Error() }
func (e *StreamReportedError) Unwrap() error { return e.Err }

func (b *OpenAiBridge) handleStream(ctx context.Context, w http.ResponseWriter, jsonBody []byte, url string, extraHeaders map[string]string, reqID string, created int64, model string, toolsEnabled bool, includeUsage bool, usageSink UsageSink) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	var usage *transform.Usage

	acc := transform.NewStreamAccumulator(reqID, created, model, toolsEnabled, func(chunk string) {
		w.Write([]byte(chunk))
		if flusher != nil {
			flusher.Flush()
		}
	})

	err := b.openStreamAsync(ctx, url, jsonBody, extraHeaders, func(line string) error {
		if !strings.HasPrefix(line, "data:") {
			return nil
		}
		delta := transform.ExtractDelta(strings.TrimSpace(line[5:]))
		// Usage may ride on the final content frame (GLM-style) instead of a
		// standalone frame; capture it and keep processing the delta below.
		if delta.Usage != nil {
			usage = delta.Usage
		}
		if !delta.IsEmpty() {
			acc.Accept(delta)
		}
		return nil
	})

	acc.Flush()

	if err != nil {
		log.Printf("[bridge] stream error: %v", err)
		errChunk := transform.MakeChunk(reqID, created, model)
		writeFinishReason(errChunk, "error")
		clearDelta(errChunk)
		setError(errChunk, err.Error())
		errBytes, _ := marshalNoEscape(errChunk)
		w.Write([]byte("data: " + string(errBytes) + "\n\n"))
		return &StreamReportedError{Err: err}
	} else {
		doneChunk := transform.MakeChunk(reqID, created, model)
		writeFinishReason(doneChunk, acc.FinishReason())
		clearDelta(doneChunk)
		doneBytes, _ := marshalNoEscape(doneChunk)
		w.Write([]byte("data: " + string(doneBytes) + "\n\n"))
	}

	// Emit the usage frame before [DONE] only when the client opted in via
	// stream_options.include_usage (OpenAI semantics). Internal statistics
	// still receive usage through the sink either way.
	if usage != nil {
		if includeUsage {
			usageChunk := transform.MakeChunk(reqID, created, model)
			usageChunk["choices"] = []map[string]interface{}{}
			usageChunk["usage"] = usage
			usageBytes, _ := marshalNoEscape(usageChunk)
			w.Write([]byte("data: " + string(usageBytes) + "\n\n"))
		}
		if usageSink != nil {
			usageSink(usage)
		}
	}

	if flusher != nil {
		flusher.Flush()
	}
	w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func (b *OpenAiBridge) handleSync(ctx context.Context, w http.ResponseWriter, jsonBody []byte, url string, extraHeaders map[string]string, reqID string, created int64, model string, toolsEnabled bool, usageSink UsageSink) error {
	var fullContent []string
	var fullReasoning []string
	toolCalls := transform.NewToolCallAccumulator()
	var usage *transform.Usage

	err := b.openStreamAsync(ctx, url, jsonBody, extraHeaders, func(line string) error {
		if !strings.HasPrefix(line, "data:") {
			return nil
		}
		delta := transform.ExtractDelta(strings.TrimSpace(line[5:]))
		// Usage may ride on the final content frame (GLM-style); capture it
		// and keep processing content/reasoning/tool calls below.
		if delta.Usage != nil {
			usage = delta.Usage
		}
		if delta.ReasoningContent != "" {
			fullReasoning = append(fullReasoning, delta.ReasoningContent)
		}
		if delta.Content != "" {
			fullContent = append(fullContent, delta.Content)
		}
		if len(delta.ToolCalls) > 0 {
			toolCalls.Append(delta.ToolCalls)
		}
		return nil
	})

	if err != nil {
		return err
	}

	fullText := strings.Join(fullContent, "")
	var fallbackToolCalls []map[string]interface{}
	if toolCalls.IsEmpty() && toolsEnabled {
		parsed := transform.ParseToolCallsText(fullText)
		if parsed != nil {
			fallbackToolCalls = transform.ToolsCallsToMaps(parsed)
		}
	}

	msg := map[string]interface{}{
		"role": "assistant",
	}
	if fallbackToolCalls != nil {
		msg["content"] = nil
		msg["tool_calls"] = fallbackToolCalls
	} else if fullText == "" && !toolCalls.IsEmpty() {
		msg["content"] = nil
	} else {
		msg["content"] = fullText
	}
	if len(fullReasoning) > 0 {
		msg["reasoning_content"] = strings.Join(fullReasoning, "")
	}
	if !toolCalls.IsEmpty() {
		msg["tool_calls"] = toolCalls.Snapshot()
	}

	finishReason := "stop"
	if !toolCalls.IsEmpty() || fallbackToolCalls != nil {
		finishReason = "tool_calls"
	}

	resp := map[string]interface{}{
		"id":      reqID,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	}
	if usage != nil {
		resp["usage"] = usage
		if usageSink != nil {
			usageSink(usage)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	respBytes, _ := marshalNoEscape(resp)
	w.Write(respBytes)
	return nil
}

func writeFinishReason(chunk map[string]interface{}, reason string) {
	choices, ok := chunk["choices"].([]map[string]interface{})
	if !ok || len(choices) == 0 {
		return
	}
	choices[0]["finish_reason"] = reason
}

func clearDelta(chunk map[string]interface{}) {
	choices, ok := chunk["choices"].([]map[string]interface{})
	if !ok || len(choices) == 0 {
		return
	}
	choices[0]["delta"] = map[string]interface{}{}
}

func setError(chunk map[string]interface{}, msg string) {
	chunk["error"] = map[string]interface{}{
		"message": msg,
		"type":    "qoder_error",
	}
}
