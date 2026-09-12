package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const (
	ProviderID = "openrouter-plugin"
	Version    = "0.2.0"
)

// HostCall invokes one of the host callbacks exposed by the C ABI.
type HostCall func(method string, request []byte) ([]byte, error)

// Plugin owns OpenRouter provider state.
type Plugin struct {
	hostCall      HostCall
	streamMu      sync.Mutex
	streamWG      sync.WaitGroup
	activeStreams map[string]activePluginStream
	shuttingDown  bool
	shutdownOnce  sync.Once
}

type activePluginStream struct {
	host             hostRPC
	upstreamStreamID string
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

type authRefreshRPCRequest struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type authModelRPCRequest struct {
	pluginapi.AuthModelRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// New constructs an OpenRouter plugin instance.
func New(hostCall HostCall) *Plugin {
	return &Plugin{
		hostCall:      hostCall,
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
			Name:             "OpenRouter Free Provider",
			Version:          Version,
			Author:           "CLIProxyAPI contributors",
			GitHubRepository: "https://openrouter.ai",
			ConfigFields:     []pluginapi.ConfigField{},
		},
		Capabilities: registrationCapabilities{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeBoth,
			ExecutorInputFormats:  []string{"openai", "claude"},
			ExecutorOutputFormats: []string{"openai", "claude"},
		},
	}
}

func (p *Plugin) parseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode OpenRouter auth parse request: %w", errUnmarshal)
	}

	var creds credentials
	if errUnmarshal := json.Unmarshal(req.RawJSON, &creds); errUnmarshal != nil {
		return nil, fmt.Errorf("decode OpenRouter credential JSON: %w", errUnmarshal)
	}
	t := strings.ToLower(strings.TrimSpace(creds.Type))
	if t != ProviderID && t != "openrouter" {
		return pluginruntime.OK(pluginapi.AuthParseResponse{Handled: false})
	}
	if errValidate := validateCredentials(creds); errValidate != nil {
		return nil, errValidate
	}

	storageJSON, errStorage := compactJSON(req.RawJSON)
	if errStorage != nil {
		return nil, errStorage
	}
	label := accountLabel(creds)
	fileName := normalizedFileName(req.FileName, label)
	auth := pluginapi.AuthData{
		Provider:    ProviderID,
		ID:          stableAuthID(creds),
		FileName:    fileName,
		Label:       label,
		StorageJSON: []byte(storageJSON),
		Metadata: map[string]any{
			"type":       ProviderID,
			"base_url":   creds.baseURL(),
			"name":       label,
			"free_only":  creds.isFreeOnly(),
		},
		Attributes: map[string]string{
			"account": label,
		},
	}
	return pluginruntime.OK(pluginapi.AuthParseResponse{Handled: true, Auth: auth})
}

func (p *Plugin) refreshAuth(raw []byte) ([]byte, error) {
	var req authRefreshRPCRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode OpenRouter refresh request: %w", errUnmarshal)
	}
	creds, errCredentials := credentialsFromStorage(req.StorageJSON)
	if errCredentials != nil {
		return nil, errCredentials
	}
	label := accountLabel(creds)
	auth := pluginapi.AuthData{
		Provider:    ProviderID,
		ID:          req.AuthID,
		Label:       label,
		StorageJSON: req.StorageJSON,
		Metadata: map[string]any{
			"type":       ProviderID,
			"base_url":   creds.baseURL(),
			"name":       label,
			"free_only":  creds.isFreeOnly(),
		},
		Attributes: req.Attributes,
	}
	if auth.ID == "" {
		auth.ID = stableAuthID(creds)
	}
	return pluginruntime.OK(pluginapi.AuthRefreshResponse{Auth: auth})
}

func (p *Plugin) modelsForAuth(raw []byte) ([]byte, error) {
	var req authModelRPCRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode OpenRouter model request: %w", errUnmarshal)
	}
	creds, errCredentials := credentialsFromStorage(req.StorageJSON)
	if errCredentials != nil {
		return nil, errCredentials
	}
	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	models, errModels := fetchModels(host, creds)
	if errModels != nil || len(models) == 0 {
		models = staticModels()
	}
	return pluginruntime.OK(pluginapi.ModelResponse{
		Provider: ProviderID,
		Models:   models,
	})
}

func (p *Plugin) staticModels(raw []byte) ([]byte, error) {
	return pluginruntime.OK(pluginapi.ModelResponse{
		Provider: ProviderID,
		Models:   staticModels(),
	})
}

func (p *Plugin) countTokens(raw []byte) ([]byte, error) {
	var req executorRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode openrouter token count request: %w", err)
	}
	if req.SourceFormat == "" {
		return nil, fmt.Errorf("source format is required")
	}

	from := sdktranslator.Format(req.SourceFormat)
	to := sdktranslator.FormatOpenAI
	translated := sdktranslator.TranslateRequest(from, to, req.Model, req.Payload, false)

	tokenCount := int64((len(translated) + 3) / 4)
	usageJSON := fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":0,"total_tokens":%d}`, tokenCount, tokenCount)
	translatedUsage := sdktranslator.TranslateTokenCount(context.Background(), to, from, tokenCount, []byte(usageJSON))

	resp := pluginapi.ExecutorResponse{
		Payload: translatedUsage,
	}
	return pluginruntime.OK(resp)
}

func (p *Plugin) httpRequest(raw []byte) ([]byte, error) {
	return pluginruntime.Failure("unsupported", "raw HTTP request is not supported for OpenRouter plugin"), nil
}

// Shutdown cancels active streams and waits for completion.
func (p *Plugin) Shutdown() {
	p.shutdownOnce.Do(func() {
		p.streamMu.Lock()
		p.shuttingDown = true
		streams := make([]activePluginStream, 0, len(p.activeStreams))
		for _, s := range p.activeStreams {
			streams = append(streams, s)
		}
		p.streamMu.Unlock()

		for _, s := range streams {
			if s.upstreamStreamID != "" {
				s.host.closeHTTPStream(s.upstreamStreamID)
			}
		}
		p.streamWG.Wait()
	})
}

func (p *Plugin) beginStream(outputStreamID string, host hostRPC, upstreamStreamID string) bool {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	if p.shuttingDown {
		return false
	}
	p.activeStreams[outputStreamID] = activePluginStream{
		host:             host,
		upstreamStreamID: upstreamStreamID,
	}
	p.streamWG.Add(1)
	return true
}

func (p *Plugin) updateUpstreamStreamID(outputStreamID, upstreamStreamID string) {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	if s, exists := p.activeStreams[outputStreamID]; exists {
		s.upstreamStreamID = upstreamStreamID
		p.activeStreams[outputStreamID] = s
	}
}

func (p *Plugin) endStream(outputStreamID string) {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	delete(p.activeStreams, outputStreamID)
	p.streamWG.Done()
}
