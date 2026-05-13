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

func TestConvertOpenAIRequestToLingmaSetsIsReasoning(t *testing.T) {
	tests := []struct {
		name            string
		reasoningEffort string
		wantReasoning   bool
	}{
		{"no reasoning_effort", "", false},
		{"reasoning_effort none", "none", false},
		{"reasoning_effort low", "low", true},
		{"reasoning_effort medium", "medium", true},
		{"reasoning_effort high", "high", true},
		{"reasoning_effort xhigh", "xhigh", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"stream":true}`)
			if tt.reasoningEffort != "" {
				raw = []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"stream":true,"reasoning_effort":"` + tt.reasoningEffort + `"}`)
			}
			payload := decodeLingmaRequestPayload(t, ConvertOpenAIRequestToLingma("test", raw, true))
			modelConfig, ok := payload["model_config"].(map[string]any)
			if !ok {
				t.Fatalf("model_config = %T, want object", payload["model_config"])
			}
			got, ok := modelConfig["is_reasoning"].(bool)
			if !ok {
				t.Fatalf("is_reasoning = %T (%v), want bool", modelConfig["is_reasoning"], modelConfig["is_reasoning"])
			}
			if got != tt.wantReasoning {
				t.Fatalf("is_reasoning = %v, want %v (reasoning_effort=%q)", got, tt.wantReasoning, tt.reasoningEffort)
			}
		})
	}
}

func TestConvertOpenAIRequestToLingmaPassesToolsThrough(t *testing.T) {
	raw := []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"stream":true,"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"location":{"type":"string"}}}}}]}`)
	payload := decodeLingmaRequestPayload(t, ConvertOpenAIRequestToLingma("test", raw, true))
	tools, ok := payload["tools"].([]any)
	if !ok {
		t.Fatalf("tools = %T, want array", payload["tools"])
	}
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool = %T, want object", tools[0])
	}
	if tool["type"] != "function" {
		t.Fatalf("tool.type = %v, want function", tool["type"])
	}
	fn, ok := tool["function"].(map[string]any)
	if !ok {
		t.Fatalf("tool.function = %T, want object", tool["function"])
	}
	if fn["name"] != "get_weather" {
		t.Fatalf("tool.function.name = %v, want get_weather", fn["name"])
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
