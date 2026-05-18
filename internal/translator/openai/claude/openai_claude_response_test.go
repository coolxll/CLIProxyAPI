package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToClaude_StreamIgnoresNullToolNameDelta(t *testing.T) {
	originalRequest := []byte(`{"stream":true}`)
	var param any

	firstChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"test-model",
		originalRequest,
		nil,
		[]byte(`data: {"id":"chatcmpl_1","model":"test-model","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}]}`),
		&param,
	)
	firstOutput := bytes.Join(firstChunks, nil)
	if !bytes.Contains(firstOutput, []byte(`"name":"read_file"`)) {
		t.Fatalf("expected first chunk to start read_file tool block, got %s", string(firstOutput))
	}

	secondChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"test-model",
		originalRequest,
		nil,
		[]byte(`data: {"id":"chatcmpl_1","model":"test-model","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":null,"arguments":"{\"path\":\"/tmp/a\"}"}}]},"finish_reason":null}]}`),
		&param,
	)
	secondOutput := bytes.Join(secondChunks, nil)
	if bytes.Contains(secondOutput, []byte(`content_block_start`)) {
		t.Fatalf("did not expect null tool name delta to start a new content block, got %s", string(secondOutput))
	}
	if bytes.Contains(secondOutput, []byte(`"name":""`)) {
		t.Fatalf("did not expect null tool name delta to emit an empty tool name, got %s", string(secondOutput))
	}
}

func TestConvertOpenAIResponseToClaudeNonStream_ThinkingBeforeTextWithoutUnknownSignature(t *testing.T) {
	rawResponse := []byte(`{
		"id":"chatcmpl_1",
		"model":"test-model",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":"final answer",
				"reasoning_content":"internal reasoning"
			},
			"finish_reason":"stop"
		}],
		"usage":{"prompt_tokens":10,"completion_tokens":5}
	}`)

	out := ConvertOpenAIResponseToClaudeNonStream(
		context.Background(),
		"test-model",
		[]byte(`{"stream":false}`),
		nil,
		rawResponse,
		nil,
	)

	root := gjson.ParseBytes(out)
	if got := root.Get("content.0.type").String(); got != "thinking" {
		t.Fatalf("content.0.type = %q, want thinking; response=%s", got, string(out))
	}
	if got := root.Get("content.0.thinking").String(); got != "internal reasoning" {
		t.Fatalf("content.0.thinking = %q, want internal reasoning", got)
	}
	if root.Get("content.0.signature").Exists() {
		t.Fatalf("thinking block should not include unknown signature, got %s", root.Get("content.0").Raw)
	}
	if got := root.Get("content.1.type").String(); got != "text" {
		t.Fatalf("content.1.type = %q, want text; response=%s", got, string(out))
	}
	if got := root.Get("content.1.text").String(); got != "final answer" {
		t.Fatalf("content.1.text = %q, want final answer", got)
	}
}

func TestConvertOpenAIResponseToClaude_StreamThinkingStartOmitsUnknownSignature(t *testing.T) {
	var param any
	chunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"test-model",
		[]byte(`{"stream":true}`),
		nil,
		[]byte(`data: {"id":"chatcmpl_1","model":"test-model","created":1,"choices":[{"index":0,"delta":{"reasoning_content":"internal reasoning"},"finish_reason":null}]}`),
		&param,
	)

	for _, chunk := range chunks {
		eventData := sseDataPayload(chunk)
		if eventData == "" {
			continue
		}
		event := gjson.Parse(eventData)
		if event.Get("type").String() != "content_block_start" {
			continue
		}
		if event.Get("content_block.type").String() != "thinking" {
			continue
		}
		if event.Get("content_block.signature").Exists() {
			t.Fatalf("thinking start block should not include unknown signature, got %s", event.Raw)
		}
		return
	}

	t.Fatalf("expected thinking content_block_start event, got %s", string(bytes.Join(chunks, nil)))
}

func sseDataPayload(chunk []byte) string {
	for _, line := range strings.Split(string(chunk), "\n") {
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimPrefix(line, "data: ")
		}
	}
	return ""
}
