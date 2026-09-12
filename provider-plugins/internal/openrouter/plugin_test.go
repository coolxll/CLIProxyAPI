package openrouter

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPluginRegister(t *testing.T) {
	plugin := New(nil)
	raw, err := plugin.Handle(pluginabi.MethodPluginRegister, []byte(`{}`))
	if err != nil {
		t.Fatalf("Handle(plugin.register) failed: %v", err)
	}

	var env pluginruntime.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope failed: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope not OK: %+v", env.Error)
	}

	var reg registration
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatalf("unmarshal registration failed: %v", err)
	}
	if reg.SchemaVersion != pluginabi.SchemaVersion {
		t.Errorf("got SchemaVersion %d, want %d", reg.SchemaVersion, pluginabi.SchemaVersion)
	}
	if !reg.Capabilities.ModelProvider || !reg.Capabilities.AuthProvider || !reg.Capabilities.Executor {
		t.Errorf("missing capabilities in registration: %+v", reg.Capabilities)
	}
	if reg.Capabilities.ExecutorModelScope != pluginapi.ExecutorModelScopeOAuth {
		t.Errorf("got ExecutorModelScope %q, want %q", reg.Capabilities.ExecutorModelScope, pluginapi.ExecutorModelScopeOAuth)
	}
	if reg.Metadata.Name != "OpenRouter Free Provider" {
		t.Errorf("got name %q, want 'OpenRouter Free Provider'", reg.Metadata.Name)
	}
}

func TestPluginAuthParse(t *testing.T) {
	plugin := New(nil)

	t.Run("handled openrouter-plugin credential", func(t *testing.T) {
		req := pluginapi.AuthParseRequest{
			FileName: "custom.json",
			RawJSON:  []byte(`{"type":"openrouter-plugin","api_key":"sk-or-v1-abc123456789","name":"my-openrouter"}`),
		}
		rawReq, _ := json.Marshal(req)
		rawResp, err := plugin.Handle(pluginabi.MethodAuthParse, rawReq)
		if err != nil {
			t.Fatalf("Handle(auth.parse) error: %v", err)
		}

		var env pluginruntime.Envelope
		_ = json.Unmarshal(rawResp, &env)
		var resp pluginapi.AuthParseResponse
		_ = json.Unmarshal(env.Result, &resp)

		if !resp.Handled {
			t.Fatalf("expected Handled to be true")
		}
		if resp.Auth.Provider != ProviderID {
			t.Errorf("got provider %q, want %q", resp.Auth.Provider, ProviderID)
		}
		if resp.Auth.Label != "my-openrouter" {
			t.Errorf("got label %q, want 'my-openrouter'", resp.Auth.Label)
		}
	})

	t.Run("handled openrouter bare type credential", func(t *testing.T) {
		req := pluginapi.AuthParseRequest{
			FileName: "openrouter.json",
			RawJSON:  []byte(`{"type":"openrouter","api_key":"sk-or-v1-xyz987654321"}`),
		}
		rawReq, _ := json.Marshal(req)
		rawResp, err := plugin.Handle(pluginabi.MethodAuthParse, rawReq)
		if err != nil {
			t.Fatalf("Handle(auth.parse) error: %v", err)
		}

		var env pluginruntime.Envelope
		_ = json.Unmarshal(rawResp, &env)
		var resp pluginapi.AuthParseResponse
		_ = json.Unmarshal(env.Result, &resp)

		if !resp.Handled {
			t.Fatalf("expected Handled to be true")
		}
		if resp.Auth.Provider != ProviderID {
			t.Errorf("got provider %q, want %q", resp.Auth.Provider, ProviderID)
		}
	})

	t.Run("unhandled different provider", func(t *testing.T) {
		req := pluginapi.AuthParseRequest{
			RawJSON: []byte(`{"type":"openai","api_key":"sk-proj-xxx"}`),
		}
		rawReq, _ := json.Marshal(req)
		rawResp, err := plugin.Handle(pluginabi.MethodAuthParse, rawReq)
		if err != nil {
			t.Fatalf("Handle(auth.parse) error: %v", err)
		}

		var env pluginruntime.Envelope
		_ = json.Unmarshal(rawResp, &env)
		var resp pluginapi.AuthParseResponse
		_ = json.Unmarshal(env.Result, &resp)

		if resp.Handled {
			t.Errorf("expected Handled to be false for different provider")
		}
	})
}

func TestPluginStaticModels(t *testing.T) {
	plugin := New(nil)
	rawResp, err := plugin.Handle(pluginabi.MethodModelStatic, []byte(`{}`))
	if err != nil {
		t.Fatalf("Handle(model.static) error: %v", err)
	}

	var env pluginruntime.Envelope
	_ = json.Unmarshal(rawResp, &env)
	var resp pluginapi.ModelResponse
	_ = json.Unmarshal(env.Result, &resp)

	if resp.Provider != ProviderID {
		t.Errorf("got provider %q, want %q", resp.Provider, ProviderID)
	}
	if len(resp.Models) != 0 {
		t.Errorf("expected 0 static models when unauthenticated, got %d", len(resp.Models))
	}
}

func TestPluginModelsForAuth(t *testing.T) {
	host := newMockHostManager()
	host.httpHandler = func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body: []byte(`{"data":[
				{"id":"google/gemma-4-31b-it:free","pricing":{"prompt":"0","completion":"0"}},
				{"id":"openai/gpt-4o","pricing":{"prompt":"0.000005","completion":"0.000015"}}
			]}`),
		}
	}
	plugin := New(host.makeHostCall())
	req := authModelRPCRequest{
		AuthModelRequest: pluginapi.AuthModelRequest{
			StorageJSON: []byte(`{"type":"openrouter","api_key":"sk-or-v1-test"}`),
		},
	}
	rawReq, _ := json.Marshal(req)
	rawResp, err := plugin.Handle(pluginabi.MethodModelForAuth, rawReq)
	if err != nil {
		t.Fatalf("Handle(model.auth) error: %v", err)
	}

	var env pluginruntime.Envelope
	_ = json.Unmarshal(rawResp, &env)
	var resp pluginapi.ModelResponse
	_ = json.Unmarshal(env.Result, &resp)

	if resp.Provider != ProviderID {
		t.Errorf("got provider %q, want %q", resp.Provider, ProviderID)
	}
	if len(resp.Models) == 0 {
		t.Fatalf("expected models for auth, got none")
	}

	hasFreeRouter := false
	for _, m := range resp.Models {
		if m.ID == "openrouter/free" {
			hasFreeRouter = true
			break
		}
	}
	if !hasFreeRouter {
		t.Errorf("expected openrouter/free in auth models")
	}

	for _, m := range resp.Models {
		if m.ID == "openrouter/openai/gpt-4o" || m.ID == "openai/gpt-4o" {
			t.Errorf("expected paid model %q to be filtered out", m.ID)
		}
	}
}

func TestPluginIdentifier(t *testing.T) {
	plugin := New(nil)
	rawResp, err := plugin.Handle(pluginabi.MethodExecutorIdentifier, []byte(`{}`))
	if err != nil {
		t.Fatalf("Handle(executor.identifier) error: %v", err)
	}

	var env pluginruntime.Envelope
	_ = json.Unmarshal(rawResp, &env)
	var resp identifierResponse
	_ = json.Unmarshal(env.Result, &resp)

	if resp.Identifier != ProviderID {
		t.Errorf("got identifier %q, want %q", resp.Identifier, ProviderID)
	}
}

func TestPluginCountTokens(t *testing.T) {
	plugin := New(nil)
	req := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:        "openrouter/free",
			SourceFormat: "openai",
			Payload:      []byte(`{"messages":[{"role":"user","content":"Hello world!"}]}`),
		},
	}
	rawReq, _ := json.Marshal(req)
	rawResp, err := plugin.Handle(pluginabi.MethodExecutorCountTokens, rawReq)
	if err != nil {
		t.Fatalf("Handle(executor.count_tokens) error: %v", err)
	}

	var env pluginruntime.Envelope
	_ = json.Unmarshal(rawResp, &env)
	if !env.OK {
		t.Fatalf("envelope not OK: %+v", env.Error)
	}

	var resp pluginapi.ExecutorResponse
	_ = json.Unmarshal(env.Result, &resp)
	if len(resp.Payload) == 0 {
		t.Errorf("expected translated usage payload, got empty")
	}
}
