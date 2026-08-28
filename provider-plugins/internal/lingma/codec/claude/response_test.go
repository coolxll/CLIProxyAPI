package claude

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertLingmaResponseToClaudeToolCallDoneStopReason(t *testing.T) {
	var param any
	ctx := context.Background()
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"get_weather"}]}`)
	toolChunk := []byte(`data:{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"NYC\"}"}}]},"index":0}]}`)

	outputs := ConvertLingmaResponseToClaude(ctx, "test", originalRequest, nil, toolChunk, &param)
	outputs = append(outputs, ConvertLingmaResponseToClaude(ctx, "test", originalRequest, nil, []byte(`data:{"body":"[DONE]"}`), &param)...)

	if !hasClaudeEvent(outputs, "content_block_start", "content_block.type", "tool_use") {
		t.Fatalf("missing tool_use content block; outputs=%q", outputs)
	}
	if got, ok := claudeMessageDeltaStopReason(outputs); !ok || got != "tool_use" {
		t.Fatalf("message_delta stop_reason = %q (found=%v), want tool_use; outputs=%q", got, ok, outputs)
	}
}

func claudeMessageDeltaStopReason(outputs [][]byte) (string, bool) {
	for _, payload := range outputs {
		for _, line := range bytes.Split(payload, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if gjson.GetBytes(data, "type").String() != "message_delta" {
				continue
			}
			return gjson.GetBytes(data, "delta.stop_reason").String(), true
		}
	}
	return "", false
}

func hasClaudeEvent(outputs [][]byte, eventType, path, want string) bool {
	for _, payload := range outputs {
		for _, line := range bytes.Split(payload, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if gjson.GetBytes(data, "type").String() != eventType {
				continue
			}
			if gjson.GetBytes(data, path).String() == want {
				return true
			}
		}
	}
	return false
}
