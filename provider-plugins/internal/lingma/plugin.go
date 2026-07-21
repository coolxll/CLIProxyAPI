package lingma

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/sjson"
)

const (
	// ProviderID is intentionally different from the native provider during the
	// shadow migration phase.
	ProviderID = "lingma-plugin"
	Version    = "0.2.0"
)

var unsafeFileCharacter = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// HostCall invokes one of the host callbacks exposed by the C ABI.
type HostCall func(method string, request []byte) ([]byte, error)

// Plugin owns Lingma authentication, model discovery, translation, execution,
// recovery, and one-shot thinking fallback state.
type Plugin struct {
	hostCall HostCall
	mu       sync.RWMutex
	config   pluginConfig
	fallback *oneShotFallback
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
	ThinkingApplier       bool                         `json:"thinking_applier"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type credentials struct {
	Type               string `json:"type"`
	MachineID          string `json:"machine_id"`
	UID                string `json:"uid"`
	OrganizationID     string `json:"organization_id"`
	CosyKey            string `json:"key"`
	SecurityOAuthToken string `json:"security_oauth_token"`
	RefreshToken       string `json:"refresh_token"`
	ExpireTime         int64  `json:"expire_time"`
	EncryptUserInfo    string `json:"encrypt_user_info"`
	UserType           string `json:"user_type"`
	Name               string `json:"name"`
}

// New constructs a Lingma shadow plugin.
func New(hostCall HostCall) *Plugin {
	return &Plugin{
		hostCall: hostCall,
		config:   defaultPluginConfig(),
		fallback: newOneShotFallback(),
	}
}

// SetHostCall updates the host callback after the C ABI host is initialized.
func (p *Plugin) SetHostCall(hostCall HostCall) {
	if p == nil {
		return
	}
	p.hostCall = hostCall
}

// Handle dispatches a C ABI JSON method.
func (p *Plugin) Handle(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var lifecycle lifecycleRequest
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &lifecycle); errUnmarshal != nil {
				return nil, fmt.Errorf("decode lifecycle request: %w", errUnmarshal)
			}
		}
		config, errConfig := parsePluginConfig(lifecycle.ConfigYAML)
		if errConfig != nil {
			return nil, fmt.Errorf("decode Lingma plugin configuration: %w", errConfig)
		}
		p.mu.Lock()
		p.config = config
		p.mu.Unlock()
		return pluginruntime.OK(pluginRegistration())
	case pluginabi.MethodPluginShutdown:
		return pluginruntime.OK(struct{}{})
	case pluginabi.MethodAuthIdentifier:
		return pluginruntime.OK(identifierResponse{Identifier: ProviderID})
	case pluginabi.MethodAuthParse:
		return p.parseAuth(request)
	case pluginabi.MethodAuthLoginStart:
		return nil, fmt.Errorf("interactive Lingma login is not implemented in the shadow plugin")
	case pluginabi.MethodAuthLoginPoll:
		return nil, fmt.Errorf("interactive Lingma login is not implemented in the shadow plugin")
	case pluginabi.MethodAuthRefresh:
		return p.refreshAuth(request)
	case pluginabi.MethodModelStatic:
		return pluginruntime.OK(pluginapi.ModelResponse{Provider: ProviderID})
	case pluginabi.MethodModelForAuth:
		return p.modelsForAuth(request)
	case pluginabi.MethodExecutorIdentifier:
		return pluginruntime.OK(identifierResponse{Identifier: ProviderID})
	case pluginabi.MethodExecutorExecute:
		return p.execute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return p.executeStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return p.countTokens(request)
	case pluginabi.MethodExecutorHTTPRequest:
		return p.httpRequest(request)
	case pluginabi.MethodThinkingIdentifier:
		return pluginruntime.OK(identifierResponse{Identifier: ProviderID})
	case pluginabi.MethodThinkingApply:
		return p.applyThinking(request)
	default:
		return pluginruntime.Failure("unknown_method", "unknown method: "+method), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Lingma Provider (shadow M2)",
			Version:          Version,
			Author:           "CLIProxyAPI contributors",
			GitHubRepository: "https://github.com/coolxll/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "api-base-url", Type: pluginapi.ConfigFieldTypeString, Description: "Lingma API base URL; intended for controlled testing."},
				{Name: "force-http-1-1", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Force upstream HTTP/1.1 to avoid HTTP/2 stream resets."},
				{Name: "thinking-fallback-enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable one-shot and in-request thinking fallback for large gm51model histories."},
				{Name: "thinking-fallback-ttl", Type: pluginapi.ConfigFieldTypeString, Description: "One-shot fallback marker lifetime."},
				{Name: "upstream-recovery", Type: pluginapi.ConfigFieldTypeObject, Description: "Transient retry settings."},
			},
		},
		Capabilities: registrationCapabilities{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{formatOpenAI, formatClaude},
			ExecutorOutputFormats: []string{formatOpenAI, formatClaude},
			ThinkingApplier:       true,
		},
	}
}

func (p *Plugin) parseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Lingma auth parse request: %w", errUnmarshal)
	}

	var creds credentials
	if errUnmarshal := json.Unmarshal(req.RawJSON, &creds); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Lingma credential JSON: %w", errUnmarshal)
	}
	if !strings.EqualFold(strings.TrimSpace(creds.Type), ProviderID) {
		return pluginruntime.OK(pluginapi.AuthParseResponse{Handled: false})
	}
	if errValidate := validateCredentials(creds); errValidate != nil {
		return nil, errValidate
	}

	storageJSON, errStorage := compactJSON(req.RawJSON)
	if errStorage != nil {
		return nil, errStorage
	}
	fileName := normalizedFileName(req.FileName, creds.UID)
	label := strings.TrimSpace(creds.Name)
	if label == "" {
		label = creds.UID
	}
	auth := pluginapi.AuthData{
		Provider:    ProviderID,
		ID:          stableAuthID(creds),
		FileName:    fileName,
		Label:       label,
		StorageJSON: storageJSON,
		Metadata: map[string]any{
			"type": ProviderID,
			"uid":  creds.UID,
			"name": label,
		},
		Attributes: map[string]string{
			"account": creds.UID,
		},
		NextRefreshAfter: nextRefreshTime(creds, time.Now()),
	}
	return pluginruntime.OK(pluginapi.AuthParseResponse{Handled: true, Auth: auth})
}

type authRefreshRPCRequest struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type authModelRPCRequest struct {
	pluginapi.AuthModelRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type thinkingRPCRequest struct {
	pluginapi.ThinkingApplyRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func (p *Plugin) refreshAuth(raw []byte) ([]byte, error) {
	var req authRefreshRPCRequest
	if errUnmarshal := unmarshalRequest(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Lingma refresh request: %w", errUnmarshal)
	}
	creds, errCredentials := credentialsFromStorage(req.StorageJSON)
	if errCredentials != nil {
		return nil, errCredentials
	}
	config := p.configSnapshot()
	if errExchange := exchangeToken(hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}, &creds, config.APIBaseURL); errExchange != nil {
		return nil, errExchange
	}
	storage := marshalStorage(creds)
	label := strings.TrimSpace(creds.Name)
	if label == "" {
		label = creds.UID
	}
	nextRefresh := nextRefreshTime(creds, time.Now())
	auth := pluginapi.AuthData{
		Provider:         ProviderID,
		ID:               req.AuthID,
		Label:            label,
		StorageJSON:      storage,
		Metadata:         sanitizedMetadata(creds, label),
		Attributes:       cloneAttributes(req.Attributes, creds.UID),
		NextRefreshAfter: nextRefresh,
	}
	if auth.ID == "" {
		auth.ID = stableAuthID(creds)
	}
	return pluginruntime.OK(pluginapi.AuthRefreshResponse{Auth: auth, NextRefreshAfter: nextRefresh})
}

func (p *Plugin) applyThinking(raw []byte) ([]byte, error) {
	var req thinkingRPCRequest
	if errUnmarshal := unmarshalRequest(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Lingma thinking request: %w", errUnmarshal)
	}
	enabled := true
	switch strings.ToLower(strings.TrimSpace(req.Config.Mode)) {
	case "none":
		enabled = false
	case "budget":
		enabled = req.Config.Budget != 0
	case "level":
		enabled = !strings.EqualFold(strings.TrimSpace(req.Config.Level), "none")
	}
	body := req.Body
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	result, errSet := sjson.SetBytes(body, "model_config.is_reasoning", enabled)
	if errSet != nil {
		return nil, errSet
	}
	if !enabled {
		result = disableThinking(result)
	}
	return pluginruntime.OK(pluginapi.PayloadResponse{Body: result})
}

func sanitizedMetadata(creds credentials, label string) map[string]any {
	metadata := map[string]any{
		"type":        ProviderID,
		"uid":         creds.UID,
		"name":        label,
		"user_type":   creds.UserType,
		"expire_time": creds.ExpireTime,
		"expires_at":  credentialExpiry(creds, time.Now()),
	}
	if creds.OrganizationID != "" {
		metadata["organization_id"] = creds.OrganizationID
	}
	return metadata
}

func cloneAttributes(source map[string]string, uid string) map[string]string {
	result := make(map[string]string, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	result["account"] = uid
	return result
}

func (p *Plugin) configSnapshot() pluginConfig {
	if p == nil {
		return defaultPluginConfig()
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config
}

func validateCredentials(creds credentials) error {
	required := []struct {
		name  string
		value string
	}{
		{name: "machine_id", value: creds.MachineID},
		{name: "uid", value: creds.UID},
		{name: "key", value: creds.CosyKey},
		{name: "security_oauth_token", value: creds.SecurityOAuthToken},
		{name: "encrypt_user_info", value: creds.EncryptUserInfo},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("Lingma credential is missing required field %s", field.name)
		}
	}
	return nil
}

func compactJSON(raw []byte) ([]byte, error) {
	var out bytes.Buffer
	if errCompact := json.Compact(&out, raw); errCompact != nil {
		return nil, fmt.Errorf("compact Lingma credential JSON: %w", errCompact)
	}
	return out.Bytes(), nil
}

func stableAuthID(creds credentials) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(creds.MachineID)))
	return ProviderID + ":" + strings.TrimSpace(creds.UID) + ":" + hex.EncodeToString(digest[:4])
}

func normalizedFileName(candidate, uid string) string {
	base := filepath.Base(strings.TrimSpace(candidate))
	if strings.EqualFold(filepath.Ext(base), ".json") && base != ".json" {
		return base
	}
	account := unsafeFileCharacter.ReplaceAllString(strings.TrimSpace(uid), "-")
	account = strings.Trim(account, "-._")
	if account == "" {
		account = "account"
	}
	return ProviderID + "-" + account + ".json"
}
