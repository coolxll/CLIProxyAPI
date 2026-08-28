package claude

import (
	chat_completions "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/lingma/openai/chat-completions"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
)

// ConvertClaudeRequestToLingma converts a Claude Messages API request to Lingma format
// by composing: Claude -> OpenAI Chat Completions -> Lingma.
func ConvertClaudeRequestToLingma(modelName string, inputRawJSON []byte, stream bool) []byte {
	openaiJSON := openaiclaude.ConvertClaudeRequestToOpenAI(modelName, inputRawJSON, stream)
	return chat_completions.ConvertOpenAIRequestToLingma(modelName, openaiJSON, stream)
}
