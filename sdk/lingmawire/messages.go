package lingmawire

import (
	"encoding/json"
	"strings"
)

// agentCommonModels are model keys that must use AgentId=agent_common.
// These models return empty responses when using agent_chat.
var agentCommonModels = map[string]bool{
	"kmodel": true,
	"mmodel": true,
}

const (
	AgentChat    = "agent_chat"
	AgentCommon  = "agent_common"
	SourceSystem = "system"
)

// IsAgentCommonModel reports whether the given model name requires the
// agent_common AgentId instead of agent_chat.
func IsAgentCommonModel(modelName string) bool {
	return agentCommonModels[strings.ToLower(strings.TrimSpace(modelName))]
}

// AgentID returns the appropriate agent_id value for the given model name.
// Models that require agent_common get "agent_common"; all others get "agent_chat".
func AgentID(modelName string) string {
	if IsAgentCommonModel(modelName) {
		return AgentCommon
	}
	return AgentChat
}

// ModelConfigSource returns the model_config.source value for the given model name.
// agent_chat models use "system" (enables reasoning); agent_common models use "".
func ModelConfigSource(modelName string) string {
	if IsAgentCommonModel(modelName) {
		return ""
	}
	return SourceSystem
}

// NormalizeAssistantToolCallContent adapts OpenAI-compatible assistant tool-call
// history to the stricter provider behind Lingma. OpenAI permits content=null
// (or omitted content) when tool_calls is present, but that provider fails to
// recognize the tool_calls and then rejects the following tool result as orphaned.
// Setting content to "" preserves the message semantics while satisfying both schemas.
//
// Only modifies messages satisfying all of:
// 1. role is "assistant"
// 2. tool_calls is non-empty
// 3. content is null or missing
//
// Normal assistant, user, and tool messages with null content are preserved unchanged.
// The original slice and maps are not mutated in-place.
func NormalizeAssistantToolCallContent(messages []map[string]any) []map[string]any {
	if len(messages) == 0 {
		return messages
	}
	normalized := make([]map[string]any, len(messages))
	for i, message := range messages {
		if message == nil {
			continue
		}
		// Clone each message map so the caller's objects are not mutated
		cloned := make(map[string]any, len(message))
		for k, v := range message {
			cloned[k] = v
		}
		normalized[i] = cloned

		role, _ := cloned["role"].(string)
		if role != "assistant" {
			continue
		}
		content, hasContent := cloned["content"]
		if hasContent && content != nil {
			continue
		}
		if !HasToolCalls(cloned["tool_calls"]) {
			continue
		}
		cloned["content"] = ""
	}
	return normalized
}

// HasToolCalls reports whether value represents a non-empty list of tool calls.
func HasToolCalls(value any) bool {
	if value == nil {
		return false
	}
	switch calls := value.(type) {
	case []any:
		return len(calls) > 0
	case []map[string]any:
		return len(calls) > 0
	case []json.RawMessage:
		return len(calls) > 0
	default:
		return false
	}
}
