package trae

import (
	"embed"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

//go:embed templates/create_agent_task_payload.json
var templatesFS embed.FS

// Translator handles translation of generic chat requests into Trae specific format.
type Translator struct {
	rawTemplate []byte
}

// NewTranslator loads and parses the static JSON template for Trae payload building.
func NewTranslator() (*Translator, error) {
	data, errRead := templatesFS.ReadFile("templates/create_agent_task_payload.json")
	if errRead != nil {
		return nil, fmt.Errorf("read trae template: %w", errRead)
	}
	return &Translator{rawTemplate: data}, nil
}

// BuildV3CreateTaskPayload constructs the full JSON payload for Trae's v3/create_agent_task endpoint.
func (t *Translator) BuildV3CreateTaskPayload(
	model, configName, prompt, sessionID, convID, userID, deviceID, workspacePath string,
) ([]byte, error) {
	if len(sessionID) < 16 {
		return nil, fmt.Errorf("session id too short: %q", sessionID)
	}
	payload := append([]byte(nil), t.rawTemplate...)

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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func replaceWorkspaceText(value, oldWorkspace, workspacePath string) string {
	if workspacePath == "" || oldWorkspace == "" {
		return value
	}
	// 1. Raw string exact replacement (with double escaped backslashes for JSON literals)
	oldEsc := strings.ReplaceAll(oldWorkspace, "\\", "\\\\")
	newEsc := strings.ReplaceAll(workspacePath, "\\", "\\\\")
	value = strings.ReplaceAll(value, oldEsc, newEsc)

	// 2. Normal replacement
	value = strings.ReplaceAll(value, oldWorkspace, workspacePath)

	// 3. Forward slash replacement (frequently used in reference paths and shell patterns)
	oldFS := strings.ReplaceAll(oldWorkspace, "\\", "/")
	newFS := strings.ReplaceAll(workspacePath, "\\", "/")
	value = strings.ReplaceAll(value, oldFS, newFS)

	return value
}
