package trae

import (
	"testing"
)

func TestResolveTraeProtocol(t *testing.T) {
	tests := []struct {
		name              string
		model             string
		metadata          map[string]any
		wantProtocol      string
		wantUpstreamModel string
	}{
		{
			name:              "V1 model with prefix",
			model:             "trae-v1/seed_m8",
			metadata:          nil,
			wantProtocol:      traeProtocolV1,
			wantUpstreamModel: "seed_m8",
		},
		{
			name:              "V1 known model",
			model:             "seed_m8",
			metadata:          nil,
			wantProtocol:      traeProtocolV1,
			wantUpstreamModel: "seed_m8",
		},
		{
			name:              "V2 model with prefix",
			model:             "trae-v2/no_thinking_model",
			metadata:          nil,
			wantProtocol:      traeProtocolV2,
			wantUpstreamModel: "no_thinking_model",
		},
		{
			name:              "V2 known model",
			model:             "no_thinking_model",
			metadata:          nil,
			wantProtocol:      traeProtocolV2,
			wantUpstreamModel: "no_thinking_model",
		},
		{
			name:              "V3 model with prefix",
			model:             "trae-v3/glm-5",
			metadata:          nil,
			wantProtocol:      traeProtocolV3,
			wantUpstreamModel: "glm-5",
		},
		{
			name:              "V3 default model",
			model:             "glm-5",
			metadata:          nil,
			wantProtocol:      traeProtocolV3,
			wantUpstreamModel: "glm-5",
		},
		{
			name:  "metadata override protocol",
			model: "some-model",
			metadata: map[string]any{
				traeProtocolMeta: "v2",
			},
			wantProtocol:      traeProtocolV2,
			wantUpstreamModel: "some-model",
		},
		{
			name:  "metadata override model name",
			model: "some-model",
			metadata: map[string]any{
				traeModelNameMeta: "override-model",
			},
			wantProtocol:      traeProtocolV3,
			wantUpstreamModel: "some-model",
		},
		{
			name:              "Solo model with prefix",
			model:             "trae-solo/glm-5",
			metadata:          nil,
			wantProtocol:      traeProtocolSolo,
			wantUpstreamModel: "glm-5",
		},
		{
			name:              "Solo known model - DeepSeek-V4-Flash",
			model:             "DeepSeek-V4-Flash",
			metadata:          nil,
			wantProtocol:      traeProtocolSolo,
			wantUpstreamModel: "DeepSeek-V4-Flash",
		},
		{
			name:              "Solo known model - DeepSeek-V4-Flash-Official",
			model:             "DeepSeek-V4-Flash-Official",
			metadata:          nil,
			wantProtocol:      traeProtocolSolo,
			wantUpstreamModel: "DeepSeek-V4-Flash-Official",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProtocol, gotUpstreamModel := resolveTraeProtocol(tt.model, tt.metadata)
			if gotProtocol != tt.wantProtocol {
				t.Errorf("resolveTraeProtocol() protocol = %v, want %v", gotProtocol, tt.wantProtocol)
			}
			if gotUpstreamModel != tt.wantUpstreamModel {
				t.Errorf("resolveTraeProtocol() upstreamModel = %v, want %v", gotUpstreamModel, tt.wantUpstreamModel)
			}
		})
	}
}

func TestResolveModelConfig(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		wantModelName  string
		wantConfigName string
	}{
		{
			name:           "known model - glm-5",
			model:          "glm-5",
			wantModelName:  "glm-5",
			wantConfigName: "glm-5",
		},
		{
			name:           "known model - deepseek-v4-pro",
			model:          "deepseek-v4-pro",
			wantModelName:  "DeepSeek-V4-Pro",
			wantConfigName: "DeepSeek-V4-Pro",
		},
		{
			name:           "unknown model",
			model:          "unknown-model",
			wantModelName:  "unknown-model",
			wantConfigName: "unknown-model",
		},
		{
			name:           "removed family alias glm passes through",
			model:          "glm",
			wantModelName:  "glm",
			wantConfigName: "glm",
		},
		{
			name:           "removed family alias kimi passes through",
			model:          "kimi",
			wantModelName:  "kimi",
			wantConfigName: "kimi",
		},
		{
			name:           "removed family alias qwen passes through",
			model:          "qwen",
			wantModelName:  "qwen",
			wantConfigName: "qwen",
		},
		{
			name:           "removed family alias deepseek passes through",
			model:          "deepseek",
			wantModelName:  "deepseek",
			wantConfigName: "deepseek",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveModelConfig(tt.model)
			if got.ModelName != tt.wantModelName {
				t.Errorf("resolveModelConfig() ModelName = %v, want %v", got.ModelName, tt.wantModelName)
			}
			if got.ConfigName != tt.wantConfigName {
				t.Errorf("resolveModelConfig() ConfigName = %v, want %v", got.ConfigName, tt.wantConfigName)
			}
		})
	}
}

func TestResolveRawChatModelConfig(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		protocol       string
		wantModelName  string
		wantConfigName string
	}{
		{
			name:           "V1 known model",
			model:          "seed_m8",
			protocol:       traeProtocolV1,
			wantModelName:  "seed_m8",
			wantConfigName: "",
		},
		{
			name:           "V1 unknown model",
			model:          "test-model",
			protocol:       traeProtocolV1,
			wantModelName:  "test-model",
			wantConfigName: "",
		},
		{
			name:           "V2 known model",
			model:          "no_thinking_model",
			protocol:       traeProtocolV2,
			wantModelName:  "no_thinking_model",
			wantConfigName: "title_generation",
		},
		{
			name:           "V2 unknown model passes through honestly",
			model:          "test-model",
			protocol:       traeProtocolV2,
			wantModelName:  "test-model",
			wantConfigName: "test-model",
		},
		{
			name:           "V2 removed fake alias gpt-4o passes through",
			model:          "gpt-4o",
			protocol:       traeProtocolV2,
			wantModelName:  "gpt-4o",
			wantConfigName: "gpt-4o",
		},
		{
			name:           "V2 removed fake alias claude-3-5-sonnet passes through",
			model:          "claude-3-5-sonnet",
			protocol:       traeProtocolV2,
			wantModelName:  "claude-3-5-sonnet",
			wantConfigName: "claude-3-5-sonnet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveRawChatModelConfig(tt.model, tt.protocol)
			if got.ModelName != tt.wantModelName {
				t.Errorf("resolveRawChatModelConfig() ModelName = %v, want %v", got.ModelName, tt.wantModelName)
			}
			if got.ConfigName != tt.wantConfigName {
				t.Errorf("resolveRawChatModelConfig() ConfigName = %v, want %v", got.ConfigName, tt.wantConfigName)
			}
		})
	}
}
