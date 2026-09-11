package trae

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

// nativeParseTraeModels is a copy of the native parseTraeModels function
// for parity testing. This avoids importing the native package which would create
// a circular dependency. The function is kept in sync with
// internal/runtime/executor/trae_models.go:247-296.
func nativeParseTraeModels(data []byte, now int64) []nativeModelInfo {
	root := gjson.ParseBytes(data)
	configs := root.Get("model_configs")
	if !configs.Exists() {
		configs = root.Get("data.model_configs")
	}
	if !configs.Exists() || !configs.IsArray() {
		return nil
	}

	models := make([]nativeModelInfo, 0, len(configs.Array()))
	seen := make(map[string]struct{})
	for _, item := range configs.Array() {
		if status := item.Get("status"); status.Exists() && !status.Bool() {
			continue
		}
		id := strings.TrimSpace(item.Get("name").String())
		if id == "" {
			continue
		}
		if strings.EqualFold(id, "Doubao_1_5_thinking_pro") {
			continue
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		displayName := nativeFirstNonEmpty(item.Get("display_name").String(), item.Get("displayName").String(), id)
		contextLength := item.Get("prompt_max_tokens").Int()
		if contextLength <= 0 {
			contextLength = item.Get("context_length").Int()
		}
		model := nativeModelInfo{
			ID:                  id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                "trae",
			DisplayName:         displayName,
			Name:                id,
			ContextLength:       contextLength,
			MaxCompletionTokens: 65536,
			SupportedParameters: []string{"tools"},
		}
		models = append(models, model)
	}
	return models
}

// nativeParseTraeDetailParamWithConfigs is a copy of the native parseTraeDetailParamWithConfigs function
// for parity testing. The function is kept in sync with
// internal/runtime/executor/trae_models.go:374-452.
func nativeParseTraeDetailParamWithConfigs(data []byte, now int64) ([]nativeModelInfo, map[string]traeDetailModelConfig) {
	root := gjson.ParseBytes(data)
	configList := root.Get("config_info_list")
	if !configList.Exists() {
		configList = root.Get("data.config_info_list")
	}
	if !configList.Exists() || !configList.IsArray() {
		return nil, nil
	}

	models := make([]nativeModelInfo, 0)
	detailConfigs := make(map[string]traeDetailModelConfig)
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
		detail := item.Get("model_detail_list.0")
		modelName := strings.TrimSpace(detail.Get("model_name").String())
		if modelName == "" {
			return true
		}
		if _, exists := seen[lower]; exists {
			return true
		}
		seen[lower] = struct{}{}

		displayName := strings.TrimSpace(item.Get("display_config.display_name").String())
		if displayName == "" {
			displayName = configName
		}

		contextLength := detail.Get("prompt_max_tokens").Int()
		maxTokens := detail.Get("max_tokens").Int()
		if maxTokens <= 0 {
			maxTokens = 16000
		}

		model := nativeModelInfo{
			ID:                  configName,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                "trae",
			DisplayName:         displayName,
			Name:                configName,
			ContextLength:       contextLength,
			MaxCompletionTokens: maxTokens,
			SupportedParameters: []string{"tools"},
		}
		models = append(models, model)
		detailConfigs[lower] = traeDetailModelConfig{
			ModelName:  modelName,
			ConfigName: configName,
		}
		return true
	})

	return models, detailConfigs
}

func nativeFirstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type nativeModelInfo struct {
	ID                  string
	Object              string
	Created             int64
	OwnedBy             string
	Type                string
	DisplayName         string
	Name                string
	ContextLength       int64
	MaxCompletionTokens int64
	SupportedParameters []string
}

// TestParseTraeModelsParity verifies plugin and native produce identical
// model parsing results for the model_list API response.
func TestParseTraeModelsParity(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "basic model list",
			data: []byte(`{
				"model_configs": [
					{"name": "deepseek-r1", "display_name": "DeepSeek R1", "prompt_max_tokens": 64000, "status": true},
					{"name": "deepseek-v3", "display_name": "DeepSeek V3", "prompt_max_tokens": 64000, "status": true},
					{"name": "glm-5", "display_name": "GLM-5", "prompt_max_tokens": 128000, "status": true}
				]
			}`),
		},
		{
			name: "with data wrapper",
			data: []byte(`{
				"data": {
					"model_configs": [
						{"name": "deepseek-r1", "display_name": "DeepSeek R1", "prompt_max_tokens": 64000, "status": true},
						{"name": "deepseek-v3", "display_name": "DeepSeek V3", "prompt_max_tokens": 64000, "status": true}
					]
				}
			}`),
		},
		{
			name: "with disabled models",
			data: []byte(`{
				"model_configs": [
					{"name": "deepseek-r1", "display_name": "DeepSeek R1", "prompt_max_tokens": 64000, "status": true},
					{"name": "disabled-model", "display_name": "Disabled", "prompt_max_tokens": 64000, "status": false},
					{"name": "glm-5", "display_name": "GLM-5", "prompt_max_tokens": 128000, "status": true}
				]
			}`),
		},
		{
			name: "with duplicates",
			data: []byte(`{
				"model_configs": [
					{"name": "deepseek-r1", "display_name": "DeepSeek R1", "prompt_max_tokens": 64000, "status": true},
					{"name": "DeepSeek-R1", "display_name": "DeepSeek R1 Duplicate", "prompt_max_tokens": 64000, "status": true},
					{"name": "glm-5", "display_name": "GLM-5", "prompt_max_tokens": 128000, "status": true}
				]
			}`),
		},
		{
			name: "with Doubao_1_5_thinking_pro",
			data: []byte(`{
				"model_configs": [
					{"name": "deepseek-r1", "display_name": "DeepSeek R1", "prompt_max_tokens": 64000, "status": true},
					{"name": "Doubao_1_5_thinking_pro", "display_name": "Doubao", "prompt_max_tokens": 64000, "status": true},
					{"name": "glm-5", "display_name": "GLM-5", "prompt_max_tokens": 128000, "status": true}
				]
			}`),
		},
		{
			name: "with context_length fallback",
			data: []byte(`{
				"model_configs": [
					{"name": "deepseek-r1", "display_name": "DeepSeek R1", "context_length": 32000, "status": true},
					{"name": "glm-5", "display_name": "GLM-5", "prompt_max_tokens": 128000, "status": true}
				]
			}`),
		},
		{
			name: "empty model list",
			data: []byte(`{"model_configs": []}`),
		},
		{
			name: "missing model_configs",
			data: []byte(`{"other_field": "value"}`),
		},
	}

	now := int64(1705000000)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pluginModelss := parseTraeModels(tt.data, now)
			nativeModels := nativeParseTraeModels(tt.data, now)

			if len(pluginModelss) != len(nativeModels) {
				t.Errorf("model count mismatch: plugin=%d, native=%d", len(pluginModelss), len(nativeModels))
				return
			}

			for i := range pluginModelss {
				plugin := pluginModelss[i]
				native := nativeModels[i]

				if plugin.ID != native.ID {
					t.Errorf("model[%d].ID mismatch: plugin=%q, native=%q", i, plugin.ID, native.ID)
				}
				if plugin.DisplayName != native.DisplayName {
					t.Errorf("model[%d].DisplayName mismatch: plugin=%q, native=%q", i, plugin.DisplayName, native.DisplayName)
				}
				if plugin.ContextLength != native.ContextLength {
					t.Errorf("model[%d].ContextLength mismatch: plugin=%d, native=%d", i, plugin.ContextLength, native.ContextLength)
				}
				if plugin.MaxCompletionTokens != native.MaxCompletionTokens {
					t.Errorf("model[%d].MaxCompletionTokens mismatch: plugin=%d, native=%d", i, plugin.MaxCompletionTokens, native.MaxCompletionTokens)
				}
				if len(plugin.SupportedParameters) != len(native.SupportedParameters) {
					t.Errorf("model[%d].SupportedParameters length mismatch: plugin=%d, native=%d", i, len(plugin.SupportedParameters), len(native.SupportedParameters))
				}
			}
		})
	}
}

// TestParseTraeDetailParamWithConfigsParity verifies plugin and native produce identical
// model parsing results for the get_detail_param API response.
func TestParseTraeDetailParamWithConfigsParity(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "basic config list",
			data: []byte(`{
				"config_info_list": [
					{
						"config_name": "deepseek-r1",
						"display_config": {"display_name": "DeepSeek R1"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					},
					{
						"config_name": "glm-5",
						"display_config": {"display_name": "GLM-5"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 128000, "completion_max_tokens": 8192}]
					}
				]
			}`),
		},
		{
			name: "with data wrapper",
			data: []byte(`{
				"data": {
					"config_info_list": [
						{
							"config_name": "deepseek-r1",
							"display_config": {"display_name": "DeepSeek R1"},
							"usage": "chat_completion",
							"config_switch": true,
							"is_invisible_to_user": false,
							"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
						}
					]
				}
			}`),
		},
		{
			name: "filter non-chat_completion",
			data: []byte(`{
				"config_info_list": [
					{
						"config_name": "deepseek-r1",
						"display_config": {"display_name": "DeepSeek R1"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					},
					{
						"config_name": "embedding-model",
						"display_config": {"display_name": "Embedding"},
						"usage": "embedding",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 8000, "completion_max_tokens": 0}]
					}
				]
			}`),
		},
		{
			name: "filter disabled configs",
			data: []byte(`{
				"config_info_list": [
					{
						"config_name": "deepseek-r1",
						"display_config": {"display_name": "DeepSeek R1"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					},
					{
						"config_name": "disabled-model",
						"display_config": {"display_name": "Disabled"},
						"usage": "chat_completion",
						"config_switch": false,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					}
				]
			}`),
		},
		{
			name: "filter invisible models",
			data: []byte(`{
				"config_info_list": [
					{
						"config_name": "deepseek-r1",
						"display_config": {"display_name": "DeepSeek R1"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					},
					{
						"config_name": "invisible-model",
						"display_config": {"display_name": "Invisible"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": true,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					}
				]
			}`),
		},
		{
			name: "filter custom models",
			data: []byte(`{
				"config_info_list": [
					{
						"config_name": "deepseek-r1",
						"display_config": {"display_name": "DeepSeek R1"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					},
					{
						"config_name": "custom_model_test",
						"display_config": {"display_name": "Custom"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					},
					{
						"config_name": "custom_claude_test",
						"display_config": {"display_name": "Custom Claude"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					}
				]
			}`),
		},
		{
			name: "filter auto models",
			data: []byte(`{
				"config_info_list": [
					{
						"config_name": "deepseek-r1",
						"display_config": {"display_name": "DeepSeek R1"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					},
					{
						"config_name": "model-auto",
						"display_config": {"display_name": "Auto"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					},
					{
						"config_name": "model_auto",
						"display_config": {"display_name": "Auto Underscore"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					}
				]
			}`),
		},
		{
			name: "with duplicates",
			data: []byte(`{
				"config_info_list": [
					{
						"config_name": "deepseek-r1",
						"display_config": {"display_name": "DeepSeek R1"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					},
					{
						"config_name": "DeepSeek-R1",
						"display_config": {"display_name": "DeepSeek R1 Duplicate"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": [{"prompt_max_tokens": 64000, "completion_max_tokens": 8192}]
					}
				]
			}`),
		},
		{
			name: "default token values",
			data: []byte(`{
				"config_info_list": [
					{
						"config_name": "deepseek-r1",
						"display_config": {"display_name": "DeepSeek R1"},
						"usage": "chat_completion",
						"config_switch": true,
						"is_invisible_to_user": false,
						"model_detail_list": []
					}
				]
			}`),
		},
		{
			name: "empty config list",
			data: []byte(`{"config_info_list": []}`),
		},
		{
			name: "missing config_info_list",
			data: []byte(`{"other_field": "value"}`),
		},
	}

	now := int64(1705000000)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pluginModels, pluginConfigs := parseTraeDetailParamWithConfigs(tt.data, now)
			nativeModels, nativeConfigs := nativeParseTraeDetailParamWithConfigs(tt.data, now)

			if len(pluginModels) != len(nativeModels) {
				t.Errorf("model count mismatch: plugin=%d, native=%d", len(pluginModels), len(nativeModels))
				return
			}

			for i := range pluginModels {
				plugin := pluginModels[i]
				native := nativeModels[i]

				if plugin.ID != native.ID {
					t.Errorf("model[%d].ID mismatch: plugin=%q, native=%q", i, plugin.ID, native.ID)
				}
				if plugin.DisplayName != native.DisplayName {
					t.Errorf("model[%d].DisplayName mismatch: plugin=%q, native=%q", i, plugin.DisplayName, native.DisplayName)
				}
				if plugin.ContextLength != native.ContextLength {
					t.Errorf("model[%d].ContextLength mismatch: plugin=%d, native=%d", i, plugin.ContextLength, native.ContextLength)
				}
				if plugin.MaxCompletionTokens != native.MaxCompletionTokens {
					t.Errorf("model[%d].MaxCompletionTokens mismatch: plugin=%d, native=%d", i, plugin.MaxCompletionTokens, native.MaxCompletionTokens)
				}
				if len(plugin.SupportedParameters) != len(native.SupportedParameters) {
					t.Errorf("model[%d].SupportedParameters length mismatch: plugin=%d, native=%d", i, len(plugin.SupportedParameters), len(native.SupportedParameters))
				}
			}
			if len(pluginConfigs) != len(nativeConfigs) {
				t.Errorf("detail config count mismatch: plugin=%d, native=%d", len(pluginConfigs), len(nativeConfigs))
			}
			for name, nativeConfig := range nativeConfigs {
				if pluginConfig := pluginConfigs[name]; pluginConfig != nativeConfig {
					t.Errorf("detail config %q mismatch: plugin=%+v, native=%+v", name, pluginConfig, nativeConfig)
				}
			}
		})
	}
}

// TestAppendTraeNoThinkingModelParity verifies plugin and native produce identical
// results when appending the no_thinking_model.
func TestAppendTraeNoThinkingModelParity(t *testing.T) {
	now := int64(1705000000)

	tests := []struct {
		name   string
		models []pluginapi.ModelInfo
	}{
		{
			name:   "empty list",
			models: []pluginapi.ModelInfo{},
		},
		{
			name: "without no_thinking_model",
			models: []pluginapi.ModelInfo{
				{ID: "deepseek-r1", DisplayName: "DeepSeek R1"},
				{ID: "glm-5", DisplayName: "GLM-5"},
			},
		},
		{
			name: "with no_thinking_model",
			models: []pluginapi.ModelInfo{
				{ID: "deepseek-r1", DisplayName: "DeepSeek R1"},
				{ID: "no_thinking_model", DisplayName: "No Thinking", SupportedParameters: []string{"tools"}},
				{ID: "glm-5", DisplayName: "GLM-5"},
			},
		},
		{
			name: "with NO_THINKING_MODEL (case insensitive)",
			models: []pluginapi.ModelInfo{
				{ID: "deepseek-r1", DisplayName: "DeepSeek R1"},
				{ID: "NO_THINKING_MODEL", DisplayName: "No Thinking", SupportedParameters: []string{"tools"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pluginResult := appendTraeNoThinkingModel(tt.models, now)

			// Convert to native format for comparison
			nativeModels := make([]nativeModelInfo, len(tt.models))
			for i, m := range tt.models {
				nativeModels[i] = nativeModelInfo{
					ID:                  m.ID,
					DisplayName:         m.DisplayName,
					SupportedParameters: m.SupportedParameters,
				}
			}
			nativeResult := nativeAppendTraeNoThinkingModel(nativeModels, now)

			if len(pluginResult) != len(nativeResult) {
				t.Errorf("result count mismatch: plugin=%d, native=%d", len(pluginResult), len(nativeResult))
				return
			}

			for i := range pluginResult {
				plugin := pluginResult[i]
				native := nativeResult[i]

				if plugin.ID != native.ID {
					t.Errorf("model[%d].ID mismatch: plugin=%q, native=%q", i, plugin.ID, native.ID)
				}
				if len(plugin.SupportedParameters) != len(native.SupportedParameters) {
					t.Errorf("model[%d].SupportedParameters length mismatch: plugin=%d, native=%d", i, len(plugin.SupportedParameters), len(native.SupportedParameters))
				}
			}
		})
	}
}

// nativeAppendTraeNoThinkingModel is a copy of the native appendTraeNoThinkingModel function.
func nativeAppendTraeNoThinkingModel(models []nativeModelInfo, now int64) []nativeModelInfo {
	for i := range models {
		if strings.EqualFold(strings.TrimSpace(models[i].ID), "no_thinking_model") {
			models[i].SupportedParameters = nativeRemoveSupportedParameter(models[i].SupportedParameters, "tools")
			return models
		}
	}
	return append(models, nativeModelInfo{
		ID:                  "no_thinking_model",
		Object:              "model",
		Created:             now,
		OwnedBy:             "trae",
		Type:                "trae",
		DisplayName:         "Trae No Thinking Model",
		Name:                "no_thinking_model",
		ContextLength:       40000,
		MaxCompletionTokens: 65536,
	})
}

func nativeRemoveSupportedParameter(parameters []string, parameter string) []string {
	filtered := parameters[:0]
	for _, current := range parameters {
		if strings.EqualFold(strings.TrimSpace(current), parameter) {
			continue
		}
		filtered = append(filtered, current)
	}
	return filtered
}
