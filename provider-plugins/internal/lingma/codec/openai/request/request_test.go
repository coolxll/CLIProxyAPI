package chat_completions

import (
	"encoding/json"
	"reflect"
	"testing"
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
	if got := payload["agent_id"]; got != "agent_chat" {
		t.Fatalf("agent_id = %v, want agent_chat", got)
	}
	if got := modelConfig["source"]; got != "system" {
		t.Fatalf("model_config.source = %v, want system", got)
	}
}

func TestConvertOpenAIRequestToLingmaProducesExpectedPayload(t *testing.T) {
	raw := []byte(`{"model":"dashscope_qmodel","messages":[{"role":"user","content":"Ping"}],"stream":true}`)
	body := ConvertOpenAIRequestToLingma("dashscope_qmodel", raw, true)
	if !json.Valid(body) {
		t.Fatalf("ConvertOpenAIRequestToLingma returned invalid JSON: %s", body)
	}
	payload := decodeLingmaRequestPayload(t, body)
	modelConfig, ok := payload["model_config"].(map[string]any)
	if !ok {
		t.Fatalf("model_config = %T, want object", payload["model_config"])
	}
	if got := modelConfig["key"]; got != "dashscope_qmodel" {
		t.Fatalf("model_config.key = %v, want dashscope_qmodel", got)
	}
}

func TestConvertOpenAIRequestToLingmaSetsIsReasoning(t *testing.T) {
	tests := []struct {
		name            string
		reasoningEffort string
		wantReasoning   bool
	}{
		{"no reasoning_effort", "", true},
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

func TestConvertOpenAIRequestToLingmaClampsMaxTokens(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want float64
	}{
		{
			name: "preserves value below hard limit",
			raw:  []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":4096}`),
			want: 4096,
		},
		{
			name: "clamps value above hard limit",
			raw:  []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":32000}`),
			want: lingmaMaxTokensHardLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := decodeLingmaRequestPayload(t, ConvertOpenAIRequestToLingma("test", tt.raw, true))
			parameters, ok := payload["parameters"].(map[string]any)
			if !ok {
				t.Fatalf("parameters = %T, want object", payload["parameters"])
			}
			if got := parameters["max_tokens"]; got != tt.want {
				t.Fatalf("max_tokens = %v, want %v", got, tt.want)
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

func TestConvertOpenAIRequestToLingmaAgentChatModel(t *testing.T) {
	raw := []byte(`{"model":"dashscope_qmodel","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	payload := decodeLingmaRequestPayload(t, ConvertOpenAIRequestToLingma("dashscope_qmodel", raw, true))

	if got := payload["agent_id"]; got != "agent_chat" {
		t.Fatalf("agent_id = %v, want agent_chat", got)
	}
	modelConfig, ok := payload["model_config"].(map[string]any)
	if !ok {
		t.Fatalf("model_config = %T, want object", payload["model_config"])
	}
	if got := modelConfig["source"]; got != "system" {
		t.Fatalf("model_config.source = %v, want system", got)
	}
	if got := modelConfig["is_reasoning"]; got != true {
		t.Fatalf("is_reasoning = %v, want true", got)
	}
}

func TestConvertOpenAIRequestToLingmaAgentChatWithReasoningNone(t *testing.T) {
	raw := []byte(`{"model":"gm51model","messages":[{"role":"user","content":"hi"}],"stream":true,"reasoning_effort":"none"}`)
	payload := decodeLingmaRequestPayload(t, ConvertOpenAIRequestToLingma("gm51model", raw, true))

	if got := payload["agent_id"]; got != "agent_common" {
		t.Fatalf("agent_id = %v, want agent_common", got)
	}
	modelConfig, ok := payload["model_config"].(map[string]any)
	if !ok {
		t.Fatalf("model_config = %T, want object", payload["model_config"])
	}
	if got := modelConfig["source"]; got != "" {
		t.Fatalf("model_config.source = %v, want empty string", got)
	}
	if got := modelConfig["is_reasoning"]; got != false {
		t.Fatalf("is_reasoning = %v, want false", got)
	}
}

func TestConvertOpenAIRequestToLingmaAgentCommonModel(t *testing.T) {
	raw := []byte(`{"model":"kmodel","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	payload := decodeLingmaRequestPayload(t, ConvertOpenAIRequestToLingma("kmodel", raw, true))

	if got := payload["agent_id"]; got != "agent_common" {
		t.Fatalf("agent_id = %v, want agent_common", got)
	}
	modelConfig, ok := payload["model_config"].(map[string]any)
	if !ok {
		t.Fatalf("model_config = %T, want object", payload["model_config"])
	}
	if got := modelConfig["source"]; got != "" {
		t.Fatalf("model_config.source = %v, want empty string", got)
	}
	if got := modelConfig["is_reasoning"]; got != false {
		t.Fatalf("is_reasoning = %v, want false", got)
	}
}

func decodeLingmaRequestPayload(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return payload
}

func TestNormalizeLingmaToolCallContent(t *testing.T) {
	toolCall := map[string]any{
		"id":   "call_1",
		"type": "function",
		"function": map[string]any{
			"name":      "bash",
			"arguments": `{"command":"pwd"}`,
		},
	}
	input := []map[string]any{
		{"role": "user", "content": "run tool"},
		{
			"role":       "assistant",
			"content":    nil,
			"tool_calls": []any{toolCall},
		},
		{"role": "tool", "tool_call_id": "call_1", "content": "/workspace"},
		{"role": "assistant", "content": nil},
		{"role": "user", "content": nil, "tool_calls": []any{toolCall}},
		{"role": "assistant", "content": nil, "tool_calls": []any{}},
		{"role": "assistant", "tool_calls": []any{toolCall}}, // content omitted
	}

	// Make a shallow copy of original maps to test immutability
	inputCopy := make([]map[string]any, len(input))
	for i, m := range input {
		c := make(map[string]any, len(m))
		for k, v := range m {
			c[k] = v
		}
		inputCopy[i] = c
	}

	normalized := NormalizeLingmaToolCallContent(input)

	// 1. assistant + content:null + non-empty tool_calls -> content: ""
	if got := normalized[1]["content"]; got != "" {
		t.Fatalf("assistant with tool_calls content = %#v, want empty string", got)
	}

	// 2. tool_calls and subsequent tool result fully preserved
	calls, ok := normalized[1]["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %#v, want 1 call", normalized[1]["tool_calls"])
	}
	if normalized[2]["role"] != "tool" || normalized[2]["tool_call_id"] != "call_1" || normalized[2]["content"] != "/workspace" {
		t.Fatalf("tool result altered: %#v", normalized[2])
	}

	// 3. assistant + missing content + non-empty tool_calls -> content: ""
	if got := normalized[6]["content"]; got != "" {
		t.Fatalf("assistant with omitted content = %#v, want empty string", got)
	}

	// 4. normal null content not altered
	if normalized[3]["content"] != nil {
		t.Fatalf("assistant without tool_calls content = %#v, want nil", normalized[3]["content"])
	}
	if normalized[4]["content"] != nil {
		t.Fatalf("user with tool_calls content = %#v, want nil", normalized[4]["content"])
	}
	if normalized[5]["content"] != nil {
		t.Fatalf("assistant with empty tool_calls content = %#v, want nil", normalized[5]["content"])
	}

	// 5. input object was not mutated in-place
	if input[1]["content"] != nil {
		t.Fatalf("input was mutated in place: %#v", input[1])
	}
	if _, hasContent := input[6]["content"]; hasContent {
		t.Fatalf("input message 6 was mutated in place: %#v", input[6])
	}
	for i := range input {
		for k, v := range inputCopy[i] {
			if !reflect.DeepEqual(input[i][k], v) {
				t.Fatalf("input[%d][%s] was mutated from %#v to %#v", i, k, v, input[i][k])
			}
		}
	}
}

func TestConvertOpenAIRequestToLingma_NormalizesAssistantToolCallNullContent(t *testing.T) {
	raw := []byte(`{
		"model": "dashscope_qmodel",
		"messages": [
			{"role": "user", "content": "run tool"},
			{
				"role": "assistant",
				"content": null,
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {"name": "bash", "arguments": "{\"command\":\"pwd\"}"}
					}
				]
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "/workspace"},
			{"role": "user", "content": null},
			{"role": "assistant", "content": null}
		],
		"stream": true
	}`)

	payload := decodeLingmaRequestPayload(t, ConvertOpenAIRequestToLingma("dashscope_qmodel", raw, true))
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) != 5 {
		t.Fatalf("messages = %#v", payload["messages"])
	}

	msg1, ok := messages[1].(map[string]any)
	if !ok {
		t.Fatalf("message 1 = %T", messages[1])
	}
	if got := msg1["content"]; got != "" {
		t.Fatalf("assistant tool-call content = %#v, want empty string", got)
	}
	if calls, ok := msg1["tool_calls"].([]any); !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %#v, want 1 tool call", msg1["tool_calls"])
	}

	// Tool message preserved
	msg2, ok := messages[2].(map[string]any)
	if !ok {
		t.Fatalf("message 2 = %T", messages[2])
	}
	if msg2["role"] != "tool" || msg2["tool_call_id"] != "call_1" || msg2["content"] != "/workspace" {
		t.Fatalf("tool message = %#v", msg2)
	}

	// Normal user null content preserved
	msg3, ok := messages[3].(map[string]any)
	if !ok {
		t.Fatalf("message 3 = %T", messages[3])
	}
	if msg3["content"] != nil {
		t.Fatalf("user null content = %#v, want nil", msg3["content"])
	}

	// Normal assistant null content preserved
	msg4, ok := messages[4].(map[string]any)
	if !ok {
		t.Fatalf("message 4 = %T", messages[4])
	}
	if msg4["content"] != nil {
		t.Fatalf("assistant null content = %#v, want nil", msg4["content"])
	}
}
