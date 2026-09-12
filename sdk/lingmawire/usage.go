package lingmawire

import (
	"encoding/json"

	"github.com/tidwall/gjson"
)

// NormalizeUsage converts Lingma usage payloads into canonical OpenAI Chat Completions usage format.
func NormalizeUsage(usage gjson.Result) json.RawMessage {
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

// NormalizeUsageBytes parses raw JSON bytes and returns canonical OpenAI usage JSON.
func NormalizeUsageBytes(raw []byte) json.RawMessage {
	if len(raw) == 0 || !gjson.ValidBytes(raw) {
		return nil
	}
	res := gjson.ParseBytes(raw)
	if usageNode := res.Get("usage"); usageNode.Exists() {
		return NormalizeUsage(usageNode)
	}
	return NormalizeUsage(res)
}
