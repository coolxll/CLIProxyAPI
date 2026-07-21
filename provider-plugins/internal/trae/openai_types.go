package trae

// OpenAI streaming chunk types used for synthesizing Trae responses into
// standard OpenAI chat.completion.chunk SSE format.

type openaiDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          string           `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
}

type openaiToolCall struct {
	Index    int            `json:"index"`
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"`
	Function openaiFunction `json:"function,omitempty"`
}

type openaiFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openaiChoice struct {
	Index        int         `json:"index"`
	Delta        openaiDelta `json:"delta"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

type openaiChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openaiChoice `json:"choices"`
	Usage   *openaiUsage   `json:"usage,omitempty"`
}

type openaiUsage struct {
	PromptTokens            int64                    `json:"prompt_tokens"`
	CompletionTokens        int64                    `json:"completion_tokens"`
	TotalTokens             int64                    `json:"total_tokens"`
	PromptTokensDetails     *openaiPromptDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *openaiCompletionDetails `json:"completion_tokens_details,omitempty"`
}

type openaiPromptDetails struct {
	CachedTokens int64 `json:"cached_tokens,omitempty"`
}

type openaiCompletionDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`
}

// openaiMessage is used for non-streaming completion responses.
type openaiMessage struct {
	Role             string           `json:"role"`
	Content          string           `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
}

// openaiNonStreamChoice is used for non-streaming completion responses.
type openaiNonStreamChoice struct {
	Index        int           `json:"index"`
	Message      openaiMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// openaiNonStreamResponse is the standard OpenAI chat.completion response.
type openaiNonStreamResponse struct {
	ID      string                  `json:"id"`
	Object  string                  `json:"object"`
	Created int64                   `json:"created"`
	Model   string                  `json:"model"`
	Choices []openaiNonStreamChoice `json:"choices"`
	Usage   openaiUsage             `json:"usage"`
}
