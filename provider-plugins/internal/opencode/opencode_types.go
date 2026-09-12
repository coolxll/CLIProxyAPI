package opencode

import "encoding/json"

// OpenCodeModelRef identifies the provider and model in OpenCode.
type OpenCodeModelRef struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// OpenCodePart represents a part of a message in OpenCode.
type OpenCodePart struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"` // "text", "reasoning", "file", "tool"
	Text     string `json:"text,omitempty"`
	Mime     string `json:"mime,omitempty"`
	URL      string `json:"url,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// SessionCreateResponse is the response from POST /session.
type SessionCreateResponse struct {
	ID string `json:"id"`
}

// SessionPromptRequest is the payload for POST /session/{id}/message.
type SessionPromptRequest struct {
	Model       OpenCodeModelRef `json:"model"`
	System      string           `json:"system,omitempty"`
	Parts       []OpenCodePart   `json:"parts"`
	MaxTokens   *int             `json:"max_tokens,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	TopP        *float64         `json:"top_p,omitempty"`
	Stop        []string         `json:"stop,omitempty"`
}

// OpenCodeEvent represents a server-sent event from GET /event.
type OpenCodeEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

// EventMessagePartUpdatedProps is for event "message.part.updated".
type EventMessagePartUpdatedProps struct {
	Part  OpenCodePartSummary `json:"part"`
	Delta string              `json:"delta,omitempty"`
}

// OpenCodePartSummary contains basic metadata about a part.
type OpenCodePartSummary struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID"`
	Type      string `json:"type"` // "text", "reasoning", "tool"
}

// EventMessagePartDeltaProps is for event "message.part.delta".
type EventMessagePartDeltaProps struct {
	SessionID string `json:"sessionID"`
	PartID    string `json:"partID"`
	Delta     string `json:"delta"`
	Field     string `json:"field"` // "text"
}

// EventMessageUpdatedProps is for event "message.updated".
type EventMessageUpdatedProps struct {
	Info OpenCodeMessageInfo `json:"info"`
}

// OpenCodeMessageInfo describes an assistant message state.
type OpenCodeMessageInfo struct {
	ID        string           `json:"id"`
	SessionID string           `json:"sessionID"`
	Role      string           `json:"role"`
	Finish    string           `json:"finish,omitempty"` // "stop", "tool"
	Tokens    OpenCodeTokens   `json:"tokens"`
	Error     *OpenCodeError   `json:"error,omitempty"`
	Time      OpenCodeTimeInfo `json:"time"`
}

// OpenCodeTokens tracks usage in OpenCode.
type OpenCodeTokens struct {
	Input     int `json:"input"`
	Output    int `json:"output"`
	Reasoning int `json:"reasoning"`
}

// OpenCodeError describes an error returned by OpenCode.
type OpenCodeError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// OpenCodeTimeInfo tracks message timing.
type OpenCodeTimeInfo struct {
	Created   int64  `json:"created"`
	Completed *int64 `json:"completed,omitempty"`
}

// SessionPromptResponse is the synchronous response from POST /session/{id}/message.
type SessionPromptResponse struct {
	Info  OpenCodeMessageInfo `json:"info"`
	Parts []OpenCodePart      `json:"parts"`
}

// ConfigProvidersResponse is the response from GET /config/providers.
type ConfigProvidersResponse struct {
	Providers json.RawMessage `json:"providers"` // can be array or object
}

// ProviderInfo describes a single provider returned by GET /config/providers.
type ProviderInfo struct {
	ID     string               `json:"id"`
	Name   string               `json:"name,omitempty"`
	Models map[string]ModelData `json:"models,omitempty"`
}

// ModelData describes a model within a ProviderInfo.
type ModelData struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Label       string `json:"label,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
}

// OpenAI compatibility structs for canonical stream and response construction.

type openaiChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openaiChoice `json:"choices"`
	Usage   *openaiUsage   `json:"usage,omitempty"`
}

type openaiChoice struct {
	Index        int         `json:"index"`
	Delta        openaiDelta `json:"delta"`
	FinishReason any         `json:"finish_reason"`
}

type openaiDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          string           `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
}

type openaiToolCall struct {
	Index    int                `json:"index,omitempty"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function openaiToolFunction `json:"function"`
}

type openaiToolFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openaiUsage struct {
	PromptTokens            int                   `json:"prompt_tokens"`
	CompletionTokens        int                   `json:"completion_tokens"`
	TotalTokens             int                   `json:"total_tokens"`
	CompletionTokensDetails *openaiReasoningUsage `json:"completion_tokens_details,omitempty"`
}

type openaiReasoningUsage struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type openaiResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []openaiResponseChoice `json:"choices"`
	Usage   openaiUsage            `json:"usage"`
}

type openaiResponseChoice struct {
	Index        int                   `json:"index"`
	Message      openaiResponseMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type openaiResponseMessage struct {
	Role             string           `json:"role"`
	Content          string           `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
}
