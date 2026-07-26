package trae

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
)

const (
	// ProviderID is intentionally different from the native provider during the
	// shadow migration phase.
	ProviderID = "trae-plugin"
	Version    = "0.2.0"
)

var unsafeFileCharacter = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// HostCall invokes one of the host callbacks exposed by the C ABI.
type HostCall func(method string, request []byte) ([]byte, error)

// Plugin owns Trae provider state.
type Plugin struct {
	hostCall       HostCall
	detailConfigMu sync.RWMutex
	detailConfigs  map[string]map[string]traeDetailModelConfig
	streamMu       sync.Mutex
	streamWG       sync.WaitGroup
	activeStreams  map[string]activePluginStream
	shuttingDown   bool
	shutdownOnce   sync.Once
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
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type credentials struct {
	Type          string `json:"type"`
	JWTToken      string `json:"jwt_token"`
	MachineID     string `json:"machine_id"`
	DeviceID      string `json:"device_id"`
	UserID        string `json:"user_id"`
	Name          string `json:"name"`
	WorkspacePath string `json:"workspace_path"`
	RefreshToken  string `json:"refresh_token"`
}

// New constructs a Trae shadow plugin.
func New(hostCall HostCall) *Plugin {
	return &Plugin{
		hostCall:      hostCall,
		detailConfigs: make(map[string]map[string]traeDetailModelConfig),
		activeStreams: make(map[string]activePluginStream),
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
		return pluginruntime.OK(pluginRegistration())
	case pluginabi.MethodPluginShutdown:
		p.Shutdown()
		return pluginruntime.OK(struct{}{})
	case pluginabi.MethodAuthIdentifier:
		return pluginruntime.OK(identifierResponse{Identifier: ProviderID})
	case pluginabi.MethodAuthParse:
		return p.parseAuth(request)
	case pluginabi.MethodAuthLoginStart:
		return p.startLogin(request)
	case pluginabi.MethodAuthLoginPoll:
		return p.pollLogin(request)
	case pluginabi.MethodAuthRefresh:
		return p.refreshAuth(request)
	case pluginabi.MethodModelStatic:
		return p.staticModels(request)
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
	default:
		return pluginruntime.Failure("unknown_method", "unknown method: "+method), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Trae Provider (shadow release candidate)",
			Version:          Version,
			Author:           "CLIProxyAPI contributors",
			GitHubRepository: "https://github.com/coolxll/CLIProxyAPI",
			ConfigFields:     []pluginapi.ConfigField{},
		},
		Capabilities: registrationCapabilities{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{"openai", "claude"},
			ExecutorOutputFormats: []string{"openai", "claude"},
		},
	}
}

func (p *Plugin) parseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Trae auth parse request: %w", errUnmarshal)
	}

	var creds credentials
	if errUnmarshal := json.Unmarshal(req.RawJSON, &creds); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Trae credential JSON: %w", errUnmarshal)
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
	fileName := normalizedFileName(req.FileName, accountLabel(creds))
	label := strings.TrimSpace(creds.Name)
	if label == "" {
		label = accountLabel(creds)
	}
	auth := pluginapi.AuthData{
		Provider:    ProviderID,
		ID:          stableAuthID(creds),
		FileName:    fileName,
		Label:       label,
		StorageJSON: storageJSON,
		Metadata: map[string]any{
			"type":    ProviderID,
			"user_id": strings.TrimSpace(creds.UserID),
			"name":    label,
		},
		Attributes: map[string]string{
			"account": accountLabel(creds),
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

func (p *Plugin) refreshAuth(raw []byte) ([]byte, error) {
	var req authRefreshRPCRequest
	if errUnmarshal := unmarshalRequest(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Trae refresh request: %w", errUnmarshal)
	}
	creds, errCredentials := credentialsFromStorage(req.StorageJSON)
	if errCredentials != nil {
		return nil, errCredentials
	}
	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	if errRefresh := refreshToken(host, &creds, traeAPIHost); errRefresh != nil {
		return nil, errRefresh
	}
	storage := marshalStorage(creds)
	label := strings.TrimSpace(creds.Name)
	if label == "" {
		label = accountLabel(creds)
	}
	nextRefresh := nextRefreshTime(creds, time.Now())
	auth := pluginapi.AuthData{
		Provider:         ProviderID,
		ID:               req.AuthID,
		Label:            label,
		StorageJSON:      storage,
		Metadata:         sanitizedMetadata(creds, label),
		Attributes:       cloneAttributes(req.Attributes, creds.UserID),
		NextRefreshAfter: nextRefresh,
	}
	if auth.ID == "" {
		auth.ID = stableAuthID(creds)
	}
	return pluginruntime.OK(pluginapi.AuthRefreshResponse{Auth: auth, NextRefreshAfter: nextRefresh})
}

func (p *Plugin) modelsForAuth(raw []byte) ([]byte, error) {
	var req authModelRPCRequest
	if errUnmarshal := unmarshalRequest(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Trae model request: %w", errUnmarshal)
	}
	creds, errCredentials := credentialsFromStorage(req.StorageJSON)
	if errCredentials != nil {
		return nil, errCredentials
	}
	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	p.replaceTraeDetailModelConfigs(req.AuthID, nil)
	models, configs, errModels := fetchModels(host, creds)
	if errModels != nil {
		return nil, errModels
	}
	p.replaceTraeDetailModelConfigs(req.AuthID, configs)
	return pluginOK(pluginapi.ModelResponse{
		Provider: ProviderID,
		Models:   models,
	})
}

func (p *Plugin) staticModels(raw []byte) ([]byte, error) {
	var req pluginapi.StaticModelRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Trae static model request: %w", errUnmarshal)
	}
	return pluginOK(pluginapi.ModelResponse{
		Provider: ProviderID,
		Models:   staticModels(),
	})
}

func validateCredentials(creds credentials) error {
	required := []struct {
		name  string
		value string
	}{
		{name: "jwt_token", value: creds.JWTToken},
		{name: "machine_id", value: creds.MachineID},
		{name: "device_id", value: creds.DeviceID},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("Trae credential is missing required field %s", field.name)
		}
	}
	return nil
}

func compactJSON(raw []byte) ([]byte, error) {
	var out bytes.Buffer
	if errCompact := json.Compact(&out, raw); errCompact != nil {
		return nil, fmt.Errorf("compact Trae credential JSON: %w", errCompact)
	}
	return out.Bytes(), nil
}

func stableAuthID(creds credentials) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(creds.JWTToken) + "\x00" + strings.TrimSpace(creds.MachineID)))
	return ProviderID + ":" + accountLabel(creds) + ":" + hex.EncodeToString(digest[:4])
}

func accountLabel(creds credentials) string {
	if userID := strings.TrimSpace(creds.UserID); userID != "" && userID != "0" {
		return userID
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(creds.JWTToken)))
	return "account-" + hex.EncodeToString(digest[:4])
}

func normalizedFileName(candidate, account string) string {
	base := filepath.Base(strings.TrimSpace(candidate))
	if strings.EqualFold(filepath.Ext(base), ".json") && base != ".json" {
		return base
	}
	account = unsafeFileCharacter.ReplaceAllString(strings.TrimSpace(account), "-")
	account = strings.Trim(account, "-._")
	if account == "" {
		account = "account"
	}
	return ProviderID + "-" + account + ".json"
}

func marshalStorage(creds credentials) []byte {
	data, err := json.Marshal(creds)
	if err != nil {
		return nil
	}
	return data
}

func sanitizedMetadata(creds credentials, label string) map[string]any {
	return map[string]any{
		"type":    ProviderID,
		"user_id": strings.TrimSpace(creds.UserID),
		"name":    label,
	}
}

func cloneAttributes(source map[string]string, userID string) map[string]string {
	result := make(map[string]string, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	result["account"] = userID
	return result
}

func unmarshalRequest(raw []byte, target any) error {
	return json.Unmarshal(raw, target)
}

func pluginOK[T any](result T) ([]byte, error) {
	return pluginruntime.OK(result)
}
