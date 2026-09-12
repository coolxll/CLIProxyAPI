package lingmawire

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestContractFixture_ProviderErrorDetailsString(t *testing.T) {
	data, err := os.ReadFile("testdata/provider_error_details_string.sse")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	info, ok := ParseError(data)
	if !ok || info == nil {
		t.Fatal("expected ParseError to return true, got false")
	}

	wantMsg := "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'."
	if info.Message != wantMsg {
		t.Errorf("Message = %q, want %q", info.Message, wantMsg)
	}
	if info.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want %d", info.StatusCode, http.StatusBadRequest)
	}
	if info.Type != "invalid_request_error" {
		t.Errorf("Type = %q, want invalid_request_error", info.Type)
	}
	if info.Code != "invalid_request_error" {
		t.Errorf("Code = %q, want invalid_request_error", info.Code)
	}
}

func TestContractFixture_ProviderErrorDetailsObject(t *testing.T) {
	data, err := os.ReadFile("testdata/provider_error_details_object.sse")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	info, ok := ParseError(data)
	if !ok || info == nil {
		t.Fatal("expected ParseError to return true, got false")
	}

	if info.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want %d", info.StatusCode, http.StatusTooManyRequests)
	}
	if info.Message != "Rate limit reached" {
		t.Errorf("Message = %q, want Rate limit reached", info.Message)
	}
	if info.Type != "rate_limit_error" {
		t.Errorf("Type = %q, want rate_limit_error", info.Type)
	}
	if info.Code != "rate_limit_exceeded" {
		t.Errorf("Code = %q, want rate_limit_exceeded", info.Code)
	}
}

func TestContractFixture_AssistantNullToolContent(t *testing.T) {
	data, err := os.ReadFile("testdata/assistant_null_tool_content.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var messages []map[string]any
	if errUnmarshal := json.Unmarshal(data, &messages); errUnmarshal != nil {
		t.Fatalf("unmarshal fixture: %v", errUnmarshal)
	}

	normalized := NormalizeAssistantToolCallContent(messages)
	if len(normalized) != len(messages) {
		t.Fatalf("len = %d, want %d", len(normalized), len(messages))
	}

	// 1. Original input is not mutated
	if messages[2]["content"] != nil {
		t.Fatalf("original messages[2][content] mutated: %v", messages[2]["content"])
	}

	// 2. Normalized assistant message has content == ""
	if normalized[2]["content"] != "" {
		t.Fatalf("normalized[2][content] = %v, want \"\"", normalized[2]["content"])
	}

	// 3. User with content: null at index 4 remains nil
	if normalized[4]["content"] != nil {
		t.Fatalf("normalized[4][content] = %v, want nil", normalized[4]["content"])
	}

	// 4. Tool calls and tool result preserved
	if !HasToolCalls(normalized[2]["tool_calls"]) {
		t.Fatal("tool_calls lost in normalized[2]")
	}
	if normalized[3]["role"] != "tool" || normalized[3]["content"] != "{\"temp\": 25}" {
		t.Fatalf("tool message corrupted: %v", normalized[3])
	}
}

func TestContractFixture_ToolCallStream(t *testing.T) {
	data, err := os.ReadFile("testdata/tool_call_stream.sse")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	lines := strings.Split(string(data), "\n")
	var chunks [][]byte
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		chunks = append(chunks, []byte(trimmed))
	}

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	// First chunk contains tool call delta
	unwrapped0 := UnwrapBody(chunks[0])
	res0 := gjson.ParseBytes(unwrapped0)
	if res0.Get("choices.0.delta.tool_calls.0.id").String() != "call_abc" {
		t.Errorf("chunk 0 tool_call id = %q, want call_abc", res0.Get("choices.0.delta.tool_calls.0.id").String())
	}
	if IsDone(chunks[0]) {
		t.Error("chunk 0 should not be done")
	}

	// Second chunk contains arguments delta
	unwrapped1 := UnwrapBody(chunks[1])
	res1 := gjson.ParseBytes(unwrapped1)
	if res1.Get("choices.0.delta.tool_calls.0.function.arguments").String() != ":\"golang\"}" {
		t.Errorf("chunk 1 arguments = %q", res1.Get("choices.0.delta.tool_calls.0.function.arguments").String())
	}
	if IsDone(chunks[1]) {
		t.Error("chunk 1 should not be done")
	}

	// Third chunk is [DONE]
	if !IsDone(chunks[2]) {
		t.Error("chunk 2 should be done")
	}
}

func TestContractFixture_UsageVariants(t *testing.T) {
	data, err := os.ReadFile("testdata/usage_variants.sse")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	unwrapped := UnwrapBody(data)
	normalized := NormalizeUsageBytes(unwrapped)
	if len(normalized) == 0 {
		t.Fatal("expected normalized usage, got nil")
	}

	res := gjson.ParseBytes(normalized)
	if res.Get("prompt_tokens").Int() != 120 {
		t.Errorf("prompt_tokens = %d, want 120", res.Get("prompt_tokens").Int())
	}
	if res.Get("completion_tokens").Int() != 45 {
		t.Errorf("completion_tokens = %d, want 45", res.Get("completion_tokens").Int())
	}
	if res.Get("total_tokens").Int() != 165 {
		t.Errorf("total_tokens = %d, want 165", res.Get("total_tokens").Int())
	}
	if res.Get("prompt_tokens_details.cached_tokens").Int() != 30 {
		t.Errorf("cached_tokens = %d, want 30", res.Get("prompt_tokens_details.cached_tokens").Int())
	}
	if res.Get("completion_tokens_details.reasoning_tokens").Int() != 15 {
		t.Errorf("reasoning_tokens = %d, want 15", res.Get("completion_tokens_details.reasoning_tokens").Int())
	}
}

func TestAgentHelpers(t *testing.T) {
	if AgentID("kmodel") != AgentCommon {
		t.Errorf("AgentID(kmodel) = %q, want %q", AgentID("kmodel"), AgentCommon)
	}
	if AgentID("mmodel") != AgentCommon {
		t.Errorf("AgentID(mmodel) = %q, want %q", AgentID("mmodel"), AgentCommon)
	}
	if AgentID("gm51model") != AgentChat {
		t.Errorf("AgentID(gm51model) = %q, want %q", AgentID("gm51model"), AgentChat)
	}

	if ModelConfigSource("kmodel") != "" {
		t.Errorf("ModelConfigSource(kmodel) = %q, want empty", ModelConfigSource("kmodel"))
	}
	if ModelConfigSource("gm51model") != SourceSystem {
		t.Errorf("ModelConfigSource(gm51model) = %q, want %q", ModelConfigSource("gm51model"), SourceSystem)
	}
}
