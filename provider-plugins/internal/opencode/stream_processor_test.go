package opencode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestStreamProcessorOpenAIEvents(t *testing.T) {
	var emitted [][]byte
	emit := func(payload []byte, usage *pluginapi.UsageDetail) error {
		emitted = append(emitted, append([]byte(nil), payload...))
		return nil
	}

	req := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model: "opencode/kimi-k2.5-free",
		},
	}
	proc := newOpencodeStreamProcessor(context.Background(), hostRPC{}, req, "ses_123", sdktranslator.FormatOpenAI, emit)

	if err := proc.start(); err != nil {
		t.Fatalf("start() failed: %v", err)
	}

	// Initial role chunk
	if len(emitted) != 1 {
		t.Fatalf("expected 1 chunk on start, got %d", len(emitted))
	}
	if !strings.Contains(string(emitted[0]), `"role":"assistant"`) {
		t.Errorf("chunk should have role assistant: %s", string(emitted[0]))
	}

	// 1. Part updated for reasoning part
	reasoningUpdate := OpenCodeEvent{
		Type: "message.part.updated",
		Properties: json.RawMessage(`{
			"part": {
				"id": "part_r1",
				"sessionID": "ses_123",
				"messageID": "msg_1",
				"type": "reasoning"
			},
			"delta": "Thinking step 1... "
		}`),
	}
	done, err := proc.processEvent(reasoningUpdate)
	if err != nil || done {
		t.Fatalf("processEvent reasoning update failed: %v, done=%v", err, done)
	}
	lastChunk := string(emitted[len(emitted)-1])
	if !strings.Contains(lastChunk, `"reasoning_content":"Thinking step 1... "`) {
		t.Errorf("expected reasoning_content delta, got: %s", lastChunk)
	}

	// 2. Part delta for reasoning part
	reasoningDelta := OpenCodeEvent{
		Type: "message.part.delta",
		Properties: json.RawMessage(`{
			"sessionID": "ses_123",
			"partID": "part_r1",
			"delta": "Thinking step 2.",
			"field": "text"
		}`),
	}
	done, err = proc.processEvent(reasoningDelta)
	if err != nil || done {
		t.Fatalf("processEvent reasoning delta failed: %v, done=%v", err, done)
	}

	// 3. Part updated for text part
	textUpdate := OpenCodeEvent{
		Type: "message.part.updated",
		Properties: json.RawMessage(`{
			"part": {
				"id": "part_t1",
				"sessionID": "ses_123",
				"messageID": "msg_1",
				"type": "text"
			},
			"delta": "Hello "
		}`),
	}
	done, err = proc.processEvent(textUpdate)
	if err != nil || done {
		t.Fatalf("processEvent text update failed: %v, done=%v", err, done)
	}
	lastChunk = string(emitted[len(emitted)-1])
	if !strings.Contains(lastChunk, `"content":"Hello "`) {
		t.Errorf("expected content delta, got: %s", lastChunk)
	}

	// 4. Part delta for text part
	textDelta := OpenCodeEvent{
		Type: "message.part.delta",
		Properties: json.RawMessage(`{
			"sessionID": "ses_123",
			"partID": "part_t1",
			"delta": "world!",
			"field": "text"
		}`),
	}
	done, err = proc.processEvent(textDelta)
	if err != nil || done {
		t.Fatalf("processEvent text delta failed: %v, done=%v", err, done)
	}

	// 5. Message updated with finish
	msgUpdate := OpenCodeEvent{
		Type: "message.updated",
		Properties: json.RawMessage(`{
			"info": {
				"id": "msg_1",
				"sessionID": "ses_123",
				"role": "assistant",
				"finish": "stop",
				"tokens": {
					"input": 15,
					"output": 10,
					"reasoning": 6
				}
			}
		}`),
	}
	done, err = proc.processEvent(msgUpdate)
	if err != nil || !done {
		t.Fatalf("processEvent message updated failed: %v, done=%v", err, done)
	}

	// Check terminal chunk and [DONE]
	doneChunk := string(emitted[len(emitted)-1])
	if !strings.Contains(doneChunk, "data: [DONE]") {
		t.Errorf("expected terminal [DONE], got: %s", doneChunk)
	}

	terminalChunk := string(emitted[len(emitted)-2])
	if !strings.Contains(terminalChunk, `"finish_reason":"stop"`) {
		t.Errorf("expected finish_reason stop, got: %s", terminalChunk)
	}
	if !strings.Contains(terminalChunk, `"prompt_tokens":15`) || !strings.Contains(terminalChunk, `"reasoning_tokens":6`) {
		t.Errorf("expected usage details in terminal chunk, got: %s", terminalChunk)
	}
}

func TestStreamProcessorToolCallsExtraction(t *testing.T) {
	var emitted [][]byte
	emit := func(payload []byte, usage *pluginapi.UsageDetail) error {
		emitted = append(emitted, append([]byte(nil), payload...))
		return nil
	}

	req := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model: "opencode/kimi-k2.5-free",
		},
	}
	proc := newOpencodeStreamProcessor(context.Background(), hostRPC{}, req, "ses_tool", sdktranslator.FormatOpenAI, emit)
	_ = proc.start()

	toolEvent := OpenCodeEvent{
		Type: "message.part.updated",
		Properties: json.RawMessage(`{
			"part": {
				"id": "p1",
				"sessionID": "ses_tool",
				"type": "text"
			},
			"delta": "<function_calls>[{\"id\":\"c1\",\"name\":\"get_weather\",\"arguments\":{\"city\":\"Tokyo\"}}]</function_calls>"
		}`),
	}
	_, _ = proc.processEvent(toolEvent)

	finishEvent := OpenCodeEvent{
		Type: "message.updated",
		Properties: json.RawMessage(`{
			"info": {
				"id": "m1",
				"sessionID": "ses_tool",
				"role": "assistant",
				"finish": "tool",
				"tokens": { "input": 5, "output": 10, "reasoning": 0 }
			}
		}`),
	}
	done, err := proc.processEvent(finishEvent)
	if err != nil || !done {
		t.Fatalf("finishEvent failed: %v, done=%v", err, done)
	}

	terminalChunk := string(emitted[len(emitted)-2])
	if !strings.Contains(terminalChunk, `"finish_reason":"tool_calls"`) {
		t.Errorf("expected finish_reason tool_calls, got: %s", terminalChunk)
	}
}
