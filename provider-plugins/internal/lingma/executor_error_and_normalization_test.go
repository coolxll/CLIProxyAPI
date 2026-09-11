package lingma

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	lingmaencoding "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/lingma"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

const realProviderErrorSSE = `data: {"body":"{\"code\":\"provider_error\",\"details\":\"{\\\"error\\\":{\\\"code\\\":\\\"invalid_request_error\\\",\\\"message\\\":\\\"Messages with role 'tool' must be a response to a preceding message with 'tool_calls'.\\\",\\\"type\\\":\\\"invalid_request_error\\\"}}\",\"message\":\"Error in upstream response\",\"statusCode\":\"BAD_REQUEST\",\"statusCodeValue\":400,\"type\":\"provider_error\"}"}`

func TestExecutePropagatesProviderErrorDetails(t *testing.T) {
	host := hostDoResponder(t, func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(realProviderErrorSSE + "\n\n"),
		}
	})
	plugin := New(host)
	payload := []byte(`{"messages":[{"role":"user","content":"Ping"}]}`)
	request, _ := json.Marshal(executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "gm51model",
			Format:          formatOpenAI,
			SourceFormat:    formatOpenAI,
			Payload:         payload,
			OriginalRequest: payload,
			StorageJSON:     syntheticCredential,
		},
		HostCallbackID: "callback-err",
	})
	_, errExecute := plugin.Handle(pluginabi.MethodExecutorExecute, request)
	if errExecute == nil {
		t.Fatal("Execute expected error, got nil")
	}
	statusErr, ok := errExecute.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("expected status error, got %T: %v", errExecute, errExecute)
	}
	if statusErr.StatusCode() != http.StatusBadRequest {
		t.Fatalf("StatusCode = %d, want %d", statusErr.StatusCode(), http.StatusBadRequest)
	}
	if !strings.Contains(errExecute.Error(), "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'.") {
		t.Fatalf("unexpected error message: %v", errExecute)
	}
	if strings.Contains(errExecute.Error(), "ended before completion") {
		t.Fatalf("error contains 'ended before completion': %v", errExecute)
	}
}

func TestExecuteStreamPropagatesProviderErrorDetails(t *testing.T) {
	stream := newScriptedStreamHost(t, []hostHTTPStreamReadResponse{
		{Payload: []byte(realProviderErrorSSE + "\n\n"), Done: true},
	})
	plugin := New(stream.call)
	payload := []byte(`{"messages":[{"role":"user","content":"Ping"}],"stream":true}`)
	request, _ := json.Marshal(executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "gm51model",
			Format:          formatOpenAI,
			SourceFormat:    formatOpenAI,
			Stream:          true,
			Payload:         payload,
			OriginalRequest: payload,
			StorageJSON:     syntheticCredential,
		},
		StreamID:       "output-err",
		HostCallbackID: "callback-stream-err",
	})
	raw, errExecute := plugin.Handle(pluginabi.MethodExecutorExecuteStream, request)
	if errExecute != nil {
		t.Fatalf("ExecuteStream unexpected error: %v", errExecute)
	}
	_ = decodeResult[executorStreamResponse](t, raw)
	select {
	case <-stream.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not close")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if !strings.Contains(stream.closeError, "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'.") {
		t.Fatalf("stream close error = %q, want unrolled provider error message", stream.closeError)
	}
	if strings.Contains(stream.closeError, "ended before completion") {
		t.Fatalf("stream close error should not be generic: %q", stream.closeError)
	}
}

func TestExecuteNormalizesAssistantNullContentWithToolCallsOpenAI(t *testing.T) {
	var capturedBody []byte
	host := hostDoResponder(t, func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		decoded, errDecode := lingmaencoding.Decode(string(req.Body))
		if errDecode != nil {
			t.Fatal(errDecode)
		}
		capturedBody = bytes.Clone(decoded)
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       completeLingmaSSE("OK"),
		}
	})
	plugin := New(host)
	payload := []byte(`{
		"messages": [
			{"role": "system", "content": "sys"},
			{"role": "user", "content": "call a tool"},
			{"role": "assistant", "content": null, "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "sunny"}
		]
	}`)
	request, _ := json.Marshal(executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "gm51model",
			Format:          formatOpenAI,
			SourceFormat:    formatOpenAI,
			Payload:         payload,
			OriginalRequest: payload,
			StorageJSON:     syntheticCredential,
		},
		HostCallbackID: "callback-norm",
	})
	if _, errExecute := plugin.Handle(pluginabi.MethodExecutorExecute, request); errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	messages := gjson.GetBytes(capturedBody, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}
	// Assistant message is at index 2
	assistantMsg := messages[2]
	if assistantMsg.Get("role").String() != "assistant" {
		t.Fatalf("expected assistant role, got %s", assistantMsg.Get("role").String())
	}
	contentNode := assistantMsg.Get("content")
	if contentNode.Type != gjson.String || contentNode.String() != "" {
		t.Fatalf("expected assistant content to be \"\", got %v (type %v)", contentNode.Value(), contentNode.Type)
	}
	toolCalls := assistantMsg.Get("tool_calls").Array()
	if len(toolCalls) != 1 || toolCalls[0].Get("id").String() != "call_1" {
		t.Fatalf("expected tool_calls preserved, got %v", assistantMsg.Get("tool_calls").Raw)
	}
	// Tool message is at index 3
	toolMsg := messages[3]
	if toolMsg.Get("role").String() != "tool" || toolMsg.Get("content").String() != "sunny" {
		t.Fatalf("expected tool message preserved, got %v", toolMsg.Raw)
	}
}

func TestExecuteNormalizesAssistantNullContentWithToolCallsClaude(t *testing.T) {
	var capturedBody []byte
	host := hostDoResponder(t, func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		decoded, errDecode := lingmaencoding.Decode(string(req.Body))
		if errDecode != nil {
			t.Fatal(errDecode)
		}
		capturedBody = bytes.Clone(decoded)
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       completeLingmaSSE("OK"),
		}
	})
	plugin := New(host)
	payload := []byte(`{
		"messages": [
			{"role": "user", "content": "call a tool"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": "sunny"}]}
		]
	}`)
	request, _ := json.Marshal(executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "gm51model",
			Format:          formatClaude,
			SourceFormat:    formatClaude,
			Payload:         payload,
			OriginalRequest: payload,
			StorageJSON:     syntheticCredential,
		},
		HostCallbackID: "callback-norm-claude",
	})
	if _, errExecute := plugin.Handle(pluginabi.MethodExecutorExecute, request); errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	messages := gjson.GetBytes(capturedBody, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}
	assistantMsg := messages[1]
	if assistantMsg.Get("role").String() != "assistant" {
		t.Fatalf("expected assistant role, got %s", assistantMsg.Get("role").String())
	}
	contentNode := assistantMsg.Get("content")
	if contentNode.Type != gjson.String || contentNode.String() != "" {
		t.Fatalf("expected assistant content to be \"\", got %v (type %v)", contentNode.Value(), contentNode.Type)
	}
	toolCalls := assistantMsg.Get("tool_calls").Array()
	if len(toolCalls) != 1 || toolCalls[0].Get("id").String() != "toolu_1" {
		t.Fatalf("expected tool_calls preserved, got %v", assistantMsg.Get("tool_calls").Raw)
	}
}
