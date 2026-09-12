package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type mockHostManager struct {
	mu           sync.Mutex
	streamBodies map[string]io.ReadCloser
	emitted      [][]byte
	streamClosed bool
}

func newMockHostManager() *mockHostManager {
	return &mockHostManager{
		streamBodies: make(map[string]io.ReadCloser),
	}
}

func (m *mockHostManager) makeHostCall() HostCall {
	return func(method string, request []byte) ([]byte, error) {
		m.mu.Lock()
		defer m.mu.Unlock()

		switch method {
		case pluginabi.MethodHostHTTPDo:
			var req hostHTTPRequest
			_ = json.Unmarshal(request, &req)
			httpReq, err := http.NewRequest(req.Request.Method, req.Request.URL, strings.NewReader(string(req.Request.Body)))
			if err != nil {
				return nil, err
			}
			for k, v := range req.Request.Headers {
				httpReq.Header[k] = v
			}
			resp, err := http.DefaultClient.Do(httpReq)
			if err != nil {
				return nil, err
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			pluginResp := pluginapi.HTTPResponse{
				StatusCode: resp.StatusCode,
				Headers:    resp.Header,
				Body:       body,
			}
			resBytes, _ := json.Marshal(pluginResp)
			return json.Marshal(pluginruntime.Envelope{OK: true, Result: resBytes})

		case pluginabi.MethodHostHTTPDoStream:
			var req hostHTTPRequest
			_ = json.Unmarshal(request, &req)
			httpReq, err := http.NewRequest(req.Request.Method, req.Request.URL, bytes.NewReader(req.Request.Body))
			if err != nil {
				return nil, err
			}
			for k, v := range req.Request.Headers {
				httpReq.Header[k] = v
			}
			resp, err := http.DefaultClient.Do(httpReq)
			if err != nil {
				return nil, err
			}
			streamID := fmt.Sprintf("stream_%d", len(m.streamBodies)+1)
			m.streamBodies[streamID] = resp.Body
			streamResp := hostHTTPStreamResponse{
				StatusCode: resp.StatusCode,
				Headers:    resp.Header,
				StreamID:   streamID,
			}
			resBytes, _ := json.Marshal(streamResp)
			return json.Marshal(pluginruntime.Envelope{OK: true, Result: resBytes})

		case pluginabi.MethodHostHTTPStreamRead:
			var req hostHTTPStreamReadRequest
			_ = json.Unmarshal(request, &req)
			body, exists := m.streamBodies[req.StreamID]
			if !exists {
				return nil, fmt.Errorf("stream not found: %s", req.StreamID)
			}
			buf := make([]byte, 1024)
			n, err := body.Read(buf)
			readResp := hostHTTPStreamReadResponse{
				Payload: buf[:n],
				Done:    err == io.EOF,
			}
			if err != nil && err != io.EOF {
				readResp.Error = err.Error()
			}
			resBytes, _ := json.Marshal(readResp)
			return json.Marshal(pluginruntime.Envelope{OK: true, Result: resBytes})

		case pluginabi.MethodHostHTTPStreamClose:
			var req hostHTTPStreamCloseRequest
			_ = json.Unmarshal(request, &req)
			if body, exists := m.streamBodies[req.StreamID]; exists {
				body.Close()
				delete(m.streamBodies, req.StreamID)
			}
			return json.Marshal(pluginruntime.Envelope{OK: true})

		case pluginabi.MethodHostStreamEmit:
			var req hostStreamEmitRequest
			_ = json.Unmarshal(request, &req)
			m.emitted = append(m.emitted, append([]byte(nil), req.Payload...))
			return json.Marshal(pluginruntime.Envelope{OK: true})

		case pluginabi.MethodHostStreamClose:
			m.streamClosed = true
			return json.Marshal(pluginruntime.Envelope{OK: true})

		case pluginabi.MethodHostLog:
			return json.Marshal(pluginruntime.Envelope{OK: true})

		default:
			return nil, fmt.Errorf("unsupported mock host call: %s", method)
		}
	}
}

func setupMockOpenCodeServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/config/providers":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"providers": [
					{
						"id": "opencode",
						"name": "OpenCode",
						"models": {
							"kimi-k2.5-free": { "name": "Kimi K2.5 (Free)" }
						}
					}
				]
			}`))

		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"ses_mock_123"}`))

		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/session/") && strings.HasSuffix(r.URL.Path, "/message"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))

		case r.Method == http.MethodGet && r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatalf("expected flusher")
			}

			// Send reasoning event
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"message.part.updated\",\"properties\":{\"part\":{\"id\":\"p1\",\"sessionID\":\"ses_mock_123\",\"type\":\"reasoning\"},\"delta\":\"Let me calculate... \"}}\n\n")
			flusher.Flush()

			// Send content event
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"message.part.updated\",\"properties\":{\"part\":{\"id\":\"p2\",\"sessionID\":\"ses_mock_123\",\"type\":\"text\"},\"delta\":\"The answer is \"}}\n\n")
			flusher.Flush()

			// Send content delta
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"message.part.delta\",\"properties\":{\"sessionID\":\"ses_mock_123\",\"partID\":\"p2\",\"delta\":\"42.\",\"field\":\"text\"}}\n\n")
			flusher.Flush()

			// Send finish event
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"message.updated\",\"properties\":{\"info\":{\"id\":\"m1\",\"sessionID\":\"ses_mock_123\",\"role\":\"assistant\",\"finish\":\"stop\",\"tokens\":{\"input\":8,\"output\":6,\"reasoning\":4}}}}\n\n")
			flusher.Flush()

			_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/session/"):
			w.WriteHeader(http.StatusOK)

		default:
			t.Logf("unexpected request to mock server: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestExecuteNonStreaming(t *testing.T) {
	server := setupMockOpenCodeServer(t)
	defer server.Close()

	mgr := newMockHostManager()
	plugin := New(mgr.makeHostCall())

	storageJSON := fmt.Sprintf(`{"type":"opencode-plugin","server_url":"%s"}`, server.URL)
	req := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "opencode/kimi-k2.5-free",
			Format:      "openai",
			StorageJSON: []byte(storageJSON),
			Payload:     []byte(`{"messages":[{"role":"user","content":"What is the answer?"}],"stream":false}`),
		},
	}

	rawReq, _ := json.Marshal(req)
	rawResp, err := plugin.Handle(pluginabi.MethodExecutorExecute, rawReq)
	if err != nil {
		t.Fatalf("Handle(executor.execute) error: %v", err)
	}

	var env pluginruntime.Envelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("execution returned not OK: %s", string(rawResp))
	}

	var resp pluginapi.ExecutorResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("unmarshal executor response: %v", err)
	}

	content := gjson.GetBytes(resp.Payload, "choices.0.message.content").String()
	if content != "The answer is 42." {
		t.Errorf("got content %q, want 'The answer is 42.'", content)
	}

	reasoning := gjson.GetBytes(resp.Payload, "choices.0.message.reasoning_content").String()
	if reasoning != "Let me calculate... " {
		t.Errorf("got reasoning %q, want 'Let me calculate... '", reasoning)
	}

	finishReason := gjson.GetBytes(resp.Payload, "choices.0.finish_reason").String()
	if finishReason != "stop" {
		t.Errorf("got finish_reason %q, want 'stop'", finishReason)
	}
}

func TestExecuteStreaming(t *testing.T) {
	server := setupMockOpenCodeServer(t)
	defer server.Close()

	mgr := newMockHostManager()
	plugin := New(mgr.makeHostCall())

	storageJSON := fmt.Sprintf(`{"type":"opencode-plugin","server_url":"%s"}`, server.URL)
	req := executorRPCRequest{
		StreamID: "out_stream_1",
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "opencode/kimi-k2.5-free",
			Format:      "openai",
			StorageJSON: []byte(storageJSON),
			Payload:     []byte(`{"messages":[{"role":"user","content":"What is the answer?"}],"stream":true}`),
		},
	}

	rawReq, _ := json.Marshal(req)
	rawResp, err := plugin.Handle(pluginabi.MethodExecutorExecuteStream, rawReq)
	if err != nil {
		t.Fatalf("Handle(executor.execute_stream) error: %v", err)
	}

	var env pluginruntime.Envelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("stream init returned not OK: %s", string(rawResp))
	}

	// Wait for goroutine to finish streaming
	plugin.streamWG.Wait()

	mgr.mu.Lock()
	emitted := mgr.emitted
	closed := mgr.streamClosed
	mgr.mu.Unlock()

	if !closed {
		t.Errorf("expected stream to be closed")
	}
	if len(emitted) == 0 {
		t.Fatalf("expected emitted chunks")
	}

	var allOutput string
	for _, chunk := range emitted {
		allOutput += string(chunk)
	}

	if !strings.Contains(allOutput, "The answer is ") || !strings.Contains(allOutput, "42.") {
		t.Errorf("missing content in output: %s", allOutput)
	}
	if !strings.Contains(allOutput, "Let me calculate... ") {
		t.Errorf("missing reasoning in output: %s", allOutput)
	}
	if !strings.Contains(allOutput, "data: [DONE]") {
		t.Errorf("missing [DONE] in output: %s", allOutput)
	}
}

func TestCloudZenExecutionNonStreaming(t *testing.T) {
	var capturedAuth string
	var capturedClient string
	var capturedSession string
	var capturedUA string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedClient = r.Header.Get("x-opencode-client")
		capturedSession = r.Header.Get("x-session-affinity")
		capturedUA = r.Header.Get("User-Agent")

		resp := `{
			"id": "chatcmpl-test-zen",
			"object": "chat.completion",
			"created": 1789176820,
			"model": "big-pickle",
			"choices": [
				{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "<function_calls>[{\"name\": \"get_weather\", \"arguments\": {\"location\": \"Tokyo\"}}]</function_calls>"
					},
					"finish_reason": "stop"
				}
			],
			"usage": {
				"prompt_tokens": 50,
				"completion_tokens": 20,
				"total_tokens": 70
			}
		}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer mockServer.Close()

	mgr := newMockHostManager()
	plugin := New(mgr.makeHostCall())

	credsJSON, _ := json.Marshal(credentials{
		Type:      "opencode",
		ServerURL: mockServer.URL,
		Mode:      "cloud",
	})

	originalReq := []byte(`{
		"model": "big-pickle",
		"messages": [{"role": "user", "content": "What is the weather in Tokyo?"}],
		"tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {}}}]
	}`)

	req := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "big-pickle",
			Format:          "openai",
			Payload:         originalReq,
			OriginalRequest: originalReq,
			StorageJSON:     credsJSON,
		},
	}

	rawReq, _ := json.Marshal(req)
	rawResp, err := plugin.Handle(pluginabi.MethodExecutorExecute, rawReq)
	if err != nil {
		t.Fatalf("Handle(executor.execute) error: %v", err)
	}

	var env pluginruntime.Envelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("execute returned not OK: %s", string(rawResp))
	}

	var execResp pluginapi.ExecutorResponse
	if err := json.Unmarshal(env.Result, &execResp); err != nil {
		t.Fatalf("unmarshal executor response: %v", err)
	}

	// Verify headers and affinity
	if capturedAuth != "Bearer public" {
		t.Errorf("expected 'Bearer public', got %q", capturedAuth)
	}
	if capturedClient != "cli" {
		t.Errorf("expected 'cli', got %q", capturedClient)
	}
	if !strings.HasPrefix(capturedSession, "ses_") {
		t.Errorf("expected session prefix 'ses_', got %q", capturedSession)
	}
	if !strings.HasPrefix(capturedUA, "opencode/") {
		t.Errorf("expected User-Agent to start with 'opencode/', got %q", capturedUA)
	}

	// Verify tool call parsing in response
	toolCalls := gjson.GetBytes(execResp.Payload, "choices.0.message.tool_calls").Array()
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d. Payload: %s", len(toolCalls), string(execResp.Payload))
	}
	if toolCalls[0].Get("function.name").String() != "get_weather" {
		t.Errorf("expected function name 'get_weather', got %q", toolCalls[0].Get("function.name").String())
	}
	if finishReason := gjson.GetBytes(execResp.Payload, "choices.0.finish_reason").String(); finishReason != "tool_calls" {
		t.Errorf("expected finish_reason 'tool_calls', got %q", finishReason)
	}
}

func TestCloudZenExecutionStreaming(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"role":"assistant","content":"Hello"}}]}`,
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":" world"}}]}`,
			`data: {"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
			`data: [DONE]`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "%s\n\n", chunk)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer mockServer.Close()

	mgr := newMockHostManager()
	plugin := New(mgr.makeHostCall())

	credsJSON, _ := json.Marshal(credentials{
		Type:      "opencode",
		ServerURL: mockServer.URL,
		Mode:      "cloud",
	})

	originalReq := []byte(`{"model":"deepseek-v4-flash-free","messages":[{"role":"user","content":"Hi"}]}`)
	req := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "deepseek-v4-flash-free",
			Format:          "openai",
			Payload:         originalReq,
			OriginalRequest: originalReq,
			StorageJSON:     credsJSON,
		},
		StreamID: "test_cloud_stream_1",
	}

	rawReq, _ := json.Marshal(req)
	rawResp, err := plugin.Handle(pluginabi.MethodExecutorExecuteStream, rawReq)
	if err != nil {
		t.Fatalf("Handle(executor.execute_stream) error: %v", err)
	}

	var env pluginruntime.Envelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("stream init returned not OK: %s", string(rawResp))
	}

	plugin.streamWG.Wait()

	mgr.mu.Lock()
	emitted := mgr.emitted
	closed := mgr.streamClosed
	mgr.mu.Unlock()

	if !closed {
		t.Errorf("expected stream to be closed")
	}
	if len(emitted) == 0 {
		t.Fatalf("expected emitted chunks")
	}

	var allOutput string
	for _, chunk := range emitted {
		allOutput += string(chunk)
	}

	if !strings.Contains(allOutput, "Hello") || !strings.Contains(allOutput, " world") {
		t.Errorf("missing content in output: %s", allOutput)
	}
	if !strings.Contains(allOutput, "data: [DONE]") {
		t.Errorf("missing [DONE] in output: %s", allOutput)
	}
}

func TestCloudZenPaidModelRequiresKey(t *testing.T) {
	mgr := newMockHostManager()
	plugin := New(mgr.makeHostCall())

	credsJSON, _ := json.Marshal(credentials{
		Type:      "opencode",
		ServerURL: "https://opencode.ai/zen",
		Mode:      "cloud",
	})

	originalReq := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"Hi"}]}`)
	req := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "claude-sonnet-4-6",
			Format:          "openai",
			Payload:         originalReq,
			OriginalRequest: originalReq,
			StorageJSON:     credsJSON,
		},
	}

	rawReq, _ := json.Marshal(req)
	_, err := plugin.Handle(pluginabi.MethodExecutorExecute, rawReq)
	if err == nil {
		t.Fatalf("expected error when calling paid model without key")
	}
	if !strings.Contains(err.Error(), "paid model on OpenCode Zen") {
		t.Errorf("expected paid model error message, got: %v", err)
	}
}
