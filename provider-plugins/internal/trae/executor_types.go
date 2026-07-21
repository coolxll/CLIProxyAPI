package trae

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// traeRequestBuildResult holds the output of request building for all protocols.
type traeRequestBuildResult struct {
	TargetURL        string
	RequestBody      []byte
	LogBody          []byte
	RequestPin       string
	RequestAt        int64
	SessionID        string
	ConversationID   string
	Protocol         string
	ExtraHeaders     http.Header
	IsToolCommit     bool
	RawResponseModel string
}

// traeDetailModelConfig holds the model/config name pair resolved from
// the detail param endpoint or request metadata.
type traeDetailModelConfig struct {
	ModelName  string
	ConfigName string
}

// Protocol constants
const (
	traeProtocolV1    = "v1"
	traeProtocolV2    = "v2"
	traeProtocolV3    = "v3"
	traeProtocolMeta  = "trae_protocol"
	traeModelNameMeta = "trae_model_name"
	traeConfigMeta    = "trae_config_name"
)

// traeStatusErr is an error with an HTTP status code for upstream failures.
type traeStatusErr struct {
	code int
	msg  string
}

func (e traeStatusErr) Error() string {
	return e.msg
}

func (e traeStatusErr) StatusCode() int {
	return e.code
}

// V3 tool commit types

type traeToolCallResult struct {
	AgentRunID           string `json:"agent_run_id"`
	ToolCallID           string `json:"toolcall_id"`
	ToolCallName         string `json:"toolcall_name"`
	ToolCallResp         string `json:"toolcall_resp"`
	ToolCallStatus       string `json:"toolcall_status"`
	ToolCallErrorMessage string `json:"toolcall_error_message"`
	IsTruncated          *bool  `json:"is_truncated"`
}

type traeCommitPayload struct {
	ConversationID  string               `json:"conversation_id"`
	TaskID          string               `json:"task_id"`
	UserID          string               `json:"user_id"`
	ToolcallResults []traeToolCallResult `json:"toolcall_results"`
	ExtraContext    any                  `json:"extra_context"`
	RequestSeq      int                  `json:"request_seq"`
	QueueID         any                  `json:"queue_id"`
	AccessType      int                  `json:"access_type"`
	IsRemoteReq     bool                 `json:"is_remote_req"`
}

// Helper functions ported from trae_common.go

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func openAIMessageText(message gjson.Result) string {
	content := message.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var builder strings.Builder
		for _, part := range content.Array() {
			text := firstNonEmpty(
				part.Get("text").String(),
				part.Get("text_content").String(),
				part.Get("input_text").String(),
			)
			if text == "" {
				continue
			}
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(text)
		}
		return builder.String()
	}
	return content.String()
}

func mapUpstreamFinishReasonToOpenAI(reason string) string {
	switch reason {
	case "max_tokens":
		return "length"
	case "end_turn", "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	default:
		return reason
	}
}

func traeUsageInt(result gjson.Result, key string) int64 {
	if val := result.Get(key); val.Exists() {
		return val.Int()
	}
	if val := result.Get(key + "_total"); val.Exists() {
		return val.Int()
	}
	return 0
}

func openAIUsageFromResult(result gjson.Result) openaiUsage {
	promptTokens := traeUsageInt(result, "prompt_tokens")
	if promptTokens == 0 {
		promptTokens = traeUsageInt(result, "input_tokens")
	}
	completionTokens := traeUsageInt(result, "completion_tokens")
	if completionTokens == 0 {
		completionTokens = traeUsageInt(result, "output_tokens")
	}
	totalTokens := traeUsageInt(result, "total_tokens")
	if totalTokens == 0 {
		totalTokens = promptTokens + completionTokens
	}

	cacheReadTokens := traeUsageInt(result, "cache_read_input_tokens")
	cacheCreationTokens := traeUsageInt(result, "cache_creation_input_tokens")
	reasoningTokens := traeUsageInt(result, "reasoning_tokens")

	usageData := openaiUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
	}
	if cachedTokens := cacheReadTokens + cacheCreationTokens; cachedTokens > 0 {
		usageData.PromptTokensDetails = &openaiPromptDetails{CachedTokens: cachedTokens}
	}
	if reasoningTokens > 0 {
		usageData.CompletionTokensDetails = &openaiCompletionDetails{ReasoningTokens: reasoningTokens}
	}
	return usageData
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	v, ok := metadata[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}
