package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

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

func TestTraeExecutorStreamStripsSplitThinkTagsFromReasoning(t *testing.T) {
	body := "data: {\"reasoning_content\":\"<thi\"}\n\n" +
		"data: {\"reasoning_content\":\"nk>reason</think_never\"}\n\n" +
		"data: {\"reasoning_content\":\"_used_abc> done\"}\n\n"
	result := runTraeStreamWithBody(t, body)

	var reasoning strings.Builder
	for _, data := range collectOpenAIStreamData(t, result) {
		if data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		if val := gjson.Get(data, "choices.0.delta.reasoning_content"); val.Exists() {
			reasoning.WriteString(val.String())
		}
	}
	got := reasoning.String()
	if strings.Contains(got, "<think") || strings.Contains(got, "</think") || strings.Contains(got, "_used_abc") {
		t.Fatalf("reasoning leaked split think tag: %q", got)
	}
	if got != "reason done" {
		t.Fatalf("reasoning = %q, want %q", got, "reason done")
	}
}

func TestTraeExecutorStreamStripsInlineToolCallFromReasoning(t *testing.T) {
	// After a tool commit, the V3 API may return reasoning_content that
	// contains inline tool_calls= markers. These must be stripped from the
	// reasoning output and converted to proper tool_calls.
	body := "event: task_created\n" +
		"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
		"data: {\"reasoning_content\":\"\\ntool_calls=[{\\\"name\\\":\\\"Bash\\\",\\\"arguments\\\":{\\\"command\\\":\\\"ls -la\\\"}}]\"}\n\n"
	result := runTraeStreamWithBody(t, body)

	var reasoning strings.Builder
	var toolCalls []gjson.Result
	for _, data := range collectOpenAIStreamData(t, result) {
		if data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		if val := gjson.Get(data, "choices.0.delta.reasoning_content"); val.Exists() {
			reasoning.WriteString(val.String())
		}
		if tc := gjson.Get(data, "choices.0.delta.tool_calls"); tc.Exists() {
			toolCalls = append(toolCalls, tc.Array()...)
		}
	}

	if strings.Contains(reasoning.String(), "tool_calls=") {
		t.Fatalf("reasoning leaked inline tool call marker: %q", reasoning.String())
	}
	if len(toolCalls) != 1 {
		t.Fatalf("tool call count = %d, want 1: %#v", len(toolCalls), toolCalls)
	}
	tc := toolCalls[0]
	if got := tc.Get("function.name").String(); got != "Bash" {
		t.Fatalf("tool name = %q, want Bash", got)
	}
	if got := tc.Get("function.arguments").String(); got != `{"command":"ls -la"}` {
		t.Fatalf("tool arguments = %q, want ls -la command JSON", got)
	}
}

func TestTraeExecutorStreamStripsInlineToolCallFromThoughtReasoning(t *testing.T) {
	// After a tool commit, the V3 API may return a thought event where
	// reasoning_content contains tool_calls= markers. The thought field
	// goes through thoughtToolParser (looks for <tool_call>), but the
	// reasoning_content field must go through inlineReasoningToolParser.
	body := "event: task_created\n" +
		"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
		"event: thought\n" +
		"data: {\"thought\":\"I need to run a command.\",\"reasoning_content\":\"\\ntool_calls=[{\\\"name\\\":\\\"Bash\\\",\\\"arguments\\\":{\\\"command\\\":\\\"pwd\\\"}}]\"}\n\n"
	result := runTraeStreamWithBody(t, body)

	var reasoning strings.Builder
	var content strings.Builder
	var toolCalls []gjson.Result
	for _, data := range collectOpenAIStreamData(t, result) {
		if data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		if val := gjson.Get(data, "choices.0.delta.reasoning_content"); val.Exists() {
			reasoning.WriteString(val.String())
		}
		if val := gjson.Get(data, "choices.0.delta.content"); val.Exists() {
			content.WriteString(val.String())
		}
		if tc := gjson.Get(data, "choices.0.delta.tool_calls"); tc.Exists() {
			toolCalls = append(toolCalls, tc.Array()...)
		}
	}

	if strings.Contains(reasoning.String(), "tool_calls=") {
		t.Fatalf("reasoning leaked inline tool call marker: %q", reasoning.String())
	}
	if len(toolCalls) != 1 {
		t.Fatalf("tool call count = %d, want 1: %#v", len(toolCalls), toolCalls)
	}
	if got := toolCalls[0].Get("function.name").String(); got != "Bash" {
		t.Fatalf("tool name = %q, want Bash", got)
	}
}

func TestTraeExecutorStreamFiltersFieldNameToolCallArtifact(t *testing.T) {
	// V3 API may send {"tool_name":"tool_name",...} where the value is the
	// literal field name. The executor must not emit this as a tool call.
	body := "event: task_created\n" +
		"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
		"event: tool_call\n" +
		"data: {\"tool_name\":\"tool_name\",\"arguments\":\"{}\",\"toolcall_id\":\"native-99\"}\n\n"
	result := runTraeStreamWithBody(t, body)

	var toolCalls []gjson.Result
	for _, data := range collectOpenAIStreamData(t, result) {
		if data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		if tc := gjson.Get(data, "choices.0.delta.tool_calls"); tc.Exists() {
			toolCalls = append(toolCalls, tc.Array()...)
		}
	}

	if len(toolCalls) != 0 {
		names := make([]string, len(toolCalls))
		for i, tc := range toolCalls {
			names[i] = tc.Get("function.name").String()
		}
		t.Fatalf("tool call count = %d, want 0 (field-name artifact should be filtered); names=%v", len(toolCalls), names)
	}
}

func TestTraeExecutorStreamEmitsFallbackContentForEmptyToolCommit(t *testing.T) {
	body := "data: {\"reasoning_content\":\"checked the tool result\"}\n\n"
	result := runTraeToolCommitStreamWithBody(t, body)

	var content strings.Builder
	var reasoning strings.Builder
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
	}

	if got := content.String(); got != "Tool result received." {
		t.Fatalf("content = %q, want fallback content", got)
	}
	if got := reasoning.String(); got != "checked the tool result" {
		t.Fatalf("reasoning = %q, want upstream reasoning", got)
	}
}

func TestTraeExecutorStreamSkipsFallbackContentWhenToolCommitHasContent(t *testing.T) {
	body := "data: {\"response\":\"actual answer\"}\n\n"
	result := runTraeToolCommitStreamWithBody(t, body)

	var content strings.Builder
	for _, data := range collectOpenAIStreamData(t, result) {
		if data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		if val := gjson.Get(data, "choices.0.delta.content"); val.Exists() {
			content.WriteString(val.String())
		}
	}

	if got := content.String(); got != "actual answer" {
		t.Fatalf("content = %q, want upstream content only", got)
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

func TestTraeExecutorStreamConvertsInlineToolCallForClaude(t *testing.T) {
	body := "event: task_created\n" +
		"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
		"data: {\"content\":\"I will inspect the directory. Bash tool_calls=[{\\\"name\\\":\\\"Bash\\\",\\\"arguments\\\":{\\\"command\\\":\\\"ls -la\\\"}}]\"}\n\n"
	result := runTraeClaudeStreamWithBody(t, body)

	var text strings.Builder
	var toolUseBlocks []gjson.Result
	var toolInput strings.Builder
	stopReason := ""
	for _, event := range collectClaudeStreamEvents(t, result) {
		payload := gjson.Parse(event.data)
		if event.typ == "content_block_delta" && payload.Get("delta.type").String() == "text_delta" {
			text.WriteString(payload.Get("delta.text").String())
		}
		if event.typ == "content_block_delta" && payload.Get("delta.type").String() == "input_json_delta" {
			toolInput.WriteString(payload.Get("delta.partial_json").String())
		}
		if event.typ == "content_block_start" && payload.Get("content_block.type").String() == "tool_use" {
			toolUseBlocks = append(toolUseBlocks, payload.Get("content_block"))
		}
		if event.typ == "message_delta" {
			stopReason = payload.Get("delta.stop_reason").String()
		}
	}

	if got := text.String(); got != "I will inspect the directory. " {
		t.Fatalf("text = %q, want assistant text without inline tool call", got)
	}
	if strings.Contains(text.String(), "tool_calls=") {
		t.Fatalf("text leaked inline tool call: %q", text.String())
	}
	if len(toolUseBlocks) != 1 {
		t.Fatalf("tool use count = %d, want 1: %#v", len(toolUseBlocks), toolUseBlocks)
	}
	toolUse := toolUseBlocks[0]
	if got := toolUse.Get("name").String(); got != "Bash" {
		t.Fatalf("tool name = %q, want Bash", got)
	}
	if got := gjson.Get(toolInput.String(), "command").String(); got != "ls -la" {
		t.Fatalf("tool input command = %q, want ls -la; input_delta=%q block=%s", got, toolInput.String(), toolUse.Raw)
	}
	state, err := decodeTraeToolID(toolUse.Get("id").String())
	if err != nil {
		t.Fatalf("decode inline tool id: %v", err)
	}
	if state.NativeID != "inline-0" || state.Name != "Bash" || state.TaskID != "task-1" || state.AgentRunID != "run-1" {
		t.Fatalf("decoded state = %#v, want inline synthetic state", state)
	}
	if stopReason != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", stopReason)
	}
}

func TestTraeExecutorStreamDropsTrailingToolCallBraceForClaude(t *testing.T) {
	body := "event: task_created\n" +
		"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
		"data: {\"response\":\"tool\"}\n\n" +
		"data: {\"response\":\"_calls=[{\\\"name\\\":\\\"Bash\\\",\\\"arguments\"}\n\n" +
		"data: {\"response\":\"\\\":{\\\"command\\\":\\\"df -h /tmp\\\"}}]}\"}\n\n"
	result := runTraeClaudeStreamWithBody(t, body)

	var text strings.Builder
	var toolUseBlocks []gjson.Result
	var toolInput strings.Builder
	for _, event := range collectClaudeStreamEvents(t, result) {
		payload := gjson.Parse(event.data)
		if event.typ == "content_block_delta" && payload.Get("delta.type").String() == "text_delta" {
			text.WriteString(payload.Get("delta.text").String())
		}
		if event.typ == "content_block_delta" && payload.Get("delta.type").String() == "input_json_delta" {
			toolInput.WriteString(payload.Get("delta.partial_json").String())
		}
		if event.typ == "content_block_start" && payload.Get("content_block.type").String() == "tool_use" {
			toolUseBlocks = append(toolUseBlocks, payload.Get("content_block"))
		}
	}

	if got := strings.TrimSpace(text.String()); got != "" {
		t.Fatalf("text leaked trailing tool call residue = %q, want empty", got)
	}
	if len(toolUseBlocks) != 1 {
		t.Fatalf("tool use count = %d, want 1: %#v", len(toolUseBlocks), toolUseBlocks)
	}
	if got := gjson.Get(toolInput.String(), "command").String(); got != "df -h /tmp" {
		t.Fatalf("tool input command = %q, want df -h /tmp; input_delta=%q", got, toolInput.String())
	}
}

func TestTraeExecutorStreamConvertsInlineToolCallFromReasoningForClaude(t *testing.T) {
	body := "event: task_created\n" +
		"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
		"data: {\"reasoning_content\":\"\\ntool_calls=[{\\\"name\\\":\\\"mcp__weather__get_current_weather\\\",\\\"arguments\\\":{\\\"location\\\":\\\"San Francisco\\\"}}]]]\"}\n\n"
	result := runTraeClaudeStreamWithBody(t, body)

	var toolUseBlocks []gjson.Result
	var toolInput strings.Builder
	stopReason := ""
	for _, event := range collectClaudeStreamEvents(t, result) {
		payload := gjson.Parse(event.data)
		if event.typ == "content_block_delta" && payload.Get("delta.type").String() == "input_json_delta" {
			toolInput.WriteString(payload.Get("delta.partial_json").String())
		}
		if event.typ == "content_block_start" && payload.Get("content_block.type").String() == "tool_use" {
			toolUseBlocks = append(toolUseBlocks, payload.Get("content_block"))
		}
		if event.typ == "message_delta" {
			stopReason = payload.Get("delta.stop_reason").String()
		}
	}

	if len(toolUseBlocks) != 1 {
		t.Fatalf("tool use count = %d, want 1: %#v", len(toolUseBlocks), toolUseBlocks)
	}
	toolUse := toolUseBlocks[0]
	if got := toolUse.Get("name").String(); got != "mcp__weather__get_current_weather" {
		t.Fatalf("tool name = %q, want MCP weather", got)
	}
	if got := gjson.Get(toolInput.String(), "location").String(); got != "San Francisco" {
		t.Fatalf("tool input location = %q, want San Francisco; input_delta=%q", got, toolInput.String())
	}
	if stopReason != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", stopReason)
	}
}

func TestTraeExecutorStreamKeepsInlineToolParserStateSeparateForContentAndReasoning(t *testing.T) {
	body := "event: task_created\n" +
		"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
		"data: {\"content\":\"prefix tool_ca\"}\n\n" +
		"data: {\"reasoning_content\":\"lls=[{\\\"name\\\":\\\"Bash\\\",\\\"arguments\\\":{\\\"command\\\":\\\"pwd\\\"}}]\"}\n\n"
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

	if got := content.String(); got != "prefix tool_ca" {
		t.Fatalf("content = %q, want split content marker fragment preserved", got)
	}
	if got := reasoning.String(); got != `lls=[{"name":"Bash","arguments":{"command":"pwd"}}]` {
		t.Fatalf("reasoning = %q, want reasoning marker fragment preserved", got)
	}
	if len(toolCalls) != 0 {
		t.Fatalf("tool call count = %d, want none across content/reasoning boundary: %#v", len(toolCalls), toolCalls)
	}
	if finishReason != "stop" {
		t.Fatalf("finish_reason = %q, want stop", finishReason)
	}
}

func TestTraeExecutorStreamDeduplicatesThoughtAndInlineToolCalls(t *testing.T) {
	body := "event: task_created\n" +
		"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
		"event: thought\n" +
		"data: {\"thought\":\"<tool_call>Bash command=\\\"ls -la\\\" />\"}\n\n" +
		"data: {\"content\":\"Bash tool_calls=[{\\\"name\\\":\\\"Bash\\\",\\\"arguments\\\":{\\\"command\\\":\\\"ls -la\\\"}}]\"}\n\n"
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
		t.Fatalf("tool call count = %d, want 1 after dedupe: %#v", len(toolCalls), toolCalls)
	}
	tc := toolCalls[0]
	if got := tc.Get("function.name").String(); got != "Bash" {
		t.Fatalf("tool name = %q, want Bash", got)
	}
	if got := tc.Get("function.arguments").String(); got != `{"command":"ls -la"}` {
		t.Fatalf("tool arguments = %q, want command JSON", got)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", finishReason)
	}
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

func runTraeToolCommitStreamWithBody(t *testing.T, body string) *cliproxyexecutor.StreamResult {
	t.Helper()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))

	encodedID, err := encodeTraeToolID(traeToolState{
		SessionID:      "session-1",
		ConversationID: "conversation-1",
		TaskID:         "task-1",
		AgentRunID:     "run-1",
		NativeID:       "native-1",
		Name:           "Read",
	})
	if err != nil {
		t.Fatalf("encode tool id: %v", err)
	}

	rawRequest, err := json.Marshal(map[string]any{
		"model":  "glm-5",
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "read file"},
			{
				"role": "assistant",
				"tool_calls": []map[string]any{{
					"id":   encodedID,
					"type": "function",
					"function": map[string]any{
						"name":      "Read",
						"arguments": `{"path":"/tmp/a.go"}`,
					},
				}},
			},
			{
				"role":         "tool",
				"tool_call_id": encodedID,
				"name":         "Read",
				"content":      "file contents",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

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

func runTraeClaudeStreamWithBody(t *testing.T, body string) *cliproxyexecutor.StreamResult {
	t.Helper()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))

	rawRequest := []byte(`{
		"model":"glm-5",
		"max_tokens":1024,
		"stream":true,
		"tools":[{"name":"Bash","description":"Run a shell command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],
		"messages":[{"role":"user","content":"list the current directory"}]
	}`)
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
		SourceFormat:    sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream returned setup error: %v", err)
	}
	return result
}

type claudeStreamEvent struct {
	typ  string
	data string
}

func collectClaudeStreamEvents(t *testing.T, result *cliproxyexecutor.StreamResult) []claudeStreamEvent {
	t.Helper()
	var events []claudeStreamEvent
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream returned error: %v", chunk.Err)
		}
		var typ string
		for _, line := range strings.Split(string(chunk.Payload), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "event:") {
				typ = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				continue
			}
			if strings.HasPrefix(line, "data:") {
				events = append(events, claudeStreamEvent{
					typ:  typ,
					data: strings.TrimSpace(strings.TrimPrefix(line, "data:")),
				})
			}
		}
	}
	return events
}

func TestTraeExecutorStreamMapsExecuteBashToClaudeBash(t *testing.T) {
	body := "data: {\"response\":\"checking\\n\"}\n\n" +
		"data: {\"response\":\"tool_calls=[{\\\"name\\\":\\\"ExecuteBash\\\",\\\"arguments\\\":{\\\"command\\\":\\\"git status\\\"}}]\"}\n\n"

	result := runTraeClaudeStreamWithBody(t, body)

	var foundBash bool
	for _, event := range collectClaudeStreamEvents(t, result) {
		if event.typ != "content_block_start" || !gjson.Valid(event.data) {
			continue
		}
		block := gjson.Get(event.data, "content_block")
		if block.Get("type").String() != "tool_use" {
			continue
		}
		name := block.Get("name").String()
		if name == "ExecuteBash" {
			t.Fatalf("tool_use name leaked Trae alias ExecuteBash: %s", event.data)
		}
		if name == "Bash" {
			foundBash = true
		}
	}

	if !foundBash {
		t.Fatal("expected Claude stream tool_use name Bash")
	}
}

func TestTraeExecutorStreamNormalizesReadFileNameForClaude(t *testing.T) {
	body := "data: {\"response\":\"tool_calls=[{\\\"name\\\":\\\"Read\\\",\\\"arguments\\\":{\\\"file_name\\\":\\\"README.md\\\"}}]\"}\n\n"

	result := runTraeClaudeStreamWithBody(t, body)

	var foundRead bool
	var toolInput strings.Builder
	for _, event := range collectClaudeStreamEvents(t, result) {
		if !gjson.Valid(event.data) {
			continue
		}
		payload := gjson.Parse(event.data)
		if event.typ == "content_block_start" && payload.Get("content_block.type").String() == "tool_use" {
			if got := payload.Get("content_block.name").String(); got == "Read" {
				foundRead = true
			}
		}
		if event.typ == "content_block_delta" && payload.Get("delta.type").String() == "input_json_delta" {
			toolInput.WriteString(payload.Get("delta.partial_json").String())
		}
	}

	if !foundRead {
		t.Fatal("expected Claude stream tool_use name Read")
	}
	input := toolInput.String()
	if gjson.Get(input, "file_name").Exists() {
		t.Fatalf("Read input leaked file_name alias: %s", input)
	}
	filePath := gjson.Get(input, "file_path").String()
	if filePath == "" {
		t.Fatalf("Read input missing file_path: %s", input)
	}
	if !filepath.IsAbs(filePath) {
		t.Fatalf("Read file_path = %q, want absolute path; input=%s", filePath, input)
	}
	if !strings.HasSuffix(filePath, string(filepath.Separator)+"README.md") {
		t.Fatalf("Read file_path = %q, want README.md path; input=%s", filePath, input)
	}
}

func TestTraeExecutorStreamKeepsClaudeToolInputBeforeUsage(t *testing.T) {
	body := "data: {\"response\":\"tool_calls=[{\\\"name\\\":\\\"Bash\\\",\\\"\"}\n\n" +
		"data: {\"response\":\"arguments\\\":{\\\"command\\\":\\\"ls -la README*\\\"}}]}\"}\n\n" +
		"event: token_usage\n" +
		"data: {\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}\n\n"

	result := runTraeClaudeStreamWithBody(t, body)

	var input strings.Builder
	for _, event := range collectClaudeStreamEvents(t, result) {
		if strings.Contains(event.data, `"object":"chat.completion.chunk"`) {
			t.Fatalf("Claude stream leaked raw OpenAI usage chunk: type=%q data=%s", event.typ, event.data)
		}
		if !gjson.Valid(event.data) {
			continue
		}
		payload := gjson.Parse(event.data)
		if event.typ == "content_block_delta" && payload.Get("delta.type").String() == "input_json_delta" {
			input.WriteString(payload.Get("delta.partial_json").String())
		}
	}

	if got := input.String(); gjson.Get(got, "command").String() != "ls -la README*" {
		t.Fatalf("Claude tool input = %q, want Bash command", got)
	}
}

func TestTraeExecutorStreamExtractsHistoryEvent(t *testing.T) {
	// Simulate history event with final output
	historyData := map[string]interface{}{
		"history_data": map[string]interface{}{
			"messages": `{"raw_messages":[{"role":"assistant","content":[{"type":"text","text":"这是最终输出内容"}]}]}`,
		},
	}
	historyJSON, _ := json.Marshal(historyData)

	sseBody := "event: history\n" +
		"data: " + string(historyJSON) + "\n\n" +
		"event: token_usage\n" +
		"data: {\"prompt_tokens\":100,\"completion_tokens\":50,\"total_tokens\":150}\n\n"

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(sseBody)),
			Request:    req,
		}, nil
	}))

	rawRequest := []byte(`{"model":"qwen-3.6-plus","messages":[{"role":"user","content":"test"}],"stream":true}`)
	result, err := NewTraeExecutor(nil).ExecuteStream(ctx, &cliproxyauth.Auth{
		Provider: "trae",
		Attributes: map[string]string{
			"jwt_token": "not-a-real-jwt",
		},
	}, cliproxyexecutor.Request{
		Model:   "qwen-3.6-plus",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream returned setup error: %v", err)
	}

	var contentChunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream returned error: %v", chunk.Err)
		}
		// Extract content from OpenAI-format chunks
		payload := string(chunk.Payload)
		if strings.Contains(payload, `"delta":{`) && strings.Contains(payload, `"content":`) {
			val := gjson.Get(payload, "choices.0.delta.content")
			if val.Exists() && val.String() != "" {
				contentChunks = append(contentChunks, val.String())
			}
		}
	}

	fullContent := strings.Join(contentChunks, "")
	if !strings.Contains(fullContent, "这是最终输出内容") {
		t.Errorf("expected history content '这是最终输出内容' in stream, got: %q", fullContent)
	}
}

func TestTraeExecutorStreamExtractsHistoryMultipleMessages(t *testing.T) {
	// History events can be full transcript snapshots; only the latest assistant message is current.
	historyData := map[string]interface{}{
		"history_data": map[string]interface{}{
			"messages": `{"raw_messages":[
				{"role":"user","content":[{"type":"text","text":"用户消息"}]},
				{"role":"assistant","content":[{"type":"text","text":"旧回复"}]},
				{"role":"assistant","content":[{"type":"text","text":"最新回复"}]}
			]}`,
		},
	}
	historyJSON, _ := json.Marshal(historyData)

	sseBody := "event: history\n" +
		"data: " + string(historyJSON) + "\n\n"

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(sseBody)),
			Request:    req,
		}, nil
	}))

	rawRequest := []byte(`{"model":"qwen-3.6-plus","messages":[{"role":"user","content":"test"}],"stream":true}`)
	result, err := NewTraeExecutor(nil).ExecuteStream(ctx, &cliproxyauth.Auth{
		Provider: "trae",
		Attributes: map[string]string{
			"jwt_token": "not-a-real-jwt",
		},
	}, cliproxyexecutor.Request{
		Model:   "qwen-3.6-plus",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream returned setup error: %v", err)
	}

	var contentChunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream returned error: %v", chunk.Err)
		}
		payload := string(chunk.Payload)
		if strings.Contains(payload, `"delta":{`) && strings.Contains(payload, `"content":`) {
			val := gjson.Get(payload, "choices.0.delta.content")
			if val.Exists() && val.String() != "" {
				contentChunks = append(contentChunks, val.String())
			}
		}
	}

	fullContent := strings.Join(contentChunks, "")
	if fullContent != "最新回复" {
		t.Errorf("expected latest history content only, got: %q", fullContent)
	}
}

func TestTraeExecutorStreamIgnoresHistoryAfterStreamedContent(t *testing.T) {
	historyData := map[string]interface{}{
		"history_data": map[string]interface{}{
			"messages": `{"raw_messages":[
				{"role":"assistant","content":[{"type":"text","text":"旧回复"}]},
				{"role":"assistant","content":[{"type":"text","text":"snapshot回复"}]}
			]}`,
		},
	}
	historyJSON, _ := json.Marshal(historyData)

	sseBody := "data: {\"response\":\"streamed reply\"}\n\n" +
		"event: history\n" +
		"data: " + string(historyJSON) + "\n\n"

	result := runTraeStreamWithBody(t, sseBody)

	var contentChunks []string
	for _, data := range collectOpenAIStreamData(t, result) {
		if data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		val := gjson.Get(data, "choices.0.delta.content")
		if val.Exists() && val.String() != "" {
			contentChunks = append(contentChunks, val.String())
		}
	}

	fullContent := strings.Join(contentChunks, "")
	if fullContent != "streamed reply" {
		t.Errorf("expected streamed content only, got: %q", fullContent)
	}
}

func TestTraeExecutorStreamHistoryNotEmittedWithToolCalls(t *testing.T) {
	// When the stream has tool calls but no content delta, history content should NOT be emitted.
	// PENDING history + tool_calls finish_reason produces semantically incorrect output.
	historyData := map[string]interface{}{
		"history_data": map[string]interface{}{
			"messages": `{"raw_messages":[
				{"role":"assistant","content":[{"type":"text","text":"stale history text"}]}
			]}`,
		},
	}
	historyJSON, _ := json.Marshal(historyData)

	// Stream: tool_call event, then history event
	sseBody := "data: {\"tool_name\":\"Bash\",\"arguments\":\"{\\\"command\\\":\\\"ls\\\"}\"}\n\n" +
		"event: history\n" +
		"data: " + string(historyJSON) + "\n\n"

	result := runTraeStreamWithBody(t, sseBody)

	var contentChunks []string
	var hasToolCalls bool
	for _, data := range collectOpenAIStreamData(t, result) {
		if data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		val := gjson.Get(data, "choices.0.delta.content")
		if val.Exists() && val.String() != "" {
			contentChunks = append(contentChunks, val.String())
		}
		tcVal := gjson.Get(data, "choices.0.delta.tool_calls")
		if tcVal.Exists() && tcVal.IsArray() && len(tcVal.Array()) > 0 {
			hasToolCalls = true
		}
	}

	if !hasToolCalls {
		t.Error("expected tool calls in stream")
	}
	fullContent := strings.Join(contentChunks, "")
	if fullContent != "" {
		t.Errorf("expected no content when tool calls present, got: %q", fullContent)
	}
}

func TestTraeExecutorStreamReasoningPromotedToContentNoHistory(t *testing.T) {
	// When the stream only has reasoning (no content), reasoning is promoted to content
	// AND stale history text is NOT used as the answer.
	historyData := map[string]interface{}{
		"history_data": map[string]interface{}{
			"messages": `{"raw_messages":[
				{"role":"assistant","content":[{"type":"text","text":"stale answer from history"}]}
			]}`,
		},
	}
	historyJSON, _ := json.Marshal(historyData)

	// Stream: reasoning_content event, then history event
	sseBody := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking...\"}}]}\n\n" +
		"event: history\n" +
		"data: " + string(historyJSON) + "\n\n"

	result := runTraeStreamWithBody(t, sseBody)

	var contentChunks []string
	for _, data := range collectOpenAIStreamData(t, result) {
		if data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		val := gjson.Get(data, "choices.0.delta.content")
		if val.Exists() && val.String() != "" {
			contentChunks = append(contentChunks, val.String())
		}
	}

	fullContent := strings.Join(contentChunks, "")
	// When only reasoning is present (no text block), the reasoning is promoted to content
	// so that clients reading only the content field get the answer.
	if fullContent != "thinking..." {
		t.Errorf("expected reasoning promoted to content, got: %q", fullContent)
	}
	// History content must NOT leak into the output.
	if strings.Contains(fullContent, "stale answer from history") {
		t.Errorf("history content should not be emitted, got: %q", fullContent)
	}
}

func TestTraeExecutorExecute_NonStreaming(t *testing.T) {
	// A: Verify Claude stop_reason "max_tokens" is translated to OpenAI "length", and inputs/outputs are parsed correctly.
	t.Run("Claude stop reason and usage", func(t *testing.T) {
		body := "event: task_created\n" +
			"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
			"data: {\"response\":\"hello from trae\", \"usage\":{\"input_tokens\":10,\"output_tokens\":20}, \"finish_reason\":\"max_tokens\"}\n\n"

		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		}))

		rawRequest := []byte(`{"model":"glm-5","messages":[{"role":"user","content":"hello"}]}`)
		resp, err := NewTraeExecutor(nil).Execute(ctx, &cliproxyauth.Auth{
			Provider: "trae",
			Attributes: map[string]string{
				"jwt_token": "not-a-real-jwt",
			},
		}, cliproxyexecutor.Request{
			Model:   "glm-5",
			Payload: rawRequest,
		}, cliproxyexecutor.Options{
			Stream:          false,
			OriginalRequest: rawRequest,
			SourceFormat:    sdktranslator.FromString("claude"),
		})
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}

		payloadStr := string(resp.Payload)
		if got := gjson.Get(payloadStr, "stop_reason").String(); got != "max_tokens" {
			t.Errorf("stop_reason = %q, want max_tokens; payload=%s", got, payloadStr)
		}
		if got := gjson.Get(payloadStr, "usage.input_tokens").Int(); got != 10 {
			t.Errorf("usage.input_tokens = %d, want 10; payload=%s", got, payloadStr)
		}
		if got := gjson.Get(payloadStr, "usage.output_tokens").Int(); got != 20 {
			t.Errorf("usage.output_tokens = %d, want 20; payload=%s", got, payloadStr)
		}
	})

	// B: Verify OpenAI streaming tool calls deltas are correctly merged by index.
	t.Run("OpenAI delta tool call merging", func(t *testing.T) {
		body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"Bash\"}}]}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"com\"}}]}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"mand\\\":\\\"ls\\\"}\"}}]}}]}\n\n"

		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		}))

		rawRequest := []byte(`{"model":"glm-5","messages":[{"role":"user","content":"run command"}]}`)
		resp, err := NewTraeExecutor(nil).Execute(ctx, &cliproxyauth.Auth{
			Provider: "trae",
			Attributes: map[string]string{
				"jwt_token": "not-a-real-jwt",
			},
		}, cliproxyexecutor.Request{
			Model:   "glm-5",
			Payload: rawRequest,
		}, cliproxyexecutor.Options{
			Stream:          false,
			OriginalRequest: rawRequest,
			SourceFormat:    sdktranslator.FromString("openai"),
		})
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}

		payloadStr := string(resp.Payload)
		tc := gjson.Get(payloadStr, "choices.0.message.tool_calls")
		if !tc.IsArray() || len(tc.Array()) != 1 {
			t.Fatalf("tool_calls = %s, want array of 1 tool call; payload=%s", tc.Raw, payloadStr)
		}
		item := tc.Array()[0]
		if got := item.Get("id").String(); got != "call_1" {
			t.Errorf("tool id = %q, want call_1", got)
		}
		if got := item.Get("function.name").String(); got != "Bash" {
			t.Errorf("tool function name = %q, want Bash", got)
		}
		if got := item.Get("function.arguments").String(); got != `{"command":"ls"}` {
			t.Errorf("tool function arguments = %q, want consolidated arguments JSON", got)
		}
	})

	// C: Verify thinking-only response (no text block) promotes reasoning to content.
	t.Run("thinking-only promoted to content", func(t *testing.T) {
		body := "event: task_created\n" +
			"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
			"event: thought\n" +
			"data: {\"reasoning_content\":\"\\nThe user said hello.\"}\n\n" +
			"data: {\"response\":\"Hello!\", \"usage\":{\"input_tokens\":10,\"output_tokens\":5}}\n\n"

		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		}))

		rawRequest := []byte(`{"model":"glm-4.7","messages":[{"role":"user","content":"hello"}]}`)
		resp, err := NewTraeExecutor(nil).Execute(ctx, &cliproxyauth.Auth{
			Provider: "trae",
			Attributes: map[string]string{
				"jwt_token": "not-a-real-jwt",
			},
		}, cliproxyexecutor.Request{
			Model:   "glm-4.7",
			Payload: rawRequest,
		}, cliproxyexecutor.Options{
			Stream:          false,
			OriginalRequest: rawRequest,
			SourceFormat:    sdktranslator.FromString("openai"),
		})
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}

		payloadStr := string(resp.Payload)
		content := gjson.Get(payloadStr, "choices.0.message.content").String()
		reasoning := gjson.Get(payloadStr, "choices.0.message.reasoning_content").String()
		// "response" event produces content; reasoning stays in reasoning_content.
		if content != "Hello!" {
			t.Errorf("content = %q, want Hello!; payload=%s", content, payloadStr)
		}
		if reasoning == "" {
			t.Errorf("reasoning_content should not be empty; payload=%s", payloadStr)
		}
	})

	// D: Verify thinking-only (no text block at all) promotes reasoning to content.
	t.Run("thinking-only no text block promoted to content", func(t *testing.T) {
		// Only a thought event with reasoning_content, no "response" event with content.
		// The usage event has no content fields, so aggregatedContent stays empty.
		body := "event: task_created\n" +
			"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
			"event: thought\n" +
			"data: {\"reasoning_content\":\"\\nHello!!!\"}\n\n" +
			"event: token_usage\n" +
			"data: {\"prompt_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}\n\n"

		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		}))

		rawRequest := []byte(`{"model":"glm-4.7","messages":[{"role":"user","content":"hello"}]}`)
		resp, err := NewTraeExecutor(nil).Execute(ctx, &cliproxyauth.Auth{
			Provider: "trae",
			Attributes: map[string]string{
				"jwt_token": "not-a-real-jwt",
			},
		}, cliproxyexecutor.Request{
			Model:   "glm-4.7",
			Payload: rawRequest,
		}, cliproxyexecutor.Options{
			Stream:          false,
			OriginalRequest: rawRequest,
			SourceFormat:    sdktranslator.FromString("openai"),
		})
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}

		payloadStr := string(resp.Payload)
		content := gjson.Get(payloadStr, "choices.0.message.content").String()
		reasoning := gjson.Get(payloadStr, "choices.0.message.reasoning_content").String()
		// No text block → reasoning promoted to content.
		if content != "\nHello!!!" {
			t.Errorf("content = %q, want \\nHello!!!; payload=%s", content, payloadStr)
		}
		if reasoning != "" {
			t.Errorf("reasoning_content should be empty after promotion; payload=%s", payloadStr)
		}
	})
}
