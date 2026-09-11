package trae

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

const (
	formatOpenAI         = "openai"
	formatClaude         = "claude"
	formatOpenAIResponse = "openai-response"
)

type traeThinkingApplier struct{}

func init() {
	thinking.RegisterProvider("trae", traeThinkingApplier{})
}

// Apply implements thinking.ProviderApplier for Trae.
// Trae doesn't have a native thinking/reasoning mode in its API, so this is a no-op
// that just returns the body unchanged. The thinking configuration is handled at the
// model level (e.g., deepseek-reasoner models always reason).
func (traeThinkingApplier) Apply(body []byte, config thinking.ThinkingConfig, _ *registry.ModelInfo) ([]byte, error) {
	// Trae doesn't support explicit thinking control in its API
	// Reasoning is implicit in certain models (deepseek-reasoner, etc.)
	return body, nil
}

func normalizeFormat(req pluginapi.ExecutorRequest) string {
	format := strings.ToLower(strings.TrimSpace(req.SourceFormat))
	if format == "" {
		format = strings.ToLower(strings.TrimSpace(req.Format))
	}
	switch format {
	case "chat-completions", "chat_completions", "openai-chat-completions", "openai_chat_completions":
		return formatOpenAI
	case "anthropic":
		return formatClaude
	case "responses", "openai_responses":
		return formatOpenAIResponse
	default:
		return format
	}
}

// translateRequestToTrae converts OpenAI/Claude format requests to Trae's internal format.
// Trae uses OpenAI-compatible format internally, so this is mostly a pass-through.
func translateRequestToTrae(format, model string, body []byte, stream bool) ([]byte, error) {
	switch format {
	case formatOpenAI, formatClaude:
		// Trae accepts OpenAI-compatible format
		// Claude format is already converted to OpenAI by the SDK translator
		return body, nil
	case formatOpenAIResponse:
		return nil, newStatusError(501, "Trae openai-response format is not supported")
	default:
		return nil, newStatusError(400, fmt.Sprintf("Trae request format %q is not supported", format))
	}
}

// translateNonStreamFromTrae converts Trae responses back to the requested format.
func translateNonStreamFromTrae(format, model string, originalRequest, translatedRequest, raw []byte) ([]byte, error) {
	// Trae responses are already in OpenAI format
	// The SDK translator handles conversion to Claude if needed
	return raw, nil
}

// translateStreamFromTrae converts Trae streaming responses back to the requested format.
func translateStreamFromTrae(format, model string, originalRequest, translatedRequest, raw []byte, state *any) ([][]byte, error) {
	// Trae streaming responses are already in OpenAI SSE format
	// The SDK translator handles conversion to Claude if needed
	return [][]byte{raw}, nil
}

// applyRequestThinking applies thinking configuration to the request.
// For Trae, this is mostly a no-op since reasoning is model-implicit.
func applyRequestThinking(body []byte, req pluginapi.ExecutorRequest, format string) []byte {
	// Trae doesn't have explicit thinking control
	// Just preserve the body as-is
	return body
}

// parseStreamUsage extracts usage information from a streaming chunk.
func parseStreamUsage(line []byte) (*pluginapi.UsageDetail, bool) {
	if len(line) == 0 || !gjson.ValidBytes(line) {
		return nil, false
	}

	root := gjson.ParseBytes(line)
	usage := root.Get("usage")
	if !usage.Exists() {
		return nil, false
	}

	promptTokens := usage.Get("prompt_tokens").Int()
	completionTokens := usage.Get("completion_tokens").Int()
	totalTokens := usage.Get("total_tokens").Int()

	if promptTokens == 0 && completionTokens == 0 && totalTokens == 0 {
		return nil, false
	}

	return &pluginapi.UsageDetail{
		InputTokens:  promptTokens,
		OutputTokens: completionTokens,
	}, true
}

// aggregateHasDone checks if the aggregated stream contains a [DONE] marker.
func aggregateHasDone(data []byte) bool {
	return strings.Contains(string(data), "data: [DONE]")
}

// aggregateRetryableSSEError checks for retryable errors in aggregated SSE data.
func aggregateRetryableSSEError(data []byte) error {
	// Trae doesn't have specific retryable SSE errors like Lingma
	// Just return nil to indicate no retryable error
	return nil
}

// retryableSSEError checks if a single SSE line contains a retryable error.
func retryableSSEError(line []byte) error {
	// Trae doesn't have specific retryable SSE errors
	return nil
}

// shouldRetryStatus determines if an HTTP status code should trigger a retry.
func shouldRetryStatus(status int) bool {
	// Trae doesn't implement retry logic in the plugin
	// The host handles retries if needed
	return false
}

// isContextCancellation checks if an error is a context cancellation.
func isContextCancellation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context canceled") || strings.Contains(msg, "context cancelled")
}
