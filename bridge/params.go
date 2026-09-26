// Parameter resolution: how fields of an OpenAI chat completion request map
// onto the gateway request body. This file is the single place where client
// parameters are interpreted. Extracted from bridge.go; behaviour is
// unchanged — verbatim forwarding lands here in the next change set.
package bridge

import (
	"log"
	"math"
	"strings"

	"qoder2api/models"
)

// resolveMaxTokens picks the completion cap for the gateway request. A positive
// integer from the client wins, matching the official client's
// LS(maxOutputTokens ?? catalogDefault), but is clamped to
// models.MaxRequestedOutputTokens so absurd values never reach the gateway
// verbatim. Otherwise the catalog value applies; the clamp is only a ceiling,
// never a default. OpenAI clients may use either max_tokens or the newer
// max_completion_tokens.
func resolveMaxTokens(reqBody map[string]interface{}, catalogDefault int) int {
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		if n, ok := positiveJSONInt(reqBody[key]); ok {
			return n
		}
		if n, ok := positiveJSONFloat(reqBody[key]); ok {
			// Compared in float64 before any int conversion: on a 32-bit build
			// int(1e18) wraps (often to 0, which positiveJSONInt then rejects),
			// so the clamp must not depend on int first. Values beyond
			// math.MaxInt64 are caught here too, where an int conversion would
			// have silently fallen back to the catalog default.
			if n > float64(models.MaxRequestedOutputTokens) {
				return models.MaxRequestedOutputTokens
			}
			return int(n)
		}
	}
	return catalogDefault
}

// positiveJSONInt parses a decoded JSON number into a positive int.
func positiveJSONInt(v interface{}) (int, bool) {
	f, ok := positiveJSONFloat(v)
	if !ok || f > float64(models.MaxRequestedOutputTokens) {
		// Values above the ceiling are handled by resolveMaxTokens, which clamps
		// them without an int conversion; anything still here is unusable.
		return 0, false
	}
	return int(f), true
}

// positiveJSONFloat parses a decoded JSON number into a positive integral
// float64, without narrowing to int. Callers that need the exact magnitude (the
// clamp) use this directly so the result is identical on 32-bit and 64-bit
// builds.
func positiveJSONFloat(v interface{}) (float64, bool) {
	n, ok := v.(float64)
	if !ok || n <= 0 || n != math.Trunc(n) {
		return 0, false
	}
	return n, true
}

func extractMessages(raw interface{}) []map[string]interface{} {
	list, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]interface{}); ok {
			result = append(result, m)
		}
	}
	return result
}

// resolveReasoningEffort validates the OpenAI-style reasoning_effort against
// the selected model's catalog metadata. Empty means preserve the gateway's
// existing default reasoning behaviour by omitting the optional field.
func resolveReasoningEffort(reqBody map[string]interface{}, ri *models.ModelReasoning) string {
	raw, _ := reqBody["reasoning_effort"].(string)
	effort := normalizeReasoningEffort(raw)
	if effort == "" {
		if strings.TrimSpace(raw) != "" {
			log.Printf("[bridge] ignoring unsupported reasoning_effort %q; keeping reasoning enabled", raw)
		}
		return ""
	}
	if ri == nil || !ri.Known {
		// Without catalog metadata the tier cannot be validated. Forwarding
		// "none" is still not safe: models that do not declare
		// thinking_config.disabled reject it with an upstream 400, so it is
		// omitted. Other tiers keep the historical pass-through.
		if effort == "none" {
			log.Printf("[bridge] reasoning_effort %q not declared by model; leaving thinking on", effort)
			return ""
		}
		return effort
	}
	if effort == "none" && ri.SupportsDisabled {
		return effort
	}
	for _, supported := range ri.Efforts {
		if effort == supported {
			return effort
		}
	}
	log.Printf("[bridge] reasoning_effort %q not supported by model; keeping model default", effort)
	return ""
}

func normalizeReasoningEffort(value string) string {
	switch effort := strings.ToLower(strings.TrimSpace(value)); effort {
	case "none", "low", "medium", "high", "xhigh", "max":
		return effort
	case "minimal":
		return "low"
	default:
		return ""
	}
}

// applyToolConfig applies tool configuration from the client request to the body struct.
func applyToolConfig(body *ChatRequestBody, reqBody map[string]interface{}) bool {
	toolsEnabled := false
	if tools, ok := reqBody["tools"].([]interface{}); ok && len(tools) > 0 {
		toolsEnabled = true
		b, _ := marshalNoEscape(tools)
		body.Tools = b
	}
	if tc, ok := reqBody["tool_choice"]; ok {
		b, _ := marshalNoEscape(tc)
		body.Parameters.ToolChoice = b
	}
	if ptc, ok := reqBody["parallel_tool_calls"]; ok {
		b, _ := marshalNoEscape(ptc)
		body.ParallelToolCalls = b
	}
	return toolsEnabled
}
