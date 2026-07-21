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

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	// ProviderID is intentionally different from the native provider during the
	// shadow migration phase.
	ProviderID = "trae-plugin"
	Version    = "0.1.0"
)

var unsafeFileCharacter = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// HostCall invokes one of the host callbacks exposed by the C ABI.
type HostCall func(method string, request []byte) ([]byte, error)

// Plugin owns Trae provider state. The initial milestone is intentionally
// limited to registration and offline credential parsing.
type Plugin struct {
	hostCall HostCall
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
	AuthProvider bool `json:"auth_provider"`
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
	return &Plugin{hostCall: hostCall}
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
	case pluginabi.MethodAuthIdentifier:
		return pluginruntime.OK(identifierResponse{Identifier: ProviderID})
	case pluginabi.MethodAuthParse:
		return p.parseAuth(request)
	case pluginabi.MethodAuthLoginStart:
		return nil, fmt.Errorf("interactive Trae login is not implemented in the shadow plugin")
	case pluginabi.MethodAuthLoginPoll:
		return nil, fmt.Errorf("interactive Trae login is not implemented in the shadow plugin")
	case pluginabi.MethodAuthRefresh:
		return nil, fmt.Errorf("Trae credential refresh is not implemented in the shadow plugin")
	default:
		return pluginruntime.Failure("unknown_method", "unknown method: "+method), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Trae Provider (shadow)",
			Version:          Version,
			Author:           "CLIProxyAPI contributors",
			GitHubRepository: "https://github.com/coolxll/CLIProxyAPI",
			ConfigFields:     []pluginapi.ConfigField{},
		},
		Capabilities: registrationCapabilities{AuthProvider: true},
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
	}
	return pluginruntime.OK(pluginapi.AuthParseResponse{Handled: true, Auth: auth})
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
