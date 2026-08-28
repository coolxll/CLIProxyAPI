package lingma

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	lingmaencoding "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/lingma"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

var syntheticCredential = []byte(`{
    "type":"lingma-plugin",
    "machine_id":"synthetic-machine-id-123456",
    "uid":"synthetic-user",
    "organization_id":"synthetic-org",
    "key":"synthetic-cosy-key",
    "security_oauth_token":"synthetic-oauth-token",
    "encrypt_user_info":"synthetic-encrypted-user",
    "user_type":"synthetic",
    "name":"Synthetic Account"
}`)

func TestRefreshAuthUsesHostTransportAndPersistsRotatedCredential(t *testing.T) {
	var mu sync.Mutex
	var urls []string
	host := func(method string, raw []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			t.Fatalf("host method = %q", method)
		}
		var req hostHTTPRequest
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			t.Fatalf("decode host request: %v", errUnmarshal)
		}
		mu.Lock()
		urls = append(urls, req.Request.URL)
		mu.Unlock()
		if !req.Request.Transport.ForceHTTP11 {
			t.Fatal("ForceHTTP11 = false")
		}
		var body []byte
		switch {
		case strings.Contains(req.Request.URL, "grantAuthInfos"):
			body = []byte(`[{"orgId":"refreshed-org"}]`)
		case strings.Contains(req.Request.URL, "/user/status"):
			body = []byte(`{"id":"refreshed-user","name":"Refreshed Account","userType":"pro","encryptUserInfo":"refreshed-info","expireTime":4102444800000}`)
		case strings.Contains(req.Request.URL, "/model/list"):
			body = []byte(`{"chat":[]}`)
		default:
			t.Fatalf("unexpected URL %q", req.Request.URL)
		}
		return pluginruntime.OK(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Headers: make(http.Header), Body: body})
	}
	plugin := New(host)
	request, errMarshal := json.Marshal(authRefreshRPCRequest{
		AuthRefreshRequest: pluginapi.AuthRefreshRequest{
			AuthID:      "lingma-plugin:synthetic",
			StorageJSON: syntheticCredential,
			Attributes:  map[string]string{"priority": "3"},
		},
		HostCallbackID: "callback-refresh",
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	raw, errRefresh := plugin.Handle(pluginabi.MethodAuthRefresh, request)
	if errRefresh != nil {
		t.Fatalf("RefreshAuth error = %v", errRefresh)
	}
	result := decodeResult[pluginapi.AuthRefreshResponse](t, raw)
	var refreshed credentials
	if errUnmarshal := json.Unmarshal(result.Auth.StorageJSON, &refreshed); errUnmarshal != nil {
		t.Fatalf("decode refreshed storage: %v", errUnmarshal)
	}
	if refreshed.UID != "refreshed-user" || refreshed.OrganizationID != "refreshed-org" || refreshed.CosyKey == "synthetic-cosy-key" {
		t.Fatalf("refreshed credential identity was not updated: uid=%q org=%q", refreshed.UID, refreshed.OrganizationID)
	}
	metadata := string(metadataJSONForTest(result.Auth))
	for _, secret := range []string{refreshed.CosyKey, "synthetic-oauth-token", "refreshed-info"} {
		if secret != "" && strings.Contains(metadata, secret) {
			t.Fatalf("auth metadata contains secret material")
		}
	}
	if !result.NextRefreshAfter.After(time.Now()) {
		t.Fatalf("NextRefreshAfter = %s", result.NextRefreshAfter)
	}
	if len(urls) != 3 {
		t.Fatalf("host call count = %d, want 3", len(urls))
	}
}

func TestModelsForAuthNormalizesCategorizedResponse(t *testing.T) {
	host := hostDoResponder(t, func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		if !strings.HasSuffix(req.URL, "/algo/api/v2/model/list") {
			t.Fatalf("model URL = %q", req.URL)
		}
		return pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{
            "chat":[{"key":"gm51model","display_name":"GM 5.1"}],
            "developer":{"dev":{"modelId":"dev_model","displayName":"Developer"}},
            "result":{"chat":[{"key":"gm51model"}]}
        }`)}
	})
	plugin := New(host)
	request, _ := json.Marshal(authModelRPCRequest{
		AuthModelRequest: pluginapi.AuthModelRequest{StorageJSON: syntheticCredential},
		HostCallbackID:   "callback-models",
	})
	raw, errModels := plugin.Handle(pluginabi.MethodModelForAuth, request)
	if errModels != nil {
		t.Fatalf("ModelsForAuth error = %v", errModels)
	}
	result := decodeResult[pluginapi.ModelResponse](t, raw)
	if result.Provider != ProviderID || len(result.Models) != 2 {
		t.Fatalf("model response = %#v", result)
	}
	if result.Models[0].ID != "gm51model" || result.Models[0].DisplayName != "GM 5.1" {
		t.Fatalf("first model = %#v", result.Models[0])
	}
}

func TestExecuteOpenAINonStreamTranslatesUsageAndSignsEncodedBody(t *testing.T) {
	var upstreamRequest pluginapi.HTTPRequest
	host := hostDoResponder(t, func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		upstreamRequest = req
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"X-Upstream": []string{"lingma"}},
			Body:       completeLingmaSSE("Pong"),
		}
	})
	plugin := New(host)
	payload := []byte(`{"model":"gm51model","messages":[{"role":"user","content":"Ping"}],"stream":false}`)
	request, _ := json.Marshal(executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "gm51model",
			Format:          formatOpenAI,
			SourceFormat:    formatOpenAI,
			Payload:         payload,
			OriginalRequest: payload,
			StorageJSON:     syntheticCredential,
		},
		HostCallbackID: "callback-execute",
	})
	raw, errExecute := plugin.Handle(pluginabi.MethodExecutorExecute, request)
	if errExecute != nil {
		t.Fatalf("Execute error = %v", errExecute)
	}
	result := decodeResult[pluginapi.ExecutorResponse](t, raw)
	if got := gjson.GetBytes(result.Payload, "choices.0.message.content").String(); got != "Pong" {
		t.Fatalf("response content = %q, payload=%s", got, result.Payload)
	}
	if result.Usage == nil || result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 4 || result.Usage.CachedTokens != 3 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	if !upstreamRequest.Transport.ForceHTTP11 || !strings.HasPrefix(upstreamRequest.Headers.Get("Authorization"), "Bearer COSY.") {
		t.Fatalf("upstream transport/signature missing")
	}
	decoded, errDecode := lingmaencoding.Decode(string(upstreamRequest.Body))
	if errDecode != nil {
		t.Fatalf("decode upstream body: %v", errDecode)
	}
	if gjson.GetBytes(decoded, "agent_id").String() != "agent_chat" || !gjson.GetBytes(decoded, "model_config.is_reasoning").Bool() {
		t.Fatalf("translated request = %s", decoded)
	}
}

func TestExecuteClaudeNonStreamParity(t *testing.T) {
	host := hostDoResponder(t, func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		return pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: completeLingmaSSE("Claude Pong")}
	})
	plugin := New(host)
	payload := []byte(`{"model":"gm51model","max_tokens":64,"messages":[{"role":"user","content":"Ping"}]}`)
	request, _ := json.Marshal(executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "gm51model",
			Format:          formatClaude,
			SourceFormat:    formatClaude,
			Payload:         payload,
			OriginalRequest: payload,
			StorageJSON:     syntheticCredential,
		},
		HostCallbackID: "callback-claude",
	})
	raw, errExecute := plugin.Handle(pluginabi.MethodExecutorExecute, request)
	if errExecute != nil {
		t.Fatalf("Execute Claude error = %v", errExecute)
	}
	result := decodeResult[pluginapi.ExecutorResponse](t, raw)
	if got := gjson.GetBytes(result.Payload, "content.0.text").String(); got != "Claude Pong" {
		t.Fatalf("Claude content = %q, payload=%s", got, result.Payload)
	}
	if result.Usage == nil || result.Usage.CachedTokens != 3 {
		t.Fatalf("Claude usage = %#v", result.Usage)
	}
}

func TestExecuteRetriesIncompleteSSEBeforeReturning(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	host := hostDoResponder(t, func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		decoded, errDecode := lingmaencoding.Decode(string(req.Body))
		if errDecode != nil {
			t.Fatal(errDecode)
		}
		mu.Lock()
		bodies = append(bodies, decoded)
		attempt := len(bodies)
		mu.Unlock()
		if attempt == 1 {
			return pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`data:{"body":"{\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}"}\n`)}
		}
		return pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: completeLingmaSSE("Recovered")}
	})
	plugin := New(host)
	registerWithConfig(t, plugin, []byte("upstream-recovery:\n  max-attempts: 2\n  base-delay: 1ns\n"))
	payload := []byte(`{"messages":[{"role":"user","content":"Ping"}]}`)
	request, _ := json.Marshal(executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{Model: "gm51model", Format: formatOpenAI, SourceFormat: formatOpenAI, Payload: payload, OriginalRequest: payload, StorageJSON: syntheticCredential},
		HostCallbackID:  "callback-retry",
	})
	if _, errExecute := plugin.Handle(pluginabi.MethodExecutorExecute, request); errExecute != nil {
		t.Fatalf("Execute retry error = %v", errExecute)
	}
	if len(bodies) != 2 || !gjson.GetBytes(bodies[1], "is_retry").Bool() {
		t.Fatalf("attempt bodies = %d, retry body=%s", len(bodies), bodies[len(bodies)-1])
	}
}

func TestExecuteStreamBridgesFragmentedFramesAndUsage(t *testing.T) {
	stream := newScriptedStreamHost(t, []hostHTTPStreamReadResponse{
		{Payload: completeLingmaSSE("Stream Pong")[:37]},
		{Payload: completeLingmaSSE("Stream Pong")[37:], Done: true},
	})
	plugin := New(stream.call)
	payload := []byte(`{"messages":[{"role":"user","content":"Ping"}],"stream":true}`)
	request, _ := json.Marshal(executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{Model: "gm51model", Format: formatOpenAI, SourceFormat: formatOpenAI, Stream: true, Payload: payload, OriginalRequest: payload, StorageJSON: syntheticCredential},
		StreamID:        "output-1",
		HostCallbackID:  "callback-stream",
	})
	raw, errExecute := plugin.Handle(pluginabi.MethodExecutorExecuteStream, request)
	if errExecute != nil {
		t.Fatalf("ExecuteStream error = %v", errExecute)
	}
	_ = decodeResult[executorStreamResponse](t, raw)
	select {
	case <-stream.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not close")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	joined := bytes.Join(stream.payloads, nil)
	if !bytes.Contains(joined, []byte("Stream Pong")) {
		t.Fatalf("stream payloads = %s", joined)
	}
	if stream.usage == nil || stream.usage.TotalTokens != 14 {
		t.Fatalf("stream usage = %#v", stream.usage)
	}
	if stream.closeError != "" {
		t.Fatalf("stream close error = %q", stream.closeError)
	}
}

func TestOpenAIResponseFormatReturnsNotImplemented(t *testing.T) {
	plugin := New(nil)
	request, _ := json.Marshal(executorRPCRequest{ExecutorRequest: pluginapi.ExecutorRequest{SourceFormat: formatOpenAIResponse}})
	_, errExecute := plugin.Handle(pluginabi.MethodExecutorExecute, request)
	status, ok := errExecute.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusNotImplemented {
		t.Fatalf("error = %T %v", errExecute, errExecute)
	}
}

func completeLingmaSSE(content string) []byte {
	inner, _ := json.Marshal(map[string]any{
		"id":    "chatcmpl-synthetic",
		"model": "gm51model",
		"choices": []any{map[string]any{
			"delta":         map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"input_tokens":                10,
			"output_tokens":               4,
			"total_tokens":                14,
			"cache_read_input_tokens":     3,
			"cache_creation_input_tokens": 1,
		},
	})
	envelope, _ := json.Marshal(map[string]string{"body": string(inner)})
	return []byte("data:" + string(envelope) + "\n\ndata: [DONE]\n\n")
}

func hostDoResponder(t *testing.T, respond func(pluginapi.HTTPRequest) pluginapi.HTTPResponse) HostCall {
	t.Helper()
	return func(method string, raw []byte) ([]byte, error) {
		if method == pluginabi.MethodHostLog {
			return pluginruntime.OK(struct{}{})
		}
		if method != pluginabi.MethodHostHTTPDo {
			t.Fatalf("host method = %q", method)
		}
		var req hostHTTPRequest
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			t.Fatalf("decode host request: %v", errUnmarshal)
		}
		return pluginruntime.OK(respond(req.Request))
	}
}

func registerWithConfig(t *testing.T, plugin *Plugin, config []byte) {
	t.Helper()
	request, _ := json.Marshal(lifecycleRequest{ConfigYAML: config})
	if _, errRegister := plugin.Handle(pluginabi.MethodPluginRegister, request); errRegister != nil {
		t.Fatalf("register plugin: %v", errRegister)
	}
}

type scriptedStreamHost struct {
	t          *testing.T
	mu         sync.Mutex
	reads      []hostHTTPStreamReadResponse
	index      int
	payloads   [][]byte
	usage      *pluginapi.UsageDetail
	closed     chan struct{}
	closeOnce  sync.Once
	closeError string
}

func newScriptedStreamHost(t *testing.T, reads []hostHTTPStreamReadResponse) *scriptedStreamHost {
	return &scriptedStreamHost{t: t, reads: reads, closed: make(chan struct{})}
}

func (h *scriptedStreamHost) call(method string, raw []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodHostHTTPDoStream:
		return pluginruntime.OK(hostHTTPStreamResponse{StatusCode: http.StatusOK, Headers: http.Header{"X-Stream": []string{"yes"}}, StreamID: "upstream-1"})
	case pluginabi.MethodHostHTTPStreamRead:
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.index >= len(h.reads) {
			return pluginruntime.OK(hostHTTPStreamReadResponse{Done: true})
		}
		result := h.reads[h.index]
		h.index++
		return pluginruntime.OK(result)
	case pluginabi.MethodHostHTTPStreamClose, pluginabi.MethodHostLog:
		return pluginruntime.OK(struct{}{})
	case pluginabi.MethodHostStreamEmit:
		var req hostStreamEmitRequest
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			h.t.Fatalf("decode stream emit: %v", errUnmarshal)
		}
		h.mu.Lock()
		if len(req.Payload) > 0 {
			h.payloads = append(h.payloads, bytes.Clone(req.Payload))
		}
		if req.Usage != nil {
			h.usage = usageDetailCopy(req.Usage)
		}
		h.mu.Unlock()
		return pluginruntime.OK(struct{}{})
	case pluginabi.MethodHostStreamClose:
		var req hostStreamCloseRequest
		_ = json.Unmarshal(raw, &req)
		h.mu.Lock()
		h.closeError = req.Error
		h.mu.Unlock()
		h.closeOnce.Do(func() { close(h.closed) })
		return pluginruntime.OK(struct{}{})
	default:
		h.t.Fatalf("unexpected host method %q", method)
		return nil, nil
	}
}

// TestParseModelsGoldenParityWithNative verifies that the plugin parseModels
// produces identical model IDs and display names as the native parseLingmaModels
// for all sanitized fixtures. The Type field differs intentionally (lingma-plugin
// vs lingma) during the shadow phase.
func TestParseModelsGoldenParityWithNative(t *testing.T) {
	fixtures := []string{
		"../../testdata/lingma/model_list_categorized.json",
		"../../testdata/lingma/model_list_array.json",
		"../../testdata/lingma/model_list_wrapped.json",
	}

	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			data, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			now := int64(1234567890)
			pluginModels := parseModels(data, now)
			nativeModels := nativeParseLingmaModelsForTest(data, now)

			if len(pluginModels) != len(nativeModels) {
				t.Fatalf("model count mismatch: plugin=%d, native=%d",
					len(pluginModels), len(nativeModels))
			}

			for i := range pluginModels {
				plugin := pluginModels[i]
				native := nativeModels[i]

				if plugin.ID != native.ID {
					t.Errorf("model[%d].ID: plugin=%q, native=%q", i, plugin.ID, native.ID)
				}
				if plugin.DisplayName != native.DisplayName {
					t.Errorf("model[%d].DisplayName: plugin=%q, native=%q",
						i, plugin.DisplayName, native.DisplayName)
				}
				if plugin.Object != native.Object {
					t.Errorf("model[%d].Object: plugin=%q, native=%q",
						i, plugin.Object, native.Object)
				}
				if plugin.OwnedBy != native.OwnedBy {
					t.Errorf("model[%d].OwnedBy: plugin=%q, native=%q",
						i, plugin.OwnedBy, native.OwnedBy)
				}
				if plugin.Created != native.Created {
					t.Errorf("model[%d].Created: plugin=%d, native=%d",
						i, plugin.Created, native.Created)
				}

				// Type is intentionally different during shadow phase
				if plugin.Type == native.Type {
					t.Errorf("model[%d].Type should differ: plugin=%q, native=%q",
						i, plugin.Type, native.Type)
				}
			}
		})
	}
}
