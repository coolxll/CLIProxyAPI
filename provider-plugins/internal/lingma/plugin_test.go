package lingma

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRegistrationDeclaresLingmaM2Capabilities(t *testing.T) {
	plugin := New(nil)
	raw, errHandle := plugin.Handle(pluginabi.MethodPluginRegister, []byte(`{"config_yaml":""}`))
	if errHandle != nil {
		t.Fatalf("Handle(plugin.register) error = %v", errHandle)
	}

	result := decodeResult[registration](t, raw)
	if result.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", result.SchemaVersion, pluginabi.SchemaVersion)
	}
	if !result.Capabilities.AuthProvider {
		t.Fatal("AuthProvider = false, want true")
	}
	if !result.Capabilities.ModelProvider || !result.Capabilities.Executor || !result.Capabilities.ThinkingApplier {
		t.Fatalf("Capabilities = %#v, want model, executor, and thinking support", result.Capabilities)
	}
	if result.Capabilities.ExecutorModelScope != pluginapi.ExecutorModelScopeOAuth {
		t.Fatalf("ExecutorModelScope = %q", result.Capabilities.ExecutorModelScope)
	}
	if strings.Join(result.Capabilities.ExecutorInputFormats, ",") != "openai,claude" {
		t.Fatalf("ExecutorInputFormats = %#v", result.Capabilities.ExecutorInputFormats)
	}
	if result.Metadata.Version != Version || !strings.Contains(result.Metadata.Name, "shadow") {
		t.Fatalf("Metadata = %#v", result.Metadata)
	}
}

func TestParseSyntheticShadowCredential(t *testing.T) {
	plugin := New(nil)
	credential := []byte(`{
        "type":"lingma-plugin",
        "machine_id":"synthetic-machine-1234",
        "uid":"synthetic-user",
        "organization_id":"synthetic-org",
        "key":"synthetic-key",
        "security_oauth_token":"synthetic-oauth",
        "encrypt_user_info":"synthetic-user-info",
        "user_type":"synthetic",
        "name":"Synthetic Account"
    }`)
	request, errMarshal := json.Marshal(pluginapi.AuthParseRequest{
		FileName: "synthetic.json",
		RawJSON:  credential,
	})
	if errMarshal != nil {
		t.Fatalf("marshal auth request: %v", errMarshal)
	}

	raw, errHandle := plugin.Handle(pluginabi.MethodAuthParse, request)
	if errHandle != nil {
		t.Fatalf("Handle(auth.parse) error = %v", errHandle)
	}
	result := decodeResult[pluginapi.AuthParseResponse](t, raw)
	if !result.Handled {
		t.Fatal("Handled = false, want true")
	}
	if result.Auth.Provider != ProviderID || result.Auth.FileName != "synthetic.json" {
		t.Fatalf("Auth = %#v", result.Auth)
	}
	if result.Auth.ID == "" || strings.Contains(result.Auth.ID, "synthetic-machine-1234") {
		t.Fatalf("Auth.ID = %q, want stable redacted identifier", result.Auth.ID)
	}
	if strings.Contains(string(metadataJSONForTest(result.Auth)), "synthetic-key") {
		t.Fatal("Auth metadata contains credential secret")
	}
	if result.Auth.NextRefreshAfter.IsZero() {
		t.Fatal("NextRefreshAfter is zero")
	}
}

func TestParseIgnoresNativeCredentialDuringShadowPhase(t *testing.T) {
	plugin := New(nil)
	request, errMarshal := json.Marshal(pluginapi.AuthParseRequest{
		RawJSON: []byte(`{"type":"lingma","machine_id":"native","uid":"native","key":"native"}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal auth request: %v", errMarshal)
	}

	raw, errHandle := plugin.Handle(pluginabi.MethodAuthParse, request)
	if errHandle != nil {
		t.Fatalf("Handle(auth.parse) error = %v", errHandle)
	}
	result := decodeResult[pluginapi.AuthParseResponse](t, raw)
	if result.Handled {
		t.Fatal("Handled = true for native Lingma credential, want false")
	}
}

func TestParseRejectsIncompleteShadowCredential(t *testing.T) {
	plugin := New(nil)
	request, errMarshal := json.Marshal(pluginapi.AuthParseRequest{
		RawJSON: []byte(`{"type":"lingma-plugin","machine_id":"machine","uid":"user"}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal auth request: %v", errMarshal)
	}

	_, errHandle := plugin.Handle(pluginabi.MethodAuthParse, request)
	if errHandle == nil || !strings.Contains(errHandle.Error(), "key") {
		t.Fatalf("Handle(auth.parse) error = %v, want missing key", errHandle)
	}
}

func decodeResult[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var envelope pluginruntime.Envelope
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("decode envelope: %v", errUnmarshal)
	}
	if !envelope.OK || envelope.Error != nil {
		t.Fatalf("envelope = %#v", envelope)
	}
	var result T
	if errUnmarshal := json.Unmarshal(envelope.Result, &result); errUnmarshal != nil {
		t.Fatalf("decode result: %v", errUnmarshal)
	}
	return result
}

// MetadataJSONForTest encodes only non-secret metadata and attributes. It is a
// test helper kept here so assertions never print StorageJSON on failure.
func metadataJSONForTest(a pluginapi.AuthData) []byte {
	raw, _ := json.Marshal(struct {
		Metadata   map[string]any    `json:"metadata"`
		Attributes map[string]string `json:"attributes"`
	}{Metadata: a.Metadata, Attributes: a.Attributes})
	return raw
}
