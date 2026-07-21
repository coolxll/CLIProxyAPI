package lingma

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// nativeNormalizeLingmaUsageForTest is a copy of the native normalizeLingmaUsage
// function from lingma_openai_response.go:505-555 for parity testing.
func nativeNormalizeLingmaUsageForTest(usage gjson.Result) json.RawMessage {
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

// nativePreserveLingmaClaudeCodeThinkingForTest is a copy of the native
// preserveLingmaClaudeCodeThinking function from lingma_executor.go:663-680.
//
// DIVERGENCE: The native implementation only sets model_config.is_reasoning.
// The plugin implementation (preserveClaudeCodeThinking in translation.go)
// additionally sets agent_id to "agent_common" and model_config.source to ""
// when thinking is disabled. This is a behavioral difference that will cause
// parity tests to fail for disabled thinking cases.
func nativePreserveLingmaClaudeCodeThinkingForTest(body, source []byte, sourceFormat string) []byte {
	if !strings.EqualFold(strings.TrimSpace(sourceFormat), "claude") {
		return body
	}
	if len(body) == 0 || !gjson.ValidBytes(body) || len(source) == 0 || !gjson.ValidBytes(source) {
		return body
	}

	enabled, ok := nativeClaudeCodeThinkingEnabledForTest(source)
	if !ok {
		return body
	}
	result, err := sjson.SetBytes(body, "model_config.is_reasoning", enabled)
	if err != nil {
		return body
	}
	return result
}

// nativeClaudeCodeThinkingEnabledForTest is a copy of the native
// claudeCodeThinkingEnabled function from lingma_executor.go:686-713.
func nativeClaudeCodeThinkingEnabledForTest(source []byte) (bool, bool) {
	if effort := gjson.GetBytes(source, "output_config.effort"); effort.Exists() && effort.Type == gjson.String {
		switch strings.ToLower(strings.TrimSpace(effort.String())) {
		case "none":
			return false, true
		case "off", "disabled":
			return false, true
		case "":
			// Empty effort is not a meaningful Claude Code thinking signal.
		default:
			return true, true
		}
	}

	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(source, "thinking.type").String()))
	switch thinkingType {
	case "disabled":
		return false, true
	case "enabled":
		if budget := gjson.GetBytes(source, "thinking.budget_tokens"); budget.Exists() && budget.Int() == 0 {
			return false, true
		}
		return true, true
	case "adaptive", "auto":
		return true, true
	default:
		return false, false
	}
}
