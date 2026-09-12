package opencode

import (
	"encoding/json"
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
		t.Fatalf("envelope not OK")
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
}

func TestPluginAuthParse(t *testing.T) {
	plugin := New(nil)

	t.Run("handled opencode credential", func(t *testing.T) {
		req := pluginapi.AuthParseRequest{
			FileName: "custom.json",
			RawJSON:  []byte(`{"type":"opencode-plugin","server_url":"http://127.0.0.1:10001","name":"local-oc"}`),
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
			t.Errorf("expected Handled to be true")
		}
		if resp.Auth.Provider != ProviderID {
			t.Errorf("got provider %q, want %q", resp.Auth.Provider, ProviderID)
		}
		if resp.Auth.Label != "local-oc" {
			t.Errorf("got label %q, want local-oc", resp.Auth.Label)
		}
	})

	t.Run("unhandled different provider", func(t *testing.T) {
		req := pluginapi.AuthParseRequest{
			RawJSON: []byte(`{"type":"different-provider"}`),
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
		t.Errorf("expected no fabricated static models, got %d", len(resp.Models))
	}
}

func TestPluginCountTokens(t *testing.T) {
	plugin := New(nil)
	req := executorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:        "opencode/kimi-k2.5-free",
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
	var resp pluginapi.ExecutorResponse
	_ = json.Unmarshal(env.Result, &resp)

	if len(resp.Payload) == 0 {
		t.Errorf("expected non-empty payload in count tokens response")
	}
}
