package helpers

import "strings"

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
