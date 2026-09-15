// Package models provides model catalog resolution with dynamic loading and fallback.
package models

import (
	"sort"
	"strconv"
	"strings"
)

// enableFlag coerces a model entry's "enable" field to a bool. Missing or
// unrecognised values are treated as enabled, matching the prior default.
func enableFlag(v interface{}) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return !(x == "false" || x == "0" || x == "False" || x == "FALSE")
	case float64:
		return x != 0
	case nil:
		return true
	default:
		return true
	}
}

// DefaultModelMap is the built-in fallback map (display_name -> qoder internal key).
// Matches catalog-v6 (2026-08-15) chat scene. Only used when dynamic fetch fails.
// Notable vs v5: Qwen3.8-Max graduated from preview (qmodel_preview ->
// qmodel_38max, renamed without "-Preview"); GLM-5.3 (gmodel) added.
var DefaultModelMap = map[string]string{
	"Qwen3.8-Max":       "qmodel_38max",
	"Qwen3.7-Max":       "qmodel_latest",
	"Qwen3.7-Plus":      "qmodel",
	"Qwen3.6-Flash":     "q36fmodel",
	"DeepSeek-V4-Pro":   "dmodel",
	"DeepSeek-V4-Flash": "dfmodel",
	"GLM-5.3":           "gmodel",
	"GLM-5.2":           "gm51model",
	"Kimi-K2.7-Code":    "kmodel",
	"MiniMax-M2.7":      "mmodel",
}

// DefaultVisionModels mirrors the gateway's is_vl metadata for the fallback
// catalog (display_name). NOTE: the gateway's is_vl flags have proven
// unreliable — do not treat this as an authoritative capability matrix.
var DefaultVisionModels = map[string]bool{
	"Qwen3.8-Max":       true,
	"Qwen3.7-Max":       true,
	"Qwen3.7-Plus":      true,
	"Qwen3.6-Flash":     true,
	"DeepSeek-V4-Pro":   true,
	"DeepSeek-V4-Flash": true,
	"GLM-5.3":           true,
	"GLM-5.2":           true,
	"Kimi-K2.7-Code":    true,
}

// PreferredDefaultKey is the default model key when model param is None/empty.
const PreferredDefaultKey = "qmodel_latest"

// DefaultScene is the catalog scene to extract.
const DefaultScene = "chat"

// DefaultMaxOutputTokens is the completion-token cap the official client falls
// back to whenever a catalog entry carries no usable max_output_tokens. It
// mirrors the client's LS() coercion, which returns 32000 for any value that is
// not a positive safe integer.
const DefaultMaxOutputTokens = 32000

// ModelCatalog holds display_name → qoder key mapping and capability metadata.
type ModelCatalog struct {
	ModelMap     map[string]string // display_name -> key
	VisionModels map[string]bool   // display_name set
	DefaultName  string            // used when model param is empty

	// Reasoning carries the gateway's per-model thinking-effort metadata,
	// keyed by the qoder internal key. Entries exist only for models whose
	// catalog record carried effort metadata; a missing entry means "unknown",
	// and callers fall back to the global effort vocabulary.
	Reasoning map[string]*ModelReasoning

	// Caps carries the per-model limits the official client reads from the
	// catalog before building a gateway request, keyed by the qoder internal
	// key. A missing entry means the catalog was unavailable, and callers fall
	// back to their own defaults.
	Caps map[string]*ModelCaps
}

// ModelCaps mirrors the limits of a model/list entry that the official client
// resolves in oJI before assembling the request body.
type ModelCaps struct {
	// IsReasoning reports whether the gateway says this model can think. The
	// official client defaults a missing field to false (T?.is_reasoning ?? !1).
	IsReasoning bool
	// MaxOutputTokens is the default completion cap, already normalized to
	// DefaultMaxOutputTokens when the catalog value was absent or unusable.
	MaxOutputTokens int
}

// flagValue coerces an optional boolean catalog field. Unlike enableFlag it
// treats a missing value as false, matching the official client's defaults for
// capability flags such as is_reasoning.
func flagValue(v interface{}) bool {
	if v == nil {
		return false
	}
	return enableFlag(v)
}

// positiveInt parses a catalog numeric field that may arrive as a JSON number
// or a numeric string, mirroring the official client's LS() coercion.
func positiveInt(v interface{}) (int, bool) {
	switch x := v.(type) {
	case float64:
		if n := int(x); float64(n) == x && n > 0 {
			return n, true
		}
	case int:
		if x > 0 {
			return x, true
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil && n > 0 {
			return n, true
		}
	}
	return 0, false
}

// ModelReasoning mirrors the reasoning-related fields of a model/list entry.
// The official client derives the exact same information before deciding
// whether a requested effort may be forwarded to the gateway.
type ModelReasoning struct {
	Efforts          []string // canonical efforts the model accepts (lowercased)
	SupportsDisabled bool     // gateway allows switching thinking off entirely
	Known            bool     // true when any effort metadata was present
}

// Keys returns sorted display names for deterministic output.
func (c *ModelCatalog) Keys() []string {
	keys := make([]string, 0, len(c.ModelMap))
	for k := range c.ModelMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// GetKey returns the qoder key for a display_name, or "" if not found.
func (c *ModelCatalog) GetKey(displayName string) string {
	return c.ModelMap[displayName]
}

// MaxOutputTokens returns the gateway's default completion cap for a qoder key,
// falling back to DefaultMaxOutputTokens when the catalog carries no entry.
func (c *ModelCatalog) MaxOutputTokens(qoderKey string) int {
	if caps, ok := c.Caps[qoderKey]; ok && caps.MaxOutputTokens > 0 {
		return caps.MaxOutputTokens
	}
	return DefaultMaxOutputTokens
}

// ReasoningDefault reports whether the gateway says a model can think. A catalog
// with no caps entry for the key reports true, preserving the bridge's
// long-standing always-on behaviour when the dynamic catalog is unavailable and
// the gateway's own is_reasoning flag cannot be consulted.
func (c *ModelCatalog) ReasoningDefault(qoderKey string) bool {
	if caps, ok := c.Caps[qoderKey]; ok {
		return caps.IsReasoning
	}
	return true
}

// DefaultCatalog returns the built-in fallback catalog.
func DefaultCatalog() *ModelCatalog {
	modelMap := make(map[string]string, len(DefaultModelMap))
	for k, v := range DefaultModelMap {
		modelMap[k] = v
	}
	vision := make(map[string]bool, len(DefaultVisionModels))
	for k, v := range DefaultVisionModels {
		vision[k] = v
	}
	return &ModelCatalog{
		ModelMap:     modelMap,
		VisionModels: vision,
		DefaultName:  nameForKey(modelMap, PreferredDefaultKey),
	}
}

// rawModelEntry / rawCatalogResponse were removed: catalog parsing walks
// the dynamic map form directly (ExtractCatalog), so the typed structs were
// dead code.

// ExtractCatalog parses a model/list response into a ModelCatalog.
// Returns nil if the format doesn't match (caller should fall back).
func ExtractCatalog(raw map[string]interface{}) *ModelCatalog {
	if raw == nil {
		return nil
	}
	sceneRaw, ok := raw[DefaultScene]
	if !ok {
		return nil
	}
	sceneList, ok := sceneRaw.([]interface{})
	if !ok {
		return nil
	}
	modelMap := map[string]string{}
	vision := map[string]bool{}
	reasoning := map[string]*ModelReasoning{}
	caps := map[string]*ModelCaps{}
	for _, item := range sceneList {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		// enable defaults to true if missing; coerce common non-bool shapes.
		enableVal, hasEnable := m["enable"]
		if hasEnable && !enableFlag(enableVal) {
			continue
		}
		key, _ := m["key"].(string)
		name, _ := m["display_name"].(string)
		if key == "" || name == "" || key == "auto" {
			continue
		}
		modelMap[name] = key
		if isVL, ok := m["is_vl"].(bool); ok && isVL {
			vision[name] = true
		}
		if ri := parseReasoningMeta(m); ri != nil {
			reasoning[key] = ri
		}
		maxOut, ok := positiveInt(m["max_output_tokens"])
		if !ok {
			maxOut = DefaultMaxOutputTokens
		}
		caps[key] = &ModelCaps{
			IsReasoning:     flagValue(m["is_reasoning"]),
			MaxOutputTokens: maxOut,
		}
	}
	if len(modelMap) == 0 {
		return nil
	}
	return &ModelCatalog{
		ModelMap:     modelMap,
		VisionModels: vision,
		Reasoning:    reasoning,
		Caps:         caps,
		DefaultName:  nameForKey(modelMap, PreferredDefaultKey),
	}
}

// parseReasoningMeta extracts the per-model thinking-effort metadata from a
// catalog entry. It returns nil when the entry carries no usable effort
// information at all, letting callers fall back to the global vocabulary.
//
// The gateway serializes "efforts" in several shapes (array of strings,
// comma/space separated string, or an object map keyed by effort); all are
// normalized here the same way the official client normalizes them.
func parseReasoningMeta(m map[string]interface{}) *ModelReasoning {
	effortsRaw, hasEfforts := m["efforts"]
	efforts := normalizeEfforts(effortsRaw)
	supportsDisabled := false
	if v, ok := m["supports_disabled"]; ok && enableFlag(v) {
		supportsDisabled = true
	}
	if !hasEfforts && !supportsDisabled {
		return nil
	}
	return &ModelReasoning{
		Efforts:          efforts,
		SupportsDisabled: supportsDisabled,
		Known:            true,
	}
}

// normalizeEfforts coerces any supported "efforts" shape into the canonical
// lowercase vocabulary (none/low/medium/high/xhigh/max), preserving order and
// dropping duplicates or unrecognized values. "off"/"disabled" are folded
// into "none", matching the official client's normalization.
func normalizeEfforts(v interface{}) []string {
	var raw []string
	switch x := v.(type) {
	case []interface{}:
		for _, item := range x {
			if s, ok := item.(string); ok {
				raw = append(raw, s)
			}
		}
	case string:
		for _, field := range strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
			raw = append(raw, field)
		}
	case map[string]interface{}:
		for k := range x {
			raw = append(raw, k)
		}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		e := canonicalEffort(s)
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

// canonicalEffort validates a single effort token against the gateway's
// vocabulary, folding the client-side aliases "off"/"disabled" into "none".
func canonicalEffort(s string) string {
	e := strings.ToLower(strings.TrimSpace(s))
	switch e {
	case "off", "disabled":
		return "none"
	case "none", "low", "medium", "high", "xhigh", "max":
		return e
	default:
		return ""
	}
}

// ResolveModel resolves a model name to (display_name, qoder_key).
// If model is empty, uses the catalog default. Returns error if not found.
func ResolveModel(model string, catalog *ModelCatalog) (string, string, error) {
	if catalog == nil {
		catalog = DefaultCatalog()
	}
	if model != "" {
		key := catalog.GetKey(model)
		if key == "" {
			supported := ""
			for i, k := range catalog.Keys() {
				if i > 0 {
					supported += ", "
				}
				supported += k
			}
			return "", "", &UnsupportedModelError{Model: model, Supported: supported}
		}
		return model, key, nil
	}
	name := catalog.DefaultName
	if name == "" {
		for k := range catalog.ModelMap {
			name = k
			break
		}
	}
	return name, catalog.ModelMap[name], nil
}

// ModelsPayload constructs the OpenAI /v1/models response.
func ModelsPayload(catalog *ModelCatalog) map[string]interface{} {
	if catalog == nil {
		catalog = DefaultCatalog()
	}
	data := make([]map[string]interface{}, 0, len(catalog.ModelMap))
	for _, name := range catalog.Keys() {
		data = append(data, map[string]interface{}{
			"id":       name,
			"object":   "model",
			"created":  0,
			"owned_by": "qoder",
		})
	}
	return map[string]interface{}{
		"object": "list",
		"data":   data,
	}
}

// UnsupportedModelError is returned when a model name is not in the catalog.
type UnsupportedModelError struct {
	Model     string
	Supported string
}

func (e *UnsupportedModelError) Error() string {
	return "Unsupported model '" + e.Model + "'. Supported: " + e.Supported
}

// nameForKey reverse-looks-up the display_name for a given qoder key,
// falling back to a cost-aware default when the key is absent.
func nameForKey(modelMap map[string]string, key string) string {
	for name, k := range modelMap {
		if k == key {
			return name
		}
	}
	return fallbackDefaultName(modelMap)
}

// fallbackDefaultName picks a default model name when the preferred key is
// absent from the dynamic catalog. It prefers cheaper tiers (flash/lite/plus
// variants) over flagship "max" models so a missing default never silently
// routes every request to the most expensive model.
func fallbackDefaultName(modelMap map[string]string) string {
	keys := make([]string, 0, len(modelMap))
	for k := range modelMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, want := range []string{"flash", "lite", "plus"} {
		for _, k := range keys {
			if strings.Contains(strings.ToLower(k), want) {
				return k
			}
		}
	}
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}
