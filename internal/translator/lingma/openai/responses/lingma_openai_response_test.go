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
