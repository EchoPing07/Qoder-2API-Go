// Chat request construction: the gateway (ChatRequestBody) protocol types and
// HandleChat, which maps an OpenAI chat completion request onto one gateway
// request. Extracted from bridge.go; behaviour is unchanged.
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"qoder2api/auth"
	"qoder2api/models"
	"qoder2api/transform"

	"github.com/google/uuid"
)

type chatContextText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type extraModelConfig struct {
	IsReasoning bool   `json:"is_reasoning"`
	Key         string `json:"key"`
	IsVL        bool   `json:"is_vl,omitempty"`
}

type originalContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type chatContextExtra struct {
	ModelConfig     extraModelConfig `json:"modelConfig"`
	OriginalContent originalContent  `json:"originalContent"`
}

type chatContext struct {
	Extra chatContextExtra `json:"extra"`
	Text  chatContextText  `json:"text"`
}

type modelConfig struct {
	Key         string `json:"key"`
	IsVL        bool   `json:"is_vl"`
	IsReasoning bool   `json:"is_reasoning"`
	Source      string `json:"source"`
}

// chatParameters mirrors the gateway's "parameters" object. The official client
// builds this map first and always attaches it to the request body; the gateway
// reads completion caps, thinking control and tool selection from here and never
// from the top level of the body.
type chatParameters struct {
	// MaxTokens caps completion tokens. Always sent, matching the official
	// client's unconditional max_tokens entry.
	MaxTokens int `json:"max_tokens"`
	// MaxThinkingTokens caps thinking tokens. A pointer so that an explicit 0
	// survives serialization: 0 is how the official client disables thinking.
	MaxThinkingTokens *int `json:"max_thinking_tokens,omitempty"`
	// ReasoningEffort is the canonical thinking tier (none/low/medium/high/
	// xhigh/max) validated against the model's catalog metadata.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// ToolChoice carries the OpenAI tool_choice value verbatim.
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
}

type business struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	BeginAt interface{} `json:"begin_at"`
}

// ChatRequestBody is the Qoder chat request body sent to the gateway.
// Field order matches baseprompt.json for consistent JSON serialization.
type ChatRequestBody struct {
	RequestID      string                   `json:"request_id"`
	RequestSetID   string                   `json:"request_set_id"`
	ChatRecordID   string                   `json:"chat_record_id"`
	Stream         bool                     `json:"stream"`
	ChatTask       string                   `json:"chat_task"`
	ChatContext    chatContext              `json:"chat_context"`
	SessionID      string                   `json:"session_id"`
	Source         int                      `json:"source"`
	Version        string                   `json:"version"`
	AliyunUserType string                   `json:"aliyun_user_type"`
	SessionType    string                   `json:"session_type"`
	AgentID        string                   `json:"agent_id"`
	TaskID         string                   `json:"task_id"`
	ModelConfig    modelConfig              `json:"model_config"`
	Messages       []transform.QoderMessage `json:"messages"`
	Business       business                 `json:"business"`
	Parameters     chatParameters           `json:"parameters"`
	Tools          json.RawMessage          `json:"tools,omitempty"`
	// ParallelToolCalls is forwarded at the top level exactly as it was before
	// the parameters object existed: no parameters equivalent is known, and
	// whether the gateway reads it from either position has not been verified,
	// so moving or deleting it would be an unverified behaviour change.
	ParallelToolCalls json.RawMessage `json:"parallel_tool_calls,omitempty"`
}

// newRequestBody creates a ChatRequestBody with hardcoded defaults that
// mirror the reference Qoder client's request template (the former
// baseprompt.json, now inlined and always in sync with the struct).
func newRequestBody() *ChatRequestBody {
	return &ChatRequestBody{
		Stream:   true,
		ChatTask: "FREE_INPUT",
		ChatContext: chatContext{
			Extra: chatContextExtra{
				ModelConfig: extraModelConfig{
					IsReasoning: false,
					Key:         "lite",
				},
				OriginalContent: originalContent{
					Type: "text",
					Text: "",
				},
			},
			Text: chatContextText{
				Type: "text",
				Text: "",
			},
		},
		Source:         1,
		Version:        "3",
		AliyunUserType: "personal_standard",
		SessionType:    "qodercli",
		AgentID:        "agent_common",
		TaskID:         "common",
		ModelConfig: modelConfig{
			Key:         "lite",
			IsVL:        false,
			IsReasoning: false,
			Source:      "system",
		},
		Messages: []transform.QoderMessage{},
		Business: business{
			Name: "hi",
		},
		Parameters: chatParameters{
			MaxTokens: models.DefaultMaxOutputTokens,
		},
	}
}

// UsageSink receives the gateway usage frame of a completed request (may be
// nil if the stream ended without one). Used for statistics aggregation.
type UsageSink func(u *transform.Usage)

// HandleChat processes a chat completion request.
// For streaming, it writes SSE chunks to w and returns nil.
// For non-streaming, it returns the response as a map.
// usageSink (may be nil) receives upstream token accounting when available.
func (b *OpenAiBridge) HandleChat(ctx context.Context, w http.ResponseWriter, reqBody map[string]interface{}, usageSink UsageSink) error {
	if err := b.EnsureFreshSession(ctx); err != nil {
		return err
	}
	identity := b.currentIdentity()
	if identity == nil {
		return fmt.Errorf("session not bootstrapped")
	}

	stream, _ := reqBody["stream"].(bool)
	modelParam, _ := reqBody["model"].(string)
	catalog := b.GetCatalog(ctx)
	openaiModel, qoderModel, err := models.ResolveModel(modelParam, catalog)
	if err != nil {
		return err
	}

	messages := extractMessages(reqBody["messages"])
	body := newRequestBody()
	nid := uuid.New().String()
	body.RequestID = nid
	body.ChatRecordID = nid
	body.RequestSetID = uuid.New().String()
	body.SessionID = uuid.New().String()
	body.Stream = true
	body.AliyunUserType = identity.UserType
	body.ModelConfig.Key = qoderModel
	body.ChatContext.Extra.ModelConfig.Key = qoderModel

	// Thinking capability comes from the gateway catalog the way the official
	// client resolves it (is_reasoning ?? false), instead of being forced on.
	// A resolved "none" tier overrides both copies further below.
	reasoningOn := catalog.ReasoningDefault(qoderModel)
	body.ModelConfig.IsReasoning = reasoningOn
	body.ChatContext.Extra.ModelConfig.IsReasoning = reasoningOn
	// Completion cap follows the official precedence: a caller-supplied value
	// wins, the catalog default is only the fallback. Clamping to the catalog
	// would silently truncate models whose advertised cap is small.
	body.Parameters.MaxTokens = resolveMaxTokens(reqBody, catalog.MaxOutputTokens(qoderModel))
	body.Business.ID = uuid.New().String()
	body.Business.BeginAt = time.Now().UnixMilli()

	prompt := transform.ExtractLatestUserPrompt(messages)
	body.ChatContext.Text.Text = prompt
	body.ChatContext.Extra.OriginalContent.Text = prompt
	body.Business.Name = truncateRunes(prompt, 30)

	toolsEnabled := applyToolConfig(body, reqBody)
	body.Messages = transform.BuildQoderMessages(messages, prompt, toolsEnabled)

	// Multimodal check
	hasImages := false
	for _, m := range messages {
		if len(transform.ExtractMessageImages(m)) > 0 {
			hasImages = true
			break
		}
	}
	if hasImages {
		if !catalog.VisionModels[openaiModel] {
			supported := ""
			visionList := make([]string, 0)
			for k := range catalog.VisionModels {
				visionList = append(visionList, k)
			}
			sortStrings(visionList)
			supported = strings.Join(visionList, ", ")
			if supported == "" {
				supported = "(none)"
			}
			return fmt.Errorf("Image input is not supported by model '%s'. Use one of: %s.", openaiModel, supported)
		}
		body.ModelConfig.IsVL = true
		body.ChatContext.Extra.ModelConfig.IsVL = true
		imgCount := 0
		for _, m := range messages {
			imgCount += len(transform.ExtractMessageImages(m))
		}
		log.Printf("[bridge] multimodal: %d image(s) attached [%s]", imgCount, openaiModel)
	}

	if effort := resolveReasoningEffort(reqBody, catalog.Reasoning[qoderModel]); effort != "" {
		// The gateway reads the tier from "parameters", never from the top level
		// of the body. The official client assembles it the same way.
		body.Parameters.ReasoningEffort = effort
		if effort == "none" {
			// Disable thinking explicitly: the client sets is_reasoning=false and
			// max_thinking_tokens=0 together. The pointer field is what lets an
			// explicit 0 survive serialization instead of being omitted.
			zero := 0
			body.Parameters.MaxThinkingTokens = &zero
			body.ModelConfig.IsReasoning = false
			body.ChatContext.Extra.ModelConfig.IsReasoning = false
		}
		log.Printf("[bridge] chat req: prompt_len=%d model=%s reasoning_effort=%s is_reasoning=%t max_tokens=%d",
			len(prompt), openaiModel, effort, body.ModelConfig.IsReasoning, body.Parameters.MaxTokens)
	} else {
		log.Printf("[bridge] chat req: prompt_len=%d model=%s reasoning=default(on) is_reasoning=%t max_tokens=%d",
			len(prompt), openaiModel, body.ModelConfig.IsReasoning, body.Parameters.MaxTokens)
	}

	url := auth.ChatURL(b.Region)
	extraHeaders := map[string]string{
		"x-model-key":    qoderModel,
		"x-model-source": body.ModelConfig.Source,
	}
	if body.ModelConfig.Source == "" {
		extraHeaders["x-model-source"] = "system"
	}

	reqID := "chatcmpl-" + uuid.New().String()[:24]
	created := time.Now().Unix()

	// Admission gate: hold one slot for the whole upstream exchange so
	// parallel client requests queue here instead of being rejected by the
	// gateway's concurrency limiter (code 10605).
	select {
	case b.chatSlots <- struct{}{}:
	default:
		log.Printf("[bridge] all %d upstream slot(s) busy; queuing request", cap(b.chatSlots))
		select {
		case b.chatSlots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() { <-b.chatSlots }()

	jsonBody, err := marshalNoEscape(body)
	if err != nil {
		return err
	}

	if stream {
		return b.handleStream(ctx, w, jsonBody, url, extraHeaders, reqID, created, openaiModel, toolsEnabled, usageSink)
	}
	return b.handleSync(ctx, w, jsonBody, url, extraHeaders, reqID, created, openaiModel, toolsEnabled, usageSink)
}
