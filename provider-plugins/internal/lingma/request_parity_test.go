package lingma

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	nativeClaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/lingma/claude"
	nativeOpenAI "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/lingma/openai/chat-completions"
	pluginClaude "github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/lingma/codec/claude"
	pluginOpenAI "github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/lingma/codec/openai/request"
)

// dynamicFields are skipped during parity comparison because they contain
// UUIDs or timestamps that differ between plugin and native invocations.
var dynamicFields = []string{"request_id", "chat_record_id", "business"}

// TestOpenAIRequestParityBasic verifies plugin and native produce identical
// Lingma payloads for a basic OpenAI chat completions request.
func TestOpenAIRequestParityBasic(t *testing.T) {
	data, err := os.ReadFile("../../testdata/lingma/request_openai_basic.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	compareRequestTranslation(t, "gm51model", data, false,
		pluginOpenAI.ConvertOpenAIRequestToLingma,
		nativeOpenAI.ConvertOpenAIRequestToLingma)
}

// TestOpenAIRequestParityWithTools verifies tool definitions are translated identically.
func TestOpenAIRequestParityWithTools(t *testing.T) {
	data, err := os.ReadFile("../../testdata/lingma/request_openai_with_tools.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	compareRequestTranslation(t, "gm51model", data, false,
		pluginOpenAI.ConvertOpenAIRequestToLingma,
		nativeOpenAI.ConvertOpenAIRequestToLingma)
}

// TestOpenAIRequestParityReasoning verifies reasoning_effort is translated identically.
func TestOpenAIRequestParityReasoning(t *testing.T) {
	data, err := os.ReadFile("../../testdata/lingma/request_openai_reasoning.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	compareRequestTranslation(t, "gm51model", data, false,
		pluginOpenAI.ConvertOpenAIRequestToLingma,
		nativeOpenAI.ConvertOpenAIRequestToLingma)
}

// TestClaudeRequestParityBasic verifies plugin and native produce identical
// Lingma payloads for a basic Claude messages request.
func TestClaudeRequestParityBasic(t *testing.T) {
	data, err := os.ReadFile("../../testdata/lingma/request_claude_basic.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	compareRequestTranslation(t, "gm51model", data, false,
		pluginClaude.ConvertClaudeRequestToLingma,
		nativeClaude.ConvertClaudeRequestToLingma)
}

type requestTranslator func(modelName string, inputRawJSON []byte, stream bool) []byte

func compareRequestTranslation(t *testing.T, model string, data []byte, stream bool, plugin, native requestTranslator) {
	t.Helper()
	pluginRaw := plugin(model, data, stream)
	nativeRaw := native(model, data, stream)

	var pluginJSON, nativeJSON map[string]any
	if err := json.Unmarshal(pluginRaw, &pluginJSON); err != nil {
		t.Fatalf("parse plugin JSON: %v\nraw: %s", err, pluginRaw)
	}
	if err := json.Unmarshal(nativeRaw, &nativeJSON); err != nil {
		t.Fatalf("parse native JSON: %v\nraw: %s", err, nativeRaw)
	}

	compareJSON(t, pluginJSON, nativeJSON, dynamicFields, "root")
}

// compareJSON recursively compares two JSON structures, skipping specified fields.
func compareJSON(t *testing.T, plugin, native any, skipFields []string, path string) {
	t.Helper()

	switch p := plugin.(type) {
	case map[string]any:
		n, ok := native.(map[string]any)
		if !ok {
			t.Errorf("%s: type mismatch: plugin=%T, native=%T", path, plugin, native)
			return
		}
		for key, pVal := range p {
			if slices.Contains(skipFields, key) {
				continue
			}
			nVal, exists := n[key]
			if !exists {
				t.Errorf("%s.%s: missing in native", path, key)
				continue
			}
			compareJSON(t, pVal, nVal, skipFields, path+"."+key)
		}
		for key := range n {
			if slices.Contains(skipFields, key) {
				continue
			}
			if _, exists := p[key]; !exists {
				t.Errorf("%s.%s: missing in plugin", path, key)
			}
		}

	case []any:
		n, ok := native.([]any)
		if !ok {
			t.Errorf("%s: type mismatch: plugin=%T, native=%T", path, plugin, native)
			return
		}
		if len(p) != len(n) {
			t.Errorf("%s: array length mismatch: plugin=%d, native=%d", path, len(p), len(n))
			return
		}
		for i := range p {
			compareJSON(t, p[i], n[i], skipFields, path+"[]")
		}

	default:
		if plugin != native {
			t.Errorf("%s: value mismatch: plugin=%v (%T), native=%v (%T)", path, plugin, plugin, native, native)
		}
	}
}
