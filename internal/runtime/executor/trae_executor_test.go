package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	traeenc "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/trae"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestTraeExecutorCountTokensReturnsApproximation(t *testing.T) {
	e := &TraeExecutor{}
	resp, err := e.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: []byte(`{"model":"glm-5","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("CountTokens returned error: %v", err)
	}

	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got <= 0 {
		t.Fatalf("usage.total_tokens = %d, want positive token count; payload=%s", got, string(resp.Payload))
	}
}

func TestOpenAIUsageFromTraeTokenUsage(t *testing.T) {
	usageData := openAIUsageFromResult(gjson.Parse(`{
		"prompt_tokens": 11,
		"completion_tokens": 7,
		"total_tokens": 18,
		"cache_creation_input_tokens": 3,
		"cache_read_input_tokens": 5,
		"reasoning_tokens": 2
	}`))

	raw, err := json.Marshal(usageData)
	if err != nil {
		t.Fatalf("marshal usage: %v", err)
	}
	if got := gjson.GetBytes(raw, "prompt_tokens").Int(); got != 11 {
		t.Fatalf("prompt_tokens = %d, want 11", got)
	}
	if got := gjson.GetBytes(raw, "completion_tokens").Int(); got != 7 {
		t.Fatalf("completion_tokens = %d, want 7", got)
	}
	if got := gjson.GetBytes(raw, "total_tokens").Int(); got != 18 {
		t.Fatalf("total_tokens = %d, want 18", got)
	}
	if got := gjson.GetBytes(raw, "prompt_tokens_details.cached_tokens").Int(); got != 8 {
		t.Fatalf("cached_tokens = %d, want 8", got)
	}
	if got := gjson.GetBytes(raw, "completion_tokens_details.reasoning_tokens").Int(); got != 2 {
		t.Fatalf("reasoning_tokens = %d, want 2", got)
	}
}

func TestTraeExecutorStreamReturnsSSEError(t *testing.T) {
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := "event: error\n" +
			"data: {\"code\":4001,\"message\":\"failed to get summary config\"}\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))

	rawRequest := []byte(`{"model":"doubao-seed-2.0-code","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	result, err := NewTraeExecutor(nil).ExecuteStream(ctx, &cliproxyauth.Auth{
		Provider: "trae",
		Attributes: map[string]string{
			"jwt_token": "not-a-real-jwt",
		},
	}, cliproxyexecutor.Request{
		Model:   "doubao-seed-2.0-code",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream returned setup error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			break
		}
	}
	if streamErr == nil {
		t.Fatal("expected stream error from Trae error event")
	}
	if !strings.Contains(streamErr.Error(), "trae error event 4001") {
		t.Fatalf("unexpected stream error: %v", streamErr)
	}
}

func TestTraeThoughtToolParser(t *testing.T) {
	tests := []struct {
		name      string
		chunks    []string
		wantText  string
		wantTools []traeThoughtToolCall
	}{
		{
			name:     "single chunk",
			chunks:   []string{`<tool_call>LS path="/tmp" />`},
			wantText: "",
			wantTools: []traeThoughtToolCall{{
				Name:      "LS",
				Arguments: `{"path":"/tmp"}`,
			}},
		},
		{
			name:     "split chunk",
			chunks:   []string{`<tool_call>LS path`, `="/tmp" />`},
			wantText: "",
			wantTools: []traeThoughtToolCall{{
				Name:      "LS",
				Arguments: `{"path":"/tmp"}`,
			}},
		},
		{
			name:     "multiple parameters",
			chunks:   []string{`<tool_call>Read path="/tmp/a.go" offset="1" limit="20" />`},
			wantText: "",
			wantTools: []traeThoughtToolCall{{
				Name:      "Read",
				Arguments: `{"limit":"20","offset":"1","path":"/tmp/a.go"}`,
			}},
		},
		{
			name:     "mixed text",
			chunks:   []string{`before <tool_call>SearchCodebase query="main" /> after`},
			wantText: "before  after",
			wantTools: []traeThoughtToolCall{{
				Name:      "SearchCodebase",
				Arguments: `{"query":"main"}`,
			}},
		},
		{
			name:      "no tool call",
			chunks:    []string{"plain thought"},
			wantText:  "plain thought",
			wantTools: nil,
		},
		{
			name:      "incomplete tool call",
			chunks:    []string{`prefix <tool_call>LS path`},
			wantText:  "prefix ",
			wantTools: nil,
		},
		{
			name:      "malformed tool call",
			chunks:    []string{`<tool_call>LS path=/tmp />`},
			wantText:  `<tool_call>LS path=/tmp />`,
			wantTools: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parser traeThoughtToolParser
			var gotText strings.Builder
			var gotTools []traeThoughtToolCall
			for _, chunk := range tt.chunks {
				got := parser.Append(chunk)
				gotText.WriteString(got.Content)
				gotTools = append(gotTools, got.ToolCalls...)
			}

			if gotText.String() != tt.wantText {
				t.Fatalf("content = %q, want %q", gotText.String(), tt.wantText)
			}
			if len(gotTools) != len(tt.wantTools) {
				t.Fatalf("tool count = %d, want %d: %#v", len(gotTools), len(tt.wantTools), gotTools)
			}
			for i := range tt.wantTools {
				if gotTools[i] != tt.wantTools[i] {
					t.Fatalf("tool[%d] = %#v, want %#v", i, gotTools[i], tt.wantTools[i])
				}
			}
		})
	}
}

func TestTraeThoughtToolParserFlushesIncompleteAtEOF(t *testing.T) {
	var parser traeThoughtToolParser
	got := parser.Append(`prefix <tool_call>LS path`)
	if got.Content != "prefix " {
		t.Fatalf("content = %q, want prefix", got.Content)
	}
	if len(got.ToolCalls) != 0 {
		t.Fatalf("tool calls = %#v, want none", got.ToolCalls)
	}
	if trailing := parser.Flush(); trailing != `<tool_call>LS path` {
		t.Fatalf("flush = %q, want incomplete markup", trailing)
	}
	if trailing := parser.Flush(); trailing != "" {
		t.Fatalf("second flush = %q, want empty", trailing)
	}
}

func TestTraeThoughtToolParserBoundsIncompleteToolBuffer(t *testing.T) {
	var parser traeThoughtToolParser
	got := parser.Append(traeThoughtToolMarker + strings.Repeat("x", maxTraeThoughtToolBuffer+1))
	if got.Content == "" {
		t.Fatal("expected oversized incomplete tool markup to be flushed as content")
	}
	if len(got.ToolCalls) != 0 {
		t.Fatalf("tool calls = %#v, want none", got.ToolCalls)
	}
	if len(parser.buffer) >= maxTraeThoughtToolBuffer {
		t.Fatalf("buffer len = %d, want bounded below %d", len(parser.buffer), maxTraeThoughtToolBuffer)
	}
}

func TestTraeExecutorStreamConvertsThoughtToolCall(t *testing.T) {
	body := "event: task_created\n" +
		"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
		"data: {\"reasoning_content\":\"internal-reasoning\"}\n\n" +
		"event: thought\n" +
		"data: {\"thought\":\"checking <tool_call>LS path\"}\n\n" +
		"event: thought\n" +
		"data: {\"thought\":\"=\\\"/tmp\\\" /> after\"}\n\n"
	result := runTraeStreamWithBody(t, body)

	var content strings.Builder
	var reasoning strings.Builder
	var toolCalls []gjson.Result
	finishReason := ""
	for _, data := range collectOpenAIStreamData(t, result) {
		if data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		if val := gjson.Get(data, "choices.0.delta.content"); val.Exists() {
			content.WriteString(val.String())
		}
		if val := gjson.Get(data, "choices.0.delta.reasoning_content"); val.Exists() {
			reasoning.WriteString(val.String())
		}
		if tc := gjson.Get(data, "choices.0.delta.tool_calls"); tc.Exists() {
			toolCalls = append(toolCalls, tc.Array()...)
		}
		if fr := gjson.Get(data, "choices.0.finish_reason").String(); fr != "" {
			finishReason = fr
		}
	}

	if got := content.String(); got != "checking  after" {
		t.Fatalf("content = %q, want thought text without tool markup", got)
	}
	if strings.Contains(content.String(), "<tool_call>") {
		t.Fatalf("content leaked tool markup: %q", content.String())
	}
	if got := reasoning.String(); got != "internal-reasoning" {
		t.Fatalf("reasoning = %q, want internal-reasoning", got)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("tool call count = %d, want 1: %#v", len(toolCalls), toolCalls)
	}
	tc := toolCalls[0]
	if got := tc.Get("function.name").String(); got != "LS" {
		t.Fatalf("tool name = %q, want LS", got)
	}
	if got := tc.Get("function.arguments").String(); got != `{"path":"/tmp"}` {
		t.Fatalf("tool arguments = %q, want path JSON", got)
	}
	state, err := decodeTraeToolID(tc.Get("id").String())
	if err != nil {
		t.Fatalf("decode thought tool id: %v", err)
	}
	if state.NativeID != "thought-0" || state.Name != "LS" || state.TaskID != "task-1" || state.AgentRunID != "run-1" {
		t.Fatalf("decoded state = %#v, want thought synthetic state", state)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", finishReason)
	}
}

func TestTraeExecutorStreamFlushesIncompleteThoughtToolCallAtEOF(t *testing.T) {
	body := "event: task_created\n" +
		"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
		"event: thought\n" +
		"data: {\"thought\":\"prefix <tool_call>LS path\"}\n\n"
	result := runTraeStreamWithBody(t, body)

	var content strings.Builder
	var toolCalls []gjson.Result
	finishReason := ""
	for _, data := range collectOpenAIStreamData(t, result) {
		if data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		if val := gjson.Get(data, "choices.0.delta.content"); val.Exists() {
			content.WriteString(val.String())
		}
		if tc := gjson.Get(data, "choices.0.delta.tool_calls"); tc.Exists() {
			toolCalls = append(toolCalls, tc.Array()...)
		}
		if fr := gjson.Get(data, "choices.0.finish_reason").String(); fr != "" {
			finishReason = fr
		}
	}

	if got := content.String(); got != `prefix <tool_call>LS path` {
		t.Fatalf("content = %q, want incomplete tool markup flushed at EOF", got)
	}
	if len(toolCalls) != 0 {
		t.Fatalf("tool call count = %d, want 0: %#v", len(toolCalls), toolCalls)
	}
	if finishReason != "stop" {
		t.Fatalf("finish_reason = %q, want stop", finishReason)
	}
}

func TestTraeExecutorStreamKeepsLegacyToolCallEvent(t *testing.T) {
	body := "event: task_created\n" +
		"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
		"event: tool_call\n" +
		"data: {\"tool_name\":\"Read\",\"toolcall_payload\":\"{\\\"path\\\":\\\"/tmp/a.go\\\"}\",\"toolcall_id\":\"native-123\"}\n\n"
	result := runTraeStreamWithBody(t, body)

	var toolCalls []gjson.Result
	finishReason := ""
	for _, data := range collectOpenAIStreamData(t, result) {
		if data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		if tc := gjson.Get(data, "choices.0.delta.tool_calls"); tc.Exists() {
			toolCalls = append(toolCalls, tc.Array()...)
		}
		if fr := gjson.Get(data, "choices.0.finish_reason").String(); fr != "" {
			finishReason = fr
		}
	}

	if len(toolCalls) != 1 {
		t.Fatalf("tool call count = %d, want 1: %#v", len(toolCalls), toolCalls)
	}
	tc := toolCalls[0]
	if got := tc.Get("function.name").String(); got != "Read" {
		t.Fatalf("tool name = %q, want Read", got)
	}
	state, err := decodeTraeToolID(tc.Get("id").String())
	if err != nil {
		t.Fatalf("decode legacy tool id: %v", err)
	}
	if state.NativeID != "native-123" || state.Name != "Read" {
		t.Fatalf("decoded state = %#v, want legacy native id and name", state)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", finishReason)
	}
}

func TestBuildTraeToolCommitRequestUsesEncodedToolName(t *testing.T) {
	creds := &traeauth.TraeCredentials{UserID: "user-1"}
	encodedID, err := encodeTraeToolID(traeToolState{
		SessionID:      "session-1",
		ConversationID: "conversation-1",
		TaskID:         "task-1",
		AgentRunID:     "run-1",
		NativeID:       "thought-0",
		Name:           "LS",
	})
	if err != nil {
		t.Fatalf("encode tool id: %v", err)
	}

	req, err := buildTraeToolCommitRequest(creds, []gjson.Result{
		gjson.Parse(`{"role":"tool","tool_call_id":"` + encodedID + `","name":"rewritten","content":"ok"}`),
	})
	if err != nil {
		t.Fatalf("build commit request: %v", err)
	}
	if got := gjson.GetBytes(req.LogBody, "toolcall_results.0.toolcall_name").String(); got != "LS" {
		t.Fatalf("toolcall_name = %q, want encoded state name LS; body=%s", got, string(req.LogBody))
	}
}

func TestResolveTraeProtocolByModelName(t *testing.T) {
	tests := []struct {
		model        string
		wantProtocol string
		wantModel    string
	}{
		{"glm-5", traeProtocolV3, "glm-5"},
		{"deepseek-R1", traeProtocolV1, "deepseek-R1"},
		{"deepseek-V3", traeProtocolV1, "deepseek-V3"},
		{"no_thinking_model", traeProtocolV2, "no_thinking_model"},
		{"glm-5__dev", traeProtocolV3, "glm-5__dev"},
		{"custom_model_gpt-5__dev", traeProtocolV3, "custom_model_gpt-5__dev"},
		{"trae/glm-5", traeProtocolV3, "glm-5"},
		{"trae/deepseek-R1", traeProtocolV1, "deepseek-R1"},
		{"trae/deepseek-V3", traeProtocolV1, "deepseek-V3"},
		{"trae/no_thinking_model", traeProtocolV2, "no_thinking_model"},
	}

	for _, tt := range tests {
		gotProtocol, gotModel := resolveTraeProtocol(tt.model, nil)
		if gotProtocol != tt.wantProtocol || gotModel != tt.wantModel {
			t.Fatalf("resolveTraeProtocol(%q) = (%q, %q), want (%q, %q)",
				tt.model, gotProtocol, gotModel, tt.wantProtocol, tt.wantModel)
		}
	}
}

func TestParseTraeModels(t *testing.T) {
	models := parseTraeModels([]byte(`{
		"model_configs": [
			{"name":"seed_m8","display_name":"Doubao-1.5-pro","status":true,"prompt_max_tokens":28000},
			{"name":"disabled_model","display_name":"Disabled","status":false,"prompt_max_tokens":40000},
			{"name":"deepseek-R1","display_name":"DeepSeek-Reasoner（R1）","status":true,"prompt_max_tokens":40000},
			{"name":"deepseek-V3","display_name":"DeepSeek-V3","status":true,"prompt_max_tokens":40000},
			{"name":"deepseek-V3-0324","display_name":"DeepSeek-V3-0324","status":true,"prompt_max_tokens":40000}
		]
	}`), 123)

	if len(models) != 4 {
		t.Fatalf("len(models) = %d, want 4", len(models))
	}
	if got := models[0].ID; got != "seed_m8" {
		t.Fatalf("first model ID = %q, want seed_m8", got)
	}
	if got := models[0].DisplayName; got != "Doubao-1.5-pro" {
		t.Fatalf("first model display = %q, want Doubao-1.5-pro", got)
	}
	if got := models[0].ContextLength; got != 28000 {
		t.Fatalf("first model context = %d, want 28000", got)
	}
	for _, model := range models {
		if model.ID == "disabled_model" {
			t.Fatal("disabled model should be excluded")
		}
	}
}

func TestAppendTraeNoThinkingModel(t *testing.T) {
	models := appendTraeNoThinkingModel(parseTraeModels([]byte(`{"model_configs":[{"name":"seed_m8","status":true}]}`), 123), 123)
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if got := models[1].ID; got != "no_thinking_model" {
		t.Fatalf("appended model ID = %q, want no_thinking_model", got)
	}
	if slices.Contains(models[1].SupportedParameters, "tools") {
		t.Fatalf("no_thinking_model should not advertise tools: %#v", models[1].SupportedParameters)
	}

	existing := appendTraeNoThinkingModel([]*registry.ModelInfo{{
		ID:                  "no_thinking_model",
		SupportedParameters: []string{"tools", "temperature"},
	}}, 123)
	if slices.Contains(existing[0].SupportedParameters, "tools") {
		t.Fatalf("existing no_thinking_model should not advertise tools: %#v", existing[0].SupportedParameters)
	}
	if !slices.Contains(existing[0].SupportedParameters, "temperature") {
		t.Fatalf("existing supported parameter should be preserved: %#v", existing[0].SupportedParameters)
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
	// It should still contain the messages.
	if got := gjson.GetBytes(plain, "0.content.0.text").String(); got != "hello" {
		t.Fatalf("v1 encrypted message text = %q, want hello; payload=%s", got, string(plain))
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
	if got := gjson.GetBytes(plain, "0.content.0.text").String(); got != "hello" {
		t.Fatalf("v2 encrypted message text = %q, want hello; payload=%s", got, string(plain))
	}
}

func TestAppendTraeV3AgentModels(t *testing.T) {
	models := appendTraeV3AgentModels(nil, 123)
	if len(models) != 7 {
		t.Fatalf("len(models) = %d, want 7", len(models))
	}
	if got := models[0].ID; got != "glm-4.7" {
		t.Fatalf("first model ID = %q, want glm-4.7", got)
	}

	// Should not duplicate
	models = appendTraeV3AgentModels(models, 123)
	if len(models) != 7 {
		t.Fatalf("after dedup len(models) = %d, want 7", len(models))
	}
}

func TestParseTraeDetailParam(t *testing.T) {
	data := []byte(`{
		"config_info_list": [
			{
				"config_name": "glm-5.1",
				"config_switch": true,
				"usage": "chat_completion",
				"display_config": {"display_name": "GLM-5.1", "multimodal": false},
				"model_detail_list": [{"model_name": "glm-5.1__dev", "max_tokens": 16000, "prompt_max_tokens": 100000}]
			},
			{
				"config_name": "kimi-k2.6",
				"config_switch": true,
				"usage": "chat_completion",
				"display_config": {"display_name": "Kimi K2.6", "multimodal": true},
				"model_detail_list": [{"model_name": "kimi-k2.6__dev", "max_tokens": 16000, "prompt_max_tokens": 100000}]
			},
			{
				"config_name": "summary_model",
				"config_switch": true,
				"usage": "summary",
				"display_config": {"display_name": "Summary"},
				"model_detail_list": [{"model_name": "summary__dev", "max_tokens": 4096, "prompt_max_tokens": 200000}]
			},
			{
				"config_name": "custom_model_placeholder",
				"config_switch": true,
				"usage": "custom_model",
				"display_config": {"display_name": "Custom"},
				"model_detail_list": [{"model_name": "custom_model_placeholder", "max_tokens": 0, "prompt_max_tokens": 184000}]
			},
			{
				"config_name": "custom_claude-3-7-sonnet",
				"config_switch": true,
				"usage": "chat_completion",
				"display_config": {"display_name": "Claude 3.7 Sonnet"},
				"model_detail_list": [{"model_name": "custom_claude-3-7-sonnet__dev", "max_tokens": 8192, "prompt_max_tokens": 100000}]
			},
			{
				"config_name": "doubao-for-auto",
				"config_switch": true,
				"usage": "chat_completion",
				"is_invisible_to_user": true,
				"display_config": {"display_name": "Doubao Auto"},
				"model_detail_list": [{"model_name": "doubao-for-auto__dev", "max_tokens": 16000, "prompt_max_tokens": 100000}]
			},
			{
				"config_name": "disabled_model",
				"config_switch": false,
				"usage": "chat_completion",
				"display_config": {"display_name": "Disabled"},
				"model_detail_list": [{"model_name": "disabled__dev", "max_tokens": 16000, "prompt_max_tokens": 100000}]
			},
			{
				"config_name": "glm-4.7-auto",
				"config_switch": true,
				"usage": "chat_completion",
				"display_config": {"display_name": "GLM 4.7 Auto"},
				"model_detail_list": [{"model_name": "glm-4.7-auto__dev", "max_tokens": 16000, "prompt_max_tokens": 100000}]
			}
		]
	}`)

	models := parseTraeDetailParam(data, 123)

	if len(models) != 2 {
		var ids []string
		for _, m := range models {
			ids = append(ids, m.ID)
		}
		t.Fatalf("len(models) = %d, want 2; got IDs: %v", len(models), ids)
	}

	if got := models[0].ID; got != "glm-5.1" {
		t.Fatalf("first model ID = %q, want glm-5.1", got)
	}
	if got := models[0].ContextLength; got != 100000 {
		t.Fatalf("glm-5.1 context = %d, want 100000", got)
	}
	if got := models[0].MaxCompletionTokens; got != 16000 {
		t.Fatalf("glm-5.1 max_tokens = %d, want 16000", got)
	}
	if models[0].SupportedInputModalities != nil {
		t.Fatalf("glm-5.1 should not be multimodal")
	}

	if got := models[1].ID; got != "kimi-k2.6" {
		t.Fatalf("second model ID = %q, want kimi-k2.6", got)
	}
	if len(models[1].SupportedInputModalities) != 2 {
		t.Fatalf("kimi-k2.6 should be multimodal, got modalities: %v", models[1].SupportedInputModalities)
	}
}

func TestTraeFetchModelsFallsBackWhenDetailParamHasNoUsableConfigs(t *testing.T) {
	tests := []struct {
		name       string
		detailBody string
	}{
		{
			name:       "empty config list",
			detailBody: `{"config_info_list":[]}`,
		},
		{
			name: "missing backend model name",
			detailBody: `{"config_info_list":[
				{
					"config_name": "new-dynamic-model",
					"config_switch": true,
					"usage": "chat_completion",
					"display_config": {"display_name": "New Dynamic Model"},
					"model_detail_list": [{"max_tokens": 16000, "prompt_max_tokens": 100000}]
				}
			]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requestedModelList bool
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body := tt.detailBody
				if strings.Contains(req.URL.Path, "model_list") {
					requestedModelList = true
					body = `{"model_configs":[{"name":"fallback_raw","status":true,"display_name":"Fallback Raw","prompt_max_tokens":32000}]}`
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    req,
				}, nil
			}))

			models, err := NewTraeExecutor(nil).FetchModels(ctx, &cliproxyauth.Auth{
				ID:       "trae-fetch-fallback",
				Provider: "trae",
				Attributes: map[string]string{
					"jwt_token": "not-a-real-jwt",
				},
			})
			if err != nil {
				t.Fatalf("FetchModels returned error: %v", err)
			}
			if !requestedModelList {
				t.Fatal("expected FetchModels to fall back to model_list")
			}
			if !hasModelID(models, "fallback_raw") {
				t.Fatalf("expected fallback model_list model, got %v", modelIDs(models))
			}
		})
	}
}

func TestTraeV3CreateTaskUsesDetailModelNameForDynamicModel(t *testing.T) {
	e := NewTraeExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "trae-dynamic-model",
		Provider: "trae",
		Attributes: map[string]string{
			"jwt_token": "not-a-real-jwt",
		},
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"config_info_list":[
			{
				"config_name": "new-dynamic-model",
				"config_switch": true,
				"usage": "chat_completion",
				"display_config": {"display_name": "New Dynamic Model"},
				"model_detail_list": [{"model_name": "new-dynamic-model__backend", "max_tokens": 16000, "prompt_max_tokens": 100000}]
			}
		]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))
	models, err := e.FetchModels(ctx, auth)
	if err != nil {
		t.Fatalf("FetchModels returned error: %v", err)
	}
	if !hasModelID(models, "new-dynamic-model") {
		t.Fatalf("expected dynamic model to be fetched, got %v", modelIDs(models))
	}
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		t.Fatalf("CredentialsFromAuth returned error: %v", err)
	}

	req, err := e.buildTraeV3CreateTaskRequest(auth, creds, "new-dynamic-model", []gjson.Result{
		gjson.Parse(`{"role":"user","content":"hello"}`),
	}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("buildTraeV3CreateTaskRequest returned error: %v", err)
	}

	if got := gjson.GetBytes(req.LogBody, "model_name").String(); got != "new-dynamic-model__backend" {
		t.Fatalf("model_name = %q, want new-dynamic-model__backend", got)
	}
	if got := gjson.GetBytes(req.LogBody, "config_name").String(); got != "new-dynamic-model" {
		t.Fatalf("config_name = %q, want new-dynamic-model", got)
	}
}

func TestTraeV3CreateTaskMetadataOverridesDetailModelConfig(t *testing.T) {
	e := NewTraeExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "trae-dynamic-model-override",
		Provider: "trae",
		Attributes: map[string]string{
			"jwt_token": "not-a-real-jwt",
		},
	}
	e.replaceTraeDetailModelConfigs(auth, map[string]traeDetailModelConfig{
		"new-dynamic-model": {
			ModelName:  "new-dynamic-model__backend",
			ConfigName: "new-dynamic-model",
		},
	})
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		t.Fatalf("CredentialsFromAuth returned error: %v", err)
	}

	req, err := e.buildTraeV3CreateTaskRequest(auth, creds, "new-dynamic-model", []gjson.Result{
		gjson.Parse(`{"role":"user","content":"hello"}`),
	}, cliproxyexecutor.Options{Metadata: map[string]any{
		traeModelNameMeta: "explicit-model",
		traeConfigMeta:    "explicit-config",
	}})
	if err != nil {
		t.Fatalf("buildTraeV3CreateTaskRequest returned error: %v", err)
	}

	if got := gjson.GetBytes(req.LogBody, "model_name").String(); got != "explicit-model" {
		t.Fatalf("model_name = %q, want explicit-model", got)
	}
	if got := gjson.GetBytes(req.LogBody, "config_name").String(); got != "explicit-config" {
		t.Fatalf("config_name = %q, want explicit-config", got)
	}
}

func hasModelID(models []*registry.ModelInfo, id string) bool {
	for _, model := range models {
		if model != nil && model.ID == id {
			return true
		}
	}
	return false
}

func modelIDs(models []*registry.ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model != nil {
			ids = append(ids, model.ID)
		}
	}
	return ids
}

func runTraeStreamWithBody(t *testing.T, body string) *cliproxyexecutor.StreamResult {
	t.Helper()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))

	rawRequest := []byte(`{"model":"glm-5","messages":[{"role":"user","content":"list files"}],"stream":true}`)
	result, err := NewTraeExecutor(nil).ExecuteStream(ctx, &cliproxyauth.Auth{
		Provider: "trae",
		Attributes: map[string]string{
			"jwt_token": "not-a-real-jwt",
		},
	}, cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream returned setup error: %v", err)
	}
	return result
}

func collectOpenAIStreamData(t *testing.T, result *cliproxyexecutor.StreamResult) []string {
	t.Helper()
	var data []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream returned error: %v", chunk.Err)
		}
		payload := strings.TrimSpace(string(chunk.Payload))
		if payload == "[DONE]" || gjson.Valid(payload) {
			data = append(data, payload)
			continue
		}
		for _, line := range strings.Split(payload, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
	}
	return data
}

func TestAppendTraeV1RawChatModels(t *testing.T) {
	models := appendTraeV1RawChatModels(nil, 123)
	if len(models) != 4 {
		t.Fatalf("len(models) = %d, want 4", len(models))
	}
	if got := models[0].ID; got != "seed_m8" {
		t.Fatalf("first model ID = %q, want seed_m8", got)
	}

	// Should not duplicate
	models = appendTraeV1RawChatModels(models, 123)
	if len(models) != 4 {
		t.Fatalf("after dedup len(models) = %d, want 4", len(models))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
