package trae

import (
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// staticModels returns the full static catalog of Trae model definitions.
func staticModels() []pluginapi.ModelInfo {
	now := time.Now().Unix()
	models := make([]pluginapi.ModelInfo, 0, 24)

	// V1 Raw Chat Models
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
	for _, m := range v1Models {
		models = append(models, pluginapi.ModelInfo{
			ID:                  m.id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                ProviderID,
			DisplayName:         m.displayName,
			ContextLength:       m.context,
			MaxCompletionTokens: 65536,
			SupportedParameters: []string{"tools"},
		})
	}

	// V2 Synthetic
	models = append(models, pluginapi.ModelInfo{
		ID:                  "no_thinking_model",
		Object:              "model",
		Created:             now,
		OwnedBy:             "trae",
		Type:                ProviderID,
		DisplayName:         "Trae No Thinking Model",
		ContextLength:       40000,
		MaxCompletionTokens: 65536,
	})

	// V3 Core Models
	type v3Model struct {
		id          string
		displayName string
		multimodal  bool
	}
	v3Core := []v3Model{
		{"DeepSeek-V4-Pro", "DeepSeek V4 Pro", false},
		{"DeepSeek-V4-Flash", "DeepSeek V4 Flash", false},
		{"Doubao-Seed-2.0-Code", "Doubao-Seed-2.0-Code", true},
		{"glm-5.1", "GLM-5.1", false},
		{"glm-5v-turbo", "GLM-5v-Turbo", true},
		{"kimi-k2.6", "Kimi K2.6", true},
		{"qwen-3.6-plus", "Qwen 3.6 Plus", true},
		{"qwen3-coder", "Qwen3 Coder", false},
		{"minimax-m2.7", "MiniMax M2.7", false},
	}
	for _, m := range v3Core {
		info := pluginapi.ModelInfo{
			ID:                  m.id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                ProviderID,
			DisplayName:         m.displayName,
			ContextLength:       100000,
			MaxCompletionTokens: 16000,
			SupportedParameters: []string{"tools"},
		}
		if m.multimodal {
			info.SupportedInputModalities = []string{"text", "image"}
		}
		models = append(models, info)
	}

	// V3 Optional Models
	v3Optional := []v3Model{
		{"glm-5", "GLM-5", false},
		{"glm-4.7", "GLM-4.7", false},
		{"kimi-k2.5", "Kimi K2.5", true},
		{"kimi-k2", "Kimi K2", false},
		{"qwen-3.5", "Qwen 3.5", true},
		{"doubao_1_8", "Doubao 1.8", true},
		{"Doubao_1_6", "Doubao 1.6", true},
		{"minimax-m2.5", "MiniMax M2.5", false},
	}
	for _, m := range v3Optional {
		info := pluginapi.ModelInfo{
			ID:                  m.id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                ProviderID,
			DisplayName:         m.displayName,
			ContextLength:       100000,
			MaxCompletionTokens: 16000,
			SupportedParameters: []string{"tools"},
		}
		if m.multimodal {
			info.SupportedInputModalities = []string{"text", "image"}
		}
		models = append(models, info)
	}

	return models
}
