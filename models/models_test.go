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
