package trae

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestTraeStreamProcessorConvertsThoughtToolCallAndUsage(t *testing.T) {
	processor, output, usages := newOpenAITestStreamProcessor(t, false)
	lines := []string{
		"event: task_created",
		`data: {"task_id":"task-1","agent_run_id":"run-1"}`,
		`data: {"reasoning_content":"internal reasoning"}`,
		"event: thought",
		`data: {"thought":"checking <tool_call>LS path"}`,
		"event: thought",
		`data: {"thought":"=\"/tmp\" /> after"}`,
		"event: token_usage",
		`data: {"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"reasoning_tokens":3}`,
		`data: {"finish_reason":"end_turn"}`,
	}
	processTraeTestLines(t, processor, lines)

	var content strings.Builder
	var reasoning strings.Builder
	var toolCalls []gjson.Result
	var finishReason string
	var terminalUsage gjson.Result
	for _, data := range openAIStreamData(output.Bytes()) {
		if data == "[DONE]" || !gjson.Valid(data) {
			continue
		}
		root := gjson.Parse(data)
		content.WriteString(root.Get("choices.0.delta.content").String())
		reasoning.WriteString(root.Get("choices.0.delta.reasoning_content").String())
		toolCalls = append(toolCalls, root.Get("choices.0.delta.tool_calls").Array()...)
		if reason := root.Get("choices.0.finish_reason").String(); reason != "" {
			finishReason = reason
			terminalUsage = root.Get("usage")
		}
	}

	if got := content.String(); got != "checking  after" {
		t.Fatalf("content = %q, want thought text without tool markup", got)
	}
	if got := reasoning.String(); got != "internal reasoning" {
		t.Fatalf("reasoning = %q, want internal reasoning", got)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("tool call count = %d, want 1", len(toolCalls))
	}
	if got := toolCalls[0].Get("function.name").String(); got != "LS" {
		t.Fatalf("tool name = %q, want LS", got)
	}
	state, err := decodeTraeToolID(toolCalls[0].Get("id").String())
	if err != nil {
		t.Fatalf("decode tool ID: %v", err)
	}
	if state.TaskID != "task-1" || state.AgentRunID != "run-1" || state.NativeID != "thought-0" {
		t.Fatalf("tool state = %#v", state)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", finishReason)
	}
	if terminalUsage.Get("total_tokens").Int() != 15 {
		t.Fatalf("terminal usage = %s", terminalUsage.Raw)
	}
	if len(*usages) != 1 || (*usages)[0].InputTokens != 10 || (*usages)[0].ReasoningTokens != 3 {
		t.Fatalf("reported usages = %#v", *usages)
	}
}

func TestTraeStreamProcessorExtractsHistoryAtNormalEOF(t *testing.T) {
	processor, output, _ := newOpenAITestStreamProcessor(t, false)
	processTraeTestLines(t, processor, []string{
		"event: history",
		`data: {"history_data":{"messages":"{\"raw_messages\":[{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"old\"}]},{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"latest\"}]}]}"}}`,
	})

	var content strings.Builder
	var sawDone bool
	for _, data := range openAIStreamData(output.Bytes()) {
		if data == "[DONE]" {
			sawDone = true
			continue
		}
		content.WriteString(gjson.Get(data, "choices.0.delta.content").String())
	}
	if got := content.String(); got != "latest" {
		t.Fatalf("content = %q, want latest history response", got)
	}
	if !sawDone {
		t.Fatal("missing terminal [DONE] marker")
	}
}

func TestTraeStreamProcessorConvertsInlineReasoningToolCall(t *testing.T) {
	processor, output, _ := newOpenAITestStreamProcessor(t, false)
	processTraeTestLines(t, processor, []string{
		`data: {"reasoning_content":"<think>plan</think_never_used> tool_calls=[{\"name\":\"Bash\",\"arguments\":{\"command\":\"pwd\"}}]"}`,
	})

	var reasoning strings.Builder
	var toolCalls []gjson.Result
	for _, data := range openAIStreamData(output.Bytes()) {
		if data == "[DONE]" {
			continue
		}
		reasoning.WriteString(gjson.Get(data, "choices.0.delta.reasoning_content").String())
		toolCalls = append(toolCalls, gjson.Get(data, "choices.0.delta.tool_calls").Array()...)
	}
	if strings.Contains(reasoning.String(), "<think") || strings.Contains(reasoning.String(), "tool_calls=") {
		t.Fatalf("reasoning leaked protocol markup: %q", reasoning.String())
	}
	if len(toolCalls) != 1 || toolCalls[0].Get("function.name").String() != "Bash" {
		t.Fatalf("tool calls = %#v", toolCalls)
	}
}

func TestTraeStreamProcessorReturnsNamedErrorEvent(t *testing.T) {
	processor, _, _ := newOpenAITestStreamProcessor(t, false)
	if err := processor.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := processor.processLine([]byte("event: error")); err != nil {
		t.Fatalf("event line: %v", err)
	}
	err := processor.processLine([]byte(`data: {"code":4001,"message":"failed to get summary config"}`))
	if err == nil || !strings.Contains(err.Error(), "trae error event 4001") {
		t.Fatalf("error = %v", err)
	}
}

func TestTraeStreamProcessorReportsIncrementalUsageAndAccumulatesTerminalUsage(t *testing.T) {
	processor, output, usages := newOpenAITestStreamProcessor(t, false)
	processTraeTestLines(t, processor, []string{
		"event: token_usage",
		`data: {"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}`,
		"event: token_usage",
		`data: {"prompt_tokens":0,"completion_tokens":3,"total_tokens":3}`,
	})
	if len(*usages) != 2 {
		t.Fatalf("usage callback count = %d, want 2", len(*usages))
	}
	if (*usages)[0].TotalTokens != 12 || (*usages)[1].TotalTokens != 3 {
		t.Fatalf("usage callbacks must be incremental: %#v", *usages)
	}
	var terminalUsage gjson.Result
	for _, data := range openAIStreamData(output.Bytes()) {
		if gjson.Get(data, "choices.0.finish_reason").String() != "" {
			terminalUsage = gjson.Get(data, "usage")
		}
	}
	if terminalUsage.Get("prompt_tokens").Int() != 10 ||
		terminalUsage.Get("completion_tokens").Int() != 5 ||
		terminalUsage.Get("total_tokens").Int() != 15 {
		t.Fatalf("terminal usage = %s", terminalUsage.Raw)
	}
}

func TestTraeStreamProcessorOnlyStopsQueueHeartbeatAfterQueueEvent(t *testing.T) {
	processor, _, _ := newOpenAITestStreamProcessor(t, false)
	if err := processor.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := processor.processLine([]byte(`data: {"response":"early content"}`)); err != nil {
		t.Fatalf("process early content: %v", err)
	}
	select {
	case <-processor.queueDone:
		t.Fatal("queue heartbeat was stopped before a queue event")
	default:
	}
	if err := processor.processLine([]byte("event: request_wait_in_queue")); err != nil {
		t.Fatalf("process queue event: %v", err)
	}
	if err := processor.processLine([]byte(`data: {}`)); err != nil {
		t.Fatalf("process queue data: %v", err)
	}
	if err := processor.processLine([]byte(`data: {"response":"ready"}`)); err != nil {
		t.Fatalf("process ready content: %v", err)
	}
	select {
	case <-processor.queueDone:
	default:
		t.Fatal("queue heartbeat was not stopped after response content")
	}
	if err := processor.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

func newOpenAITestStreamProcessor(t *testing.T, toolCommit bool) (*traeStreamProcessor, *bytes.Buffer, *[]pluginapi.UsageDetail) {
	t.Helper()
	var output bytes.Buffer
	var usages []pluginapi.UsageDetail
	req := executorRPCRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		Model:           "test-model",
		SourceFormat:    formatOpenAI,
		OriginalRequest: []byte(`{"model":"test-model","messages":[],"stream":true}`),
	}}
	build := &traeRequestBuildResult{
		SessionID:      "session-1",
		ConversationID: "conversation-1",
		IsToolCommit:   toolCommit,
	}
	processor := newTraeStreamProcessor(
		context.Background(),
		hostRPC{},
		req,
		build,
		req.OriginalRequest,
		sdktranslator.FormatOpenAI,
		false,
		func(payload []byte, usage *pluginapi.UsageDetail) error {
			if len(payload) > 0 {
				output.Write(payload)
				output.WriteByte('\n')
			}
			if usage != nil {
				usages = append(usages, *usage)
			}
			return nil
		},
	)
	return processor, &output, &usages
}

func processTraeTestLines(t *testing.T, processor *traeStreamProcessor, lines []string) {
	t.Helper()
	if err := processor.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	for _, line := range lines {
		if err := processor.processLine([]byte(line)); err != nil {
			t.Fatalf("process %q: %v", line, err)
		}
	}
	if err := processor.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

func openAIStreamData(stream []byte) []string {
	var data []string
	for _, line := range bytes.Split(stream, []byte{'\n'}) {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			data = append(data, strings.TrimSpace(string(bytes.TrimPrefix(trimmed, []byte("data:")))))
		}
	}
	return data
}
