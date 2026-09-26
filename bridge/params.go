// Parameter resolution: how fields of an OpenAI chat completion request map
// onto the gateway request body. This file is the single place where client
// parameters are interpreted.
//
// Policy (verbatim): a client-provided parameter value is forwarded
// unchanged — no clamping, no catalog validation, no tier normalization, no
// silent fallback substitution. Values the gateway rejects come back to the
// client as upstream errors (see handler.go's error mapping). The only
// synthesized values are (a) the catalog default for max_tokens when the
// client sent none, mirroring the official client's own fallback, and
// (b) the "none" encoding — the gateway protocol's way of switching thinking
// off entirely. Local 400s are reserved for values that cannot be encoded at
// all (wrong JSON type) or that this backend structurally cannot honor
// (n != 1).
package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// RequestParamError marks a client request field that cannot be encoded for
// the gateway (wrong JSON type) or that this backend structurally cannot
// honor (n != 1). The chat handler maps it to HTTP 400 invalid_request_error.
type RequestParamError struct {
	msg string
}

func (e *RequestParamError) Error() string { return e.msg }

// passthroughParams are request fields forwarded verbatim into the gateway
// "parameters" object. The gateway contract only defines max_tokens,
// max_thinking_tokens, reasoning_effort and tool_choice; every other key is
// the upstream's to accept or reject — we neither validate nor drop values.
// Kept sorted so the serialized parameter order is deterministic.
var passthroughParams = []string{
	"frequency_penalty", "logit_bias", "n", "presence_penalty",
	"response_format", "seed", "stop", "temperature", "top_p",
}

// resolveReasoningEffort returns the client-requested thinking tier,
// verbatim — no vocabulary check, no case folding, no alias mapping
// ("minimal" stays "minimal"; the gateway is the judge). Precedence: the
// top-level reasoning_effort string, then the Responses-style
// reasoning.effort object. An empty result means the client sent nothing.
func resolveReasoningEffort(reqBody map[string]interface{}) (string, error) {
	if raw, present := reqBody["reasoning_effort"]; present {
		s, ok := raw.(string)
		if !ok {
			return "", &RequestParamError{fmt.Sprintf("reasoning_effort must be a string, got %s", jsonTypeName(raw))}
		}
		return s, nil
	}
	if reasoning, ok := reqBody["reasoning"].(map[string]interface{}); ok {
		if raw, present := reasoning["effort"]; present {
			s, ok := raw.(string)
			if !ok {
				return "", &RequestParamError{fmt.Sprintf("reasoning.effort must be a string, got %s", jsonTypeName(raw))}
			}
			return s, nil
		}
	}
	return "", nil
}

// validateSingleChoice rejects n != 1. The response channel structurally
// produces exactly one choice; silently returning one when the client asked
// for three is exactly the quiet substitution this bridge refuses to make.
// n = 1 (or absent) is fine and forwarded like any passthrough field.
func validateSingleChoice(reqBody map[string]interface{}) error {
	raw, present := reqBody["n"]
	if !present {
		return nil
	}
	num, ok := numberJSON(raw)
	if !ok {
		return &RequestParamError{fmt.Sprintf("n must be a number, got %s", jsonTypeName(raw))}
	}
	f, err := strconv.ParseFloat(string(num), 64)
	if err != nil || f != 1 {
		return &RequestParamError{fmt.Sprintf("n=%s is not supported: this backend always returns a single choice", num)}
	}
	return nil
}

// buildParameters assembles the gateway "parameters" object from the client
// request. Key order: the four gateway-defined fields first (max_tokens,
// max_thinking_tokens, reasoning_effort, tool_choice — the order the
// official client emits), then passthrough keys alphabetically.
func buildParameters(reqBody map[string]interface{}, effort string, catalogDefaultMaxTokens int) (json.RawMessage, error) {
	if err := validateSingleChoice(reqBody); err != nil {
		return nil, err
	}
	obj := newJSONObject()

	// max_tokens: any JSON number is forwarded verbatim — 0, negatives,
	// fractions, huge magnitudes. The gateway enforces its own range and its
	// rejection comes back to the client. max_completion_tokens is the newer
	// alias; max_tokens wins when both are present. Only an absent pair falls
	// back to the catalog default.
	maxRaw, err := maxTokensField(reqBody)
	if err != nil {
		return nil, err
	}
	if maxRaw == nil {
		maxRaw = json.RawMessage(strconv.Itoa(catalogDefaultMaxTokens))
	}
	obj.set("max_tokens", maxRaw)

	// max_thinking_tokens: a client-supplied value is a gateway-defined
	// parameter like any other and is forwarded verbatim (it even overrides
	// the synthesized off-switch below — the gateway judges the combination,
	// we do not). Only effort=="none" WITHOUT a client value synthesizes the
	// explicit 0 the official client sends to switch thinking off entirely.
	mttRaw, mttPresent, err := maxThinkingTokensField(reqBody)
	if err != nil {
		return nil, err
	}
	if mttPresent {
		obj.set("max_thinking_tokens", mttRaw)
	} else if effort == "none" {
		// The gateway protocol's thinking off-switch: the official client
		// sends max_thinking_tokens=0 alongside reasoning_effort=none. The
		// explicit zero must serialize, not be omitted.
		obj.set("max_thinking_tokens", json.RawMessage("0"))
	}
	if effort != "" {
		obj.set("reasoning_effort", effort)
	}

	if tc, present := reqBody["tool_choice"]; present {
		b, err := marshalNoEscape(tc)
		if err != nil {
			return nil, err
		}
		obj.set("tool_choice", json.RawMessage(b))
	}

	for _, key := range passthroughParams {
		if raw, present := reqBody[key]; present {
			b, err := marshalNoEscape(raw)
			if err != nil {
				return nil, err
			}
			obj.set(key, json.RawMessage(b))
		}
	}

	return obj.marshal()
}

// maxThinkingTokensField extracts a client-supplied thinking budget, verbatim
// (any JSON number — the gateway validates the range). The boolean reports
// presence; a wrong JSON type is a RequestParamError so it is not silently
// swallowed by the none-synthesis fallback.
func maxThinkingTokensField(reqBody map[string]interface{}) (json.RawMessage, bool, error) {
	raw, present := reqBody["max_thinking_tokens"]
	if !present {
		return nil, false, nil
	}
	num, ok := numberJSON(raw)
	if !ok {
		return nil, false, &RequestParamError{fmt.Sprintf("max_thinking_tokens must be a number, got %s", jsonTypeName(raw))}
	}
	return num, true, nil
}

// maxTokensField extracts the client completion cap. max_tokens wins over
// max_completion_tokens; both are type-checked when present so a malformed
// value is reported instead of being silently swallowed by the other
// field's presence. Nil means the client sent neither field.
func maxTokensField(reqBody map[string]interface{}) (json.RawMessage, error) {
	var chosen json.RawMessage
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		raw, present := reqBody[key]
		if !present {
			continue
		}
		num, ok := numberJSON(raw)
		if !ok {
			return nil, &RequestParamError{fmt.Sprintf("%s must be a number, got %s", key, jsonTypeName(raw))}
		}
		if chosen == nil {
			chosen = num
		}
	}
	return chosen, nil
}

// streamIncludeUsage reads stream_options.include_usage (OpenAI semantics:
// the terminal usage frame is only emitted for streaming responses when the
// client opted in). Non-streaming responses always carry usage.
func streamIncludeUsage(reqBody map[string]interface{}) bool {
	so, ok := reqBody["stream_options"].(map[string]interface{})
	if !ok {
		return false
	}
	v, _ := so["include_usage"].(bool)
	return v
}

// numberJSON converts a decoded JSON number into its raw JSON bytes.
// json.Number (produced by UseNumber decoding) preserves the client's exact
// token — including magnitudes like 1e18 and precision beyond float64;
// float64 (maps built directly in tests) is re-marshaled.
func numberJSON(v interface{}) (json.RawMessage, bool) {
	switch n := v.(type) {
	case json.Number:
		return json.RawMessage(n), true
	case float64:
		b, err := json.Marshal(n)
		if err != nil {
			return nil, false
		}
		return b, true
	default:
		return nil, false
	}
}

// jsonTypeName names the JSON type of a decoded value for error messages.
func jsonTypeName(v interface{}) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64, json.Number:
		return "number"
	case string:
		return "string"
	case []interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// jsonObject assembles a JSON object with insertion-ordered keys: Go maps
// marshal alphabetically, which would scramble the parameter order the
// official client emits.
type jsonObject struct {
	keys []string
	vals map[string]interface{}
}

func newJSONObject() *jsonObject {
	return &jsonObject{vals: make(map[string]interface{})}
}

func (o *jsonObject) set(key string, val interface{}) {
	if _, exists := o.vals[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = val
}

func (o *jsonObject) marshal() (json.RawMessage, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := marshalNoEscape(o.vals[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// applyToolConfig applies tool configuration from the client request to the
// body struct. tool_choice rides inside "parameters" and is assembled by
// buildParameters above, not here.
func applyToolConfig(body *ChatRequestBody, reqBody map[string]interface{}) bool {
	toolsEnabled := false
	if tools, ok := reqBody["tools"].([]interface{}); ok && len(tools) > 0 {
		toolsEnabled = true
		b, _ := marshalNoEscape(tools)
		body.Tools = b
	}
	if ptc, ok := reqBody["parallel_tool_calls"]; ok {
		b, _ := marshalNoEscape(ptc)
		body.ParallelToolCalls = b
	}
	return toolsEnabled
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
