package main

import (
	"fmt"

	chat_completions "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/lingma/openai/chat-completions"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
)

func main() {
	claudePayload := []byte(`{
		"model": "claude-3-7-sonnet-20250219",
		"messages": [{"role": "user", "content": "hello"}],
		"thinking": {"type": "adaptive"},
		"output_config": {"effort": "high"}
	}`)

	openaiPayload := openaiclaude.ConvertClaudeRequestToOpenAI("lingma-model", claudePayload, true)
	fmt.Printf("OpenAI Payload: %s\n", string(openaiPayload))

	lingmaPayload := chat_completions.ConvertOpenAIRequestToLingma("lingma-model", openaiPayload, true)
	fmt.Printf("Lingma Payload: %s\n", string(lingmaPayload))
}
