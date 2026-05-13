package executor

import "testing"

func TestParseLingmaModelsCategorizedResponse(t *testing.T) {
	raw := []byte(`{
		"chat": [
			{"key": "dashscope_qmodel", "display_name": "DashScope QModel"}
		],
		"developer": {
			"dev": {"modelId": "dev_model", "displayName": "Developer Model"}
		}
	}`)

	models := parseLingmaModels(raw, 123)
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].ID != "dashscope_qmodel" || models[0].DisplayName != "DashScope QModel" {
		t.Fatalf("models[0] = %#v", models[0])
	}
	if models[1].ID != "dev_model" || models[1].DisplayName != "Developer Model" {
		t.Fatalf("models[1] = %#v", models[1])
	}
}

func TestParseLingmaModelsArrayResponse(t *testing.T) {
	raw := []byte(`[
		{"modelName": "qwen-2.5-max"},
		{"id": "qwen-2.5-max"},
		{"model_id": "qwen-coder", "name": "Qwen Coder"}
	]`)

	models := parseLingmaModels(raw, 123)
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].ID != "qwen-2.5-max" {
		t.Fatalf("models[0].ID = %q, want qwen-2.5-max", models[0].ID)
	}
	if models[1].ID != "qwen-coder" || models[1].DisplayName != "Qwen Coder" {
		t.Fatalf("models[1] = %#v", models[1])
	}
}
