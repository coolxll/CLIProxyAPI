package lingma

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

// pluginNormalizeLingmaUsageForTest is a copy of the plugin normalizeLingmaUsage
// function from codec/openai/response/response.go:505-555 for parity testing.
func pluginNormalizeLingmaUsageForTest(usage gjson.Result) json.RawMessage {
	if !usage.Exists() {
		return nil
	}
	promptNode := usage.Get("prompt_tokens")
	if !promptNode.Exists() {
		promptNode = usage.Get("input_tokens")
	}
	promptTokens := promptNode.Int()
	completionNode := usage.Get("completion_tokens")
	if !completionNode.Exists() {
		completionNode = usage.Get("output_tokens")
	}
	completionTokens := completionNode.Int()
	totalNode := usage.Get("total_tokens")
	totalTokens := totalNode.Int()
	if !totalNode.Exists() || (totalTokens == 0 && promptTokens+completionTokens > 0) {
		totalTokens = promptTokens + completionTokens
	}
	out := map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      totalTokens,
	}

	// Map cached tokens from various possible locations
	if v := usage.Get("prompt_tokens_details.cached_tokens"); v.Exists() && v.Int() > 0 {
		out["prompt_tokens_details"] = map[string]any{"cached_tokens": v.Int()}
	} else if v := usage.Get("input_tokens_details.cached_tokens"); v.Exists() && v.Int() > 0 {
		out["prompt_tokens_details"] = map[string]any{"cached_tokens": v.Int()}
	} else if v := usage.Get("cached_tokens"); v.Exists() && v.Int() > 0 {
		out["prompt_tokens_details"] = map[string]any{"cached_tokens": v.Int()}
	} else if v := usage.Get("cache_read_input_tokens"); v.Exists() && v.Int() > 0 {
		out["prompt_tokens_details"] = map[string]any{"cached_tokens": v.Int()}
	}

	// Map reasoning tokens
	if v := usage.Get("completion_tokens_details.reasoning_tokens"); v.Exists() && v.Int() > 0 {
		out["completion_tokens_details"] = map[string]any{"reasoning_tokens": v.Int()}
	} else if v := usage.Get("output_tokens_details.reasoning_tokens"); v.Exists() && v.Int() > 0 {
		out["completion_tokens_details"] = map[string]any{"reasoning_tokens": v.Int()}
	} else if v := usage.Get("reasoning_tokens"); v.Exists() && v.Int() > 0 {
		out["completion_tokens_details"] = map[string]any{"reasoning_tokens": v.Int()}
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return json.RawMessage(usage.Raw)
	}
	return encoded
}

// TestNormalizeLingmaUsageParity verifies plugin and native produce identical
// usage normalization for various input formats.
func TestNormalizeLingmaUsageParity(t *testing.T) {
	tests := []struct {
		name  string
		usage string
	}{
		{
			name:  "standard_openai_format",
			usage: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`,
		},
		{
			name:  "input_output_tokens",
			usage: `{"input_tokens":10,"output_tokens":5,"total_tokens":15}`,
		},
		{
			name:  "missing_total_calculated",
			usage: `{"prompt_tokens":10,"completion_tokens":5}`,
		},
		{
			name:  "zero_total_recalculated",
			usage: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":0}`,
		},
		{
			name:  "cached_tokens_in_prompt_details",
			usage: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":3}}`,
		},
		{
			name:  "cached_tokens_in_input_details",
			usage: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":3}}`,
		},
		{
			name:  "cached_tokens_at_root",
			usage: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cached_tokens":3}`,
		},
		{
			name:  "cache_read_input_tokens",
			usage: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cache_read_input_tokens":3}`,
		},
		{
			name:  "reasoning_tokens_in_completion_details",
			usage: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"completion_tokens_details":{"reasoning_tokens":2}}`,
		},
		{
			name:  "reasoning_tokens_in_output_details",
			usage: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"output_tokens_details":{"reasoning_tokens":2}}`,
		},
		{
			name:  "reasoning_tokens_at_root",
			usage: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"reasoning_tokens":2}`,
		},
		{
			name:  "all_fields",
			usage: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cache_read_input_tokens":3,"reasoning_tokens":2}`,
		},
		{
			name:  "zero_cached_ignored",
			usage: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cached_tokens":0}`,
		},
		{
			name:  "zero_reasoning_ignored",
			usage: `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"reasoning_tokens":0}`,
		},
		{
			name:  "empty_usage",
			usage: `{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usageResult := gjson.Parse(tt.usage)
			pluginResult := pluginNormalizeLingmaUsageForTest(usageResult)
			nativeResult := nativeNormalizeLingmaUsageForTest(usageResult)

			var pluginJSON, nativeJSON map[string]any
			if err := json.Unmarshal(pluginResult, &pluginJSON); err != nil {
				t.Fatalf("parse plugin result: %v\nraw: %s", err, pluginResult)
			}
			if err := json.Unmarshal(nativeResult, &nativeJSON); err != nil {
				t.Fatalf("parse native result: %v\nraw: %s", err, nativeResult)
			}

			compareJSON(t, pluginJSON, nativeJSON, nil, "usage")
		})
	}
}

// TestNormalizeLingmaUsageNonExistent verifies both implementations return nil
// for non-existent usage.
func TestNormalizeLingmaUsageNonExistent(t *testing.T) {
	usageResult := gjson.Parse(`{}`)
	nonExistent := usageResult.Get("usage")

	pluginResult := pluginNormalizeLingmaUsageForTest(nonExistent)
	nativeResult := nativeNormalizeLingmaUsageForTest(nonExistent)

	if pluginResult != nil {
		t.Errorf("plugin should return nil for non-existent usage, got: %s", pluginResult)
	}
	if nativeResult != nil {
		t.Errorf("native should return nil for non-existent usage, got: %s", nativeResult)
	}
}
