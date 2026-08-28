package lingma

import (
	"bufio"
	"bytes"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func parseAggregateUsage(data []byte) (*pluginapi.UsageDetail, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, 5*1024*1024)
	var last *pluginapi.UsageDetail
	for scanner.Scan() {
		if detail, ok := parseStreamUsage(scanner.Bytes()); ok {
			last = detail
		}
	}
	return last, last != nil
}

func parseStreamUsage(line []byte) (*pluginapi.UsageDetail, bool) {
	payload := bytes.TrimSpace(line)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(bytes.TrimPrefix(payload, []byte("data:")))
	}
	return parseUsagePayload(payload, 0)
}

func parseUsagePayload(payload []byte, depth int) (*pluginapi.UsageDetail, bool) {
	if depth > 4 || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return nil, false
	}
	root := gjson.ParseBytes(payload)
	if body := root.Get("body"); body.Exists() && body.Type == gjson.String {
		inner := strings.TrimSpace(body.String())
		if inner != "" && inner != "[DONE]" {
			return parseUsagePayload([]byte(inner), depth+1)
		}
	}
	usage := root.Get("usage")
	if !usage.Exists() || !usage.IsObject() {
		return nil, false
	}
	detail := &pluginapi.UsageDetail{
		InputTokens:         firstUsageInt(usage, "prompt_tokens", "input_tokens"),
		OutputTokens:        firstUsageInt(usage, "completion_tokens", "output_tokens"),
		TotalTokens:         usage.Get("total_tokens").Int(),
		CachedTokens:        firstUsageInt(usage, "prompt_tokens_details.cached_tokens", "input_tokens_details.cached_tokens", "cached_tokens", "cache_read_input_tokens"),
		CacheReadTokens:     usage.Get("cache_read_input_tokens").Int(),
		CacheCreationTokens: usage.Get("cache_creation_input_tokens").Int(),
		ReasoningTokens:     firstUsageInt(usage, "completion_tokens_details.reasoning_tokens", "output_tokens_details.reasoning_tokens", "reasoning_tokens"),
	}
	if detail.TotalTokens == 0 {
		detail.TotalTokens = detail.InputTokens + detail.OutputTokens
	}
	if detail.InputTokens == 0 && detail.OutputTokens == 0 && detail.TotalTokens == 0 && detail.CachedTokens == 0 && detail.ReasoningTokens == 0 {
		return nil, false
	}
	return detail, true
}

func firstUsageInt(node gjson.Result, paths ...string) int64 {
	for _, path := range paths {
		if value := node.Get(path); value.Exists() {
			return value.Int()
		}
	}
	return 0
}
