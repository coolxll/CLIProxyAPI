package claude

import (
	"context"
	"encoding/json"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/lingma/helpers"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/lingma/openai/responses"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
	"github.com/tidwall/gjson"
)

type lingmaClaudeStreamState struct {
	lingmaState any
	claudeState any
}

// ConvertLingmaResponseToClaude converts Lingma SSE responses to Claude Messages API format
// by composing: Lingma -> OpenAI Chat Completions -> Claude.
func ConvertLingmaResponseToClaude(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &lingmaClaudeStreamState{}
	}
	st := (*param).(*lingmaClaudeStreamState)

	// Step 1: Convert Lingma -> OpenAI Chat Completions chunks
	openaiChunks := responses.ConvertLingmaResponseToOpenAI(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, &st.lingmaState)

	var result [][]byte
	for _, chunk := range openaiChunks {
		// Check if this chunk is a Lingma error (has "error" field, no "choices")
		if errResult := gjson.GetBytes(chunk, "error"); errResult.Exists() && !gjson.GetBytes(chunk, "choices").Exists() {
			errType := errResult.Get("type").String()
			if errType == "" {
				errType = "api_error"
			}
			errMsg := errResult.Get("message").String()
			if errMsg == "" {
				errMsg = "unknown error from lingma"
			}
			claudeErr := map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    errType,
					"message": errMsg,
				},
			}
			if encoded, err := json.Marshal(claudeErr); err == nil {
				sseEvent := translatorcommon.SSEEventData("error", encoded)
				sseEvent = append(sseEvent, '\n', '\n') // Standard SSE format requires double newline
				result = append(result, sseEvent)
			}
			continue
		}

		// Step 2: Wrap as SSE for the Claude converter (which expects data: prefix)
		sseChunk := append([]byte("data: "), chunk...)
		sseChunk = append(sseChunk, '\n', '\n')

		claudeEvents := openaiclaude.ConvertOpenAIResponseToClaude(ctx, modelName, originalRequestRawJSON, requestRawJSON, sseChunk, &st.claudeState)
		result = append(result, claudeEvents...)
	}

	// Handle [DONE]: if the raw input was [DONE], propagate to Claude converter
	isDone := helpers.IsLingmaDone(rawJSON)
	if isDone {
		doneSSE := []byte("data: [DONE]\n\n")
		claudeEvents := openaiclaude.ConvertOpenAIResponseToClaude(ctx, modelName, originalRequestRawJSON, requestRawJSON, doneSSE, &st.claudeState)
		result = append(result, claudeEvents...)

		// After [DONE], also close the stream if the done handler didn't already
		// (e.g. if no message_start was ever emitted).
		if cleanup := openaiclaude.CloseStream(&st.claudeState); len(cleanup) > 0 {
			result = append(result, cleanup...)
		}
	}

	return result
}

// ConvertLingmaResponseToClaudeNonStream converts a non-streaming Lingma response to Claude format
// by composing: Lingma -> OpenAI Chat Completions -> Claude.
func ConvertLingmaResponseToClaudeNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	// Step 1: Aggregate Lingma SSE -> single OpenAI Chat Completions JSON
	openaiJSON := responses.ConvertLingmaResponseToOpenAINonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, nil)

	// Step 2: Convert OpenAI Chat Completions -> Claude
	return openaiclaude.ConvertOpenAIResponseToClaudeNonStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, openaiJSON, nil)
}

func ClaudeTokenCount(_ context.Context, count int64) []byte {
	return translatorcommon.ClaudeInputTokensJSON(count)
}
