package openrouter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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
	httpHandler  func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse
	// modelsBody answers GET /models so virtual routers and auto-failover resolve
	// against a live listing. Defaults to testLiveModelsJSON.
	modelsBody string
}

func newMockHostManager() *mockHostManager {
	return &mockHostManager{
		streamBodies: make(map[string]io.ReadCloser),
		modelsBody:   testLiveModelsJSON,
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
			if req.Request.Method == http.MethodGet && strings.Contains(req.Request.URL, "/models") {
				body := m.modelsBody
				if body == "" {
					body = testLiveModelsJSON
				}
				return pluginruntime.OK(pluginapi.HTTPResponse{
					StatusCode: http.StatusOK,
					Body:       []byte(body),
				})
			}
			if m.httpHandler != nil {
				resp := m.httpHandler(req.Request)
				return pluginruntime.OK(resp)
			}
			return pluginruntime.OK(pluginapi.HTTPResponse{
				StatusCode: http.StatusOK,
				Body:       []byte(`{"choices":[{"message":{"content":"ok"}}]}`),
			})

		case pluginabi.MethodHostHTTPDoStream:
			var req hostHTTPRequest
			_ = json.Unmarshal(request, &req)
			var resp pluginapi.HTTPResponse
			if m.httpHandler != nil {
				resp = m.httpHandler(req.Request)
			} else {
				resp = pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte("")}
			}

			streamID := fmt.Sprintf("stream_%d", len(m.streamBodies)+1)
			m.streamBodies[streamID] = io.NopCloser(bytes.NewReader(resp.Body))
			streamResp := hostHTTPStreamResponse{
				StatusCode: resp.StatusCode,
				Headers:    resp.Headers,
				StreamID:   streamID,
			}
			return pluginruntime.OK(streamResp)

		case pluginabi.MethodHostHTTPStreamRead:
			var req hostHTTPStreamReadRequest
			_ = json.Unmarshal(request, &req)
			body, exists := m.streamBodies[req.StreamID]
			if !exists {
				return nil, fmt.Errorf("stream not found: %s", req.StreamID)
			}
			buf := make([]byte, 512)
			n, err := body.Read(buf)
			readResp := hostHTTPStreamReadResponse{
				Payload: buf[:n],
				Done:    err == io.EOF,
			}
			if err != nil && err != io.EOF {
				readResp.Error = err.Error()
			}
			return pluginruntime.OK(readResp)

		case pluginabi.MethodHostHTTPStreamClose:
			var req hostHTTPStreamCloseRequest
			_ = json.Unmarshal(request, &req)
			if body, exists := m.streamBodies[req.StreamID]; exists {
				_ = body.Close()
				delete(m.streamBodies, req.StreamID)
			}
			return pluginruntime.OK(nil)

		case pluginabi.MethodHostStreamEmit:
			var req hostStreamEmitRequest
			_ = json.Unmarshal(request, &req)
			m.emitted = append(m.emitted, append([]byte(nil), req.Payload...))
			return pluginruntime.OK(nil)

		case pluginabi.MethodHostStreamClose:
			m.streamClosed = true
			return pluginruntime.OK(nil)

		case pluginabi.MethodHostLog:
			return pluginruntime.OK(nil)

		default:
			return nil, fmt.Errorf("unsupported mock host call: %s", method)
		}
	}
}

func TestExecute_NonStreaming_Success(t *testing.T) {
	mockHost := newMockHostManager()
	mockHost.httpHandler = func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		if !strings.HasSuffix(req.URL, "/chat/completions") {
			return pluginapi.HTTPResponse{StatusCode: http.StatusNotFound}
		}
		authH := req.Headers.Get("Authorization")
		if authH != "Bearer sk-or-v1-test" {
			return pluginapi.HTTPResponse{StatusCode: http.StatusUnauthorized}
		}
		respBody := `{
			"id": "gen-123",
			"model": "openrouter/free",
			"choices": [
				{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Hello from OpenRouter!",
						"reasoning": "Reasoning step 1"
					},
					"finish_reason": "stop"
				}
			],
			"usage": {
				"prompt_tokens": 10,
				"completion_tokens": 8,
				"total_tokens": 18
			}
		}`
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(respBody),
		}
	}

	plugin := New(mockHost.makeHostCall())
	credJSON := `{"type":"openrouter-plugin","api_key":"sk-or-v1-test"}`

	execReq := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "openrouter/free",
			Format:      "openai",
			Payload:     []byte(`{"messages":[{"role":"user","content":"Hi"}]}`),
			StorageJSON: []byte(credJSON),
		},
	}
	rawReq, _ := json.Marshal(execReq)
	rawResp, err := plugin.Handle(pluginabi.MethodExecutorExecute, rawReq)
	if err != nil {
		t.Fatalf("Handle(executor.execute) failed: %v", err)
	}

	var env pluginruntime.Envelope
	_ = json.Unmarshal(rawResp, &env)
	if !env.OK {
		t.Fatalf("envelope not OK: %+v", env.Error)
	}

	var execResp pluginapi.ExecutorResponse
	_ = json.Unmarshal(env.Result, &execResp)

	content := gjson.GetBytes(execResp.Payload, "choices.0.message.content").String()
	if content != "Hello from OpenRouter!" {
		t.Errorf("got content %q, want %q", content, "Hello from OpenRouter!")
	}

	// Verify reasoning was normalized to reasoning_content
	reasoningContent := gjson.GetBytes(execResp.Payload, "choices.0.message.reasoning_content").String()
	if reasoningContent != "Reasoning step 1" {
		t.Errorf("got reasoning_content %q, want %q", reasoningContent, "Reasoning step 1")
	}
}

func TestExecute_AutoFailover_429(t *testing.T) {
	mockHost := newMockHostManager()
	attempt := 0
	mockHost.httpHandler = func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		attempt++
		if attempt == 1 {
			// First attempt returns 429
			return pluginapi.HTTPResponse{
				StatusCode: http.StatusTooManyRequests,
				Body:       []byte(`{"error":{"message":"Rate limit exceeded"}}`),
			}
		}
		// Second attempt (failover) succeeds
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"choices":[{"message":{"role":"assistant","content":"Recovered from fallback"}}]}`),
		}
	}

	plugin := New(mockHost.makeHostCall())
	credJSON := `{"type":"openrouter-plugin","api_key":"sk-or-v1-test","auto_failover":true}`

	execReq := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "google/gemma-4-31b-it:free",
			Format:      "openai",
			Payload:     []byte(`{"messages":[{"role":"user","content":"Hi"}]}`),
			StorageJSON: []byte(credJSON),
		},
	}
	rawReq, _ := json.Marshal(execReq)
	rawResp, err := plugin.Handle(pluginabi.MethodExecutorExecute, rawReq)
	if err != nil {
		t.Fatalf("Handle(executor.execute) failed: %v", err)
	}

	var env pluginruntime.Envelope
	_ = json.Unmarshal(rawResp, &env)
	if !env.OK {
		t.Fatalf("envelope not OK: %+v", env.Error)
	}

	var execResp pluginapi.ExecutorResponse
	_ = json.Unmarshal(env.Result, &execResp)

	content := gjson.GetBytes(execResp.Payload, "choices.0.message.content").String()
	if content != "Recovered from fallback" {
		t.Errorf("got content %q, want %q", content, "Recovered from fallback")
	}

	// Verify model cooldown was recorded
	if !globalCooldowns.isCoolingDown("google/gemma-4-31b-it:free") {
		t.Errorf("expected google/gemma-4-31b-it:free to be cooling down")
	}
}

func TestExecuteStream_Success_WithReasoningNormalization(t *testing.T) {
	mockHost := newMockHostManager()
	mockHost.httpHandler = func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		sseChunks := []string{
			"data: {\"id\":\"gen-1\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"reasoning\":\"Pondering answer...\"}}]}\n\n",
			"data: {\"id\":\"gen-1\",\"choices\":[{\"delta\":{\"content\":\"Hello there!\"}}]}\n\n",
			"data: {\"id\":\"gen-1\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":10,\"total_tokens\":15}}\n\n",
			"data: [DONE]\n\n",
		}
		fullSSE := strings.Join(sseChunks, "")
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(fullSSE),
		}
	}

	plugin := New(mockHost.makeHostCall())
	credJSON := `{"type":"openrouter-plugin","api_key":"sk-or-v1-test"}`

	streamReq := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "openrouter/free",
			Format:      "openai",
			Payload:     []byte(`{"messages":[{"role":"user","content":"Hello"}]}`),
			StorageJSON: []byte(credJSON),
		},
		StreamID: "downstream_stream_1",
	}

	rawReq, _ := json.Marshal(streamReq)
	rawResp, err := plugin.Handle(pluginabi.MethodExecutorExecuteStream, rawReq)
	if err != nil {
		t.Fatalf("Handle(executor.execute_stream) failed: %v", err)
	}

	var env pluginruntime.Envelope
	_ = json.Unmarshal(rawResp, &env)
	if !env.OK {
		t.Fatalf("envelope not OK: %+v", env.Error)
	}

	// Wait for stream pump to finish
	time.Sleep(100 * time.Millisecond)

	mockHost.mu.Lock()
	emitted := mockHost.emitted
	closed := mockHost.streamClosed
	mockHost.mu.Unlock()

	if !closed {
		t.Errorf("expected downstream stream to be closed")
	}
	if len(emitted) == 0 {
		t.Fatalf("expected emitted chunks, got none")
	}

	var allOutput string
	for _, chunk := range emitted {
		allOutput += string(chunk)
	}

	// Verify reasoning normalized to reasoning_content
	if !strings.Contains(allOutput, `"reasoning_content":"Pondering answer..."`) {
		t.Errorf("expected reasoning_content in emitted chunks, got: %s", allOutput)
	}
	if !strings.Contains(allOutput, `"content":"Hello there!"`) {
		t.Errorf("expected content in emitted chunks, got: %s", allOutput)
	}
	if !strings.Contains(allOutput, "data: [DONE]") {
		t.Errorf("expected [DONE] in emitted chunks, got: %s", allOutput)
	}
}

func TestExecute_MissingAPIKey(t *testing.T) {
	mockHost := newMockHostManager()
	plugin := New(mockHost.makeHostCall())

	credJSON := `{"type":"openrouter-plugin"}` // missing api_key
	execReq := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "openrouter/free",
			Format:      "openai",
			Payload:     []byte(`{"messages":[{"role":"user","content":"Hi"}]}`),
			StorageJSON: []byte(credJSON),
		},
	}
	rawReq, _ := json.Marshal(execReq)
	_, err := plugin.Handle(pluginabi.MethodExecutorExecute, rawReq)
	if err == nil {
		t.Errorf("expected error when api_key is missing, got nil")
	}
}

func TestExecute_VirtualModel_TierS(t *testing.T) {
	mockHost := newMockHostManager()
	var receivedModel string
	mockHost.httpHandler = func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		receivedModel = gjson.GetBytes(req.Body, "model").String()
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"choices":[{"message":{"content":"Tier S output"}}]}`),
		}
	}

	plugin := New(mockHost.makeHostCall())
	credJSON := `{"type":"openrouter-plugin","api_key":"sk-or-v1-test"}`

	execReq := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "free:s",
			Format:      "openai",
			Payload:     []byte(`{"messages":[{"role":"user","content":"Hi"}]}`),
			StorageJSON: []byte(credJSON),
		},
	}
	rawReq, _ := json.Marshal(execReq)
	rawResp, err := plugin.Handle(pluginabi.MethodExecutorExecute, rawReq)
	if err != nil {
		t.Fatalf("Handle(executor.execute) failed: %v", err)
	}

	var env pluginruntime.Envelope
	_ = json.Unmarshal(rawResp, &env)
	if !env.OK {
		t.Fatalf("envelope not OK: %+v", env.Error)
	}

	qReceived := lookupQuality(receivedModel)
	if qReceived.Tier != TierS && qReceived.Tier != TierSPlus {
		t.Errorf("expected Tier S or S+ model sent to upstream, got %s (model: %s)", qReceived.Tier, receivedModel)
	}
}

func TestExecute_VirtualModel_Coding_AutoFailover(t *testing.T) {
	mockHost := newMockHostManager()
	var attempts []string
	mockHost.httpHandler = func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		modelSent := gjson.GetBytes(req.Body, "model").String()
		attempts = append(attempts, modelSent)
		if len(attempts) == 1 {
			// First attempt returns 429
			return pluginapi.HTTPResponse{
				StatusCode: http.StatusTooManyRequests,
				Body:       []byte(`{"error":{"message":"rate limit"}}`),
			}
		}
		// Second attempt succeeds
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"choices":[{"message":{"content":"def solve(): return 42"}}]}`),
		}
	}

	plugin := New(mockHost.makeHostCall())
	credJSON := `{"type":"openrouter-plugin","api_key":"sk-or-v1-test","auto_failover":true}`

	execReq := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "free:coding",
			Format:      "openai",
			Payload:     []byte(`{"messages":[{"role":"user","content":"write code"}]}`),
			StorageJSON: []byte(credJSON),
		},
	}
	rawReq, _ := json.Marshal(execReq)
	rawResp, err := plugin.Handle(pluginabi.MethodExecutorExecute, rawReq)
	if err != nil {
		t.Fatalf("Handle(executor.execute) failed: %v", err)
	}

	var env pluginruntime.Envelope
	_ = json.Unmarshal(rawResp, &env)
	if !env.OK {
		t.Fatalf("envelope not OK: %+v", env.Error)
	}

	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts with auto-failover, got %d", len(attempts))
	}
	if attempts[0] == attempts[1] {
		t.Errorf("failover called the same model twice: %s", attempts[0])
	}
}
