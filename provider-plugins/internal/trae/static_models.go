package trae

import (
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// staticModels returns the modern active catalog of Trae model definitions.
func staticModels() []pluginapi.ModelInfo {
	now := time.Now().Unix()
	models := make([]pluginapi.ModelInfo, 0, 24)

	// Modern Active Models (Raw v2 / Solo / V3)
	type modernModel struct {
		id          string
		displayName string
		context     int64
		multimodal  bool
	}
	activeList := []modernModel{
		// Aliases
		{"auto", "Auto (GLM-5.2)", 128000, true},
		{"claude-3-5-sonnet", "Claude 3.5 Sonnet (GLM-5.2)", 128000, true},
		{"gpt-4o", "GPT-4o (GLM-5.2)", 128000, true},

		// GLM
		{"glm-5.3", "GLM-5.3", 128000, true},
		{"glm-5.2", "GLM-5.2", 128000, true},
		{"glm-5v-turbo", "GLM-5v-Turbo", 128000, true},

		// DeepSeek
		{"DeepSeek-V4-Pro-Official", "DeepSeek V4 Pro 正式版", 128000, false},
		{"DeepSeek-V4-Pro", "DeepSeek V4 Pro", 128000, false},
		{"DeepSeek-V4-Flash-Official", "DeepSeek V4 Flash 正式版", 128000, false},
		{"DeepSeek-V4-Flash", "DeepSeek V4 Flash", 128000, false},

		// Doubao Seed
		{"Doubao-Seed-2.1-Pro", "Seed-2.1-Pro", 128000, true},
		{"Doubao-Seed-2.1-Turbo", "Seed-2.1-Turbo", 128000, true},
		{"Doubao-Seed-Code", "Seed-Code", 128000, false},
		{"Doubao-Seed-Evolving", "Seed-Evolving", 128000, false},

		// Kimi
		{"kimi-k3", "Kimi K3", 128000, true},
		{"kimi-k2.7-code", "Kimi K2.7 Code", 128000, false},

		// Frontier
		{"minimax-m3", "MiniMax M3", 128000, false},
		{"qwen3.8-max", "Qwen 3.8 Max", 128000, true},
		{"qwen-3.7-plus", "Qwen 3.7 Plus", 128000, true},
		{"qwen3-coder", "Qwen3 Coder", 128000, false},
	}
	for _, m := range activeList {
		info := pluginapi.ModelInfo{
			ID:                  m.id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                ProviderID,
			DisplayName:         m.displayName,
			ContextLength:       m.context,
			MaxCompletionTokens: 65536,
			SupportedParameters: []string{"tools"},
		}
		if m.multimodal {
			info.SupportedInputModalities = []string{"text", "image"}
		}
		models = append(models, info)
	}

	return models
}
