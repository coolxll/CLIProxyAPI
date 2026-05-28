// Package registry provides model definitions and lookup helpers for various AI providers.
// Static model metadata is loaded from the embedded models.json file and can be refreshed from network.
package registry

import (
	"strings"
)

const (
	codexBuiltinImageModelID      = "gpt-image-2"
	xaiBuiltinImageModelID        = "grok-imagine-image"
	xaiBuiltinImageQualityModelID = "grok-imagine-image-quality"
	xaiBuiltinVideoModelID        = "grok-imagine-video"
)

// staticModelsJSON mirrors the top-level structure of models.json.
type staticModelsJSON struct {
	Claude      []*ModelInfo `json:"claude"`
	Gemini      []*ModelInfo `json:"gemini"`
	Vertex      []*ModelInfo `json:"vertex"`
	GeminiCLI   []*ModelInfo `json:"gemini-cli"`
	AIStudio    []*ModelInfo `json:"aistudio"`
	CodexFree   []*ModelInfo `json:"codex-free"`
	CodexTeam   []*ModelInfo `json:"codex-team"`
	CodexPlus   []*ModelInfo `json:"codex-plus"`
	CodexPro    []*ModelInfo `json:"codex-pro"`
	Kimi        []*ModelInfo `json:"kimi"`
	Antigravity []*ModelInfo `json:"antigravity"`
	Lingma      []*ModelInfo `json:"lingma"`
	XAI         []*ModelInfo `json:"xai"`
}

// GetClaudeModels returns the standard Claude model definitions.
func GetClaudeModels() []*ModelInfo {
	return cloneModelInfos(getModels().Claude)
}

// GetGeminiModels returns the standard Gemini model definitions.
func GetGeminiModels() []*ModelInfo {
	return cloneModelInfos(getModels().Gemini)
}

// GetGeminiVertexModels returns Gemini model definitions for Vertex AI.
func GetGeminiVertexModels() []*ModelInfo {
	return cloneModelInfos(getModels().Vertex)
}

// GetGeminiCLIModels returns Gemini model definitions for the Gemini CLI.
func GetGeminiCLIModels() []*ModelInfo {
	return cloneModelInfos(getModels().GeminiCLI)
}

// GetAIStudioModels returns model definitions for AI Studio.
func GetAIStudioModels() []*ModelInfo {
	return cloneModelInfos(getModels().AIStudio)
}

// GetCodexFreeModels returns model definitions for the Codex free plan tier.
func GetCodexFreeModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexFree))
}

// GetCodexTeamModels returns model definitions for the Codex team plan tier.
func GetCodexTeamModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexTeam))
}

// GetCodexPlusModels returns model definitions for the Codex plus plan tier.
func GetCodexPlusModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexPlus))
}

// GetCodexProModels returns model definitions for the Codex pro plan tier.
func GetCodexProModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexPro))
}

// GetKimiModels returns the standard Kimi (Moonshot AI) model definitions.
func GetKimiModels() []*ModelInfo {
	return cloneModelInfos(getModels().Kimi)
}

// GetTraeModels returns the standard Trae model definitions.
func GetTraeModels() []*ModelInfo {
	return cloneModelInfos(traeModelInfos())
}

// GetAntigravityModels returns the standard Antigravity model definitions.
func GetAntigravityModels() []*ModelInfo {
	return cloneModelInfos(getModels().Antigravity)
}

// GetLingmaModels returns the standard Lingma model definitions.
func GetLingmaModels() []*ModelInfo {
	return cloneModelInfos(getModels().Lingma)
}

// GetXAIModels returns the standard xAI Grok model definitions.
func GetXAIModels() []*ModelInfo {
	return WithXAIBuiltins(cloneModelInfos(getModels().XAI))
}

// WithCodexBuiltins injects hard-coded Codex-only model definitions that should
// not depend on remote models.json updates. Built-ins replace any matching IDs
// already present in the provided slice.
func WithCodexBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models, codexBuiltinImageModelInfo())
}

// WithXAIBuiltins injects hard-coded xAI image/video model definitions that should
// not depend on remote models.json updates.
func WithXAIBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models, xaiBuiltinImageModelInfo(), xaiBuiltinImageQualityModelInfo(), xaiBuiltinVideoModelInfo())
}

func codexBuiltinImageModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          codexBuiltinImageModelID,
		Object:      "model",
		Created:     1704067200, // 2024-01-01
		OwnedBy:     "openai",
		Type:        "openai",
		DisplayName: "GPT Image 2",
		Version:     codexBuiltinImageModelID,
	}
}

func xaiBuiltinImageModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinImageModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Image",
		Name:        xaiBuiltinImageModelID,
		Description: "xAI Grok image generation model.",
	}
}

func xaiBuiltinImageQualityModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinImageQualityModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Image Quality",
		Name:        xaiBuiltinImageQualityModelID,
		Description: "xAI Grok higher-fidelity image generation model.",
	}
}

func xaiBuiltinVideoModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinVideoModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Video",
		Name:        xaiBuiltinVideoModelID,
		Description: "xAI Grok video generation model.",
	}
}

func traeModelInfos() []*ModelInfo {
	models := []struct {
		id          string
		displayName string
		context     int
		maxTokens   int
		multimodal  bool
	}{
		// V1 raw chat models
		{"seed_m8", "Doubao 1.5 Pro", 28000, 65536, false},
		{"deepseek-R1", "DeepSeek Reasoner R1", 40000, 65536, false},
		{"deepseek-V3", "DeepSeek V3", 40000, 65536, false},
		{"deepseek-V3-0324", "DeepSeek V3 0324", 40000, 65536, false},
		// V2 synthetic
		{"no_thinking_model", "Trae No Thinking Model", 40000, 65536, false},
		// V3 core models
		{"DeepSeek-V4-Pro", "DeepSeek V4 Pro", 100000, 16000, false},
		{"DeepSeek-V4-Flash", "DeepSeek V4 Flash", 100000, 16000, false},
		{"Doubao-Seed-2.0-Code", "Doubao-Seed-2.0-Code", 100000, 16000, true},
		{"glm-5.1", "GLM-5.1", 100000, 16000, false},
		{"glm-5v-turbo", "GLM-5v-Turbo", 100000, 16000, true},
		{"kimi-k2.6", "Kimi K2.6", 100000, 16000, true},
		{"qwen-3.6-plus", "Qwen 3.6 Plus", 100000, 16000, true},
		{"qwen3-coder", "Qwen3 Coder", 100000, 16000, false},
		{"minimax-m2.7", "MiniMax M2.7", 100000, 16000, false},
		// V3 optional models
		{"glm-5", "GLM-5", 100000, 16000, false},
		{"glm-4.7", "GLM-4.7", 100000, 16000, false},
		{"kimi-k2.5", "Kimi K2.5", 100000, 16000, true},
		{"kimi-k2", "Kimi K2", 100000, 16000, false},
		{"qwen-3.5", "Qwen 3.5", 100000, 16000, true},
		{"doubao_1_8", "Doubao 1.8", 100000, 16000, true},
		{"Doubao_1_6", "Doubao 1.6", 100000, 16000, true},
		{"minimax-m2.5", "MiniMax M2.5", 100000, 16000, false},
	}

	out := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		info := &ModelInfo{
			ID:                  model.id,
			Object:              "model",
			Created:             1704067200,
			OwnedBy:             "trae",
			Type:                "trae",
			DisplayName:         model.displayName,
			Name:                model.id,
			ContextLength:       model.context,
			MaxCompletionTokens: model.maxTokens,
			SupportedParameters: []string{"tools"},
		}
		if model.multimodal {
			info.SupportedInputModalities = []string{"text", "image"}
		}
		out = append(out, info)
	}
	return out
}

func upsertModelInfos(models []*ModelInfo, extras ...*ModelInfo) []*ModelInfo {
	if len(extras) == 0 {
		return models
	}

	extraIDs := make(map[string]struct{}, len(extras))
	extraList := make([]*ModelInfo, 0, len(extras))
	for _, extra := range extras {
		if extra == nil {
			continue
		}
		id := strings.TrimSpace(extra.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := extraIDs[key]; exists {
			continue
		}
		extraIDs[key] = struct{}{}
		extraList = append(extraList, cloneModelInfo(extra))
	}

	if len(extraList) == 0 {
		return models
	}

	filtered := make([]*ModelInfo, 0, len(models)+len(extraList))
	for _, model := range models {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if _, exists := extraIDs[strings.ToLower(id)]; exists {
			continue
		}
		filtered = append(filtered, model)
	}

	filtered = append(filtered, extraList...)
	return filtered
}

// cloneModelInfos returns a shallow copy of the slice with each element deep-cloned.
func cloneModelInfos(models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := make([]*ModelInfo, len(models))
	for i, m := range models {
		out[i] = cloneModelInfo(m)
	}
	return out
}

// GetStaticModelDefinitionsByChannel returns static model definitions for a given channel/provider.
// It returns nil when the channel is unknown.
//
// Supported channels:
//   - claude
//   - gemini
//   - vertex
//   - gemini-cli
//   - aistudio
//   - codex
//   - kimi
//   - trae
//   - antigravity
//   - lingma
//   - xai
func GetStaticModelDefinitionsByChannel(channel string) []*ModelInfo {
	key := strings.ToLower(strings.TrimSpace(channel))
	switch key {
	case "claude":
		return GetClaudeModels()
	case "gemini":
		return GetGeminiModels()
	case "vertex":
		return GetGeminiVertexModels()
	case "gemini-cli":
		return GetGeminiCLIModels()
	case "aistudio":
		return GetAIStudioModels()
	case "codex":
		return GetCodexProModels()
	case "kimi":
		return GetKimiModels()
	case "trae":
		return GetTraeModels()
	case "antigravity":
		return GetAntigravityModels()
	case "lingma":
		return GetLingmaModels()
	case "xai", "x-ai", "grok":
		return GetXAIModels()
	default:
		return nil
	}
}

// LookupStaticModelInfo searches all static model definitions for a model by ID.
// Returns nil if no matching model is found.
func LookupStaticModelInfo(modelID string) *ModelInfo {
	if modelID == "" {
		return nil
	}

	data := getModels()
	allModels := [][]*ModelInfo{
		data.Claude,
		data.Gemini,
		data.Vertex,
		data.GeminiCLI,
		data.AIStudio,
		data.CodexPro,
		data.Kimi,
		traeModelInfos(),
		data.Antigravity,
		data.Lingma,
		data.XAI,
	}
	for _, models := range allModels {
		for _, m := range models {
			if m != nil && strings.EqualFold(m.ID, modelID) {
				return cloneModelInfo(m)
			}
		}
	}

	return nil
}
