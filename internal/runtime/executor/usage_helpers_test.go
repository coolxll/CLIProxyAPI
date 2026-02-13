package executor

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestResolveUsageSource_Anonymization(t *testing.T) {
	// Test that it anonymizes API keys from auth attributes
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key": "sk-sensitive-key",
		},
	}
	got := resolveUsageSource(auth, "")
	if len(got) != 16 {
		t.Errorf("expected 16 chars hash, got %q", got)
	}
	if got == "sk-sensitive-key" {
		t.Error("API key was not anonymized")
	}

	// Test that it anonymizes ctx API key
	gotCtx := resolveUsageSource(nil, "sk-another-sensitive-key")
	if len(gotCtx) != 16 {
		t.Errorf("expected 16 chars hash, got %q", gotCtx)
	}
	if gotCtx == "sk-another-sensitive-key" {
		t.Error("Context API key was not anonymized")
	}

	// Test that it preserves non-sensitive IDs
	authID := &cliproxyauth.Auth{
		Provider: "gemini-cli",
		ID:       "user-123",
	}
	gotID := resolveUsageSource(authID, "")
	if gotID != "user-123" {
		t.Errorf("expected user-123, got %q", gotID)
	}
}

func TestParseOpenAIUsageChatCompletions(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail := parseOpenAIUsage(data)
	if detail.InputTokens != 1 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 1)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.TotalTokens != 3 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 3)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 5)
	}
}

func TestParseOpenAIUsageResponses(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
	detail := parseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}
