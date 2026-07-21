package trae

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

const (
	traeDetailParamURL = "https://trae-api-cn.mchost.guru/api/ide/v1/get_detail_param"
	traeModelListURL   = "https://trae-api-cn.mchost.guru/api/ide/v1/model_list?type=llm_raw_chat"
)

// fetchModels attempts detail param first, falls back to model list.
func fetchModels(host hostRPC, creds credentials) ([]pluginapi.ModelInfo, error) {
	now := time.Now().Unix()

	models, err := fetchModelsFromDetailParam(host, creds, now)
	if err != nil {
		models, err = fetchModelsFromModelList(host, creds, now)
		if err != nil {
			return nil, err
		}
	}

	models = appendTraeNoThinkingModel(models, now)
	return models, nil
}

func setTraeCommonHeaders(header http.Header, creds credentials) {
	header.Set("Authorization", "Cloud-IDE-JWT "+creds.JWTToken)
	header.Set("X-App-Id", "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8")
	header.Set("x-app-version", "default")
	header.Set("x-ide-version-code", "20260508")
	header.Set("x-app-version-code", "20260401")
	header.Set("x-device-brand", "Lenovo")
	header.Set("x-device-cpu", "AMD")
	header.Set("x-device-id", creds.DeviceID)
	header.Set("x-machine-id", creds.MachineID)
	header.Set("x-os-version", "Linux")
	header.Set("x-device-type", "linux")
	header.Set("x-ide-version", "3.3.55")
	header.Set("x-ide-version-type", "stable")
	header.Set("request-traffic-type", "prod")
	header.Set("get-svc", "1")
}

func fetchModelsFromDetailParam(host hostRPC, creds credentials, now int64) ([]pluginapi.ModelInfo, error) {
	body := `{"function":"chat_v3","config_names":null,"need_prompt":false,"current_config_info":null,"poly_prompt":true,"mode_type":null,"agent_type":"builder_v3","ab_force_vids":null,"ab_autotest_advanced_mode":null,"access_type":0}`
	headers := http.Header{"Content-Type": []string{"application/json"}}
	setTraeCommonHeaders(headers, creds)

	resp, err := host.do(pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     traeDetailParamURL,
		Headers: headers,
		Body:    []byte(body),
	})
	if err != nil {
		return nil, fmt.Errorf("Trae detail param request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Trae detail param HTTP %d: %s", resp.StatusCode, safeUpstreamMessage(resp.Body))
	}

	models := parseTraeDetailParamWithConfigs(resp.Body, now)
	models = appendTraeV1RawChatModels(models, now)
	models = appendTraeV3AgentModels(models, now)
	return models, nil
}

func fetchModelsFromModelList(host hostRPC, creds credentials, now int64) ([]pluginapi.ModelInfo, error) {
	headers := http.Header{}
	setTraeCommonHeaders(headers, creds)

	resp, err := host.do(pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     traeModelListURL,
		Headers: headers,
	})
	if err != nil {
		return nil, fmt.Errorf("Trae model list request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Trae model list HTTP %d: %s", resp.StatusCode, safeUpstreamMessage(resp.Body))
	}

	models := parseTraeModels(resp.Body, now)
	models = appendTraeV3AgentModels(models, now)
	return models, nil
}

// parseTraeDetailParamWithConfigs parses the get_detail_param response.
func parseTraeDetailParamWithConfigs(data []byte, now int64) []pluginapi.ModelInfo {
	root := gjson.ParseBytes(data)
	configList := root.Get("config_info_list")
	if !configList.Exists() {
		configList = root.Get("data.config_info_list")
	}
	if !configList.Exists() || !configList.IsArray() {
		return nil
	}

	models := make([]pluginapi.ModelInfo, 0)
	seen := make(map[string]struct{})

	configList.ForEach(func(_, item gjson.Result) bool {
		usage := item.Get("usage").String()
		if usage != "chat_completion" {
			return true
		}
		if !item.Get("config_switch").Bool() {
			return true
		}
		if item.Get("is_invisible_to_user").Bool() {
			return true
		}
		configName := strings.TrimSpace(item.Get("config_name").String())
		if configName == "" {
			return true
		}
		lower := strings.ToLower(configName)
		if strings.HasPrefix(lower, "custom_model_") ||
			strings.HasPrefix(lower, "custom_claude") ||
			strings.HasPrefix(lower, "custom_gemini") {
			return true
		}
		if strings.HasSuffix(lower, "-auto") || strings.HasSuffix(lower, "_auto") {
			return true
		}

		dedupeKey := strings.ToLower(configName)
		if _, exists := seen[dedupeKey]; exists {
			return true
		}
		seen[dedupeKey] = struct{}{}

		displayName := strings.TrimSpace(item.Get("display_config.display_name").String())
		if displayName == "" {
			displayName = configName
		}

		// Read context/max tokens from model_detail_list.0 (native behavior)
		contextLength := item.Get("model_detail_list.0.prompt_max_tokens").Int()
		if contextLength == 0 {
			contextLength = 100000
		}
		maxTokens := item.Get("model_detail_list.0.max_tokens").Int()
		if maxTokens == 0 {
			maxTokens = 16000
		}

		info := pluginapi.ModelInfo{
			ID:                  configName,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                ProviderID,
			DisplayName:         displayName,
			ContextLength:       contextLength,
			MaxCompletionTokens: maxTokens,
			SupportedParameters: []string{"tools"},
		}

		if item.Get("display_config.multimodal").Bool() {
			info.SupportedInputModalities = []string{"text", "image"}
		}

		models = append(models, info)
		return true
	})

	return models
}

// parseTraeModels parses the model_list response.
func parseTraeModels(data []byte, now int64) []pluginapi.ModelInfo {
	root := gjson.ParseBytes(data)
	configs := root.Get("model_configs")
	if !configs.Exists() {
		configs = root.Get("data.model_configs")
	}
	if !configs.Exists() || !configs.IsArray() {
		return nil
	}

	models := make([]pluginapi.ModelInfo, 0)
	seen := make(map[string]struct{})

	configs.ForEach(func(_, item gjson.Result) bool {
		if status := item.Get("status"); status.Exists() && !status.Bool() {
			return true
		}
		id := strings.TrimSpace(item.Get("name").String())
		if id == "" {
			id = strings.TrimSpace(item.Get("id").String())
		}
		if id == "" {
			return true
		}
		// Skip known problematic model
		if strings.EqualFold(id, "Doubao_1_5_thinking_pro") {
			return true
		}

		dedupeKey := strings.ToLower(id)
		if _, exists := seen[dedupeKey]; exists {
			return true
		}
		seen[dedupeKey] = struct{}{}

		displayName := strings.TrimSpace(item.Get("display_name").String())
		if displayName == "" {
			displayName = strings.TrimSpace(item.Get("displayName").String())
		}
		if displayName == "" {
			displayName = id
		}

		contextLength := item.Get("prompt_max_tokens").Int()
		if contextLength == 0 {
			contextLength = item.Get("context_length").Int()
		}
		if contextLength == 0 {
			contextLength = 40000
		}

		models = append(models, pluginapi.ModelInfo{
			ID:                  id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                ProviderID,
			DisplayName:         displayName,
			ContextLength:       contextLength,
			MaxCompletionTokens: 65536,
			SupportedParameters: []string{"tools"},
		})
		return true
	})

	return models
}

// appendTraeNoThinkingModel ensures no_thinking_model is present.
func appendTraeNoThinkingModel(models []pluginapi.ModelInfo, now int64) []pluginapi.ModelInfo {
	for i, m := range models {
		if strings.EqualFold(m.ID, "no_thinking_model") {
			// Remove tools from supported parameters for existing entry
			models[i].SupportedParameters = nil
			return models
		}
	}
	return append(models, pluginapi.ModelInfo{
		ID:                  "no_thinking_model",
		Object:              "model",
		Created:             now,
		OwnedBy:             "trae",
		Type:                ProviderID,
		DisplayName:         "Trae No Thinking Model",
		ContextLength:       40000,
		MaxCompletionTokens: 65536,
	})
}

// appendTraeV1RawChatModels adds V1 raw chat models if not already present.
func appendTraeV1RawChatModels(models []pluginapi.ModelInfo, now int64) []pluginapi.ModelInfo {
	v1Models := []struct {
		id          string
		displayName string
		context     int64
	}{
		{"seed_m8", "Doubao 1.5 Pro", 28000},
		{"deepseek-R1", "DeepSeek Reasoner R1", 40000},
		{"deepseek-V3", "DeepSeek V3", 40000},
		{"deepseek-V3-0324", "DeepSeek V3 0324", 40000},
	}

	for _, v1 := range v1Models {
		if modelExists(models, v1.id) {
			continue
		}
		models = append(models, pluginapi.ModelInfo{
			ID:                  v1.id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                ProviderID,
			DisplayName:         v1.displayName,
			ContextLength:       v1.context,
			MaxCompletionTokens: 65536,
			SupportedParameters: []string{"tools"},
		})
	}
	return models
}

// appendTraeV3AgentModels adds V3 agent models if not already present.
func appendTraeV3AgentModels(models []pluginapi.ModelInfo, now int64) []pluginapi.ModelInfo {
	v3Models := []struct {
		id          string
		displayName string
	}{
		{"glm-4.7", "GLM-4.7"},
		{"glm-5", "GLM-5"},
		{"glm-5.1", "GLM-5.1"},
		{"DeepSeek-V4-Pro", "DeepSeek V4 Pro"},
		{"DeepSeek-V4-Flash", "DeepSeek V4 Flash"},
		{"kimi-k2.6", "Kimi K2.6"},
		{"qwen-3.6-plus", "Qwen 3.6 Plus"},
	}

	for _, v3 := range v3Models {
		if modelExists(models, v3.id) {
			continue
		}
		models = append(models, pluginapi.ModelInfo{
			ID:                  v3.id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                ProviderID,
			DisplayName:         v3.displayName,
			ContextLength:       16000,
			MaxCompletionTokens: 65536,
			SupportedParameters: []string{"tools"},
		})
	}
	return models
}

func modelExists(models []pluginapi.ModelInfo, id string) bool {
	for _, m := range models {
		if strings.EqualFold(m.ID, id) {
			return true
		}
	}
	return false
}
