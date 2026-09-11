package claude

import (
	"encoding/json"
	"testing"
)

func TestConvertClaudeRequestToLingma_ToolUseContentNormalization(t *testing.T) {
	// Claude Messages API request with tool_use from assistant and tool_result from user
	claudeRequest := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{"role": "user", "content": "What is the weather in Paris?"},
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "toolu_123",
						"name": "get_weather",
						"input": {"location": "Paris"}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_123",
						"content": "Sunny, 22°C"
					}
				]
			}
		]
	}`)

	lingmaRaw := ConvertClaudeRequestToLingma("gm51model", claudeRequest, false)
	if !json.Valid(lingmaRaw) {
		t.Fatalf("invalid JSON output: %s", lingmaRaw)
	}

	var payload map[string]any
	if err := json.Unmarshal(lingmaRaw, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) < 3 {
		t.Fatalf("messages = %#v", payload["messages"])
	}

	// Assistant message with tool_calls must have content: "" (not nil, not omitted)
	assistantMsg, ok := messages[1].(map[string]any)
	if !ok {
		t.Fatalf("assistant message = %T", messages[1])
	}
	if got := assistantMsg["content"]; got != "" {
		t.Fatalf("assistant content = %#v, want empty string", got)
	}
	calls, ok := assistantMsg["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("assistant tool_calls = %#v, want 1 call", assistantMsg["tool_calls"])
	}

	// Tool result message must follow and be preserved
	toolMsg, ok := messages[2].(map[string]any)
	if !ok {
		t.Fatalf("tool message = %T", messages[2])
	}
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "toolu_123" {
		t.Fatalf("tool result message = %#v", toolMsg)
	}
}
