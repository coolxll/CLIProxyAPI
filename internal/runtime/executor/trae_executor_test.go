package executor

import (
	"context"
	"encoding/json"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestTraeExecutorCountTokensReturnsApproximation(t *testing.T) {
	e := &TraeExecutor{}
	resp, err := e.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: []byte(`{"model":"glm-5","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("CountTokens returned error: %v", err)
	}

	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got <= 0 {
		t.Fatalf("usage.total_tokens = %d, want positive token count; payload=%s", got, string(resp.Payload))
	}
}

func TestOpenAIUsageFromTraeTokenUsage(t *testing.T) {
	usageData := openAIUsageFromResult(gjson.Parse(`{
		"prompt_tokens": 11,
		"completion_tokens": 7,
		"total_tokens": 18,
		"cache_creation_input_tokens": 3,
		"cache_read_input_tokens": 5,
		"reasoning_tokens": 2
	}`))

	raw, err := json.Marshal(usageData)
	if err != nil {
		t.Fatalf("marshal usage: %v", err)
	}
	if got := gjson.GetBytes(raw, "prompt_tokens").Int(); got != 11 {
		t.Fatalf("prompt_tokens = %d, want 11", got)
	}
	if got := gjson.GetBytes(raw, "completion_tokens").Int(); got != 7 {
		t.Fatalf("completion_tokens = %d, want 7", got)
	}
	if got := gjson.GetBytes(raw, "total_tokens").Int(); got != 18 {
		t.Fatalf("total_tokens = %d, want 18", got)
	}
	if got := gjson.GetBytes(raw, "prompt_tokens_details.cached_tokens").Int(); got != 8 {
		t.Fatalf("cached_tokens = %d, want 8", got)
	}
	if got := gjson.GetBytes(raw, "completion_tokens_details.reasoning_tokens").Int(); got != 2 {
		t.Fatalf("reasoning_tokens = %d, want 2", got)
	}
}
