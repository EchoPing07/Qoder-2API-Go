// Package bridge implements the OpenAI-compatible API bridge with session
// management and streaming/sync forwarding for a single Qoder PAT.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"qoder2api/auth"
	"qoder2api/models"
	"qoder2api/transform"

	"github.com/google/uuid"
)

var (
	refreshMarginMs = int64(2 * 3600 * 1000) // 2 hours
	catalogTTL      = float64(600)           // 10 minutes
)

// --- Chat request body types ---

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

type business struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	BeginAt interface{} `json:"begin_at"`
}

// ChatRequestBody is the Qoder chat request body sent to the gateway.
// Field order matches baseprompt.json for consistent JSON serialization.
type ChatRequestBody struct {
	RequestID         string                   `json:"request_id"`
	RequestSetID      string                   `json:"request_set_id"`
	ChatRecordID      string                   `json:"chat_record_id"`
	Stream            bool                     `json:"stream"`
	ChatTask          string                   `json:"chat_task"`
	ChatContext       chatContext              `json:"chat_context"`
	SessionID         string                   `json:"session_id"`
	Source            int                      `json:"source"`
	Version           string                   `json:"version"`
	AliyunUserType    string                   `json:"aliyun_user_type"`
	SessionType       string                   `json:"session_type"`
	AgentID           string                   `json:"agent_id"`
	TaskID            string                   `json:"task_id"`
	ModelConfig       modelConfig              `json:"model_config"`
	Messages          []transform.QoderMessage `json:"messages"`
	Business          business                 `json:"business"`
	Tools             json.RawMessage          `json:"tools,omitempty"`
	ToolChoice        json.RawMessage          `json:"tool_choice,omitempty"`
	ParallelToolCalls json.RawMessage          `json:"parallel_tool_calls,omitempty"`
}

// newRequestBody creates a ChatRequestBody with defaults from baseprompt.json.
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
	}
}

// marshalNoEscape serializes v to compact JSON without HTML escaping.
func marshalNoEscape(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// --- OpenAiBridge ---

// OpenAiBridge manages a single PAT's session and forwards chat requests.
type OpenAiBridge struct {
	Region *auth.RegionConfig
	pat    string

	machineID    string
	machineToken string
	machineType  string

	mu            sync.Mutex // guards sess/identity/tokens/expiry/template
	refreshMu     sync.Mutex // serializes renewal network calls
	sess          *auth.SessionContext
	identity      *auth.AuthIdentity
	refreshToken  string
	securityOauth string
	expireTimeMs  int64
	bootstrapped  bool

	catalogMu sync.Mutex
	catalog   *models.ModelCatalog
	catalogTs float64

	templateLoaded bool
}

// NewOpenAiBridge creates a new bridge for the given PAT.
func NewOpenAiBridge(pat string, region *auth.RegionConfig) *OpenAiBridge {
	if region == nil {
		region = auth.CN
	}
	machineID := uuid.New().String()
	rawToken := (uuid.New().String() + uuid.New().String())
	if len(rawToken) > 50 {
		rawToken = rawToken[:50]
	}
	machineToken := rawToken
	machineType := uuid.New().String()
	if len(machineType) > 18 {
		machineType = machineType[:18]
	}
	return &OpenAiBridge{
		Region:       region,
		pat:          pat,
		machineID:    machineID,
		machineToken: machineToken,
		machineType:  machineType,
	}
}

// bootstrapSession performs a cold PAT → jobToken exchange.
func (b *OpenAiBridge) bootstrapSession(ctx context.Context) error {
	jt, err := auth.ExchangeJobToken(ctx, b.pat, b.machineID, b.machineToken, b.machineType, b.Region)
	if err != nil {
		return err
	}
	name, _ := jt["name"].(string)
	id, _ := jt["id"].(string)
	exp, _ := jt["expireTime"]
	log.Printf("[bridge] session for %s (%s) [%s] exp=%v", name, id, b.Region.Name, exp)
	b.applyJobToken(jt)
	b.templateLoaded = true
	b.bootstrapped = true
	return nil
}

func (b *OpenAiBridge) applyJobToken(jt map[string]interface{}) {
	name, _ := jt["name"].(string)
	id, _ := jt["id"].(string)
	userType, _ := jt["userType"].(string)
	if userType == "" {
		userType = "personal_standard"
	}
	securityOauth, _ := jt["securityOauthToken"].(string)
	refreshToken, _ := jt["refreshToken"].(string)

	identity := auth.AuthIdentity{
		Name:               name,
		Aid:                id,
		UID:                id,
		UserType:           userType,
		SecurityOauthToken: securityOauth,
		RefreshToken:       refreshToken,
	}
	sess := auth.NewSession(identity, b.machineID, b.machineToken, b.machineType)

	b.mu.Lock()
	b.sess = sess
	b.identity = &identity
	b.refreshToken = refreshToken
	b.securityOauth = securityOauth
	b.expireTimeMs = toInt64(jt["expireTime"])
	b.mu.Unlock()
}

func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		val, _ := strconv.ParseInt(n, 10, 64)
		return val
	case json.Number:
		val, _ := n.Int64()
		return val
	default:
		return 0
	}
}

func (b *OpenAiBridge) needsRefresh() bool {
	nowMs := time.Now().UnixMilli()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sess == nil || b.expireTimeMs == 0 || nowMs > b.expireTimeMs-refreshMarginMs
}

func (b *OpenAiBridge) currentSess() *auth.SessionContext {
	b.mu.Lock()
	sess := b.sess
	b.mu.Unlock()
	if sess == nil {
		panic("session not bootstrapped")
	}
	return sess
}

// doRenew renews the session token via refreshToken.
func (b *OpenAiBridge) doRenew(ctx context.Context, force bool) error {
	b.mu.Lock()
	rt := b.refreshToken
	sot := b.securityOauth
	b.mu.Unlock()

	jt, err := auth.RefreshJobToken(ctx, b.pat, rt, sot, b.machineID, b.machineToken, b.machineType, b.Region)
	if err != nil {
		if !force {
			return err
		}
		log.Printf("[bridge] refresh rejected (%v); falling back to PAT exchange", err)
		jt, err = auth.ExchangeJobToken(ctx, b.pat, b.machineID, b.machineToken, b.machineType, b.Region)
		if err != nil {
			return err
		}
		log.Printf("[bridge] PAT re-exchange ok (exp=%v)", jt["expireTime"])
	} else {
		label := "refreshed"
		if force {
			label = "force-refreshed"
		}
		log.Printf("[bridge] session %s (exp=%v)", label, jt["expireTime"])
	}
	b.applyJobToken(jt)
	return nil
}

// EnsureFreshSession proactively rotates the session token before expiry.
func (b *OpenAiBridge) EnsureFreshSession(ctx context.Context) error {
	if b.bootstrapped && !b.needsRefresh() {
		return nil
	}
	b.refreshMu.Lock()
	defer b.refreshMu.Unlock()
	if !b.bootstrapped {
		return b.bootstrapSession(ctx)
	}
	if !b.needsRefresh() {
		return nil
	}
	return b.doRenew(ctx, false)
}

func (b *OpenAiBridge) forceRefresh(ctx context.Context) error {
	b.refreshMu.Lock()
	defer b.refreshMu.Unlock()
	return b.doRenew(ctx, true)
}

// GetCatalog returns the dynamic model catalog (TTL cached).
func (b *OpenAiBridge) GetCatalog(ctx context.Context) *models.ModelCatalog {
	now := float64(time.Now().Unix())
	b.catalogMu.Lock()
	cached := b.catalog
	if cached != nil && now-b.catalogTs < catalogTTL {
		b.catalogMu.Unlock()
		return cached
	}
	b.catalogMu.Unlock()

	var fetched *models.ModelCatalog
	err := b.EnsureFreshSession(ctx)
	if err == nil {
		raw, err := auth.FetchModelCatalog(ctx, b.currentSess(), b.Region)
		if err == nil {
			fetched = models.ExtractCatalog(raw)
			if fetched != nil {
				log.Printf("[models] dynamic catalog loaded: %d models [%s]", len(fetched.Keys()), strings.Join(fetched.Keys(), ", "))
			} else {
				log.Printf("[models] WARN model/list response shape unexpected; using fallback")
			}
		} else {
			log.Printf("[models] WARN dynamic fetch failed (%v); using fallback", err)
		}
	} else {
		log.Printf("[models] WARN ensure_fresh_session failed (%v); using fallback", err)
	}

	cat := fetched
	if cat == nil {
		cat = models.DefaultCatalog()
	}
	b.catalogMu.Lock()
	b.catalog = cat
	b.catalogTs = float64(now)
	b.catalogMu.Unlock()
	return cat
}

// openStreamAsync opens the SSE stream with auth retry (refresh once on 401
// before any content is produced).
func (b *OpenAiBridge) openStreamAsync(ctx context.Context, url string, jsonBody []byte, extraHeaders map[string]string, callback func(line string) error) error {
	for attempt := 1; attempt <= 2; attempt++ {
		sess := b.currentSess()
		produced := false
		err := auth.OpenStreamLines(ctx, sess, url, jsonBody, extraHeaders, func(line string) error {
			produced = true
			return callback(line)
		})
		if err == nil {
			return nil
		}
		var authErr *auth.AuthError
		if errors.As(err, &authErr) {
			if produced || attempt >= 2 {
				return err
			}
			log.Printf("[bridge] auth error before any content (%v); refreshing and retrying once", err)
			if refreshErr := b.forceRefresh(ctx); refreshErr != nil {
				log.Printf("[bridge] reactive refresh failed: %v", refreshErr)
				return err
			}
			continue
		}
		return err
	}
	return nil
}

// HandleChat processes a chat completion request.
// For streaming, it writes SSE chunks to w and returns nil.
// For non-streaming, it returns the response as a map.
func (b *OpenAiBridge) HandleChat(ctx context.Context, w http.ResponseWriter, reqBody map[string]interface{}) error {
	if err := b.EnsureFreshSession(ctx); err != nil {
		return err
	}
	if b.identity == nil {
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
	body.AliyunUserType = b.identity.UserType
	body.ModelConfig.Key = qoderModel
	body.ModelConfig.IsReasoning = true
	body.ChatContext.Extra.ModelConfig.Key = qoderModel
	body.ChatContext.Extra.ModelConfig.IsReasoning = true
	body.Business.ID = uuid.New().String()
	body.Business.BeginAt = time.Now().UnixMilli()

	prompt := transform.ExtractLatestUserPrompt(messages)
	body.ChatContext.Text.Text = prompt
	body.ChatContext.Extra.OriginalContent.Text = prompt
	if len(prompt) > 30 {
		body.Business.Name = prompt[:30]
	} else {
		body.Business.Name = prompt
	}

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

	log.Printf("[bridge] chat req: prompt_len=%d model=%s", len(prompt), openaiModel)

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

	jsonBody, err := marshalNoEscape(body)
	if err != nil {
		return err
	}

	if stream {
		return b.handleStream(ctx, w, jsonBody, url, extraHeaders, reqID, created, openaiModel, toolsEnabled)
	}
	return b.handleSync(ctx, w, jsonBody, url, extraHeaders, reqID, created, openaiModel, toolsEnabled)
}

func (b *OpenAiBridge) handleStream(ctx context.Context, w http.ResponseWriter, jsonBody []byte, url string, extraHeaders map[string]string, reqID string, created int64, model string, toolsEnabled bool) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

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
	} else {
		doneChunk := transform.MakeChunk(reqID, created, model)
		writeFinishReason(doneChunk, acc.FinishReason())
		clearDelta(doneChunk)
		doneBytes, _ := marshalNoEscape(doneChunk)
		w.Write([]byte("data: " + string(doneBytes) + "\n\n"))
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

func (b *OpenAiBridge) handleSync(ctx context.Context, w http.ResponseWriter, jsonBody []byte, url string, extraHeaders map[string]string, reqID string, created int64, model string, toolsEnabled bool) error {
	var fullContent []string
	var fullReasoning []string
	toolCalls := transform.NewToolCallAccumulator()

	err := b.openStreamAsync(ctx, url, jsonBody, extraHeaders, func(line string) error {
		if !strings.HasPrefix(line, "data:") {
			return nil
		}
		delta := transform.ExtractDelta(strings.TrimSpace(line[5:]))
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
			fallbackToolCalls = toolsCallsToMaps(parsed)
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

	w.Header().Set("Content-Type", "application/json")
	respBytes, _ := marshalNoEscape(resp)
	w.Write(respBytes)
	return nil
}

// --- BridgeResolver ---

// BridgeResolver resolves an API key to the current single OpenAiBridge.
// Returns nil if the key is invalid or no PAT is configured.
// The concrete implementation is provided by the caller (main package).
type BridgeResolver func(apiKey string) *OpenAiBridge

// --- HTTP Handlers ---

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		token := strings.TrimSpace(auth[7:])
		if token != "" {
			return token
		}
	}
	return ""
}

func writeError(w http.ResponseWriter, statusCode int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	resp := map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    errType,
		},
	}
	b, _ := marshalNoEscape(resp)
	w.Write(b)
}

// MakeChatHandler creates the /v1/chat/completions handler.
// The resolver validates the API key and returns the single bridge instance.
func MakeChatHandler(resolver BridgeResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" {
			writeError(w, 401, "invalid_request_error", "Missing Authorization: Bearer <key>")
			return
		}
		var b *OpenAiBridge
		if resolver != nil {
			b = resolver(token)
		}
		if b == nil {
			writeError(w, 401, "invalid_request_error", "Invalid API key or no PAT configured")
			return
		}

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			writeError(w, 400, "invalid_request_error", "Invalid JSON body: "+err.Error())
			return
		}

		err := b.HandleChat(r.Context(), w, reqBody)
		if err != nil {
			var valErr *models.UnsupportedModelError
			if errors.As(err, &valErr) {
				writeError(w, 400, "invalid_request_error", valErr.Error())
				return
			}
			errMsg := err.Error()
			if strings.Contains(errMsg, "Unsupported model") || strings.Contains(errMsg, "not supported") {
				writeError(w, 400, "invalid_request_error", errMsg)
				return
			}
			writeError(w, 500, "qoder_error", errMsg)
		}
	}
}

// MakeModelsHandler creates the /v1/models handler.
// The resolver validates the API key and returns the single bridge instance.
// If the key is invalid or no PAT is configured, the built-in default catalog
// is returned as a fallback.
func MakeModelsHandler(resolver BridgeResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		var b *OpenAiBridge
		if resolver != nil && token != "" {
			b = resolver(token)
		}
		if b != nil {
			catalog := b.GetCatalog(r.Context())
			payload := models.ModelsPayload(catalog)
			w.Header().Set("Content-Type", "application/json")
			bs, _ := marshalNoEscape(payload)
			w.Write(bs)
			return
		}
		payload := models.ModelsPayload(nil)
		w.Header().Set("Content-Type", "application/json")
		bs, _ := marshalNoEscape(payload)
		w.Write(bs)
	}
}

// --- Helpers ---

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
		body.ToolChoice = b
	}
	if ptc, ok := reqBody["parallel_tool_calls"]; ok {
		b, _ := marshalNoEscape(ptc)
		body.ParallelToolCalls = b
	}
	return toolsEnabled
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

func toolsCallsToMaps(calls []transform.NormalizedToolCall) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(calls))
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

func sortStrings(s []string) {
	sort.Strings(s)
}
