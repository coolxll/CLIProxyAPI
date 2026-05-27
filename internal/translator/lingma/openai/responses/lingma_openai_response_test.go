package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertLingmaResponseToOpenAITrimsSSEPrefix(t *testing.T) {
	raw := []byte(`data:{"headers":{"Content-Type":["application/json"]},"body":"{\"choices\":[{\"delta\":{\"content\":\"Pong\"}}]}","statusCodeValue":200,"statusCode":"OK"}`)

	chunks := ConvertLingmaResponseToOpenAI(context.Background(), "dashscope_qmodel", nil, nil, raw, nil)
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	if got := gjson.GetBytes(chunks[0], "choices.0.delta.content").String(); got != "Pong" {
		t.Fatalf("content = %q, want Pong; chunk=%s", got, chunks[0])
	}
}

func TestConvertLingmaResponseToOpenAIConvertsMetadataOnlyStream(t *testing.T) {
	var param any
	start := []byte(`data:{"headers":{"Content-Type":["application/json"]},"body":"{\"id\":\"chatcmpl-test\",\"model\":\"auto\"}","statusCodeValue":200,"statusCode":"OK"}`)
	startChunks := ConvertLingmaResponseToOpenAI(context.Background(), "dashscope_qmodel", nil, nil, start, &param)
	if len(startChunks) != 1 {
		t.Fatalf("len(startChunks) = %d, want 1", len(startChunks))
	}
	if got := gjson.GetBytes(startChunks[0], "choices.0.delta.role").String(); got != "assistant" {
		t.Fatalf("start role = %q, want assistant; chunk=%s", got, startChunks[0])
	}

	finish := []byte(`data:{"firstTokenDuration":1,"totalDuration":2,"serverDuration":3}`)
	finishChunks := ConvertLingmaResponseToOpenAI(context.Background(), "dashscope_qmodel", nil, nil, finish, &param)
	if len(finishChunks) != 1 {
		t.Fatalf("len(finishChunks) = %d, want 1", len(finishChunks))
	}
	if got := gjson.GetBytes(finishChunks[0], "id").String(); got != "chatcmpl-test" {
		t.Fatalf("finish id = %q, want chatcmpl-test; chunk=%s", got, finishChunks[0])
	}
	if got := gjson.GetBytes(finishChunks[0], "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q, want stop; chunk=%s", got, finishChunks[0])
	}
}

func TestConvertLingmaResponseToOpenAISkipsFinishEventAfterFinishReason(t *testing.T) {
	var param any
	final := []byte(`data:{"choices":[{"delta":{"content":""},"finish_reason":"stop","index":0}]}`)
	finalChunks := ConvertLingmaResponseToOpenAI(context.Background(), "dashscope_qmodel", nil, nil, final, &param)
	if len(finalChunks) != 1 {
		t.Fatalf("len(finalChunks) = %d, want 1", len(finalChunks))
	}

	finish := []byte(`data:{"firstTokenDuration":1,"totalDuration":2,"serverDuration":3}`)
	finishChunks := ConvertLingmaResponseToOpenAI(context.Background(), "dashscope_qmodel", nil, nil, finish, &param)
	if len(finishChunks) != 0 {
		t.Fatalf("len(finishChunks) = %d, want 0; chunk=%s", len(finishChunks), finishChunks)
	}
}

func TestConvertLingmaResponseToOpenAINonStreamAggregatesSSE(t *testing.T) {
	raw := []byte(`data:{"headers":{"Content-Type":["application/json"]},"body":"{\"id\":\"chatcmpl-test\",\"model\":\"auto\"}","statusCodeValue":200,"statusCode":"OK"}
data:{"headers":{"Content-Type":["application/json"]},"body":"{\"choices\":[{\"delta\":{\"content\":\"Po\"}}]}","statusCodeValue":200,"statusCode":"OK"}
data:{"headers":{"Content-Type":["application/json"]},"body":"{\"choices\":[{\"delta\":{\"content\":\"ng\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}","statusCodeValue":200,"statusCode":"OK"}
event:finish
data:{"firstTokenDuration":1,"totalDuration":2,"serverDuration":3}`)

	out := ConvertLingmaResponseToOpenAINonStream(context.Background(), "dashscope_qmodel", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "id").String(); got != "chatcmpl-test" {
		t.Fatalf("id = %q, want chatcmpl-test; out=%s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "Pong" {
		t.Fatalf("content = %q, want Pong; out=%s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q, want stop; out=%s", got, out)
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 2 {
		t.Fatalf("usage.total_tokens = %d, want 2; out=%s", got, out)
	}
}

func TestConvertLingmaResponseToOpenAIPassesToolCallsInStream(t *testing.T) {
	var param any
	raw := []byte(`data:{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"NYC\"}"}}]},"index":0}]}`)
	chunks := ConvertLingmaResponseToOpenAI(context.Background(), "test", nil, nil, raw, &param)
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	tcs := gjson.GetBytes(chunks[0], "choices.0.delta.tool_calls")
	if !tcs.Exists() || !tcs.IsArray() {
		t.Fatalf("tool_calls not found in chunk: %s", chunks[0])
	}
	if len(tcs.Array()) != 1 {
		t.Fatalf("len(tool_calls) = %d, want 1", len(tcs.Array()))
	}
	if got := tcs.Array()[0].Get("id").String(); got != "call_123" {
		t.Fatalf("tool_calls.0.id = %q, want call_123", got)
	}
	if got := tcs.Array()[0].Get("function.name").String(); got != "get_weather" {
		t.Fatalf("tool_calls.0.function.name = %q, want get_weather", got)
	}
}

func TestConvertLingmaResponseToOpenAIFinishReasonToolCalls(t *testing.T) {
	var param any
	// First chunk with tool_calls
	toolChunk := []byte(`data:{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"index":0}]}`)
	ConvertLingmaResponseToOpenAI(context.Background(), "test", nil, nil, toolChunk, &param)

	// Timing event that triggers synthetic finish
	finish := []byte(`data:{"firstTokenDuration":1,"totalDuration":2,"serverDuration":3}`)
	chunks := ConvertLingmaResponseToOpenAI(context.Background(), "test", nil, nil, finish, &param)
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	if got := gjson.GetBytes(chunks[0], "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls; chunk=%s", got, chunks[0])
	}
}

func TestConvertLingmaResponseToOpenAISynthesizesToolFinishOnDone(t *testing.T) {
	var param any
	toolChunk := []byte(`data:{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"index":0}]}`)
	ConvertLingmaResponseToOpenAI(context.Background(), "test", nil, nil, toolChunk, &param)

	chunks := ConvertLingmaResponseToOpenAI(context.Background(), "test", nil, nil, []byte(`data:{"body":"[DONE]"}`), &param)
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	if got := gjson.GetBytes(chunks[0], "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls; chunk=%s", got, chunks[0])
	}
}

func TestConvertLingmaResponseToOpenAINonStreamWithToolCalls(t *testing.T) {
	raw := []byte(`data:{"headers":{"Content-Type":["application/json"]},"body":"{\"choices\":[{\"delta\":{\"content\":\"\"},\"index\":0}]}","statusCodeValue":200,"statusCode":"OK"}
data:{"headers":{"Content-Type":["application/json"]},"body":"{\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_abc\",\"type\":\"function\",\"function\":{\"name\":\"search\",\"arguments\":\"{\\\"q\\\":\\\"test\\\"}\"}}]},\"finish_reason\":\"tool_calls\",\"index\":0}]}","statusCodeValue":200,"statusCode":"OK"}`)

	out := ConvertLingmaResponseToOpenAINonStream(context.Background(), "test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls; out=%s", got, out)
	}
	tcs := gjson.GetBytes(out, "choices.0.message.tool_calls")
	if !tcs.Exists() || !tcs.IsArray() {
		t.Fatalf("message.tool_calls not found: %s", out)
	}
	if len(tcs.Array()) != 1 {
		t.Fatalf("len(tool_calls) = %d, want 1", len(tcs.Array()))
	}
	if got := tcs.Array()[0].Get("function.name").String(); got != "search" {
		t.Fatalf("tool_calls.0.function.name = %q, want search", got)
	}
}

func TestConvertLingmaResponseToOpenAIExtractsUsageWithChoices(t *testing.T) {
	var param any
	// Lingma sends usage inside the body alongside choices (double-JSON envelope)
	raw := []byte(`data:{"headers":{"Content-Type":["application/json"]},"body":"{\"choices\":[{\"delta\":{\"content\":\"Pong\"},\"finish_reason\":\"stop\"}],\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}","statusCodeValue":200,"statusCode":"OK"}`)
	chunks := ConvertLingmaResponseToOpenAI(context.Background(), "test", nil, nil, raw, &param)

	// Should produce 2 chunks: one for content, one for usage
	if len(chunks) < 2 {
		t.Fatalf("len(chunks) = %d, want at least 2; chunks=%v", len(chunks), chunks)
	}

	// Find the usage chunk (the one with empty choices and usage field)
	var usageChunk []byte
	for _, c := range chunks {
		if gjson.GetBytes(c, "usage").Exists() && len(gjson.GetBytes(c, "choices").Array()) == 0 {
			usageChunk = c
			break
		}
	}
	if usageChunk == nil {
		t.Fatalf("no usage chunk found among %d chunks", len(chunks))
	}
	if got := gjson.GetBytes(usageChunk, "usage.prompt_tokens").Int(); got != 10 {
		t.Fatalf("usage.prompt_tokens = %d, want 10; chunk=%s", got, usageChunk)
	}
	if got := gjson.GetBytes(usageChunk, "usage.completion_tokens").Int(); got != 20 {
		t.Fatalf("usage.completion_tokens = %d, want 20; chunk=%s", got, usageChunk)
	}
	if got := gjson.GetBytes(usageChunk, "usage.total_tokens").Int(); got != 30 {
		t.Fatalf("usage.total_tokens = %d, want 30; chunk=%s", got, usageChunk)
	}
}

func TestConvertLingmaResponseToOpenAIExtractsUsageWithChoicesDirectFormat(t *testing.T) {
	var param any
	// Direct OpenAI format with usage alongside choices (no envelope)
	raw := []byte(`data:{"choices":[{"delta":{"content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":15,"total_tokens":20}}`)
	chunks := ConvertLingmaResponseToOpenAI(context.Background(), "test", nil, nil, raw, &param)

	if len(chunks) < 2 {
		t.Fatalf("len(chunks) = %d, want at least 2; chunks=%v", len(chunks), chunks)
	}

	var usageChunk []byte
	for _, c := range chunks {
		if gjson.GetBytes(c, "usage").Exists() && len(gjson.GetBytes(c, "choices").Array()) == 0 {
			usageChunk = c
			break
		}
	}
	if usageChunk == nil {
		t.Fatalf("no usage chunk found among %d chunks", len(chunks))
	}
	if got := gjson.GetBytes(usageChunk, "usage.prompt_tokens").Int(); got != 5 {
		t.Fatalf("usage.prompt_tokens = %d, want 5; chunk=%s", got, usageChunk)
	}
	if got := gjson.GetBytes(usageChunk, "usage.completion_tokens").Int(); got != 15 {
		t.Fatalf("usage.completion_tokens = %d, want 15; chunk=%s", got, usageChunk)
	}
}

func TestConvertLingmaResponseToOpenAISkipsNullUsageWithChoices(t *testing.T) {
	var param any
	// OpenAI-style streaming sends "usage":null on intermediate frames before the final usage
	raw := []byte(`data:{"choices":[{"delta":{"content":"Hi"},"finish_reason":null}],"usage":null}`)
	chunks := ConvertLingmaResponseToOpenAI(context.Background(), "test", nil, nil, raw, &param)

	// Should produce only content chunks, no usage chunk
	for _, c := range chunks {
		if gjson.GetBytes(c, "usage").Exists() && len(gjson.GetBytes(c, "choices").Array()) == 0 {
			t.Fatalf("unexpected usage chunk from null usage: %s", c)
		}
	}
}

func TestConvertLingmaResponseToOpenAISkipsZeroTokenUsageWithChoices(t *testing.T) {
	var param any
	// Empty usage object with all-zero fields should not emit a usage chunk
	raw := []byte(`data:{"choices":[{"delta":{"content":"Hi"}}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
	chunks := ConvertLingmaResponseToOpenAI(context.Background(), "test", nil, nil, raw, &param)

	for _, c := range chunks {
		if gjson.GetBytes(c, "usage").Exists() && len(gjson.GetBytes(c, "choices").Array()) == 0 {
			t.Fatalf("unexpected usage chunk from zero-token usage: %s", c)
		}
	}
}

func TestConvertLingmaResponseToOpenAINonStreamConsolidatesLingmaUsage(t *testing.T) {
	raw := []byte(`data: {"headers":{},"body":"{\"choices\":[{\"delta\":{\"content\":\"Pong\"},\"finish_reason\":\"stop\"}]}","statusCodeValue":200,"statusCode":"OK"}
data: {"firstTokenDuration":100,"totalDuration":200,"serverDuration":150,"usage":{"input_tokens":10,"output_tokens":20}}`)

	out := ConvertLingmaResponseToOpenAINonStream(context.Background(), "dashscope_qmodel", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "Pong" {
		t.Fatalf("content = %q, want Pong; out=%s", got, out)
	}
	if got := gjson.GetBytes(out, "usage.prompt_tokens").Int(); got != 10 {
		t.Fatalf("usage.prompt_tokens = %d, want 10; out=%s", got, out)
	}
	if got := gjson.GetBytes(out, "usage.completion_tokens").Int(); got != 20 {
		t.Fatalf("usage.completion_tokens = %d, want 20; out=%s", got, out)
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 30 {
		t.Fatalf("usage.total_tokens = %d, want 30; out=%s", got, out)
	}
}
func TestConvertLingmaResponseToOpenAIExtractsThought(t *testing.T) {
	t.Run("non-stream", func(t *testing.T) {
		raw := []byte(`data:{"body":"{\"choices\":[{\"delta\":{\"content\":\"<thought>I should say hello.</thought>Hello!\"}}]}"}`)
		out := ConvertLingmaResponseToOpenAINonStream(context.Background(), "test", nil, nil, raw, nil)

		if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "Hello!" {
			t.Fatalf("content = %q, want Hello!; out=%s", got, out)
		}
		if got := gjson.GetBytes(out, "choices.0.message.reasoning_content").String(); got != "I should say hello." {
			t.Fatalf("reasoning_content = %q, want I should say hello.; out=%s", got, out)
		}
	})

	t.Run("stream", func(t *testing.T) {
		var param any
		raw := []byte(`data:{"choices":[{"delta":{"content":"<thought>thinking</thought>content"}}]}`)
		chunks := ConvertLingmaResponseToOpenAI(context.Background(), "test", nil, nil, raw, &param)

		if len(chunks) != 2 {
			t.Fatalf("len(chunks) = %d, want 2", len(chunks))
		}

		if got := gjson.GetBytes(chunks[0], "choices.0.delta.reasoning_content").String(); got != "thinking" {
			t.Fatalf("chunk[0] reasoning_content = %q, want thinking; chunk=%s", got, chunks[0])
		}
		if got := gjson.GetBytes(chunks[1], "choices.0.delta.content").String(); got != "content" {
			t.Fatalf("chunk[1] content = %q, want content; chunk=%s", got, chunks[1])
		}
	})

	t.Run("stream-split", func(t *testing.T) {
		var param any
		chunk1 := []byte(`data:{"choices":[{"delta":{"content":"<thought>part1"}}]}`)
		chunk2 := []byte(`data:{"choices":[{"delta":{"content":" part2</thought>final"}}]}`)

		res1 := ConvertLingmaResponseToOpenAI(context.Background(), "test", nil, nil, chunk1, &param)
		if len(res1) != 1 {
			t.Fatalf("len(res1) = %d, want 1", len(res1))
		}
		if got := gjson.GetBytes(res1[0], "choices.0.delta.reasoning_content").String(); got != "part1" {
			t.Fatalf("res1 reasoning_content = %q, want part1", got)
		}

		res2 := ConvertLingmaResponseToOpenAI(context.Background(), "test", nil, nil, chunk2, &param)
		if len(res2) != 2 {
			t.Fatalf("len(res2) = %d, want 2", len(res2))
		}
		if got := gjson.GetBytes(res2[0], "choices.0.delta.reasoning_content").String(); got != " part2" {
			t.Fatalf("res2[0] reasoning_content = %q, want  part2", got)
		}
		if got := gjson.GetBytes(res2[1], "choices.0.delta.content").String(); got != "final" {
			t.Fatalf("res2[1] content = %q, want final", got)
		}
	})

	t.Run("stream-envelope", func(t *testing.T) {
		var param any
		raw := []byte(`data:{"headers":{},"body":"{\"choices\":[{\"delta\":{\"content\":\"<thought>thinking</thought>content\"}}]}"}`)
		chunks := ConvertLingmaResponseToOpenAI(context.Background(), "test", nil, nil, raw, &param)

		if len(chunks) != 2 {
			t.Fatalf("len(chunks) = %d, want 2", len(chunks))
		}

		if got := gjson.GetBytes(chunks[0], "choices.0.delta.reasoning_content").String(); got != "thinking" {
			t.Fatalf("chunk[0] reasoning_content = %q, want thinking; chunk=%s", got, chunks[0])
		}
		if got := gjson.GetBytes(chunks[1], "choices.0.delta.content").String(); got != "content" {
			t.Fatalf("chunk[1] content = %q, want content; chunk=%s", got, chunks[1])
		}
	})
}
