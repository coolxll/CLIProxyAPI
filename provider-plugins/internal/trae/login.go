package trae

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	traeAuthHost       = "https://www.trae.cn"
	traeOAuthFlowTTL   = 10 * time.Minute
	defaultTraeID      = "2569994131757818"
	traeCallbackPrefix = ".oauth-"
)

type authLoginStartRPCRequest struct {
	pluginapi.AuthLoginStartRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type authLoginPollRPCRequest struct {
	pluginapi.AuthLoginPollRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type oauthCallbackFilePayload struct {
	Code  string `json:"code"`
	State string `json:"state"`
	Error string `json:"error"`
}

func (p *Plugin) startLogin(raw []byte) ([]byte, error) {
	var req authLoginStartRPCRequest
	if errUnmarshal := unmarshalRequest(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Trae login start request: %w", errUnmarshal)
	}
	callbackURL, errCallback := traeCallbackURL(req.BaseURL)
	if errCallback != nil {
		return nil, errCallback
	}
	state, errState := newOAuthState()
	if errState != nil {
		return nil, errState
	}
	query := callbackURL.Query()
	query.Set("provider", ProviderID)
	query.Set("state", state)
	callbackURL.RawQuery = query.Encode()

	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	loginHost := resolveTraeLoginHost(host, state)
	machineID := firstNonEmpty(metadataString(req.Metadata, "machine_id"), defaultTraeID)
	deviceID := firstNonEmpty(metadataString(req.Metadata, "device_id"), defaultTraeID)
	verificationURL := buildTraeVerificationURL(loginHost, state, callbackURL.String(), machineID, deviceID)
	expiresAt := time.Now().Add(traeOAuthFlowTTL).UTC()

	return pluginOK(pluginapi.AuthLoginStartResponse{
		Provider:  ProviderID,
		URL:       verificationURL,
		State:     state,
		ExpiresAt: expiresAt,
		Metadata: map[string]any{
			"login_host": loginHost,
			"machine_id": machineID,
			"device_id":  deviceID,
			"expires_at": expiresAt,
		},
	})
}

func (p *Plugin) pollLogin(raw []byte) ([]byte, error) {
	var req authLoginPollRPCRequest
	if errUnmarshal := unmarshalRequest(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Trae login poll request: %w", errUnmarshal)
	}
	state := strings.TrimSpace(req.State)
	if !validOAuthState(state) {
		return nil, fmt.Errorf("invalid Trae login state")
	}
	if expiry, ok := metadataTime(req.Metadata, "expires_at"); ok && time.Now().After(expiry) {
		return pluginOK(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "Trae login expired",
		})
	}
	authDir := strings.TrimSpace(req.Host.AuthDir)
	if authDir == "" {
		return nil, fmt.Errorf("Trae login auth directory is empty")
	}
	callbackPath := filepath.Join(authDir, traeCallbackPrefix+ProviderID+"-"+state+".oauth")
	data, errRead := os.ReadFile(callbackPath)
	if errors.Is(errRead, os.ErrNotExist) {
		return pluginOK(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending})
	}
	if errRead != nil {
		return nil, fmt.Errorf("read Trae login callback: %w", errRead)
	}
	defer func() {
		_ = os.Remove(callbackPath)
	}()

	var callback oauthCallbackFilePayload
	if errUnmarshal := json.Unmarshal(data, &callback); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Trae login callback: %w", errUnmarshal)
	}
	if callback.State != state {
		return pluginOK(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "Trae login state does not match",
		})
	}
	if message := strings.TrimSpace(callback.Error); message != "" {
		return pluginOK(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: message,
		})
	}

	fragment, errFragment := url.ParseQuery(strings.TrimLeft(strings.TrimSpace(callback.Code), "#?"))
	if errFragment != nil {
		return nil, fmt.Errorf("decode Trae login callback fragment: %w", errFragment)
	}
	refresh := strings.TrimSpace(fragment.Get("refreshToken"))
	if refresh == "" {
		refresh = strings.TrimSpace(fragment.Get("refresh_token"))
	}
	if refresh == "" {
		return pluginOK(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "Trae login callback is missing refresh token",
		})
	}

	loginHost := firstNonEmpty(
		fragment.Get("loginHost"),
		fragment.Get("host"),
		metadataString(req.Metadata, "login_host"),
		traeAPIHost,
	)
	creds := credentials{
		Type:         ProviderID,
		MachineID:    firstNonEmpty(metadataString(req.Metadata, "machine_id"), defaultTraeID),
		DeviceID:     firstNonEmpty(metadataString(req.Metadata, "device_id"), defaultTraeID),
		RefreshToken: refresh,
	}
	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	if errRefresh := refreshToken(host, &creds, loginHost); errRefresh != nil {
		return nil, fmt.Errorf("exchange Trae login token: %w", errRefresh)
	}
	if errValidate := validateCredentials(creds); errValidate != nil {
		return nil, errValidate
	}
	label := accountLabel(creds)
	creds.Name = "trae-" + label
	auth := pluginapi.AuthData{
		Provider:         ProviderID,
		ID:               stableAuthID(creds),
		FileName:         normalizedFileName("", label),
		Label:            creds.Name,
		StorageJSON:      marshalStorage(creds),
		Metadata:         sanitizedMetadata(creds, creds.Name),
		Attributes:       map[string]string{"account": label},
		NextRefreshAfter: nextRefreshTime(creds, time.Now()),
	}
	return pluginOK(pluginapi.AuthLoginPollResponse{
		Status: pluginapi.AuthLoginStatusSuccess,
		Auth:   auth,
	})
}

func newOAuthState() (string, error) {
	var raw [24]byte
	if _, errRead := rand.Read(raw[:]); errRead != nil {
		return "", fmt.Errorf("generate Trae login state: %w", errRead)
	}
	return hex.EncodeToString(raw[:]), nil
}

func validOAuthState(state string) bool {
	if state == "" || len(state) > 128 || strings.Contains(state, "..") {
		return false
	}
	for _, r := range state {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func traeCallbackURL(raw string) (*url.URL, error) {
	parsed, errParse := url.Parse(strings.TrimSpace(raw))
	if errParse != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("Trae login callback URL is invalid")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("Trae login callback URL must use HTTP or HTTPS")
	}
	return parsed, nil
}

func resolveTraeLoginHost(host hostRPC, state string) string {
	payload, errMarshal := json.Marshal(map[string]string{
		"loginTraceID":   state,
		"login_trace_id": state,
	})
	if errMarshal != nil {
		return traeAuthHost
	}
	for _, endpoint := range []string{
		traeAPIHost + "/cloudide/api/v3/trae/GetLoginGuidance",
		traeAuthHost + "/cloudide/api/v3/trae/GetLoginGuidance",
	} {
		resp, errDo := host.do(pluginapi.HTTPRequest{
			Method: http.MethodPost,
			URL:    endpoint,
			Headers: http.Header{
				"Accept":       []string{"application/json"},
				"Content-Type": []string{"application/json"},
				"User-Agent":   []string{"Trae/1.0.0 CLIProxyAPI-provider-plugin"},
			},
			Body: payload,
		})
		if errDo != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		var result map[string]any
		if errUnmarshal := json.Unmarshal(resp.Body, &result); errUnmarshal != nil {
			continue
		}
		if loginHost := extractString(result, [][]string{
			{"Result", "LoginHost"},
			{"Result", "loginHost"},
			{"result", "LoginHost"},
			{"result", "loginHost"},
			{"data", "loginHost"},
			{"LoginHost"},
			{"loginHost"},
		}); loginHost != "" {
			return loginHost
		}
	}
	return traeAuthHost
}

func buildTraeVerificationURL(loginHost, state, callbackURL, machineID, deviceID string) string {
	hostURL, errParse := url.Parse(strings.TrimSpace(loginHost))
	if errParse != nil || hostURL == nil || hostURL.Scheme == "" || hostURL.Host == "" {
		hostURL, _ = url.Parse(traeAuthHost)
	}
	query := url.Values{
		"login_version":     {"1"},
		"auth_from":         {"trae"},
		"login_channel":     {"native_ide"},
		"plugin_version":    {"2.3.33255"},
		"auth_type":         {"local"},
		"client_id":         {traeAuthClientID},
		"redirect":          {"0"},
		"login_trace_id":    {state},
		"auth_callback_url": {callbackURL},
		"x_device_type":     {"desktop"},
		"x_os_version":      {runtime.GOOS},
		"x_device_brand":    {""},
		"x_app_version":     {"3.5.54"},
		"x_app_type":        {"stable"},
		"x_env":             {"production"},
	}
	if machineID != "" {
		query.Set("machine_id", machineID)
		query.Set("x_machine_id", machineID)
	}
	if deviceID != "" {
		query.Set("device_id", deviceID)
		query.Set("x_device_id", deviceID)
	}
	return hostURL.Scheme + "://" + hostURL.Host + "/authorization?" + query.Encode()
}

func metadataTime(metadata map[string]any, key string) (time.Time, bool) {
	value, ok := metadata[key]
	if !ok {
		return time.Time{}, false
	}
	switch typed := value.(type) {
	case string:
		parsed, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(typed))
		return parsed, errParse == nil
	case time.Time:
		return typed, !typed.IsZero()
	default:
		return time.Time{}, false
	}
}
