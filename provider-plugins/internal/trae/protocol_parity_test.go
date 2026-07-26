package trae

import (
	"testing"
)

// nativeResolveTraeProtocol is a copy of the native resolveTraeProtocol function
// for parity testing. This avoids importing the native package which would create
// a circular dependency. The function is kept in sync with
// internal/runtime/executor/trae_models.go:111-147.
func nativeResolveTraeProtocol(model string, metadata map[string]any) (string, string) {
	model = stripWhitespace(model)
	if protocol := nativeNormalizeTraeProtocol(nativeMetadataString(metadata, traeProtocolMeta)); protocol != "" {
		return protocol, nativeStripTraeProtocolPrefix(model)
	}

	// Strip the case-insensitive provider prefix "trae/" if present
	if stripped, ok := nativeStripCaseInsensitivePrefix(model, "trae/"); ok {
		model = stripped
	}

	for _, candidate := range []struct {
		prefix   string
		protocol string
	}{
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
		if stripped, ok := nativeStripCaseInsensitivePrefix(model, candidate.prefix); ok {
			return candidate.protocol, stripped
		}
	}
	if nativeIsTraeV1RawChatModel(model) {
		return traeProtocolV1, model
	}
	if nativeIsTraeV2RawChatModel(model) {
		return traeProtocolV2, model
	}
	return traeProtocolV3, model
}

func nativeIsTraeV1RawChatModel(model string) bool {
	switch toLower(stripWhitespace(model)) {
	case "seed_m8", "deepseek-r1", "deepseek-v3", "deepseek-v3-0324":
		return true
	default:
		return false
	}
}

func nativeIsTraeV2RawChatModel(model string) bool {
	key := toLower(stripWhitespace(model))
	return key == "no_thinking_model"
}

func nativeStripTraeProtocolPrefix(model string) string {
	for _, prefix := range []string{"trae-v1/", "raw-v1/", "v1/", "trae-v2/", "raw-v2/", "v2/", "trae-v3/", "agent/", "v3/"} {
		if stripped, ok := nativeStripCaseInsensitivePrefix(model, prefix); ok {
			return stripped
		}
	}
	return model
}

func nativeStripCaseInsensitivePrefix(value, prefix string) (string, bool) {
	if len(value) < len(prefix) || !equalFold(value[:len(prefix)], prefix) {
		return value, false
	}
	return value[len(prefix):], true
}

func nativeNormalizeTraeProtocol(protocol string) string {
	switch toLower(stripWhitespace(protocol)) {
	case "1", "v1", "raw-v1", "llm_raw_chat_v1":
		return traeProtocolV1
	case "2", "v2", "raw-v2", "llm_raw_chat", "llm_raw_chat_v2":
		return traeProtocolV2
	case "3", "v3", "agent", "builder", "builder_v3", "create_agent_task":
		return traeProtocolV3
	default:
		return ""
	}
}

func nativeMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if val, ok := metadata[key]; ok {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return ""
}

// Helper functions to avoid importing strings package
func stripWhitespace(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\n' && s[i] != '\r' {
			result = append(result, s[i])
		}
	}
	return string(result)
}

func toLower(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		result[i] = c
	}
	return string(result)
}

func equalFold(s1, s2 string) bool {
	if len(s1) != len(s2) {
		return false
	}
	for i := 0; i < len(s1); i++ {
		c1, c2 := s1[i], s2[i]
		if c1 >= 'A' && c1 <= 'Z' {
			c1 += 'a' - 'A'
		}
		if c2 >= 'A' && c2 <= 'Z' {
			c2 += 'a' - 'A'
		}
		if c1 != c2 {
			return false
		}
	}
	return true
}

// TestResolveTraeProtocolParity verifies plugin and native produce identical
// protocol resolution results for various model names and metadata.
func TestResolveTraeProtocolParity(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		metadata map[string]any
	}{
		{"v1 prefix", "trae-v1/deepseek-r1", nil},
		{"v2 prefix", "trae-v2/no_thinking_model", nil},
		{"v3 prefix", "trae-v3/glm-5", nil},
		{"raw-v1 prefix", "raw-v1/seed_m8", nil},
		{"agent prefix", "agent/glm-5.1", nil},
		{"v1 model", "deepseek-r1", nil},
		{"v2 model", "no_thinking_model", nil},
		{"v3 model", "glm-5", nil},
		{"trae prefix", "trae/deepseek-v3", nil},
		{"metadata v1", "custom-model", map[string]any{traeProtocolMeta: "v1"}},
		{"metadata v2", "custom-model", map[string]any{traeProtocolMeta: "llm_raw_chat"}},
		{"metadata v3", "custom-model", map[string]any{traeProtocolMeta: "builder_v3"}},
		{"case insensitive", "TRAE-V1/DeepSeek-R1", nil},
		{"whitespace", "  trae-v1/deepseek-r1  ", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pluginProto, pluginModel := resolveTraeProtocol(tt.model, tt.metadata)
			nativeProto, nativeModel := nativeResolveTraeProtocol(tt.model, tt.metadata)

			if pluginProto != nativeProto {
				t.Errorf("protocol mismatch: plugin=%q, native=%q", pluginProto, nativeProto)
			}
			if pluginModel != nativeModel {
				t.Errorf("model mismatch: plugin=%q, native=%q", pluginModel, nativeModel)
			}
		})
	}
}

// TestIsTraeV1RawChatModelParity verifies plugin and native produce identical
// results for V1 raw chat model detection.
func TestIsTraeV1RawChatModelParity(t *testing.T) {
	models := []string{
		"seed_m8",
		"deepseek-r1",
		"deepseek-v3",
		"deepseek-v3-0324",
		"DEEPSEEK-R1",
		"  seed_m8  ",
		"glm-5",
		"no_thinking_model",
		"unknown",
	}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			plugin := isTraeV1RawChatModel(model)
			native := nativeIsTraeV1RawChatModel(model)

			if plugin != native {
				t.Errorf("mismatch: plugin=%v, native=%v", plugin, native)
			}
		})
	}
}

// TestIsTraeV2RawChatModelParity verifies plugin and native produce identical
// results for V2 raw chat model detection.
func TestIsTraeV2RawChatModelParity(t *testing.T) {
	models := []string{
		"no_thinking_model",
		"NO_THINKING_MODEL",
		"  no_thinking_model  ",
		"seed_m8",
		"deepseek-r1",
		"unknown",
	}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			plugin := isTraeV2RawChatModel(model)
			native := nativeIsTraeV2RawChatModel(model)

			if plugin != native {
				t.Errorf("mismatch: plugin=%v, native=%v", plugin, native)
			}
		})
	}
}

// TestStripTraeProtocolPrefixParity verifies plugin and native produce identical
// results for protocol prefix stripping.
func TestStripTraeProtocolPrefixParity(t *testing.T) {
	models := []string{
		"trae-v1/deepseek-r1",
		"raw-v1/seed_m8",
		"v1/custom",
		"trae-v2/no_thinking_model",
		"raw-v2/custom",
		"v2/custom",
		"trae-v3/glm-5",
		"agent/glm-5.1",
		"v3/custom",
		"TRAE-V1/DeepSeek-R1",
		"no-prefix",
		"trae/deepseek-v3",
	}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			plugin := stripTraeProtocolPrefix(model)
			native := nativeStripTraeProtocolPrefix(model)

			if plugin != native {
				t.Errorf("mismatch: plugin=%q, native=%q", plugin, native)
			}
		})
	}
}

// TestNormalizeTraeProtocolParity verifies plugin and native produce identical
// results for protocol normalization.
func TestNormalizeTraeProtocolParity(t *testing.T) {
	protocols := []string{
		"1", "v1", "raw-v1", "llm_raw_chat_v1",
		"2", "v2", "raw-v2", "llm_raw_chat", "llm_raw_chat_v2",
		"3", "v3", "agent", "builder", "builder_v3", "create_agent_task",
		"V1", "V2", "V3",
		"  v1  ",
		"unknown",
		"",
	}

	for _, protocol := range protocols {
		t.Run(protocol, func(t *testing.T) {
			plugin := normalizeTraeProtocol(protocol)
			native := nativeNormalizeTraeProtocol(protocol)

			if plugin != native {
				t.Errorf("mismatch: plugin=%q, native=%q", plugin, native)
			}
		})
	}
}
