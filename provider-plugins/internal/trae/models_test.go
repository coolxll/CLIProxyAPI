package trae

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestFetchModelsFromDetailParam(t *testing.T) {
	data, err := os.ReadFile("../../testdata/trae/model_list_detail_param.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	host := hostDoResponder(t, func(req pluginapi.HTTPRequest) pluginapi.HTTPResponse {
		if !strings.Contains(req.URL, "/api/ide/v1/get_detail_param") {
			t.Fatalf("unexpected URL: %s", req.URL)
		}
		return pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       data,
		}
	})

	creds := credentials{
		JWTToken:  "test-token",
		MachineID: "test-machine",
		DeviceID:  "test-device",
	}

	rpc := hostRPC{call: host}
	models, err := fetchModels(rpc, creds)
	if err != nil {
		t.Fatalf("fetch models: %v", err)
	}

	// Should have 2 valid models from detail param + V1 models + V3 models + no_thinking_model
	if len(models) < 5 {
		t.Errorf("expected at least 5 models, got %d", len(models))
	}

	// Check that DeepSeek-V4-Pro is present
	found := false
	for _, m := range models {
		if m.ID == "DeepSeek-V4-Pro" {
			found = true
			if m.DisplayName != "DeepSeek V4 Pro" {
				t.Errorf("expected display name 'DeepSeek V4 Pro', got %s", m.DisplayName)
			}
			if m.ContextLength != 100000 {
				t.Errorf("expected context length 100000, got %d", m.ContextLength)
			}
			break
		}
	}
	if !found {
		t.Error("DeepSeek-V4-Pro not found in models")
	}

	// Check that disabled/hidden/custom models are filtered out
	for _, m := range models {
		if m.ID == "disabled-model" || m.ID == "hidden-model" || m.ID == "custom_model_test" || m.ID == "test-auto" {
			t.Errorf("filtered model %s should not be present", m.ID)
		}
	}
}

func TestFetchModelsFromModelList(t *testing.T) {
	data, err := os.ReadFile("../../testdata/trae/model_list_raw_chat.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	callCount := 0
	host := func(method string, raw []byte) ([]byte, error) {
		if method == pluginabi.MethodHostLog {
			return pluginruntime.OK(struct{}{})
		}
		if method != pluginabi.MethodHostHTTPDo {
			t.Fatalf("unexpected host method %q", method)
		}
		callCount++
		var req hostHTTPRequest
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			t.Fatalf("decode host request: %v", errUnmarshal)
		}

		// First call to detail param fails, second call to model list succeeds
		if strings.Contains(req.Request.URL, "/api/ide/v1/get_detail_param") {
			return pluginruntime.OK(pluginapi.HTTPResponse{
				StatusCode: http.StatusInternalServerError,
				Body:       []byte(`{"error":"internal error"}`),
			})
		}
		if strings.Contains(req.Request.URL, "/api/ide/v1/model_list") {
			return pluginruntime.OK(pluginapi.HTTPResponse{
				StatusCode: http.StatusOK,
				Body:       data,
			})
		}
		t.Fatalf("unexpected URL: %s", req.Request.URL)
		return nil, nil
	}

	creds := credentials{
		JWTToken:  "test-token",
		MachineID: "test-machine",
		DeviceID:  "test-device",
	}

	rpc := hostRPC{call: host}
	models, err := fetchModels(rpc, creds)
	if err != nil {
		t.Fatalf("fetch models: %v", err)
	}

	// Should have 2 valid models from model list + V3 models + no_thinking_model
	if len(models) < 3 {
		t.Errorf("expected at least 3 models, got %d", len(models))
	}

	// Check that DeepSeek-V4-Pro is present
	found := false
	for _, m := range models {
		if m.ID == "DeepSeek-V4-Pro" {
			found = true
			if m.DisplayName != "DeepSeek V4 Pro" {
				t.Errorf("expected display name 'DeepSeek V4 Pro', got %s", m.DisplayName)
			}
			break
		}
	}
	if !found {
		t.Error("DeepSeek-V4-Pro not found in models")
	}

	// Check that disabled and Doubao_1_5_thinking_pro are filtered out
	for _, m := range models {
		if m.ID == "disabled-model" || m.ID == "Doubao_1_5_thinking_pro" {
			t.Errorf("filtered model %s should not be present", m.ID)
		}
	}
}

func TestParseTraeDetailParamWithConfigs(t *testing.T) {
	data, err := os.ReadFile("../../testdata/trae/model_list_detail_param.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	models := parseTraeDetailParamWithConfigs(data, 1234567890)

	// Should have 2 valid models (DeepSeek-V4-Pro and Doubao-Seed-2.0-Code)
	if len(models) != 2 {
		t.Errorf("expected 2 models, got %d", len(models))
	}

	// Check DeepSeek-V4-Pro
	var deepseek *pluginapi.ModelInfo
	for i := range models {
		if models[i].ID == "DeepSeek-V4-Pro" {
			deepseek = &models[i]
			break
		}
	}
	if deepseek == nil {
		t.Fatal("DeepSeek-V4-Pro not found")
	}
	if deepseek.DisplayName != "DeepSeek V4 Pro" {
		t.Errorf("expected display name 'DeepSeek V4 Pro', got %s", deepseek.DisplayName)
	}
	if deepseek.ContextLength != 100000 {
		t.Errorf("expected context length 100000, got %d", deepseek.ContextLength)
	}
	if deepseek.MaxCompletionTokens != 16000 {
		t.Errorf("expected max completion tokens 16000, got %d", deepseek.MaxCompletionTokens)
	}
	if len(deepseek.SupportedInputModalities) != 0 {
		t.Errorf("expected no input modalities, got %v", deepseek.SupportedInputModalities)
	}

	// Check Doubao-Seed-2.0-Code (multimodal)
	var doubao *pluginapi.ModelInfo
	for i := range models {
		if models[i].ID == "Doubao-Seed-2.0-Code" {
			doubao = &models[i]
			break
		}
	}
	if doubao == nil {
		t.Fatal("Doubao-Seed-2.0-Code not found")
	}
	if len(doubao.SupportedInputModalities) != 2 {
		t.Errorf("expected 2 input modalities, got %v", doubao.SupportedInputModalities)
	}
}

func TestParseTraeModels(t *testing.T) {
	data, err := os.ReadFile("../../testdata/trae/model_list_raw_chat.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	models := parseTraeModels(data, 1234567890)

	// Should have 2 valid models (DeepSeek-V4-Pro and glm-5.1)
	if len(models) != 2 {
		t.Errorf("expected 2 models, got %d", len(models))
	}

	// Check that disabled and Doubao_1_5_thinking_pro are filtered out
	for _, m := range models {
		if m.ID == "disabled-model" || m.ID == "Doubao_1_5_thinking_pro" {
			t.Errorf("filtered model %s should not be present", m.ID)
		}
	}
}

func TestAppendTraeNoThinkingModel(t *testing.T) {
	models := []pluginapi.ModelInfo{
		{ID: "model1", DisplayName: "Model 1"},
	}

	result := appendTraeNoThinkingModel(models, 1234567890)

	if len(result) != 2 {
		t.Errorf("expected 2 models, got %d", len(result))
	}

	// Check that no_thinking_model was added
	found := false
	for _, m := range result {
		if m.ID == "no_thinking_model" {
			found = true
			if m.DisplayName != "Trae No Thinking Model" {
				t.Errorf("expected display name 'Trae No Thinking Model', got %s", m.DisplayName)
			}
			if m.ContextLength != 40000 {
				t.Errorf("expected context length 40000, got %d", m.ContextLength)
			}
			break
		}
	}
	if !found {
		t.Error("no_thinking_model not found")
	}
}

func TestAppendTraeNoThinkingModelAlreadyExists(t *testing.T) {
	models := []pluginapi.ModelInfo{
		{ID: "no_thinking_model", DisplayName: "Existing", SupportedParameters: []string{"tools"}},
	}

	result := appendTraeNoThinkingModel(models, 1234567890)

	if len(result) != 1 {
		t.Errorf("expected 1 model, got %d", len(result))
	}

	// Check that tools parameter was removed
	if len(result[0].SupportedParameters) != 0 {
		t.Errorf("expected no supported parameters, got %v", result[0].SupportedParameters)
	}
}

func TestAppendTraeV1RawChatModels(t *testing.T) {
	models := []pluginapi.ModelInfo{
		{ID: "existing-model", DisplayName: "Existing"},
	}

	result := appendTraeV1RawChatModels(models, 1234567890)

	// Should add 4 V1 models
	if len(result) != 5 {
		t.Errorf("expected 5 models, got %d", len(result))
	}

	// Check that seed_m8 was added
	found := false
	for _, m := range result {
		if m.ID == "seed_m8" {
			found = true
			if m.DisplayName != "Doubao 1.5 Pro" {
				t.Errorf("expected display name 'Doubao 1.5 Pro', got %s", m.DisplayName)
			}
			if m.ContextLength != 28000 {
				t.Errorf("expected context length 28000, got %d", m.ContextLength)
			}
			break
		}
	}
	if !found {
		t.Error("seed_m8 not found")
	}
}

func TestAppendTraeV3AgentModels(t *testing.T) {
	models := []pluginapi.ModelInfo{
		{ID: "existing-model", DisplayName: "Existing"},
	}

	result := appendTraeV3AgentModels(models, 1234567890)

	// Should add 7 V3 models
	if len(result) != 8 {
		t.Errorf("expected 8 models, got %d", len(result))
	}

	// Check that glm-4.7 was added
	found := false
	for _, m := range result {
		if m.ID == "glm-4.7" {
			found = true
			if m.DisplayName != "GLM-4.7" {
				t.Errorf("expected display name 'GLM-4.7', got %s", m.DisplayName)
			}
			if m.ContextLength != 16000 {
				t.Errorf("expected context length 16000, got %d", m.ContextLength)
			}
			break
		}
	}
	if !found {
		t.Error("glm-4.7 not found")
	}
}

func TestStaticModels(t *testing.T) {
	models := staticModels()

	// Should have all static models (4 V1 + 1 V2 + 9 V3 core + 8 V3 optional = 22)
	if len(models) < 20 {
		t.Errorf("expected at least 20 static models, got %d", len(models))
	}

	// Check that all models have Type = ProviderID
	for _, m := range models {
		if m.Type != ProviderID {
			t.Errorf("model %s has type %s, expected %s", m.ID, m.Type, ProviderID)
		}
	}

	// Check that V1 models are present
	v1Models := []string{"seed_m8", "deepseek-R1", "deepseek-V3", "deepseek-V3-0324"}
	for _, id := range v1Models {
		found := false
		for _, m := range models {
			if m.ID == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("V1 model %s not found", id)
		}
	}

	// Check that V2 no_thinking_model is present
	found := false
	for _, m := range models {
		if m.ID == "no_thinking_model" {
			found = true
			break
		}
	}
	if !found {
		t.Error("V2 no_thinking_model not found")
	}

	// Check that V3 models are present
	v3Models := []string{"DeepSeek-V4-Pro", "glm-5.1", "kimi-k2.6"}
	for _, id := range v3Models {
		found := false
		for _, m := range models {
			if m.ID == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("V3 model %s not found", id)
		}
	}
}

func TestModelExists(t *testing.T) {
	models := []pluginapi.ModelInfo{
		{ID: "model1"},
		{ID: "Model2"},
	}

	if !modelExists(models, "model1") {
		t.Error("model1 should exist")
	}
	if !modelExists(models, "MODEL1") {
		t.Error("MODEL1 should exist (case-insensitive)")
	}
	if !modelExists(models, "model2") {
		t.Error("model2 should exist (case-insensitive)")
	}
	if modelExists(models, "model3") {
		t.Error("model3 should not exist")
	}
}

func TestSetTraeCommonHeaders(t *testing.T) {
	creds := credentials{
		JWTToken:  "test-jwt",
		MachineID: "test-machine",
		DeviceID:  "test-device",
	}

	headers := http.Header{}
	setTraeCommonHeaders(headers, creds)

	if got := headers.Get("Authorization"); got != "Cloud-IDE-JWT test-jwt" {
		t.Errorf("Authorization = %q", got)
	}
	if got := headers.Get("X-App-Id"); got != "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8" {
		t.Errorf("X-App-Id = %q", got)
	}
	if got := headers.Get("x-device-id"); got != "test-device" {
		t.Errorf("x-device-id = %q", got)
	}
	if got := headers.Get("x-machine-id"); got != "test-machine" {
		t.Errorf("x-machine-id = %q", got)
	}
}
