package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	traetranslator "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/trae"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	traeenc "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/trae"
	"github.com/tidwall/gjson"
)

type TraeToolCallResult struct {
	AgentRunID           string `json:"agent_run_id"`
	ToolCallID           string `json:"toolcall_id"`
	ToolCallName         string `json:"toolcall_name"`
	ToolCallResp         string `json:"toolcall_resp"`
	ToolCallStatus       string `json:"toolcall_status"`
	ToolCallErrorMessage string `json:"toolcall_error_message"`
	IsTruncated          *bool  `json:"is_truncated"`
}

type TraeCommitPayload struct {
	ConversationID  string               `json:"conversation_id"`
	TaskID          string               `json:"task_id"`
	UserID          string               `json:"user_id"`
	ToolcallResults []TraeToolCallResult `json:"toolcall_results"`
	ExtraContext    any                  `json:"extra_context"`
	RequestSeq      int                  `json:"request_seq"`
	QueueID         any                  `json:"queue_id"`
	AccessType      int                  `json:"access_type"`
	IsRemoteReq     bool                 `json:"is_remote_req"`
}

func buildTraeToolCommitRequest(creds *traeauth.TraeCredentials, toolMessages []gjson.Result) (*traeRequestBuildResult, error) {
	var toolcallResults []TraeToolCallResult
	var firstState traeToolState
	for idx, tm := range toolMessages {
		tcID := tm.Get("tool_call_id").String()
		state, err := decodeTraeToolID(tcID)
		if err != nil {
			return nil, fmt.Errorf("decode tool call id: %w", err)
		}
		if idx == 0 {
			firstState = state
		}
		toolcallResults = append(toolcallResults, TraeToolCallResult{
			AgentRunID:           state.AgentRunID,
			ToolCallID:           state.NativeID,
			ToolCallName:         firstNonEmpty(state.Name, tm.Get("name").String()),
			ToolCallResp:         tm.Get("content").String(),
			ToolCallStatus:       "success",
			ToolCallErrorMessage: "",
			IsTruncated:          nil,
		})
	}

	commitPayload := TraeCommitPayload{
		ConversationID:  firstState.ConversationID,
		TaskID:          firstState.TaskID,
		UserID:          creds.UserID,
		ToolcallResults: toolcallResults,
		RequestSeq:      1,
		IsRemoteReq:     false,
	}
	plainBytes, err := json.Marshal(commitPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal trae tool commit payload: %w", err)
	}
	encrypted, err := traeenc.EncryptMessage(plainBytes)
	if err != nil {
		return nil, fmt.Errorf("encrypt trae tool commit payload: %w", err)
	}
	return &traeRequestBuildResult{
		TargetURL:      "https://trae-api-cn.mchost.guru/api/agent/v3/commit_toolcall_result",
		RequestBody:    []byte(encrypted.Message),
		LogBody:        plainBytes,
		RequestPin:     encrypted.RequestPin,
		RequestAt:      encrypted.RequestAt,
		SessionID:      firstState.SessionID,
		ConversationID: firstState.ConversationID,
		Protocol:       traeProtocolV3,
		IsToolCommit:   true,
	}, nil
}

func (e *TraeExecutor) buildTraeV3CreateTaskRequest(
	auth *cliproxyauth.Auth,
	creds *traeauth.TraeCredentials,
	upstreamModel string,
	openaiReq []byte,
	messages []gjson.Result,
	opts cliproxyexecutor.Options,
) (*traeRequestBuildResult, error) {
	userPrompt := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Get("role").String() == "user" {
			userPrompt = openAIMessageText(messages[i])
			break
		}
	}
	if toolInstructions := buildTraeToolShimInstructions(openaiReq); toolInstructions != "" {
		userPrompt = toolInstructions + "\n\nUser request:\n" + userPrompt
	}

	activeSessionID := strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
	activeConvID := strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
	modelConfig := traetranslator.ResolveModelConfig(upstreamModel)
	if detailConfig, ok := e.traeDetailModelConfig(auth, upstreamModel); ok {
		modelConfig.ModelName = detailConfig.ModelName
		modelConfig.ConfigName = detailConfig.ConfigName
	}
	if modelName := metadataString(opts.Metadata, traeModelNameMeta); modelName != "" {
		modelConfig.ModelName = modelName
	}
	if configName := metadataString(opts.Metadata, traeConfigMeta); configName != "" {
		modelConfig.ConfigName = configName
	}

	workspacePath := traeauth.WorkspacePathFromAuth(auth, "")
	if workspacePath == "" {
		if pwd, errWd := os.Getwd(); errWd == nil {
			workspacePath = pwd
		} else {
			workspacePath = "C:\\Workspace\\Personal"
		}
	}

	trans, err := e.getTranslator()
	if err != nil {
		return nil, err
	}
	plainBytes, err := trans.BuildV3CreateTaskPayload(
		modelConfig.ModelName,
		modelConfig.ConfigName,
		userPrompt,
		activeSessionID,
		activeConvID,
		creds.UserID,
		creds.DeviceID,
		workspacePath,
	)
	if err != nil {
		return nil, fmt.Errorf("build trae task payload: %w", err)
	}
	encrypted, err := traeenc.EncryptMessage(plainBytes)
	if err != nil {
		return nil, fmt.Errorf("encrypt trae task payload: %w", err)
	}
	return &traeRequestBuildResult{
		TargetURL:      "https://trae-api-cn.mchost.guru/api/agent/v3/create_agent_task",
		RequestBody:    []byte(encrypted.Message),
		LogBody:        plainBytes,
		RequestPin:     encrypted.RequestPin,
		RequestAt:      encrypted.RequestAt,
		SessionID:      activeSessionID,
		ConversationID: activeConvID,
		Protocol:       traeProtocolV3,
	}, nil
}
