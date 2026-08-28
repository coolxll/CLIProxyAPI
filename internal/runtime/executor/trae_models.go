package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func (e *TraeExecutor) FetchModels(ctx context.Context, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		return nil, err
	}
	e.replaceTraeDetailModelConfigs(auth, nil)

	now := time.Now().Unix()
	models, err := e.fetchModelsFromDetailParam(ctx, creds, auth, now)
	if err != nil {
		log.Warnf("trae get_detail_param failed, falling back to model_list: %v", err)
		models, err = e.fetchModelsFromModelList(ctx, creds, auth, now)
		if err != nil {
			return nil, err
		}
	}
	models = appendTraeNoThinkingModel(models, now)
	return models, nil
}

func (e *TraeExecutor) fetchModelsFromDetailParam(ctx context.Context, creds *traeauth.TraeCredentials, auth *cliproxyauth.Auth, now int64) ([]*registry.ModelInfo, error) {
	body := []byte(`{"function":"chat_v3","config_names":null,"need_prompt":false,"current_config_info":null,"poly_prompt":true,"mode_type":null,"agent_type":"builder_v3","ab_force_vids":null,"ab_autotest_advanced_mode":null,"access_type":0}`)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, traeDetailParamURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	setTraeCommonHeaders(httpReq.Header, creds)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("trae executor: close detail param response body error: %v", errClose)
		}
	}()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("get_detail_param API error (%d): %s", httpResp.StatusCode, string(b))
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	models, configs := parseTraeDetailParamWithConfigs(data, now)
	if len(models) == 0 {
		return nil, fmt.Errorf("get_detail_param returned no usable chat_completion configs")
	}
	e.replaceTraeDetailModelConfigs(auth, configs)
	models = appendTraeV1RawChatModels(models, now)
	models = appendTraeV3AgentModels(models, now)
	return models, nil
}

func (e *TraeExecutor) fetchModelsFromModelList(ctx context.Context, creds *traeauth.TraeCredentials, auth *cliproxyauth.Auth, now int64) ([]*registry.ModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, traeModelListURL, nil)
	if err != nil {
		return nil, err
	}
	setTraeCommonHeaders(httpReq.Header, creds)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("trae executor: close model list response body error: %v", errClose)
		}
	}()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("model list API error (%d): %s", httpResp.StatusCode, string(b))
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	models := parseTraeModels(data, now)
	models = appendTraeV3AgentModels(models, now)
	return models, nil
}

func resolveTraeProtocol(model string, metadata map[string]any) (string, string) {
	model = strings.TrimSpace(model)
	if protocol := normalizeTraeProtocol(metadataString(metadata, traeProtocolMeta)); protocol != "" {
		return protocol, stripTraeProtocolPrefix(model)
	}

	// Strip the case-insensitive provider prefix "trae/" if present
	if stripped, ok := stripCaseInsensitivePrefix(model, "trae/"); ok {
		model = stripped
	}

	for _, candidate := range []struct {
		prefix   string
		protocol string
	}{
		{"trae-v1/", traeProtocolV1},
		{"raw-v1/", traeProtocolV1},
		{"v1/", traeProtocolV1},
		{"trae-v2/", traeProtocolV2},
		{"raw-v2/", traeProtocolV2},
		{"v2/", traeProtocolV2},
		{"trae-v3/", traeProtocolV3},
		{"agent/", traeProtocolV3},
		{"v3/", traeProtocolV3},
	} {
		if stripped, ok := stripCaseInsensitivePrefix(model, candidate.prefix); ok {
			return candidate.protocol, stripped
		}
	}
	if isTraeV1RawChatModel(model) {
		return traeProtocolV1, model
	}
	if isTraeV2RawChatModel(model) {
		return traeProtocolV2, model
	}
	return traeProtocolV3, model
}

func isTraeV1RawChatModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "seed_m8", "deepseek-r1", "deepseek-v3", "deepseek-v3-0324":
		return true
	default:
		return false
	}
}

func isTraeV2RawChatModel(model string) bool {
	key := strings.ToLower(strings.TrimSpace(model))
	return key == "no_thinking_model"
}

func stripTraeProtocolPrefix(model string) string {
	for _, prefix := range []string{"trae-v1/", "raw-v1/", "v1/", "trae-v2/", "raw-v2/", "v2/", "trae-v3/", "agent/", "v3/"} {
		if stripped, ok := stripCaseInsensitivePrefix(model, prefix); ok {
			return stripped
		}
	}
	return model
}

func stripCaseInsensitivePrefix(value, prefix string) (string, bool) {
	if len(value) < len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		return value, false
	}
	return value[len(prefix):], true
}

func normalizeTraeProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "1", "v1", "raw-v1", "llm_raw_chat_v1":
		return traeProtocolV1
	case "2", "v2", "raw-v2", "llm_raw_chat", "llm_raw_chat_v2":
		return traeProtocolV2
	case "3", "v3", "agent", "builder", "builder_v3", "create_agent_task":
		return traeProtocolV3
	default:
		return ""
	}
}

func (e *TraeExecutor) replaceTraeDetailModelConfigs(auth *cliproxyauth.Auth, configs map[string]traeDetailModelConfig) {
	authID := traeAuthID(auth)
	if authID == "" {
		return
	}
	e.detailConfigMu.Lock()
	defer e.detailConfigMu.Unlock()
	if len(configs) == 0 {
		delete(e.detailConfigs, authID)
		return
	}
	if e.detailConfigs == nil {
		e.detailConfigs = make(map[string]map[string]traeDetailModelConfig)
	}
	copied := make(map[string]traeDetailModelConfig, len(configs))
	for key, config := range configs {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || strings.TrimSpace(config.ModelName) == "" || strings.TrimSpace(config.ConfigName) == "" {
			continue
		}
		copied[key] = traeDetailModelConfig{
			ModelName:  strings.TrimSpace(config.ModelName),
			ConfigName: strings.TrimSpace(config.ConfigName),
		}
	}
	if len(copied) == 0 {
		delete(e.detailConfigs, authID)
		return
	}
	e.detailConfigs[authID] = copied
}

func (e *TraeExecutor) traeDetailModelConfig(auth *cliproxyauth.Auth, configName string) (traeDetailModelConfig, bool) {
	authID := traeAuthID(auth)
	configName = strings.ToLower(strings.TrimSpace(configName))
	if authID == "" || configName == "" {
		return traeDetailModelConfig{}, false
	}
	e.detailConfigMu.RLock()
	defer e.detailConfigMu.RUnlock()
	configs := e.detailConfigs[authID]
	if len(configs) == 0 {
		return traeDetailModelConfig{}, false
	}
	config, ok := configs[configName]
	return config, ok
}

func traeAuthID(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return strings.TrimSpace(auth.ID)
}

func parseTraeModels(data []byte, now int64) []*registry.ModelInfo {
	root := gjson.ParseBytes(data)
	configs := root.Get("model_configs")
	if !configs.Exists() {
		configs = root.Get("data.model_configs")
	}
	if !configs.Exists() || !configs.IsArray() {
		return nil
	}

	models := make([]*registry.ModelInfo, 0, len(configs.Array()))
	seen := make(map[string]struct{})
	for _, item := range configs.Array() {
		if status := item.Get("status"); status.Exists() && !status.Bool() {
			continue
		}
		id := strings.TrimSpace(item.Get("name").String())
		if id == "" {
			continue
		}
		if strings.EqualFold(id, "Doubao_1_5_thinking_pro") {
			continue
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		displayName := firstNonEmpty(item.Get("display_name").String(), item.Get("displayName").String(), id)
		contextLength := int(item.Get("prompt_max_tokens").Int())
		if contextLength <= 0 {
			contextLength = int(item.Get("context_length").Int())
		}
		model := &registry.ModelInfo{
			ID:                  id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                "trae",
			DisplayName:         displayName,
			Name:                id,
			ContextLength:       contextLength,
			MaxCompletionTokens: 65536,
			SupportedParameters: []string{"tools"},
		}
		models = append(models, model)
	}
	return models
}

func appendTraeNoThinkingModel(models []*registry.ModelInfo, now int64) []*registry.ModelInfo {
	for _, model := range models {
		if model != nil && strings.EqualFold(strings.TrimSpace(model.ID), "no_thinking_model") {
			model.SupportedParameters = removeSupportedParameter(model.SupportedParameters, "tools")
			return models
		}
	}
	return append(models, &registry.ModelInfo{
		ID:                  "no_thinking_model",
		Object:              "model",
		Created:             now,
		OwnedBy:             "trae",
		Type:                "trae",
		DisplayName:         "Trae No Thinking Model",
		Name:                "no_thinking_model",
		ContextLength:       40000,
		MaxCompletionTokens: 65536,
	})
}

func removeSupportedParameter(parameters []string, parameter string) []string {
	filtered := parameters[:0]
	for _, current := range parameters {
		if strings.EqualFold(strings.TrimSpace(current), parameter) {
			continue
		}
		filtered = append(filtered, current)
	}
	return filtered
}

func appendTraeV3AgentModels(models []*registry.ModelInfo, now int64) []*registry.ModelInfo {
	v3Models := []struct {
		id          string
		displayName string
		context     int
	}{
		{"glm-4.7", "GLM-4.7", 16000},
		{"glm-5", "GLM-5", 16000},
		{"glm-5.1", "GLM-5.1", 16000},
		{"DeepSeek-V4-Pro", "DeepSeek V4 Pro", 16000},
		{"DeepSeek-V4-Flash", "DeepSeek V4 Flash", 16000},
		{"kimi-k2.6", "Kimi K2.6", 16000},
		{"qwen-3.6-plus", "Qwen 3.6 Plus", 16000},
	}
	existing := make(map[string]struct{}, len(models))
	for _, m := range models {
		if m != nil {
			existing[strings.ToLower(strings.TrimSpace(m.ID))] = struct{}{}
		}
	}
	for _, v := range v3Models {
		if _, ok := existing[strings.ToLower(v.id)]; ok {
			continue
		}
		models = append(models, &registry.ModelInfo{
			ID:                  v.id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                "trae",
			DisplayName:         v.displayName,
			Name:                v.id,
			ContextLength:       v.context,
			MaxCompletionTokens: 65536,
			SupportedParameters: []string{"tools"},
		})
	}
	return models
}

func parseTraeDetailParam(data []byte, now int64) []*registry.ModelInfo {
	models, _ := parseTraeDetailParamWithConfigs(data, now)
	return models
}

func parseTraeDetailParamWithConfigs(data []byte, now int64) ([]*registry.ModelInfo, map[string]traeDetailModelConfig) {
	root := gjson.ParseBytes(data)
	configs := root.Get("config_info_list")
	if !configs.Exists() {
		configs = root.Get("data.config_info_list")
	}
	if !configs.Exists() || !configs.IsArray() {
		return nil, nil
	}

	models := make([]*registry.ModelInfo, 0, len(configs.Array()))
	detailConfigs := make(map[string]traeDetailModelConfig)
	seen := make(map[string]struct{})
	for _, item := range configs.Array() {
		if usage := item.Get("usage").String(); usage != "chat_completion" {
			continue
		}
		if !item.Get("config_switch").Bool() {
			continue
		}
		if item.Get("is_invisible_to_user").Bool() {
			continue
		}
		configName := strings.TrimSpace(item.Get("config_name").String())
		if configName == "" {
			continue
		}
		lower := strings.ToLower(configName)
		if strings.HasPrefix(lower, "custom_model_") || strings.HasPrefix(lower, "custom_claude") || strings.HasPrefix(lower, "custom_gemini") {
			continue
		}
		if strings.HasSuffix(lower, "-auto") || strings.HasSuffix(lower, "_auto") {
			continue
		}
		detail := item.Get("model_detail_list.0")
		modelName := strings.TrimSpace(detail.Get("model_name").String())
		if modelName == "" {
			continue
		}
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}

		displayConfig := item.Get("display_config")
		displayName := displayConfig.Get("display_name").String()
		if displayName == "" {
			displayName = configName
		}

		contextLength := int(detail.Get("prompt_max_tokens").Int())
		maxTokens := int(detail.Get("max_tokens").Int())
		if maxTokens <= 0 {
			maxTokens = 16000
		}

		model := &registry.ModelInfo{
			ID:                  configName,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                "trae",
			DisplayName:         displayName,
			Name:                configName,
			ContextLength:       contextLength,
			MaxCompletionTokens: maxTokens,
			SupportedParameters: []string{"tools"},
		}
		if displayConfig.Get("multimodal").Bool() {
			model.SupportedInputModalities = []string{"text", "image"}
		}
		models = append(models, model)
		detailConfigs[lower] = traeDetailModelConfig{
			ModelName:  modelName,
			ConfigName: configName,
		}
	}
	return models, detailConfigs
}

func appendTraeV1RawChatModels(models []*registry.ModelInfo, now int64) []*registry.ModelInfo {
	v1Models := []struct {
		id          string
		displayName string
		context     int
	}{
		{"seed_m8", "Doubao 1.5 Pro", 28000},
		{"deepseek-R1", "DeepSeek Reasoner R1", 40000},
		{"deepseek-V3", "DeepSeek V3", 40000},
		{"deepseek-V3-0324", "DeepSeek V3 0324", 40000},
	}
	existing := make(map[string]struct{}, len(models))
	for _, m := range models {
		if m != nil {
			existing[strings.ToLower(strings.TrimSpace(m.ID))] = struct{}{}
		}
	}
	for _, v := range v1Models {
		if _, ok := existing[strings.ToLower(v.id)]; ok {
			continue
		}
		models = append(models, &registry.ModelInfo{
			ID:                  v.id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                "trae",
			DisplayName:         v.displayName,
			Name:                v.id,
			ContextLength:       v.context,
			MaxCompletionTokens: 65536,
			SupportedParameters: []string{"tools"},
		})
	}
	return models
}
