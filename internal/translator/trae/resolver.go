package trae

import (
	"strings"
)

// ModelConfig wraps the model name and config name required by Trae backend.
type ModelConfig struct {
	ModelName  string
	ConfigName string
}

// ResolveRawChatModelConfig maps a requested model to Trae llm_raw_chat model/config names.
func ResolveRawChatModelConfig(requested, protocol string) ModelConfig {
	key := strings.ToLower(strings.TrimSpace(requested))
	key = strings.TrimPrefix(key, "trae/")
	key = strings.TrimPrefix(key, "trae-v1/")
	key = strings.TrimPrefix(key, "trae-v2/")
	key = strings.TrimPrefix(key, "raw-v1/")
	key = strings.TrimPrefix(key, "raw-v2/")
	key = strings.TrimPrefix(key, "v1/")
	key = strings.TrimPrefix(key, "v2/")

	if protocol == "v1" {
		exact := map[string]ModelConfig{
			"seed_m8":           {ModelName: "seed_m8"},
			"doubao-1.5-pro":    {ModelName: "seed_m8"},
			"deepseek-r1":       {ModelName: "deepseek-R1"},
			"deepseek-v3":       {ModelName: "deepseek-V3"},
			"deepseek-v3-0324":  {ModelName: "deepseek-V3-0324"},
			"deepseek-reasoner": {ModelName: "deepseek-R1"},
			"r1":                {ModelName: "deepseek-R1"},
			"reasoner":          {ModelName: "deepseek-R1"},
			"deepseek":          {ModelName: "deepseek-V3"},
			"v3":                {ModelName: "deepseek-V3"},
			"v3-0324":           {ModelName: "deepseek-V3-0324"},
		}
		if cfg, ok := exact[key]; ok {
			return cfg
		}
		return ModelConfig{ModelName: requested}
	}

	exact := map[string]ModelConfig{
		"no_thinking_model": {ModelName: "no_thinking_model", ConfigName: "title_generation"},
		"no-thinking":       {ModelName: "no_thinking_model", ConfigName: "title_generation"},
	}
	if cfg, ok := exact[key]; ok {
		return cfg
	}
	return ModelConfig{ModelName: "no_thinking_model", ConfigName: "title_generation"}
}

// ResolveModelConfig maps a user-requested model to a valid Trae ModelConfig.
func ResolveModelConfig(requested string) ModelConfig {
	key := strings.ToLower(strings.TrimSpace(requested))
	key = strings.TrimPrefix(key, "trae/")

	exact := map[string]ModelConfig{
		// Top Curated Models
		"glm-5.1":           {ModelName: "glm-5.1", ConfigName: "glm-5.1"},
		"glm-5":             {ModelName: "glm-5", ConfigName: "glm-5"},
		"glm-5v-turbo":      {ModelName: "glm-5v-turbo", ConfigName: "glm-5v-turbo"},
		"deepseek-v4-pro":   {ModelName: "DeepSeek-V4-Pro", ConfigName: "DeepSeek-V4-Pro"},
		"deepseek-v4-flash": {ModelName: "DeepSeek-V4-Flash", ConfigName: "DeepSeek-V4-Flash"},
		"kimi-k2.6":         {ModelName: "kimi-k2.6", ConfigName: "kimi-k2.6"},
		"qwen-3.6-plus":     {ModelName: "qwen-3.6-plus__v2", ConfigName: "qwen-3.6-plus"},

		// Other GLM
		"glm-4.7": {ModelName: "glm-4.7", ConfigName: "glm-4.7"},
		"glm-4.6": {ModelName: "glm-4.6", ConfigName: "glm-4.6"},

		// Other Kimi
		"kimi-k2.5": {ModelName: "kimi-k2.5", ConfigName: "kimi-k2.5"},
		"kimi-k2":   {ModelName: "kimi-k2", ConfigName: "kimi-k2"},

		// Other Qwen
		"qwen-3.5-plus": {ModelName: "qwen-3.5", ConfigName: "qwen-3.5"},
		"qwen-3-coder":  {ModelName: "qwen3-coder__v2", ConfigName: "qwen3-coder"},

		// Doubao
		"doubao-seed-2.0-code": {ModelName: "Doubao-Seed-2.0-Code__v2", ConfigName: "Doubao-Seed-2.0-Code"},
		"doubao-seed-1.8":      {ModelName: "doubao_1_8", ConfigName: "doubao_1_8"},
		"doubao-seed-1.6":      {ModelName: "Doubao_1_6", ConfigName: "Doubao_1_6"},
		"doubao-auto":          {ModelName: "doubao-for-auto", ConfigName: "doubao-for-auto"},

		// MiniMax
		"minimax-m2.7": {ModelName: "minimax-m2.7", ConfigName: "minimax-m2.7"},
		"minimax-m2.5": {ModelName: "minimax-m2.5", ConfigName: "minimax-m2.5"},
		"minimax-m2.1": {ModelName: "minimax-m2.1", ConfigName: "minimax-m2.1"},
		"minimax-m2":   {ModelName: "minimax-m2", ConfigName: "minimax-m2"},

		// Explicit Aliases (Transformed from old switch contains logic)
		"deepseek-flash":  {ModelName: "DeepSeek-V4-Flash", ConfigName: "DeepSeek-V4-Flash"},
		"deepseek-v4":     {ModelName: "DeepSeek-V4-Pro", ConfigName: "DeepSeek-V4-Pro"},
		"deepseek":        {ModelName: "DeepSeek-V4-Pro", ConfigName: "DeepSeek-V4-Pro"},
		"glm":             {ModelName: "glm-5", ConfigName: "glm-5"},
		"kimi":            {ModelName: "kimi-k2.6", ConfigName: "kimi-k2.6"},
		"qwen":            {ModelName: "qwen-3.6-plus__v2", ConfigName: "qwen-3.6-plus"},
		"qwen-3.6":        {ModelName: "qwen-3.6-plus__v2", ConfigName: "qwen-3.6-plus"},
		"qwen-3.5":        {ModelName: "qwen-3.5", ConfigName: "qwen-3.5"},
		"qwen-3":          {ModelName: "qwen3-coder__v2", ConfigName: "qwen3-coder"},
		"qwen3":           {ModelName: "qwen3-coder__v2", ConfigName: "qwen3-coder"},
		"qwen-coder":      {ModelName: "qwen3-coder__v2", ConfigName: "qwen3-coder"},
		"qwencoder":       {ModelName: "qwen3-coder__v2", ConfigName: "qwen3-coder"},
		"doubao":          {ModelName: "Doubao-Seed-2.0-Code__v2", ConfigName: "Doubao-Seed-2.0-Code"},
		"doubao-2.0":      {ModelName: "Doubao-Seed-2.0-Code__v2", ConfigName: "Doubao-Seed-2.0-Code"},
		"doubao-1.8":      {ModelName: "doubao_1_8", ConfigName: "doubao_1_8"},
		"doubao-1.6":      {ModelName: "Doubao_1_6", ConfigName: "Doubao_1_6"},
		"doubao_auto":     {ModelName: "doubao-for-auto", ConfigName: "doubao-for-auto"},
		"doubao-for-auto": {ModelName: "doubao-for-auto", ConfigName: "doubao-for-auto"},
		"minimax":         {ModelName: "minimax-m2", ConfigName: "minimax-m2"},
		"minimax-2.7":     {ModelName: "minimax-m2.7", ConfigName: "minimax-m2.7"},
		"minimax-2.5":     {ModelName: "minimax-m2.5", ConfigName: "minimax-m2.5"},
		"minimax-2.1":     {ModelName: "minimax-m2.1", ConfigName: "minimax-m2.1"},
	}

	if cfg, ok := exact[key]; ok {
		return cfg
	}

	return ModelConfig{ModelName: requested, ConfigName: requested}
}
