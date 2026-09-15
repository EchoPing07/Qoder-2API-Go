package models

import "testing"

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

func TestDefaultCatalogHasNoReasoningMetadata(t *testing.T) {
	cat := DefaultCatalog()
	if cat.Reasoning != nil {
		t.Errorf("fallback catalog must not claim per-model effort knowledge, got %v", cat.Reasoning)
	}
}
