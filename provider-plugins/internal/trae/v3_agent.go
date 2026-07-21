package trae

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	traeenc "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/trae"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

//go:embed templates/create_agent_task_payload.json
var v3TemplatesFS embed.FS

// v3Template holds the cached V3 task template.
var v3Template []byte

func init() {
	data, err := v3TemplatesFS.ReadFile("templates/create_agent_task_payload.json")
	if err != nil {
		panic(fmt.Sprintf("read trae v3 template: %v", err))
	}
	v3Template = data
}

func buildTraeToolCommitRequest(creds credentials, toolMessages []gjson.Result) (*traeRequestBuildResult, error) {
	var toolcallResults []traeToolCallResult
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
		toolcallResults = append(toolcallResults, traeToolCallResult{
			AgentRunID:           state.AgentRunID,
			ToolCallID:           state.NativeID,
			ToolCallName:         firstNonEmpty(state.Name, tm.Get("name").String()),
			ToolCallResp:         tm.Get("content").String(),
			ToolCallStatus:       "success",
			ToolCallErrorMessage: "",
			IsTruncated:          nil,
		})
	}

	commitPayload := traeCommitPayload{
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

func buildTraeV3CreateTaskRequest(
	creds credentials,
	upstreamModel string,
	openaiReq []byte,
	messages []gjson.Result,
	metadata map[string]any,
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
	modelConfig := resolveModelConfig(upstreamModel)
	if modelName := metadataString(metadata, traeModelNameMeta); modelName != "" {
		modelConfig.ModelName = modelName
	}
	if configName := metadataString(metadata, traeConfigMeta); configName != "" {
		modelConfig.ConfigName = configName
	}

	workspacePath := metadataString(metadata, "workspace_path")
	if workspacePath == "" {
		workspacePath = "C:\\Workspace\\Personal"
	}

	plainBytes, err := buildV3CreateTaskPayload(
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

// buildV3CreateTaskPayload constructs the full JSON payload for Trae's v3/create_agent_task endpoint.
func buildV3CreateTaskPayload(
	model, configName, prompt, sessionID, convID, userID, deviceID, workspacePath string,
) ([]byte, error) {
	if len(sessionID) < 16 {
		return nil, fmt.Errorf("session id too short: %q", sessionID)
	}
	payload := append([]byte(nil), v3Template...)

	var err error
	set := func(path string, value any) error {
		payload, err = sjson.SetBytes(payload, path, value)
		if err != nil {
			return fmt.Errorf("set %s: %w", path, err)
		}
		return nil
	}

	for _, op := range []struct {
		path  string
		value any
	}{
		{"model_name", model},
		{"config_name", configName},
		{"session_id", sessionID},
		{"conversation_id", convID},
		{"user_id", userID},
		{"device_id", deviceID},
		{"history_id_list", []string{}},
		{"user_input.id", "user_in_" + sessionID[:16]},
		{"user_input.messages", []map[string]string{{"type": "text", "text_content": prompt}}},
	} {
		if err = set(op.path, op.value); err != nil {
			return nil, err
		}
	}

	oldWorkspace := ""
	variablesRaw := gjson.GetBytes(payload, "render_context.variables").String()
	if variablesRaw != "" {
		oldWorkspace = firstNonEmpty(
			gjson.Get(variablesRaw, "workspace_path").String(),
			gjson.Get(variablesRaw, "workspace_folder").String(),
			gjson.Get(variablesRaw, "workspace_folders").String(),
		)
		variablesRaw = replaceWorkspaceText(variablesRaw, oldWorkspace, workspacePath)
		for _, op := range []struct {
			path  string
			value any
		}{
			{"workspace_folder", workspacePath},
			{"workspace_folders", workspacePath},
			{"workspace_path", workspacePath},
			{"raw_input", prompt},
			{"input", prompt},
			{"unique_user_id", userID},
			{"current_time", time.Now().Format("20060102 15:04:05, Monday")},
		} {
			variablesRaw, err = sjson.Set(variablesRaw, op.path, op.value)
			if err != nil {
				return nil, fmt.Errorf("set render_context.variables.%s: %w", op.path, err)
			}
		}
		if err = set("render_context.variables", variablesRaw); err != nil {
			return nil, err
		}
	}

	for _, path := range []string{
		"render_context.references.current_file.file_path",
		"render_context.references.current_file.workspace_path",
	} {
		current := gjson.GetBytes(payload, path)
		if current.Exists() && current.String() != "" {
			if err = set(path, replaceWorkspaceText(current.String(), oldWorkspace, workspacePath)); err != nil {
				return nil, err
			}
		}
	}

	return payload, nil
}

func replaceWorkspaceText(value, oldWorkspace, workspacePath string) string {
	if workspacePath == "" || oldWorkspace == "" {
		return value
	}
	oldEsc := strings.ReplaceAll(oldWorkspace, "\\", "\\\\")
	newEsc := strings.ReplaceAll(workspacePath, "\\", "\\\\")
	value = strings.ReplaceAll(value, oldEsc, newEsc)

	value = strings.ReplaceAll(value, oldWorkspace, workspacePath)

	oldFS := strings.ReplaceAll(oldWorkspace, "\\", "/")
	newFS := strings.ReplaceAll(workspacePath, "\\", "/")
	value = strings.ReplaceAll(value, oldFS, newFS)

	return value
}
