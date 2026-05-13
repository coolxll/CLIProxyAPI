package chat_completions

import (
	"encoding/json"
	"testing"

	lingmaencoding "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/lingma"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestConvertOpenAIRequestToLingmaUsesFullModelConfig(t *testing.T) {
	raw := []byte(`{"model":"dashscope_qmodel","messages":[{"role":"user","content":"Ping! Reply with Pong only."}],"stream":true}`)
	payload := decodeLingmaRequestPayload(t, ConvertOpenAIRequestToLingma("dashscope_qmodel", raw, true))

	modelConfig, ok := payload["model_config"].(map[string]any)
	if !ok {
		t.Fatalf("model_config = %T, want object", payload["model_config"])
	}
	requiredKeys := []string{
		"key",
		"display_name",
		"model",
		"format",
		"is_vl",
		"is_reasoning",
		"api_key",
		"url",
		"source",
		"max_input_tokens",
		"enable",
		"price_factor",
		"original_price_factor",
		"is_default",
		"is_new",
		"exclude_tags",
		"tags",
		"icon",
		"strategies",
	}
	for _, key := range requiredKeys {
		if _, exists := modelConfig[key]; !exists {
			t.Fatalf("model_config missing %q: %#v", key, modelConfig)
		}
	}
	if got := modelConfig["key"]; got != "dashscope_qmodel" {
		t.Fatalf("model_config.key = %v, want dashscope_qmodel", got)
	}

	business, ok := payload["business"].(map[string]any)
	if !ok {
		t.Fatalf("business = %T, want object", payload["business"])
	}
	if _, ok := business["relation"].(map[string]any); !ok {
		t.Fatalf("business.relation = %#v, want empty object", business["relation"])
	}
	if got := payload["stream"]; got != true {
		t.Fatalf("stream = %v, want true", got)
	}
}

func TestLingmaTranslatorRegisteredFromOpenAIToLingma(t *testing.T) {
	raw := []byte(`{"model":"dashscope_qmodel","messages":[{"role":"user","content":"Ping"}],"stream":true}`)
	encoded := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAI, sdktranslator.FromString("lingma"), "dashscope_qmodel", raw, true)
	if json.Valid(encoded) {
		t.Fatalf("TranslateRequest returned unencoded JSON fallback: %s", encoded)
	}
	payload := decodeLingmaRequestPayload(t, encoded)
	if got := payload["model_config"].(map[string]any)["key"]; got != "dashscope_qmodel" {
		t.Fatalf("model_config.key = %v, want dashscope_qmodel", got)
	}
}

func decodeLingmaRequestPayload(t *testing.T, encoded []byte) map[string]any {
	t.Helper()
	payloadJSON, err := lingmaencoding.Decode(string(encoded))
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return payload
}
