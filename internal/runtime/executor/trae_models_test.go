package executor

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestResolveTraeProtocolByModelName(t *testing.T) {
	tests := []struct {
		model        string
		wantProtocol string
		wantModel    string
	}{
		{"glm-5", traeProtocolV3, "glm-5"},
		{"deepseek-R1", traeProtocolV1, "deepseek-R1"},
		{"deepseek-V3", traeProtocolV1, "deepseek-V3"},
		{"no_thinking_model", traeProtocolV2, "no_thinking_model"},
		{"glm-5__dev", traeProtocolV3, "glm-5__dev"},
		{"custom_model_gpt-5__dev", traeProtocolV3, "custom_model_gpt-5__dev"},
		{"trae/glm-5", traeProtocolV3, "glm-5"},
		{"trae/deepseek-R1", traeProtocolV1, "deepseek-R1"},
		{"trae/deepseek-V3", traeProtocolV1, "deepseek-V3"},
		{"trae/no_thinking_model", traeProtocolV2, "no_thinking_model"},
	}

	for _, tt := range tests {
		gotProtocol, gotModel := resolveTraeProtocol(tt.model, nil)
		if gotProtocol != tt.wantProtocol || gotModel != tt.wantModel {
			t.Fatalf("resolveTraeProtocol(%q) = (%q, %q), want (%q, %q)",
				tt.model, gotProtocol, gotModel, tt.wantProtocol, tt.wantModel)
		}
	}
}

func TestParseTraeModels(t *testing.T) {
	models := parseTraeModels([]byte(`{
		"model_configs": [
			{"name":"seed_m8","display_name":"Doubao-1.5-pro","status":true,"prompt_max_tokens":28000},
			{"name":"disabled_model","display_name":"Disabled","status":false,"prompt_max_tokens":40000},
			{"name":"deepseek-R1","display_name":"DeepSeek-Reasoner（R1）","status":true,"prompt_max_tokens":40000},
			{"name":"deepseek-V3","display_name":"DeepSeek-V3","status":true,"prompt_max_tokens":40000},
			{"name":"deepseek-V3-0324","display_name":"DeepSeek-V3-0324","status":true,"prompt_max_tokens":40000}
		]
	}`), 123)

	if len(models) != 4 {
		t.Fatalf("len(models) = %d, want 4", len(models))
	}
	if got := models[0].ID; got != "seed_m8" {
		t.Fatalf("first model ID = %q, want seed_m8", got)
	}
	if got := models[0].DisplayName; got != "Doubao-1.5-pro" {
		t.Fatalf("first model display = %q, want Doubao-1.5-pro", got)
	}
	if got := models[0].ContextLength; got != 28000 {
		t.Fatalf("first model context = %d, want 28000", got)
	}
	for _, model := range models {
		if model.ID == "disabled_model" {
			t.Fatal("disabled model should be excluded")
		}
	}
}

func TestAppendTraeNoThinkingModel(t *testing.T) {
	models := appendTraeNoThinkingModel(parseTraeModels([]byte(`{"model_configs":[{"name":"seed_m8","status":true}]}`), 123), 123)
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if got := models[1].ID; got != "no_thinking_model" {
		t.Fatalf("appended model ID = %q, want no_thinking_model", got)
	}
	if slices.Contains(models[1].SupportedParameters, "tools") {
		t.Fatalf("no_thinking_model should not advertise tools: %#v", models[1].SupportedParameters)
	}

	existing := appendTraeNoThinkingModel([]*registry.ModelInfo{{
		ID:                  "no_thinking_model",
		SupportedParameters: []string{"tools", "temperature"},
	}}, 123)
	if slices.Contains(existing[0].SupportedParameters, "tools") {
		t.Fatalf("existing no_thinking_model should not advertise tools: %#v", existing[0].SupportedParameters)
	}
	if !slices.Contains(existing[0].SupportedParameters, "temperature") {
		t.Fatalf("existing supported parameter should be preserved: %#v", existing[0].SupportedParameters)
	}
}

func TestAppendTraeV3AgentModels(t *testing.T) {
	models := appendTraeV3AgentModels(nil, 123)
	if len(models) != 7 {
		t.Fatalf("len(models) = %d, want 7", len(models))
	}
	if got := models[0].ID; got != "glm-4.7" {
		t.Fatalf("first model ID = %q, want glm-4.7", got)
	}

	// Should not duplicate
	models = appendTraeV3AgentModels(models, 123)
	if len(models) != 7 {
		t.Fatalf("after dedup len(models) = %d, want 7", len(models))
	}
}

func TestParseTraeDetailParam(t *testing.T) {
	data := []byte(`{
		"config_info_list": [
			{
				"config_name": "glm-5.1",
				"config_switch": true,
				"usage": "chat_completion",
				"display_config": {"display_name": "GLM-5.1", "multimodal": false},
				"model_detail_list": [{"model_name": "glm-5.1__dev", "max_tokens": 16000, "prompt_max_tokens": 100000}]
			},
			{
				"config_name": "kimi-k2.6",
				"config_switch": true,
				"usage": "chat_completion",
				"display_config": {"display_name": "Kimi K2.6", "multimodal": true},
				"model_detail_list": [{"model_name": "kimi-k2.6__dev", "max_tokens": 16000, "prompt_max_tokens": 100000}]
			},
			{
				"config_name": "summary_model",
				"config_switch": true,
				"usage": "summary",
				"display_config": {"display_name": "Summary"},
				"model_detail_list": [{"model_name": "summary__dev", "max_tokens": 4096, "prompt_max_tokens": 200000}]
			},
			{
				"config_name": "custom_model_placeholder",
				"config_switch": true,
				"usage": "custom_model",
				"display_config": {"display_name": "Custom"},
				"model_detail_list": [{"model_name": "custom_model_placeholder", "max_tokens": 0, "prompt_max_tokens": 184000}]
			},
			{
				"config_name": "custom_claude-3-7-sonnet",
				"config_switch": true,
				"usage": "chat_completion",
				"display_config": {"display_name": "Claude 3.7 Sonnet"},
				"model_detail_list": [{"model_name": "custom_claude-3-7-sonnet__dev", "max_tokens": 8192, "prompt_max_tokens": 100000}]
			},
			{
				"config_name": "doubao-for-auto",
				"config_switch": true,
				"usage": "chat_completion",
				"is_invisible_to_user": true,
				"display_config": {"display_name": "Doubao Auto"},
				"model_detail_list": [{"model_name": "doubao-for-auto__dev", "max_tokens": 16000, "prompt_max_tokens": 100000}]
			},
			{
				"config_name": "disabled_model",
				"config_switch": false,
				"usage": "chat_completion",
				"display_config": {"display_name": "Disabled"},
				"model_detail_list": [{"model_name": "disabled__dev", "max_tokens": 16000, "prompt_max_tokens": 100000}]
			},
			{
				"config_name": "glm-4.7-auto",
				"config_switch": true,
				"usage": "chat_completion",
				"display_config": {"display_name": "GLM 4.7 Auto"},
				"model_detail_list": [{"model_name": "glm-4.7-auto__dev", "max_tokens": 16000, "prompt_max_tokens": 100000}]
			}
		]
	}`)

	models := parseTraeDetailParam(data, 123)

	if len(models) != 2 {
		var ids []string
		for _, m := range models {
			ids = append(ids, m.ID)
		}
		t.Fatalf("len(models) = %d, want 2; got IDs: %v", len(models), ids)
	}

	if got := models[0].ID; got != "glm-5.1" {
		t.Fatalf("first model ID = %q, want glm-5.1", got)
	}
	if got := models[0].ContextLength; got != 100000 {
		t.Fatalf("glm-5.1 context = %d, want 100000", got)
	}
	if got := models[0].MaxCompletionTokens; got != 16000 {
		t.Fatalf("glm-5.1 max_tokens = %d, want 16000", got)
	}
	if models[0].SupportedInputModalities != nil {
		t.Fatalf("glm-5.1 should not be multimodal")
	}

	if got := models[1].ID; got != "kimi-k2.6" {
		t.Fatalf("second model ID = %q, want kimi-k2.6", got)
	}
	if len(models[1].SupportedInputModalities) != 2 {
		t.Fatalf("kimi-k2.6 should be multimodal, got modalities: %v", models[1].SupportedInputModalities)
	}
}

func TestTraeFetchModelsFallsBackWhenDetailParamHasNoUsableConfigs(t *testing.T) {
	tests := []struct {
		name       string
		detailBody string
	}{
		{
			name:       "empty config list",
			detailBody: `{"config_info_list":[]}`,
		},
		{
			name: "missing backend model name",
			detailBody: `{"config_info_list":[
				{
					"config_name": "new-dynamic-model",
					"config_switch": true,
					"usage": "chat_completion",
					"display_config": {"display_name": "New Dynamic Model"},
					"model_detail_list": [{"max_tokens": 16000, "prompt_max_tokens": 100000}]
				}
			]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requestedModelList bool
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body := tt.detailBody
				if strings.Contains(req.URL.Path, "model_list") {
					requestedModelList = true
					body = `{"model_configs":[{"name":"fallback_raw","status":true,"display_name":"Fallback Raw","prompt_max_tokens":32000}]}`
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    req,
				}, nil
			}))

			models, err := NewTraeExecutor(nil).FetchModels(ctx, &cliproxyauth.Auth{
				ID:       "trae-fetch-fallback",
				Provider: "trae",
				Attributes: map[string]string{
					"jwt_token": "not-a-real-jwt",
				},
			})
			if err != nil {
				t.Fatalf("FetchModels returned error: %v", err)
			}
			if !requestedModelList {
				t.Fatal("expected FetchModels to fall back to model_list")
			}
			if !hasModelID(models, "fallback_raw") {
				t.Fatalf("expected fallback model_list model, got %v", modelIDs(models))
			}
		})
	}
}

func TestTraeV3CreateTaskUsesDetailModelNameForDynamicModel(t *testing.T) {
	e := NewTraeExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "trae-dynamic-model",
		Provider: "trae",
		Attributes: map[string]string{
			"jwt_token": "not-a-real-jwt",
		},
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"config_info_list":[
			{
				"config_name": "new-dynamic-model",
				"config_switch": true,
				"usage": "chat_completion",
				"display_config": {"display_name": "New Dynamic Model"},
				"model_detail_list": [{"model_name": "new-dynamic-model__backend", "max_tokens": 16000, "prompt_max_tokens": 100000}]
			}
		]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))
	models, err := e.FetchModels(ctx, auth)
	if err != nil {
		t.Fatalf("FetchModels returned error: %v", err)
	}
	if !hasModelID(models, "new-dynamic-model") {
		t.Fatalf("expected dynamic model to be fetched, got %v", modelIDs(models))
	}
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		t.Fatalf("CredentialsFromAuth returned error: %v", err)
	}

	req, err := e.buildTraeV3CreateTaskRequest(auth, creds, "new-dynamic-model", nil, []gjson.Result{
		gjson.Parse(`{"role":"user","content":"hello"}`),
	}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("buildTraeV3CreateTaskRequest returned error: %v", err)
	}

	if got := gjson.GetBytes(req.LogBody, "model_name").String(); got != "new-dynamic-model__backend" {
		t.Fatalf("model_name = %q, want new-dynamic-model__backend", got)
	}
	if got := gjson.GetBytes(req.LogBody, "config_name").String(); got != "new-dynamic-model" {
		t.Fatalf("config_name = %q, want new-dynamic-model", got)
	}
}

func TestTraeV3CreateTaskMetadataOverridesDetailModelConfig(t *testing.T) {
	e := NewTraeExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "trae-dynamic-model-override",
		Provider: "trae",
		Attributes: map[string]string{
			"jwt_token": "not-a-real-jwt",
		},
	}
	e.replaceTraeDetailModelConfigs(auth, map[string]traeDetailModelConfig{
		"new-dynamic-model": {
			ModelName:  "new-dynamic-model__backend",
			ConfigName: "new-dynamic-model",
		},
	})
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		t.Fatalf("CredentialsFromAuth returned error: %v", err)
	}

	req, err := e.buildTraeV3CreateTaskRequest(auth, creds, "new-dynamic-model", nil, []gjson.Result{
		gjson.Parse(`{"role":"user","content":"hello"}`),
	}, cliproxyexecutor.Options{Metadata: map[string]any{
		traeModelNameMeta: "explicit-model",
		traeConfigMeta:    "explicit-config",
	}})
	if err != nil {
		t.Fatalf("buildTraeV3CreateTaskRequest returned error: %v", err)
	}

	if got := gjson.GetBytes(req.LogBody, "model_name").String(); got != "explicit-model" {
		t.Fatalf("model_name = %q, want explicit-model", got)
	}
	if got := gjson.GetBytes(req.LogBody, "config_name").String(); got != "explicit-config" {
		t.Fatalf("config_name = %q, want explicit-config", got)
	}
}

func hasModelID(models []*registry.ModelInfo, id string) bool {
	for _, model := range models {
		if model != nil && model.ID == id {
			return true
		}
	}
	return false
}

func modelIDs(models []*registry.ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model != nil {
			ids = append(ids, model.ID)
		}
	}
	return ids
}

func TestAppendTraeV1RawChatModels(t *testing.T) {
	models := appendTraeV1RawChatModels(nil, 123)
	if len(models) != 4 {
		t.Fatalf("len(models) = %d, want 4", len(models))
	}
	if got := models[0].ID; got != "seed_m8" {
		t.Fatalf("first model ID = %q, want seed_m8", got)
	}

	// Should not duplicate
	models = appendTraeV1RawChatModels(models, 123)
	if len(models) != 4 {
		t.Fatalf("after dedup len(models) = %d, want 4", len(models))
	}
}
