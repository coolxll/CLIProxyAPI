package lingma

import (
	"os"
	"testing"
)

func TestParseModelsCategorizedResponse(t *testing.T) {
	data, err := os.ReadFile("../../testdata/lingma/model_list_categorized.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	models := parseModels(data, 1234567890)

	if len(models) != 3 {
		t.Fatalf("len(models) = %d, want 3", len(models))
	}

	// Verify chat model
	if models[0].ID != "synthetic_chat_model" {
		t.Errorf("models[0].ID = %q, want synthetic_chat_model", models[0].ID)
	}
	if models[0].DisplayName != "Synthetic Chat Model" {
		t.Errorf("models[0].DisplayName = %q", models[0].DisplayName)
	}
	if models[0].Type != ProviderID {
		t.Errorf("models[0].Type = %q, want %q", models[0].Type, ProviderID)
	}
	if models[0].OwnedBy != "alibaba" {
		t.Errorf("models[0].OwnedBy = %q", models[0].OwnedBy)
	}

	// Verify developer model
	if models[1].ID != "synthetic_dev_model" {
		t.Errorf("models[1].ID = %q", models[1].ID)
	}

	// Verify assistant model
	if models[2].ID != "synthetic_assistant" {
		t.Errorf("models[2].ID = %q", models[2].ID)
	}
}

func TestParseModelsArrayResponse(t *testing.T) {
	data, err := os.ReadFile("../../testdata/lingma/model_list_array.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	models := parseModels(data, 1234567890)

	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2 (deduplication)", len(models))
	}

	// First model from modelName field
	if models[0].ID != "synthetic-model-alpha" {
		t.Errorf("models[0].ID = %q", models[0].ID)
	}

	// Second model with explicit name field
	if models[1].ID != "synthetic-model-beta" {
		t.Errorf("models[1].ID = %q", models[1].ID)
	}
	if models[1].DisplayName != "Synthetic Beta" {
		t.Errorf("models[1].DisplayName = %q", models[1].DisplayName)
	}
}

func TestParseModelsWrappedResponse(t *testing.T) {
	data, err := os.ReadFile("../../testdata/lingma/model_list_wrapped.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	models := parseModels(data, 1234567890)

	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}

	if models[0].ID != "wrapped_chat_model" {
		t.Errorf("models[0].ID = %q", models[0].ID)
	}
	if models[1].ID != "wrapped_dev_model" {
		t.Errorf("models[1].ID = %q", models[1].ID)
	}
}

func TestParseModelsEdgeCases(t *testing.T) {
	data, err := os.ReadFile("../../testdata/lingma/model_list_edge_cases.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	models := parseModels(data, 1234567890)

	if len(models) != 3 {
		t.Fatalf("len(models) = %d, want 3 (whitespace, fallback, dedup)", len(models))
	}

	// Whitespace trimming
	if models[0].ID != "whitespace-key" {
		t.Errorf("models[0].ID = %q, want trimmed", models[0].ID)
	}

	// Fallback to modelId when key is empty
	if models[1].ID != "fallback-id" {
		t.Errorf("models[1].ID = %q", models[1].ID)
	}

	// Case-insensitive deduplication
	if models[2].ID != "duplicate-model" {
		t.Errorf("models[2].ID = %q, want lowercase (first seen)", models[2].ID)
	}
}

func TestParseModelsEmptyInput(t *testing.T) {
	models := parseModels([]byte(`{}`), 123)
	if len(models) != 0 {
		t.Errorf("len(models) = %d, want 0", len(models))
	}

	models = parseModels([]byte(`[]`), 123)
	if len(models) != 0 {
		t.Errorf("len(models) = %d, want 0", len(models))
	}
}

func TestParseModelsMalformedJSON(t *testing.T) {
	models := parseModels([]byte(`not json`), 123)
	if len(models) != 0 {
		t.Errorf("len(models) = %d, want 0 for malformed input", len(models))
	}
}
