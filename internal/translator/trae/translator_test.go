package trae

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestTranslator(t *testing.T) {
	trans, err := NewTranslator()
	if err != nil {
		t.Fatalf("NewTranslator failed: %v", err)
	}

	model := "DeepSeek-V4-Pro"
	configName := "DeepSeek-V4-Pro"
	prompt := "Check the current repository changes."
	sessionID := "12345678901234567890"
	convID := "conv-123"
	userID := "user-456"
	deviceID := "device-789"
	workspacePath := "C:\\My\\New\\Workspace"

	payloadBytes, err := trans.BuildV3CreateTaskPayload(
		model, configName, prompt, sessionID, convID, userID, deviceID, workspacePath,
	)
	if err != nil {
		t.Fatalf("BuildV3CreateTaskPayload failed: %v", err)
	}

	payloadStr := string(payloadBytes)

	// Validate outer variables
	if gjson.Get(payloadStr, "model_name").String() != model {
		t.Errorf("expected model_name %q, got %q", model, gjson.Get(payloadStr, "model_name").String())
	}
	if gjson.Get(payloadStr, "config_name").String() != configName {
		t.Errorf("expected config_name %q, got %q", configName, gjson.Get(payloadStr, "config_name").String())
	}
	if gjson.Get(payloadStr, "session_id").String() != sessionID {
		t.Errorf("expected session_id %q, got %q", sessionID, gjson.Get(payloadStr, "session_id").String())
	}
	if gjson.Get(payloadStr, "conversation_id").String() != convID {
		t.Errorf("expected conversation_id %q, got %q", convID, gjson.Get(payloadStr, "conversation_id").String())
	}
	if gjson.Get(payloadStr, "user_id").String() != userID {
		t.Errorf("expected user_id %q, got %q", userID, gjson.Get(payloadStr, "user_id").String())
	}
	if gjson.Get(payloadStr, "device_id").String() != deviceID {
		t.Errorf("expected device_id %q, got %q", deviceID, gjson.Get(payloadStr, "device_id").String())
	}

	// Validate nested raw variables JSON string
	variablesRaw := gjson.Get(payloadStr, "render_context.variables").String()
	if !gjson.Valid(variablesRaw) {
		t.Fatal("render_context.variables is not a valid JSON string")
	}

	if gjson.Get(variablesRaw, "workspace_path").String() != workspacePath {
		t.Errorf("expected variables.workspace_path %q, got %q", workspacePath, gjson.Get(variablesRaw, "workspace_path").String())
	}
	if gjson.Get(variablesRaw, "workspace_folder").String() != workspacePath {
		t.Errorf("expected variables.workspace_folder %q, got %q", workspacePath, gjson.Get(variablesRaw, "workspace_folder").String())
	}
	if gjson.Get(variablesRaw, "raw_input").String() != prompt {
		t.Errorf("expected variables.raw_input %q, got %q", prompt, gjson.Get(variablesRaw, "raw_input").String())
	}
	if gjson.Get(variablesRaw, "unique_user_id").String() != userID {
		t.Errorf("expected variables.unique_user_id %q, got %q", userID, gjson.Get(variablesRaw, "unique_user_id").String())
	}

	// Make sure no captured local workspace or home paths remain in the clean template.
	for _, leaked := range []string{"auto-vps-manager", "coolx", ".trae-cn"} {
		if strings.Contains(payloadStr, leaked) {
			t.Errorf("payload still contains captured local value %q", leaked)
		}
	}

	if rawRules := gjson.Get(payloadStr, "raw_rules"); !rawRules.Exists() || !rawRules.IsArray() || len(rawRules.Array()) != 0 {
		t.Errorf("expected raw_rules to stay empty, got %s", rawRules.Raw)
	}
	if got := gjson.Get(payloadStr, "agent_version").String(); got != "v3" {
		t.Errorf("expected agent_version v3, got %q", got)
	}
	if got := gjson.Get(payloadStr, "request_seq").Int(); got != 1 {
		t.Errorf("expected request_seq 1, got %d", got)
	}
	if extraConfig := gjson.Get(payloadStr, "extra_config"); !extraConfig.Exists() || !extraConfig.IsObject() {
		t.Errorf("expected extra_config object, got %s", extraConfig.Raw)
	}
	if skills := gjson.Get(payloadStr, "skill_list"); !skills.Exists() || !skills.IsArray() || len(skills.Array()) != 0 {
		t.Errorf("expected skill_list to stay empty, got %s", skills.Raw)
	}
}

func TestResolveModelConfig(t *testing.T) {
	tests := []struct {
		requested string
		expectedM string
		expectedC string
	}{
		{"glm-5.1", "glm-5.1", "glm-5.1"},
		{"trae/DeepSeek-V4-Pro", "DeepSeek-V4-Pro", "DeepSeek-V4-Pro"},
		{"deepseek-flash", "DeepSeek-V4-Flash", "DeepSeek-V4-Flash"},
		{"qwen-3.6", "qwen-3.6-plus__v2", "qwen-3.6-plus"},
		{"unknown-random-model", "unknown-random-model", "unknown-random-model"},
	}

	for _, tt := range tests {
		cfg := ResolveModelConfig(tt.requested)
		if cfg.ModelName != tt.expectedM || cfg.ConfigName != tt.expectedC {
			t.Errorf("ResolveModelConfig(%q) = (%q, %q); expected (%q, %q)",
				tt.requested, cfg.ModelName, cfg.ConfigName, tt.expectedM, tt.expectedC)
		}
	}
}
