package trae

import (
	"strings"
)

// resolveTraeProtocol determines the protocol version (v1/v2/v3) and upstream model name
// from a model identifier and optional metadata.
func resolveTraeProtocol(model string, metadata map[string]any) (string, string) {
	model = strings.TrimSpace(model)
	if protocol := normalizeTraeProtocol(metadataString(metadata, traeProtocolMeta)); protocol != "" {
		return protocol, stripTraeProtocolPrefix(model)
	}

	// Strip the case-insensitive provider prefix "trae/" if present
	if stripped, ok := stripCaseInsensitivePrefix(model, "trae/"); ok {
		model = stripped
	}

	for _, candidate := range []struct {
		prefix   string
		protocol string
	}{
		{"trae-solo/", traeProtocolSolo},
		{"solo/", traeProtocolSolo},
		{"utils/", traeProtocolSolo},
		{"trae-v1/", traeProtocolV1},
		{"raw-v1/", traeProtocolV1},
		{"v1/", traeProtocolV1},
		{"trae-v2/", traeProtocolV2},
		{"raw-v2/", traeProtocolV2},
		{"v2/", traeProtocolV2},
		{"trae-v3/", traeProtocolV3},
		{"agent/", traeProtocolV3},
		{"v3/", traeProtocolV3},
	} {
		if stripped, ok := stripCaseInsensitivePrefix(model, candidate.prefix); ok {
			return candidate.protocol, stripped
		}
	}
	if isTraeV1RawChatModel(model) {
		return traeProtocolV1, model
	}
	if isTraeV2RawChatModel(model) {
		return traeProtocolV2, model
	}
	return traeProtocolV3, model
}

func isTraeV1RawChatModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "seed_m8", "deepseek-r1", "deepseek-v3", "deepseek-v3-0324":
		return true
	default:
		return false
	}
}

func isTraeV2RawChatModel(model string) bool {
	key := strings.ToLower(strings.TrimSpace(model))
	return key == "no_thinking_model" || key == "no-thinking"
}

func stripTraeProtocolPrefix(model string) string {
	for _, prefix := range []string{"trae-solo/", "solo/", "utils/", "trae-v1/", "raw-v1/", "v1/", "trae-v2/", "raw-v2/", "v2/", "trae-v3/", "agent/", "v3/"} {
		if stripped, ok := stripCaseInsensitivePrefix(model, prefix); ok {
			return stripped
		}
	}
	return model
}

func stripCaseInsensitivePrefix(value, prefix string) (string, bool) {
	if len(value) < len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		return value, false
	}
	return value[len(prefix):], true
}

func normalizeTraeProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "solo", "solo_work", "solo_work_lite", "utils", "llm_utils_chat":
		return traeProtocolSolo
	case "1", "v1", "raw-v1", "llm_raw_chat_v1":
		return traeProtocolV1
	case "2", "v2", "raw-v2", "llm_raw_chat", "llm_raw_chat_v2", "raw":
		return traeProtocolV2
	case "3", "v3", "agent", "builder", "builder_v3", "create_agent_task":
		return traeProtocolV3
	default:
		return ""
	}
}

// resolveRawChatModelConfig maps a requested model to Trae llm_raw_chat model/config names.
func resolveRawChatModelConfig(requested, protocol string) traeDetailModelConfig {
	key := strings.ToLower(strings.TrimSpace(requested))
	key = strings.TrimPrefix(key, "trae/")
	key = strings.TrimPrefix(key, "trae-solo/")
	key = strings.TrimPrefix(key, "trae-v1/")
	key = strings.TrimPrefix(key, "trae-v2/")
	key = strings.TrimPrefix(key, "raw-v1/")
	key = strings.TrimPrefix(key, "raw-v2/")
	key = strings.TrimPrefix(key, "solo/")
	key = strings.TrimPrefix(key, "utils/")
	key = strings.TrimPrefix(key, "v1/")
	key = strings.TrimPrefix(key, "v2/")

	if protocol == traeProtocolV1 {
		exact := map[string]traeDetailModelConfig{
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
		return traeDetailModelConfig{ModelName: requested}
	}

	exact := map[string]traeDetailModelConfig{
		// Default / Generic Protocol Aliases
		"auto":              {ConfigName: "glm-5.2", ModelName: "glm-5.2", DisplayName: "GLM-5.2"},
		"claude-3-5-sonnet": {ConfigName: "glm-5.2", ModelName: "glm-5.2", DisplayName: "GLM-5.2"},
		"gpt-4o":            {ConfigName: "glm-5.2", ModelName: "glm-5.2", DisplayName: "GLM-5.2"},

		// GLM Models
		"glm-5.3": {ConfigName: "glm-5.3", ModelName: "glm-5.3", DisplayName: "GLM-5.3"},
		"glm-5.2": {ConfigName: "glm-5.2", ModelName: "glm-5.2", DisplayName: "GLM-5.2"},
		"glm-5.1": {ConfigName: "glm-5.1", ModelName: "glm-5.1", DisplayName: "GLM-5.1"},
		"glm-5":   {ConfigName: "glm-5", ModelName: "glm-5", DisplayName: "GLM-5"},

		// DeepSeek Models
		"deepseek-v4-pro-official":   {ConfigName: "DeepSeek-V4-Pro-Official", ModelName: "DeepSeek-V4-Pro-Official", DisplayName: "DeepSeek-V4-Pro 正式版"},
		"deepseek-v4-pro":            {ConfigName: "DeepSeek-V4-Pro", ModelName: "DeepSeek-V4-Pro", DisplayName: "DeepSeek-V4-Pro"},
		"deepseek-v4-flash-official": {ConfigName: "DeepSeek-V4-Flash-Official", ModelName: "DeepSeek-V4-Flash-Official", DisplayName: "DeepSeek-V4-Flash 正式版"},
		"deepseek-v4-flash":          {ConfigName: "DeepSeek-V4-Flash", ModelName: "DeepSeek-V4-Flash", DisplayName: "DeepSeek-V4-Flash"},

		// Doubao Seed Models
		"doubao-seed-2.1-pro":   {ConfigName: "Doubao-Seed-2.1-Pro", ModelName: "Doubao-Seed-2.1-Pro", DisplayName: "Seed-2.1-Pro"},
		"doubao-seed-2.1-turbo": {ConfigName: "Doubao-Seed-2.1-Turbo", ModelName: "Doubao-Seed-2.1-Turbo", DisplayName: "Seed-2.1-Turbo"},
		"doubao-seed-code":      {ConfigName: "Doubao-Seed-Code", ModelName: "Doubao-Seed-Code", DisplayName: "Seed-Code"},
		"doubao-seed-evolving":  {ConfigName: "Doubao-Seed-Evolving", ModelName: "Doubao-Seed-Evolving", DisplayName: "Seed-Evolving"},
		"doubao-seed-2.0-code":  {ConfigName: "Doubao-Seed-2.0-Code", ModelName: "Doubao-Seed-2.0-Code__v2", DisplayName: "Doubao-Seed-2.0-Code"},

		// Kimi Models
		"kimi-k3":        {ConfigName: "kimi-k3", ModelName: "kimi-k3", DisplayName: "Kimi-K3"},
		"kimi-k2.7-code": {ConfigName: "kimi-k2.7-code", ModelName: "kimi-k2.7-code", DisplayName: "Kimi-K2.7-Code"},
		"kimi-k2.6":      {ConfigName: "kimi-k2.6", ModelName: "kimi-k2.6", DisplayName: "Kimi-K2.6"},

		// Frontier Models
		"minimax-m3":    {ConfigName: "minimax-m3", ModelName: "minimax-m3", DisplayName: "MiniMax-M3"},
		"qwen3.8-max":   {ConfigName: "qwen3.8-max", ModelName: "qwen3.8-max", DisplayName: "Qwen3.8-Max"},
		"qwen-3.7-plus": {ConfigName: "qwen-3.7-plus", ModelName: "qwen-3.7-plus", DisplayName: "Qwen3.7-Plus"},

		// Synthetic
		"no_thinking_model": {ModelName: "no_thinking_model", ConfigName: "title_generation", DisplayName: "Trae No Thinking Model"},
		"no-thinking":       {ModelName: "no_thinking_model", ConfigName: "title_generation", DisplayName: "Trae No Thinking Model"},
	}
	if cfg, ok := exact[key]; ok {
		return cfg
	}
	if protocol == traeProtocolV2 {
		return traeDetailModelConfig{ModelName: "no_thinking_model", ConfigName: "title_generation", DisplayName: "Trae No Thinking Model"}
	}
	return traeDetailModelConfig{ModelName: requested, ConfigName: requested, DisplayName: requested}
}

// resolveModelConfig maps a user-requested model to a valid Trae ModelConfig for V3.
func resolveModelConfig(requested string) traeDetailModelConfig {
	key := strings.ToLower(strings.TrimSpace(requested))
	key = strings.TrimPrefix(key, "trae/")

	exact := map[string]traeDetailModelConfig{
		// Top Curated Models
		"glm-5.3":           {ModelName: "glm-5.3", ConfigName: "glm-5.3", DisplayName: "GLM-5.3"},
		"glm-5.2":           {ModelName: "glm-5.2", ConfigName: "glm-5.2", DisplayName: "GLM-5.2"},
		"glm-5.1":           {ModelName: "glm-5.1", ConfigName: "glm-5.1", DisplayName: "GLM-5.1"},
		"glm-5":             {ModelName: "glm-5", ConfigName: "glm-5", DisplayName: "GLM-5"},
		"glm-5v-turbo":      {ModelName: "glm-5v-turbo", ConfigName: "glm-5v-turbo", DisplayName: "GLM-5v-Turbo"},
		"deepseek-v4-pro":   {ModelName: "DeepSeek-V4-Pro", ConfigName: "DeepSeek-V4-Pro", DisplayName: "DeepSeek V4 Pro"},
		"deepseek-v4-flash": {ModelName: "DeepSeek-V4-Flash", ConfigName: "DeepSeek-V4-Flash", DisplayName: "DeepSeek V4 Flash"},
		"kimi-k3":           {ModelName: "kimi-k3", ConfigName: "kimi-k3", DisplayName: "Kimi K3"},
		"kimi-k2.7-code":    {ModelName: "kimi-k2.7-code", ConfigName: "kimi-k2.7-code", DisplayName: "Kimi K2.7 Code"},
		"kimi-k2.6":         {ModelName: "kimi-k2.6", ConfigName: "kimi-k2.6", DisplayName: "Kimi K2.6"},
		"qwen3.8-max":       {ModelName: "qwen3.8-max", ConfigName: "qwen3.8-max", DisplayName: "Qwen 3.8 Max"},
		"qwen-3.7-plus":     {ModelName: "qwen-3.7-plus", ConfigName: "qwen-3.7-plus", DisplayName: "Qwen 3.7 Plus"},
		"qwen-3.6-plus":     {ModelName: "qwen-3.6-plus__v2", ConfigName: "qwen-3.6-plus", DisplayName: "Qwen 3.6 Plus"},

		// Other GLM
		"glm-4.7": {ModelName: "glm-4.7", ConfigName: "glm-4.7", DisplayName: "GLM-4.7"},
		"glm-4.6": {ModelName: "glm-4.6", ConfigName: "glm-4.6", DisplayName: "GLM-4.6"},

		// Other Kimi
		"kimi-k2.5": {ModelName: "kimi-k2.5", ConfigName: "kimi-k2.5", DisplayName: "Kimi K2.5"},
		"kimi-k2":   {ModelName: "kimi-k2", ConfigName: "kimi-k2", DisplayName: "Kimi K2"},

		// Other Qwen
		"qwen-3.5-plus": {ModelName: "qwen-3.5", ConfigName: "qwen-3.5", DisplayName: "Qwen 3.5"},
		"qwen-3-coder":  {ModelName: "qwen3-coder__v2", ConfigName: "qwen3-coder", DisplayName: "Qwen3 Coder"},

		// Doubao
		"doubao-seed-2.1-pro":   {ModelName: "Doubao-Seed-2.1-Pro", ConfigName: "Doubao-Seed-2.1-Pro", DisplayName: "Seed-2.1-Pro"},
		"doubao-seed-2.1-turbo": {ModelName: "Doubao-Seed-2.1-Turbo", ConfigName: "Doubao-Seed-2.1-Turbo", DisplayName: "Seed-2.1-Turbo"},
		"doubao-seed-code":      {ModelName: "Doubao-Seed-Code", ConfigName: "Doubao-Seed-Code", DisplayName: "Seed-Code"},
		"doubao-seed-evolving":  {ModelName: "Doubao-Seed-Evolving", ConfigName: "Doubao-Seed-Evolving", DisplayName: "Seed-Evolving"},
		"doubao-seed-2.0-code":  {ModelName: "Doubao-Seed-2.0-Code__v2", ConfigName: "Doubao-Seed-2.0-Code", DisplayName: "Doubao-Seed-2.0-Code"},
		"doubao-seed-1.8":       {ModelName: "doubao_1_8", ConfigName: "doubao_1_8", DisplayName: "Doubao 1.8"},
		"doubao-seed-1.6":       {ModelName: "Doubao_1_6", ConfigName: "Doubao_1_6", DisplayName: "Doubao 1.6"},
		"doubao-auto":           {ModelName: "doubao-for-auto", ConfigName: "doubao-for-auto", DisplayName: "Doubao Auto"},

		// MiniMax
		"minimax-m3":   {ModelName: "minimax-m3", ConfigName: "minimax-m3", DisplayName: "MiniMax M3"},
		"minimax-m2.7": {ModelName: "minimax-m2.7", ConfigName: "minimax-m2.7", DisplayName: "MiniMax M2.7"},
		"minimax-m2.5": {ModelName: "minimax-m2.5", ConfigName: "minimax-m2.5", DisplayName: "MiniMax M2.5"},
		"minimax-m2.1": {ModelName: "minimax-m2.1", ConfigName: "minimax-m2.1", DisplayName: "MiniMax M2.1"},
		"minimax-m2":   {ModelName: "minimax-m2", ConfigName: "minimax-m2", DisplayName: "MiniMax M2"},

		// Explicit Aliases
		"deepseek-flash":  {ModelName: "DeepSeek-V4-Flash", ConfigName: "DeepSeek-V4-Flash", DisplayName: "DeepSeek V4 Flash"},
		"deepseek-v4":     {ModelName: "DeepSeek-V4-Pro", ConfigName: "DeepSeek-V4-Pro", DisplayName: "DeepSeek V4 Pro"},
		"deepseek":        {ModelName: "DeepSeek-V4-Pro", ConfigName: "DeepSeek-V4-Pro", DisplayName: "DeepSeek V4 Pro"},
		"glm":             {ModelName: "glm-5.2", ConfigName: "glm-5.2", DisplayName: "GLM-5.2"},
		"kimi":            {ModelName: "kimi-k3", ConfigName: "kimi-k3", DisplayName: "Kimi K3"},
		"qwen":            {ModelName: "qwen-3.6-plus__v2", ConfigName: "qwen-3.6-plus", DisplayName: "Qwen 3.6 Plus"},
		"qwen-3.6":        {ModelName: "qwen-3.6-plus__v2", ConfigName: "qwen-3.6-plus", DisplayName: "Qwen 3.6 Plus"},
		"qwen-3.5":        {ModelName: "qwen-3.5", ConfigName: "qwen-3.5", DisplayName: "Qwen 3.5"},
		"qwen-3":          {ModelName: "qwen3-coder__v2", ConfigName: "qwen3-coder", DisplayName: "Qwen3 Coder"},
		"qwen3":           {ModelName: "qwen3-coder__v2", ConfigName: "qwen3-coder", DisplayName: "Qwen3 Coder"},
		"qwen-coder":      {ModelName: "qwen3-coder__v2", ConfigName: "qwen3-coder", DisplayName: "Qwen3 Coder"},
		"qwencoder":       {ModelName: "qwen3-coder__v2", ConfigName: "qwen3-coder", DisplayName: "Qwen3 Coder"},
		"doubao":          {ModelName: "Doubao-Seed-2.0-Code__v2", ConfigName: "Doubao-Seed-2.0-Code", DisplayName: "Doubao-Seed-2.0-Code"},
		"doubao-2.0":      {ModelName: "Doubao-Seed-2.0-Code__v2", ConfigName: "Doubao-Seed-2.0-Code", DisplayName: "Doubao-Seed-2.0-Code"},
		"doubao-1.8":      {ModelName: "doubao_1_8", ConfigName: "doubao_1_8", DisplayName: "Doubao 1.8"},
		"doubao-1.6":      {ModelName: "Doubao_1_6", ConfigName: "Doubao_1_6", DisplayName: "Doubao 1.6"},
		"doubao_auto":     {ModelName: "doubao-for-auto", ConfigName: "doubao-for-auto", DisplayName: "Doubao Auto"},
		"doubao-for-auto": {ModelName: "doubao-for-auto", ConfigName: "doubao-for-auto", DisplayName: "Doubao Auto"},
		"minimax":         {ModelName: "minimax-m3", ConfigName: "minimax-m3", DisplayName: "MiniMax M3"},
		"minimax-2.7":     {ModelName: "minimax-m2.7", ConfigName: "minimax-m2.7", DisplayName: "MiniMax M2.7"},
		"minimax-2.5":     {ModelName: "minimax-m2.5", ConfigName: "minimax-m2.5", DisplayName: "MiniMax M2.5"},
		"minimax-2.1":     {ModelName: "minimax-m2.1", ConfigName: "minimax-m2.1", DisplayName: "MiniMax M2.1"},
	}

	if cfg, ok := exact[key]; ok {
		return cfg
	}

	return traeDetailModelConfig{ModelName: requested, ConfigName: requested, DisplayName: requested}
}
