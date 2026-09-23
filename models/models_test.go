package models

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveModelByName(t *testing.T) {
	name, key, err := ResolveModel("Qwen3.7-Max", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "Qwen3.7-Max" || key != "qmodel_latest" {
		t.Errorf("got (%s, %s), want (Qwen3.7-Max, qmodel_latest)", name, key)
	}
}

func TestResolveModelByDefault(t *testing.T) {
	name, key, err := ResolveModel("", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "Qwen3.7-Max" || key != "qmodel_latest" {
		t.Errorf("got (%s, %s), want (Qwen3.7-Max, qmodel_latest)", name, key)
	}
}

func TestResolveModelRejectsUnknown(t *testing.T) {
	_, _, err := ResolveModel("unknown-model", nil)
	if err == nil {
		t.Fatal("expected error for unknown model")
	}
}

func TestModelsPayloadExposesAllModels(t *testing.T) {
	payload := ModelsPayload(nil)
	data, _ := payload["data"].([]map[string]interface{})
	if len(data) == 0 {
		t.Fatal("expected non-empty model list")
	}
	ids := map[string]bool{}
	for _, m := range data {
		id, _ := m["id"].(string)
		ids[id] = true
		if m["owned_by"] != "qoder" {
			t.Errorf("model %s has wrong owned_by: %v", id, m["owned_by"])
		}
	}
	if !ids["Qwen3.7-Max"] {
		t.Error("Qwen3.7-Max not in payload")
	}
	if !ids["Qwen3.7-Plus"] {
		t.Error("Qwen3.7-Plus not in payload")
	}
}

func TestExtractCatalog(t *testing.T) {
	raw := map[string]interface{}{
		"chat": []interface{}{
			map[string]interface{}{"key": "a", "display_name": "ModelA", "enable": true, "is_vl": true},
			map[string]interface{}{"key": "b", "display_name": "ModelB", "enable": true, "is_vl": false},
			map[string]interface{}{"key": "auto", "display_name": "Auto", "enable": true, "is_vl": true},
		},
	}
	cat := ExtractCatalog(raw)
	if cat == nil {
		t.Fatal("expected non-nil catalog")
	}
	if !cat.VisionModels["ModelA"] {
		t.Error("ModelA should support vision")
	}
	if cat.VisionModels["ModelB"] {
		t.Error("ModelB should not support vision")
	}
	if _, ok := cat.ModelMap["Auto"]; ok {
		t.Error("auto should be skipped")
	}
}

func TestDefaultCatalogVisionModels(t *testing.T) {
	cat := DefaultCatalog()
	if cat.VisionModels["MiniMax-M2.7"] {
		t.Error("MiniMax-M2.7 should not support vision")
	}
	if !cat.VisionModels["Qwen3.7-Plus"] {
		t.Error("Qwen3.7-Plus should support vision")
	}
}

// --- Regression tests for review fixes ---

// When the preferred default key is absent from a dynamic catalog, the
// fallback must prefer cheap tiers over flagship models (dictionary order
// used to pick whatever came first, potentially the most expensive model).
func TestFallbackDefaultPrefersCheapTier(t *testing.T) {
	raw := map[string]interface{}{
		"chat": []interface{}{
			map[string]interface{}{"key": "k1", "display_name": "Zeta-Max-Ultra", "enable": true},
			map[string]interface{}{"key": "k2", "display_name": "Alpha-Flash", "enable": true},
		},
	}
	cat := ExtractCatalog(raw)
	if cat.DefaultName != "Alpha-Flash" {
		t.Errorf("expected cheap-tier Alpha-Flash as default, got %q", cat.DefaultName)
	}
	// Cost preference is tier first, then name for determinism.
	raw2 := map[string]interface{}{
		"chat": []interface{}{
			map[string]interface{}{"key": "k1", "display_name": "B-Lite"},
			map[string]interface{}{"key": "k2", "display_name": "A-Lite"},
		},
	}
	if got := ExtractCatalog(raw2).DefaultName; got != "A-Lite" {
		t.Errorf("expected deterministic A-Lite, got %q", got)
	}
}

// --- Per-model reasoning metadata parsing (efforts / supports_disabled) ---

func TestExtractCatalogParsesReasoningMetadata(t *testing.T) {
	raw := map[string]interface{}{
		"chat": []interface{}{
			// Array shape with aliases and junk entries.
			map[string]interface{}{"key": "a", "display_name": "ModelA", "enable": true,
				"efforts":           []interface{}{"low", " Medium ", "xhigh", "off", "turbo", 42},
				"supports_disabled": true},
			// Comma/space separated string shape.
			map[string]interface{}{"key": "b", "display_name": "ModelB", "enable": true,
				"efforts": "high, max"},
			// Object-map shape (keys are the efforts, values are descriptors).
			map[string]interface{}{"key": "c", "display_name": "ModelC", "enable": true,
				"efforts": map[string]interface{}{"low": map[string]interface{}{}, "xhigh": map[string]interface{}{"is_default": true}}},
			// No reasoning metadata at all: must be absent from the map.
			map[string]interface{}{"key": "d", "display_name": "ModelD", "enable": true},
			// Only supports_disabled: on/off switch without effort tiers.
			map[string]interface{}{"key": "e", "display_name": "ModelE", "enable": true,
				"supports_disabled": "true"},
		},
	}
	cat := ExtractCatalog(raw)
	if cat == nil {
		t.Fatal("expected non-nil catalog")
	}

	a := cat.Reasoning["a"]
	if a == nil {
		t.Fatal("ModelA reasoning metadata missing")
	}
	wantA := []string{"low", "medium", "xhigh", "none"}
	if len(a.Efforts) != len(wantA) {
		t.Fatalf("ModelA efforts = %v, want %v", a.Efforts, wantA)
	}
	for i, e := range wantA {
		if a.Efforts[i] != e {
			t.Errorf("ModelA efforts[%d] = %q, want %q", i, a.Efforts[i], e)
		}
	}
	if !a.SupportsDisabled {
		t.Error("ModelA supports_disabled should be true")
	}
	if !a.Known {
		t.Error("ModelA should be Known")
	}

	b := cat.Reasoning["b"]
	if b == nil {
		t.Fatal("ModelB reasoning metadata missing")
	}
	if len(b.Efforts) != 2 || b.Efforts[0] != "high" || b.Efforts[1] != "max" {
		t.Errorf("ModelB efforts = %v, want [high max]", b.Efforts)
	}
	if b.SupportsDisabled {
		t.Error("ModelB supports_disabled should default to false")
	}

	c := cat.Reasoning["c"]
	if c == nil {
		t.Fatal("ModelC reasoning metadata missing")
	}
	if len(c.Efforts) != 2 || c.Efforts[0] != "low" || c.Efforts[1] != "xhigh" {
		t.Errorf("ModelC efforts = %v, want [low xhigh]", c.Efforts)
	}

	if _, ok := cat.Reasoning["d"]; ok {
		t.Error("ModelD without metadata should not appear in Reasoning")
	}

	e := cat.Reasoning["e"]
	if e == nil {
		t.Fatal("ModelE reasoning metadata missing")
	}
	if len(e.Efforts) != 0 {
		t.Errorf("ModelE efforts = %v, want empty", e.Efforts)
	}
	if !e.SupportsDisabled {
		t.Error("ModelE supports_disabled should be coerced from \"true\"")
	}
}

// The live gateway nests reasoning metadata under thinking_config. Parsing only
// the flat form made every model look like it advertised nothing, which in turn
// let unsupported tiers (notably "none") reach the upstream and fail with 400.
func TestExtractCatalogParsesNestedThinkingConfig(t *testing.T) {
	raw := map[string]interface{}{
		"chat": []interface{}{
			// Tiered model that may also switch thinking off.
			map[string]interface{}{"key": "tiered", "display_name": "Tiered", "enable": true,
				"is_reasoning": true,
				"thinking_config": map[string]interface{}{
					"disabled": map[string]interface{}{},
					"enabled": map[string]interface{}{
						"efforts": map[string]interface{}{
							"low":     map[string]interface{}{},
							"medium":  map[string]interface{}{"is_default": true},
							"xhigh":   map[string]interface{}{},
							"quantum": map[string]interface{}{},
						},
						"is_default": true,
					},
				}},
			// On/off only: enabled has no efforts block and disabled is absent, so
			// "none" must NOT be forwarded to this model.
			map[string]interface{}{"key": "toggle", "display_name": "Toggle", "enable": true,
				"is_reasoning": true,
				"thinking_config": map[string]interface{}{
					"enabled": map[string]interface{}{"description": "Enable thinking", "is_default": true},
				}},
			// thinking_config explicitly null (as q37fmodel/mmodel ship it).
			map[string]interface{}{"key": "nullcfg", "display_name": "NullCfg", "enable": true,
				"is_reasoning": true, "thinking_config": nil},
		},
	}
	cat := ExtractCatalog(raw)
	if cat == nil {
		t.Fatal("expected non-nil catalog")
	}

	tiered := cat.Reasoning["tiered"]
	if tiered == nil {
		t.Fatal("nested thinking_config was not parsed")
	}
	if !tiered.Known {
		t.Error("tiered model should be Known")
	}
	if !tiered.SupportsDisabled {
		t.Error("tiered model declares thinking_config.disabled and should support none")
	}
	for _, want := range []string{"low", "medium", "xhigh"} {
		found := false
		for _, got := range tiered.Efforts {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("tiered efforts = %v, missing %q", tiered.Efforts, want)
		}
	}
	for _, got := range tiered.Efforts {
		if got == "quantum" {
			t.Errorf("tiered efforts = %v, unknown tier must be dropped", tiered.Efforts)
		}
	}

	toggle := cat.Reasoning["toggle"]
	if toggle == nil {
		t.Fatal("thinking_config without disabled must still produce metadata")
	}
	if toggle.SupportsDisabled {
		t.Error("toggle model has no thinking_config.disabled; none must not be forwarded")
	}
	if len(toggle.Efforts) != 0 {
		t.Errorf("toggle efforts = %v, want empty", toggle.Efforts)
	}

	// thinking_config explicitly null (as q37fmodel/mmodel ship it): no
	// metadata at all, which is what makes the bridge omit an unverified tier.
	nullcfg := cat.Reasoning["nullcfg"]
	if nullcfg != nil {
		t.Errorf("null thinking_config should advertise nothing, got %+v", nullcfg)
	}
}

// The flat legacy shape must keep working alongside the nested one.
func TestExtractCatalogStillAcceptsFlatReasoningMetadata(t *testing.T) {
	raw := map[string]interface{}{
		"chat": []interface{}{
			map[string]interface{}{"key": "flat", "display_name": "Flat", "enable": true,
				"efforts": []interface{}{"low", "xhigh"}, "supports_disabled": true},
		},
	}
	cat := ExtractCatalog(raw)
	if cat == nil {
		t.Fatal("expected non-nil catalog")
	}
	flat := cat.Reasoning["flat"]
	if flat == nil || !flat.SupportsDisabled {
		t.Fatalf("flat metadata regressed: %+v", flat)
	}
	if len(flat.Efforts) != 2 || flat.Efforts[0] != "low" || flat.Efforts[1] != "xhigh" {
		t.Errorf("flat efforts = %v, want [low xhigh]", flat.Efforts)
	}
}

func TestDefaultCatalogHasNoReasoningMetadata(t *testing.T) {
	cat := DefaultCatalog()
	if cat.Reasoning != nil {
		t.Errorf("fallback catalog must not claim per-model effort knowledge, got %v", cat.Reasoning)
	}
}

// The bridge reads is_reasoning and max_output_tokens out of the catalog the
// same way the official client does, so both must survive parsing, including
// their fallbacks for missing or malformed values.
func TestExtractCatalogParsesModelCaps(t *testing.T) {
	raw := map[string]interface{}{
		"chat": []interface{}{
			// Reasoning model with an explicit cap.
			map[string]interface{}{"key": "a", "display_name": "ModelA", "enable": true,
				"is_reasoning": true, "max_output_tokens": 8000},
			// Non-reasoning model; cap arrives as a numeric string.
			map[string]interface{}{"key": "b", "display_name": "ModelB", "enable": true,
				"is_reasoning": false, "max_output_tokens": "6000"},
			// No flags at all: is_reasoning defaults to true (thinking stays on
			// whenever the gateway has not demonstrably disabled it, matching the
			// pre-catalog behaviour), cap to the fallback.
			map[string]interface{}{"key": "c", "display_name": "ModelC", "enable": true},
			// Unusable cap must fall back, not leak a zero that would truncate output.
			map[string]interface{}{"key": "d", "display_name": "ModelD", "enable": true,
				"is_reasoning": true, "max_output_tokens": 0},
		},
	}
	cat := ExtractCatalog(raw)
	if cat == nil {
		t.Fatal("expected non-nil catalog")
	}

	a := cat.Caps["a"]
	if a == nil {
		t.Fatal("ModelA caps missing")
	}
	if !a.IsReasoning {
		t.Error("ModelA is_reasoning should be true")
	}
	if a.MaxOutputTokens != 8000 {
		t.Errorf("ModelA max_output_tokens = %d, want 8000", a.MaxOutputTokens)
	}

	b := cat.Caps["b"]
	if b == nil {
		t.Fatal("ModelB caps missing")
	}
	if b.IsReasoning {
		t.Error("ModelB is_reasoning should be false")
	}
	if b.MaxOutputTokens != 6000 {
		t.Errorf("ModelB max_output_tokens = %d, want 6000 (parsed from string)", b.MaxOutputTokens)
	}

	c := cat.Caps["c"]
	if c == nil {
		t.Fatal("ModelC caps missing")
	}
	if !c.IsReasoning {
		t.Error("ModelC is_reasoning should default to true for a flagless entry (preserve always-on behaviour)")
	}
	if c.MaxOutputTokens != DefaultMaxOutputTokens {
		t.Errorf("ModelC max_output_tokens = %d, want %d", c.MaxOutputTokens, DefaultMaxOutputTokens)
	}

	if got := cat.MaxOutputTokens("d"); got != DefaultMaxOutputTokens {
		t.Errorf("MaxOutputTokens(d) = %d, want %d for an unusable cap", got, DefaultMaxOutputTokens)
	}
	if got := cat.MaxOutputTokens("missing"); got != DefaultMaxOutputTokens {
		t.Errorf("MaxOutputTokens(missing) = %d, want %d", got, DefaultMaxOutputTokens)
	}
	if got := cat.ReasoningDefault("a"); !got {
		t.Error("ReasoningDefault(a) = false, want true")
	}
	if got := cat.ReasoningDefault("b"); got {
		t.Error("ReasoningDefault(b) = true, want false (explicit is_reasoning=false)")
	}
	if got := cat.ReasoningDefault("c"); !got {
		t.Error("ReasoningDefault(c) = false, want true (flagless entry keeps thinking on)")
	}
	// A model absent from caps must not silently lose thinking either; the
	// bridge only reaches this branch when the dynamic catalog is unavailable.
	if got := cat.ReasoningDefault("missing"); !got {
		t.Error("ReasoningDefault(missing) = false, want true (preserve always-on fallback)")
	}
}

func TestDefaultCatalogCapsFallBack(t *testing.T) {
	cat := DefaultCatalog()
	if cat.Caps != nil {
		t.Errorf("fallback catalog must not claim per-model caps, got %v", cat.Caps)
	}
	if got := cat.MaxOutputTokens(PreferredDefaultKey); got != DefaultMaxOutputTokens {
		t.Errorf("MaxOutputTokens = %d, want %d", got, DefaultMaxOutputTokens)
	}
	if got := cat.ReasoningDefault(PreferredDefaultKey); !got {
		t.Error("ReasoningDefault = false, want true when the catalog carries no caps")
	}
}

// The thinking capability must be resolved the way the official client does:
// a present thinking_config.enabled block wins over the top-level flag, then
// is_reasoning, then true. The live catalog contradicts the top-level flag
// for dfmodel / kmodel_latest (is_reasoning=false next to a fully populated
// enabled block) and reading the flag first silently switched their thinking
// off on every request. Entries below mirror the real model/list shapes.
func TestExtractCatalogReasoningCapabilityPrecedence(t *testing.T) {
	efforts := func() map[string]interface{} {
		return map[string]interface{}{"low": map[string]interface{}{}, "high": map[string]interface{}{}}
	}

	cases := []struct {
		name  string
		entry map[string]interface{}
		want  bool
	}{
		{"dfmodel: enabled+disabled nested block beats is_reasoning=false (the §16 regression)",
			map[string]interface{}{"is_reasoning": false,
				"thinking_config": map[string]interface{}{"enabled": map[string]interface{}{"efforts": efforts()}, "disabled": map[string]interface{}{}}},
			true},
		{"kmodel_latest: enabled-only nested block beats is_reasoning=false",
			map[string]interface{}{"is_reasoning": false,
				"thinking_config": map[string]interface{}{"enabled": map[string]interface{}{"efforts": efforts()}}},
			true},
		{"gmodel/gfmodel shape: enabled only, flag true",
			map[string]interface{}{"is_reasoning": true,
				"thinking_config": map[string]interface{}{"enabled": map[string]interface{}{"efforts": efforts()}}},
			true},
		{"qmodel_38max shape: enabled+disabled, flag true",
			map[string]interface{}{"is_reasoning": true,
				"thinking_config": map[string]interface{}{"enabled": map[string]interface{}{"efforts": efforts()}, "disabled": map[string]interface{}{}}},
			true},
		{"kmodel shape: enabled only, flag true",
			map[string]interface{}{"is_reasoning": true,
				"thinking_config": map[string]interface{}{"enabled": map[string]interface{}{"efforts": efforts()}}},
			true},
		{"enabled block present but explicitly null cannot think",
			map[string]interface{}{"is_reasoning": true,
				"thinking_config": map[string]interface{}{"enabled": nil}},
			false},
		{"thinking_config explicitly null falls through to the flag (true)",
			map[string]interface{}{"is_reasoning": true, "thinking_config": nil},
			true},
		{"thinking_config null falls through to the flag (false)",
			map[string]interface{}{"is_reasoning": false, "thinking_config": nil},
			false},
		{"mmodel shape: no thinking_config, flag false",
			map[string]interface{}{"is_reasoning": false},
			false},
		{"q37fmodel shape: no thinking_config, flag true",
			map[string]interface{}{"is_reasoning": true},
			true},
		{"is_reasoning as the string \"false\" is coerced",
			map[string]interface{}{"is_reasoning": "false"},
			false},
		// A malformed (non-object, non-null) thinking_config has an unknown
		// shape: trusting the contradictory top-level flag could silently
		// disable thinking for an entry that advertises it.
		{"thinking_config as a string keeps thinking on despite is_reasoning=false",
			map[string]interface{}{"is_reasoning": false, "thinking_config": "yes"},
			true},
		{"thinking_config as an array keeps thinking on despite is_reasoning=false",
			map[string]interface{}{"is_reasoning": false, "thinking_config": []interface{}{}},
			true},
		{"thinking_config as a number keeps thinking on despite is_reasoning=false",
			map[string]interface{}{"is_reasoning": false, "thinking_config": float64(1)},
			true},
		{"no flags at all keeps thinking on",
			map[string]interface{}{},
			true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := map[string]interface{}{"key": "k", "display_name": "M", "enable": true}
			for k, v := range tc.entry {
				entry[k] = v
			}
			cat := ExtractCatalog(map[string]interface{}{"chat": []interface{}{entry}})
			if cat == nil {
				t.Fatal("expected non-nil catalog")
			}
			caps := cat.Caps["k"]
			if caps == nil {
				t.Fatal("caps entry missing")
			}
			if caps.IsReasoning != tc.want {
				t.Errorf("Caps.IsReasoning = %t, want %t", caps.IsReasoning, tc.want)
			}
			if got := cat.ReasoningDefault("k"); got != tc.want {
				t.Errorf("ReasoningDefault = %t, want %t", got, tc.want)
			}
		})
	}
}

// The hand-written fixtures above cover the structural branches, but a parser
// that only passes them can still be wrong about the live payload (that is
// exactly what made the original flat-field parser pass while production
// rejected every tier). When the captured real dump is present locally it is
// replayed here, so a schema drift shows up as a test failure rather than a
// live 400. probe/ is gitignored, so the test skips when it is absent — CI must
// not depend on it.
func TestExtractCatalogAgainstCapturedRealDump(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "probe", "catalog_local.json"))
	if err != nil {
		t.Skipf("captured catalog dump unavailable: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("dump is not valid JSON: %v", err)
	}
	cat := ExtractCatalog(doc)
	if cat == nil {
		t.Fatal("the captured dump produced no catalog")
	}

	// Every entry must resolve a reasoning decision; none may be silently absent
	// from both tables (that absence is what left the tier check inert).
	if len(cat.Caps) == 0 {
		t.Fatal("no capability entries were parsed from the real dump")
	}
	// ModelMap is display_name -> key, so the keys are its values.
	for display, key := range cat.ModelMap {
		if _, ok := cat.Caps[key]; !ok {
			t.Errorf("model %q (%s) has no capability entry", display, key)
		}
	}

	// The specific models the review found regressed. dfmodel / kmodel_latest
	// ship is_reasoning=false alongside a populated thinking_config.enabled, and
	// the nested block is what actually lets the model think.
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"dfmodel", true},
		{"kmodel_latest", true},
		{"mmodel", false}, // no enabled block and is_reasoning=false
	} {
		if got := cat.ReasoningDefault(tc.key); got != tc.want {
			t.Errorf("ReasoningDefault(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}

	// "none" may only be forwarded when the entry declares thinking_config.disabled.
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"gmodel", false},
		{"gfmodel", false},
		{"kmodel", false},
		{"kmodel_latest", false},
		{"qmodel_38max", true},
		{"dfmodel", true},
	} {
		ri := cat.Reasoning[tc.key]
		got := ri != nil && ri.SupportsDisabled
		if got != tc.want {
			t.Errorf("SupportsDisabled(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}

	// The declared tiers must be parsed from the nested efforts object, not left
	// empty: dfmodel advertises high/low/max.
	if ri := cat.Reasoning["dfmodel"]; ri == nil {
		t.Error("dfmodel has no reasoning metadata")
	} else if len(ri.Efforts) == 0 {
		t.Error("dfmodel declared no efforts; the nested path was not read")
	} else {
		for _, want := range []string{"high", "low", "max"} {
			found := false
			for _, got := range ri.Efforts {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("dfmodel efforts %v missing %q", ri.Efforts, want)
			}
		}
	}
}
