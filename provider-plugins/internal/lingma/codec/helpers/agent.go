package helpers

import "github.com/router-for-me/CLIProxyAPI/v7/sdk/lingmawire"

const (
	AgentChat    = lingmawire.AgentChat
	AgentCommon  = lingmawire.AgentCommon
	SourceSystem = lingmawire.SourceSystem
)

// IsAgentCommonModel reports whether the given model name requires the
// agent_common AgentId instead of agent_chat.
func IsAgentCommonModel(modelName string) bool {
	return lingmawire.IsAgentCommonModel(modelName)
}

// AgentID returns the appropriate agent_id value for the given model name.
// Models that require agent_common get "agent_common"; all others get "agent_chat".
func AgentID(modelName string) string {
	return lingmawire.AgentID(modelName)
}

// ModelConfigSource returns the model_config.source value for the given model name.
// agent_chat models use "system" (enables reasoning); agent_common models use "".
func ModelConfigSource(modelName string) string {
	return lingmawire.ModelConfigSource(modelName)
}
