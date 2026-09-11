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

func TestConvertLingmaResponseToClaude_ProviderErrorStream(t *testing.T) {
	raw := []byte(`data: {
  "headers": {"Content-Type": ["application/json"]},
  "body": "{\"code\":\"provider_error\",\"message\":\"Error in upstream response\",\"request_id\":\"1869151502422008\",\"type\":\"provider_error\",\"details\":\"{\\\"error\\\":{\\\"message\\\":\\\"Messages with role 'tool' must be a response to a preceding message with 'tool_calls'\\\",\\\"type\\\":\\\"invalid_request_error\\\",\\\"param\\\":null,\\\"code\\\":\\\"invalid_request_error\\\"}}\"}",
  "statusCodeValue": 400,
  "statusCode": "BAD_REQUEST"
}`)

	var param any
	outputs := ConvertLingmaResponseToClaude(context.Background(), "claude-3-5-sonnet", nil, nil, raw, &param)
	if len(outputs) == 0 {
		t.Fatalf("expected Claude error output, got 0 chunks")
	}

	foundErrorEvent := false
	wantMsg := "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'"
	for _, payload := range outputs {
		for _, block := range bytes.Split(payload, []byte("\n\n")) {
			trimmed := bytes.TrimSpace(block)
			if len(trimmed) == 0 {
				continue
			}
			lines := bytes.Split(trimmed, []byte("\n"))
			for _, line := range lines {
				line = bytes.TrimSpace(line)
				if bytes.HasPrefix(line, []byte("data:")) {
					data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
					errNode := gjson.GetBytes(data, "error")
					if errNode.Exists() {
						foundErrorEvent = true
						if got := errNode.Get("message").String(); got != wantMsg {
							t.Errorf("error.message = %q, want %q", got, wantMsg)
						}
						if got := errNode.Get("type").String(); got != "invalid_request_error" {
							t.Errorf("error.type = %q, want invalid_request_error", got)
						}
					}
				}
			}
		}
	}
	if !foundErrorEvent {
		t.Fatalf("did not find Claude error event in outputs: %q", outputs)
	}
}

func TestConvertLingmaResponseToClaudeNonStream_ProviderError(t *testing.T) {
	raw := []byte(`data: {
  "headers": {"Content-Type": ["application/json"]},
  "body": "{\"code\":\"provider_error\",\"message\":\"Error in upstream response\",\"request_id\":\"1869151502422008\",\"type\":\"provider_error\",\"details\":\"{\\\"error\\\":{\\\"message\\\":\\\"Messages with role 'tool' must be a response to a preceding message with 'tool_calls'\\\",\\\"type\\\":\\\"invalid_request_error\\\",\\\"param\\\":null,\\\"code\\\":\\\"invalid_request_error\\\"}}\"}",
  "statusCodeValue": 400,
  "statusCode": "BAD_REQUEST"
}`)

	out := ConvertLingmaResponseToClaudeNonStream(context.Background(), "claude-3-5-sonnet", nil, nil, raw, nil)
	if gjson.GetBytes(out, "type").String() != "error" {
		t.Fatalf("response type = %q, want error; payload=%s", gjson.GetBytes(out, "type").String(), out)
	}
	wantMsg := "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'"
	if got := gjson.GetBytes(out, "error.message").String(); got != wantMsg {
		t.Fatalf("error.message = %q, want %q", got, wantMsg)
	}
	if got := gjson.GetBytes(out, "error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error", got)
	}
}
