package executor

import (
	"encoding/json"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	traeenc "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/trae"
	"github.com/tidwall/gjson"
)

func TestBuildTraeRawChatMessagesV1PrependsToolShim(t *testing.T) {
	messages := buildTraeRawChatMessages([]byte(`{
		"tools": [
			{"type":"function","function":{"name":"Read","description":"Read file","parameters":{"type":"object","properties":{"file_path":{"type":"string"}}}}}
		],
		"messages": [
			{"role":"user","content":"读取 README 文件"}
		]
	}`), traeProtocolV1)

	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2; messages=%v", len(messages), messages)
	}
	if got := messages[0]["role"]; got != "system" {
		t.Fatalf("first message role = %v, want system", got)
	}
	content, ok := messages[0]["content"].([]map[string]string)
	if !ok || len(content) != 1 {
		t.Fatalf("first message content = %#v, want text content", messages[0]["content"])
	}
	if !strings.Contains(content[0]["text"], "tool_calls=") {
		t.Fatalf("first message missing tool shim: %q", content[0]["text"])
	}
	if got := messages[1]["role"]; got != "user" {
		t.Fatalf("second message role = %v, want user", got)
	}
}

func TestBuildTraeRawChatRequestV1(t *testing.T) {
	req, err := buildTraeRawChatRequest(traeProtocolV1, "deepseek-R1", []byte(`{
		"model":"deepseek-R1",
		"messages":[{"role":"user","content":"hello"}]
	}`), cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("buildTraeRawChatRequest v1 error: %v", err)
	}

	if !strings.HasSuffix(req.TargetURL, "/api/ide/v1/llm_raw_chat") {
		t.Fatalf("TargetURL = %q, want v1 llm_raw_chat", req.TargetURL)
	}
	if got := gjson.GetBytes(req.RequestBody, "model_name").String(); got != "deepseek-R1" {
		t.Fatalf("model_name = %q, want deepseek-R1", got)
	}

	plain, err := traeenc.DecryptMessage(gjson.GetBytes(req.RequestBody, "message").String(), req.RequestPin, req.RequestAt)
	if err != nil {
		t.Fatalf("decrypt v1 raw chat payload: %v", err)
	}
	if got := gjson.GetBytes(plain, "0.content.0.text").String(); got != "hello" {
		t.Fatalf("v1 encrypted message text = %q, want hello; payload=%s", got, string(plain))
	}
}

func TestBuildTraeRawChatRequestV1ToolsInOuterEnvelope(t *testing.T) {
	// V1 raw chat: tools must be plaintext in the outer envelope, NOT inside the encrypted payload.
	req, err := buildTraeRawChatRequest(traeProtocolV1, "deepseek-R1", []byte(`{
		"model":"deepseek-R1",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{"type":"function","function":{"name":"bash","description":"Run a command","parameters":{"type":"object"}}}]
	}`), cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("buildTraeRawChatRequest v1 error: %v", err)
	}

	// Tools should appear in the outer envelope as plaintext.
	outerTools := gjson.GetBytes(req.RequestBody, "tools")
	if !outerTools.Exists() || !outerTools.IsArray() || len(outerTools.Array()) == 0 {
		t.Fatalf("v1 outer envelope should contain tools; body=%s", string(req.RequestBody))
	}
	if got := outerTools.Array()[0].Get("function.name").String(); got != "bash" {
		t.Fatalf("v1 outer envelope tool name = %q, want bash", got)
	}

	plain, err := traeenc.DecryptMessage(gjson.GetBytes(req.RequestBody, "message").String(), req.RequestPin, req.RequestAt)
	if err != nil {
		t.Fatalf("decrypt v1 raw chat payload: %v", err)
	}
	// The decrypted payload should be a plain messages array, NOT a {messages, tools} object.
	if gjson.GetBytes(plain, "tools").Exists() {
		t.Fatalf("v1 encrypted payload should NOT contain tools; payload=%s", string(plain))
	}
	// It should still contain the user message after the shim instructions.
	if got := gjson.GetBytes(plain, "1.content.0.text").String(); got != "hello" {
		t.Fatalf("v1 encrypted message text = %q, want hello; payload=%s", got, string(plain))
	}
}

func TestBuildTraeRawChatRequestStripsClaudeOnlyFields(t *testing.T) {
	req, err := buildTraeRawChatRequest(traeProtocolV1, "deepseek-R1", []byte(`{
		"model":"deepseek-R1",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{"type":"function","function":{"name":"Bash","description":"Run a command","parameters":{"type":"object"}}}],
		"betas":["interleaved-thinking-2025-05-14"],
		"thinking":{"type":"adaptive"},
		"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},
		"output_config":{"effort":"high"}
	}`), cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("buildTraeRawChatRequest v1 error: %v", err)
	}

	for _, key := range []string{"betas", "thinking", "context_management", "output_config"} {
		if gjson.GetBytes(req.RequestBody, key).Exists() {
			t.Fatalf("v1 outer envelope should not contain %s; body=%s", key, string(req.RequestBody))
		}
	}

	plain, err := traeenc.DecryptMessage(gjson.GetBytes(req.RequestBody, "message").String(), req.RequestPin, req.RequestAt)
	if err != nil {
		t.Fatalf("decrypt v1 raw chat payload: %v", err)
	}
	for _, key := range []string{"betas", "thinking", "context_management", "output_config"} {
		if gjson.GetBytes(plain, key).Exists() {
			t.Fatalf("v1 encrypted payload should not contain %s; payload=%s", key, string(plain))
		}
	}
}

func TestBuildTraeRawChatRequestV1WrapsToolResultAsText(t *testing.T) {
	encodedID, err := encodeTraeToolID(traeToolState{
		SessionID:      "session-1",
		ConversationID: "conversation-1",
		TaskID:         "task-1",
		AgentRunID:     "run-1",
		NativeID:       "inline-0",
		Name:           "Bash",
	})
	if err != nil {
		t.Fatalf("encode tool id: %v", err)
	}

	rawRequest, err := json.Marshal(map[string]any{
		"model": "deepseek-R1",
		"messages": []map[string]any{
			{"role": "user", "content": "show git diff"},
			{
				"role":    "assistant",
				"content": "I will inspect the diff.",
				"tool_calls": []map[string]any{{
					"id":   encodedID,
					"type": "function",
					"function": map[string]any{
						"name":      "Bash",
						"arguments": `{"command":"git diff"}`,
					},
				}},
			},
			{
				"role":         "tool",
				"tool_call_id": encodedID,
				"content":      "diff output",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal raw request: %v", err)
	}

	req, err := buildTraeRawChatRequest(traeProtocolV1, "deepseek-R1", rawRequest, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("buildTraeRawChatRequest v1 error: %v", err)
	}

	plain, err := traeenc.DecryptMessage(gjson.GetBytes(req.RequestBody, "message").String(), req.RequestPin, req.RequestAt)
	if err != nil {
		t.Fatalf("decrypt v1 raw chat payload: %v", err)
	}
	if strings.Contains(string(plain), `"role":"tool"`) {
		t.Fatalf("v1 encrypted payload must not contain native tool role; payload=%s", string(plain))
	}
	if strings.Contains(string(plain), `"tool_calls"`) {
		t.Fatalf("v1 encrypted payload must not contain native tool_calls; payload=%s", string(plain))
	}
	assistantMsg := gjson.GetBytes(plain, "1")
	if got := assistantMsg.Get("role").String(); got != "assistant" {
		t.Fatalf("message[1].role = %q, want assistant; payload=%s", got, string(plain))
	}
	if got := assistantMsg.Get("content.0.text").String(); got != "I will inspect the diff." {
		t.Fatalf("assistant content = %q, want original text; payload=%s", got, string(plain))
	}
	toolMsg := gjson.GetBytes(plain, "2")
	if got := toolMsg.Get("role").String(); got != "user" {
		t.Fatalf("message[2].role = %q, want user wrapped tool result; payload=%s", got, string(plain))
	}
	toolText := toolMsg.Get("content.0.text").String()
	for _, want := range []string{"<tool_result>", encodedID, "<name>Bash</name>", "diff output"} {
		if !strings.Contains(toolText, want) {
			t.Fatalf("wrapped tool result missing %q; text=%q payload=%s", want, toolText, string(plain))
		}
	}
	if strings.Contains(toolText, `"role":"tool"`) || strings.Contains(toolText, "tool_calls") {
		t.Fatalf("wrapped tool result leaked native tool history; text=%q payload=%s", toolText, string(plain))
	}
}

func TestBuildTraeRawChatRequestV1DropsInvalidEmptyClaudeToolHistory(t *testing.T) {
	encodedID, err := encodeTraeToolID(traeToolState{
		SessionID:      "session-1",
		ConversationID: "conversation-1",
		TaskID:         "task-1",
		AgentRunID:     "run-1",
		NativeID:       "inline-0",
		Name:           "Bash",
	})
	if err != nil {
		t.Fatalf("encode tool id: %v", err)
	}

	rawRequest, err := json.Marshal(map[string]any{
		"model": "deepseek-R1",
		"messages": []map[string]any{
			{"role": "user", "content": "read README"},
			{
				"role":    "assistant",
				"content": "I will inspect the file.",
				"tool_calls": []map[string]any{{
					"id":   encodedID,
					"type": "function",
					"function": map[string]any{
						"name":      "Bash",
						"arguments": `{}`,
					},
				}},
			},
			{
				"role":         "tool",
				"tool_call_id": encodedID,
				"name":         "Bash",
				"content":      "InputValidationError: Bash failed because the required parameter `command` is missing",
			},
			{"role": "user", "content": "read README again"},
		},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "Bash",
				"description": "Run a command",
				"parameters":  map[string]any{"type": "object"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("marshal raw request: %v", err)
	}

	req, err := buildTraeRawChatRequest(traeProtocolV1, "deepseek-R1", rawRequest, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("buildTraeRawChatRequest v1 error: %v", err)
	}
	plain, err := traeenc.DecryptMessage(gjson.GetBytes(req.RequestBody, "message").String(), req.RequestPin, req.RequestAt)
	if err != nil {
		t.Fatalf("decrypt v1 raw chat payload: %v", err)
	}
	if strings.Contains(string(plain), encodedID) {
		t.Fatalf("invalid empty tool history leaked into v1 payload: %s", string(plain))
	}
	if strings.Contains(string(plain), "InputValidationError") {
		t.Fatalf("tool validation error leaked into v1 payload: %s", string(plain))
	}
	if got := gjson.GetBytes(plain, "1.content.0.text").String(); got != "read README" {
		t.Fatalf("first user message = %q, want read README; payload=%s", got, string(plain))
	}
	if got := gjson.GetBytes(plain, "2.content.0.text").String(); got != "read README again" {
		t.Fatalf("final user message = %q, want read README again; payload=%s", got, string(plain))
	}
}

func TestBuildTraeRawChatRequestV1DropsInvalidClaudeToolResultBlockHistory(t *testing.T) {
	encodedID, err := encodeTraeToolID(traeToolState{
		SessionID:      "session-1",
		ConversationID: "conversation-1",
		TaskID:         "task-1",
		AgentRunID:     "run-1",
		NativeID:       "inline-0",
		Name:           "Bash",
	})
	if err != nil {
		t.Fatalf("encode tool id: %v", err)
	}

	rawRequest, err := json.Marshal(map[string]any{
		"model": "deepseek-R1",
		"messages": []map[string]any{
			{
				"role":    "assistant",
				"content": "I will inspect the file.",
				"tool_calls": []map[string]any{{
					"id":   encodedID,
					"type": "function",
					"function": map[string]any{
						"name":      "Bash",
						"arguments": `{}`,
					},
				}},
			},
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type":        "tool_result",
						"tool_use_id": encodedID,
						"is_error":    true,
						"content":     "InputValidationError: the required parameter `command` is missing",
					},
					{"type": "text", "text": "[Request interrupted by user]\n"},
					{"type": "text", "text": "读取 README 文件"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal raw request: %v", err)
	}

	req, err := buildTraeRawChatRequest(traeProtocolV1, "deepseek-R1", rawRequest, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("buildTraeRawChatRequest v1 error: %v", err)
	}
	plain, err := traeenc.DecryptMessage(gjson.GetBytes(req.RequestBody, "message").String(), req.RequestPin, req.RequestAt)
	if err != nil {
		t.Fatalf("decrypt v1 raw chat payload: %v", err)
	}
	if strings.Contains(string(plain), encodedID) || strings.Contains(string(plain), "tool_calls") {
		t.Fatalf("invalid Claude tool_result history leaked into v1 payload: %s", string(plain))
	}
	if strings.Contains(string(plain), "InputValidationError") {
		t.Fatalf("tool validation error leaked into v1 payload: %s", string(plain))
	}
	if got := gjson.GetBytes(plain, "0.content.0.text").String(); got != "[Request interrupted by user]\n读取 README 文件" {
		t.Fatalf("preserved user text = %q, want interrupted text and prompt; payload=%s", got, string(plain))
	}
}

func TestBuildTraeRawChatRequestV2NoThinkingModel(t *testing.T) {
	req, err := buildTraeRawChatRequest(traeProtocolV2, "no_thinking_model", []byte(`{
		"model":"no_thinking_model",
		"messages":[{"role":"system","content":"brief"},{"role":"user","content":"hello"}]
	}`), cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("buildTraeRawChatRequest v2 error: %v", err)
	}

	if !strings.HasSuffix(req.TargetURL, "/api/ide/v2/llm_raw_chat") {
		t.Fatalf("TargetURL = %q, want v2 llm_raw_chat", req.TargetURL)
	}
	if got := gjson.GetBytes(req.RequestBody, "model_name").String(); got != "no_thinking_model" {
		t.Fatalf("model_name = %q, want no_thinking_model", got)
	}
	if got := gjson.GetBytes(req.RequestBody, "config_name").String(); got != "title_generation" {
		t.Fatalf("config_name = %q, want title_generation", got)
	}
	if got := gjson.GetBytes(req.RequestBody, "messages").Array(); len(got) != 0 {
		t.Fatalf("outer messages len = %d, want 0", len(got))
	}
	if got := req.ExtraHeaders.Get("X-App-Function"); got != "utils" {
		t.Fatalf("X-App-Function = %q, want utils", got)
	}

	plain, err := traeenc.DecryptMessage(gjson.GetBytes(req.RequestBody, "message").String(), req.RequestPin, req.RequestAt)
	if err != nil {
		t.Fatalf("decrypt v2 raw chat payload: %v", err)
	}
	if got := gjson.GetBytes(plain, "1.content.0.text").String(); got != "hello" {
		t.Fatalf("v2 encrypted message text = %q, want hello; payload=%s", got, string(plain))
	}
}

func TestBuildTraeRawChatRequestV2OmitsTools(t *testing.T) {
	// V2 raw chat does not support tools; they must be omitted from both inner and outer payloads.
	req, err := buildTraeRawChatRequest(traeProtocolV2, "no_thinking_model", []byte(`{
		"model":"no_thinking_model",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{"type":"function","function":{"name":"bash","description":"Run a command","parameters":{"type":"object"}}}]
	}`), cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("buildTraeRawChatRequest v2 error: %v", err)
	}

	// Outer envelope should NOT contain tools.
	if gjson.GetBytes(req.RequestBody, "tools").Exists() {
		t.Fatalf("v2 outer envelope should NOT contain tools; body=%s", string(req.RequestBody))
	}

	plain, err := traeenc.DecryptMessage(gjson.GetBytes(req.RequestBody, "message").String(), req.RequestPin, req.RequestAt)
	if err != nil {
		t.Fatalf("decrypt v2 raw chat payload: %v", err)
	}
	// Inner payload should NOT contain tools.
	if gjson.GetBytes(plain, "tools").Exists() {
		t.Fatalf("v2 encrypted payload should NOT contain tools; payload=%s", string(plain))
	}
}
