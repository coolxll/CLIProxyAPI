package trae

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestTraeLoginStartAndPoll(t *testing.T) {
	authDir := t.TempDir()
	var refreshCalls int
	host := func(method string, raw []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			t.Fatalf("unexpected host method %q", method)
		}
		var request hostHTTPRequest
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			t.Fatalf("decode host request: %v", errUnmarshal)
		}
		switch {
		case strings.Contains(request.Request.URL, "GetLoginGuidance"):
			return pluginruntime.OK(pluginapi.HTTPResponse{
				StatusCode: http.StatusOK,
				Body:       []byte(`{"Result":{"LoginHost":"https://www.trae.cn"}}`),
			})
		case strings.Contains(request.Request.URL, "ExchangeToken"):
			refreshCalls++
			return pluginruntime.OK(pluginapi.HTTPResponse{
				StatusCode: http.StatusOK,
				Body: []byte(`{"Result":{"Token":"` + syntheticTraeJWT(t, "user-42") +
					`","RefreshToken":"refresh-2"}}`),
			})
		case strings.Contains(request.Request.URL, "checkin_credits"):
			return pluginruntime.OK(pluginapi.HTTPResponse{
				StatusCode: http.StatusOK,
				Body:       []byte(`{"code":0,"msg":"success"}`),
			})
		default:
			t.Fatalf("unexpected URL %q", request.Request.URL)
			return nil, nil
		}
	}
	plugin := New(host)

	startRaw, errMarshal := json.Marshal(authLoginStartRPCRequest{
		AuthLoginStartRequest: pluginapi.AuthLoginStartRequest{
			Provider: ProviderID,
			BaseURL:  "http://127.0.0.1:8317/v0/management/oauth-callback",
			Host:     pluginapi.HostConfigSummary{AuthDir: authDir},
		},
		HostCallbackID: "callback-start",
	})
	if errMarshal != nil {
		t.Fatalf("marshal start request: %v", errMarshal)
	}
	startEnvelope, errStart := plugin.Handle(pluginabi.MethodAuthLoginStart, startRaw)
	if errStart != nil {
		t.Fatalf("start login: %v", errStart)
	}
	start := decodeResult[pluginapi.AuthLoginStartResponse](t, startEnvelope)
	if start.Provider != ProviderID || start.State == "" {
		t.Fatalf("unexpected start response: %+v", start)
	}
	verificationURL, errParse := url.Parse(start.URL)
	if errParse != nil {
		t.Fatalf("parse verification URL: %v", errParse)
	}
	callbackURL, errParse := url.Parse(verificationURL.Query().Get("auth_callback_url"))
	if errParse != nil {
		t.Fatalf("parse callback URL: %v", errParse)
	}
	if verificationURL.Query().Get("login_trace_id") != start.State {
		t.Fatalf("verification URL is missing login_trace_id: %s", verificationURL)
	}
	if callbackURL.Path != "/authorize" {
		t.Fatalf("callback URL path must be /authorize, got: %s", callbackURL)
	}

	pollReq := authLoginPollRPCRequest{
		AuthLoginPollRequest: pluginapi.AuthLoginPollRequest{
			Provider: ProviderID,
			State:    start.State,
			Host:     pluginapi.HostConfigSummary{AuthDir: authDir},
			Metadata: start.Metadata,
		},
		HostCallbackID: "callback-poll",
	}
	pollRaw, _ := json.Marshal(pollReq)
	pendingEnvelope, errPending := plugin.Handle(pluginabi.MethodAuthLoginPoll, pollRaw)
	if errPending != nil {
		t.Fatalf("poll pending: %v", errPending)
	}
	pending := decodeResult[pluginapi.AuthLoginPollResponse](t, pendingEnvelope)
	if pending.Status != pluginapi.AuthLoginStatusPending {
		t.Fatalf("pending status = %q", pending.Status)
	}

	callback := oauthCallbackFilePayload{
		Code:  "refreshToken=refresh-1&loginHost=" + url.QueryEscape("https://api.trae.com.cn"),
		State: start.State,
	}
	callbackData, _ := json.Marshal(callback)
	callbackPath := filepath.Join(authDir, traeCallbackPrefix+ProviderID+"-"+start.State+".oauth")
	if errWrite := os.WriteFile(callbackPath, callbackData, 0o600); errWrite != nil {
		t.Fatalf("write callback: %v", errWrite)
	}

	successEnvelope, errPoll := plugin.Handle(pluginabi.MethodAuthLoginPoll, pollRaw)
	if errPoll != nil {
		t.Fatalf("poll success: %v", errPoll)
	}
	success := decodeResult[pluginapi.AuthLoginPollResponse](t, successEnvelope)
	if success.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("success status = %q, message = %q", success.Status, success.Message)
	}
	if success.Auth.Provider != ProviderID || !strings.HasPrefix(success.Auth.ID, ProviderID+":user-42:") {
		t.Fatalf("unexpected auth: %+v", success.Auth)
	}
	var stored credentials
	if errUnmarshal := json.Unmarshal(success.Auth.StorageJSON, &stored); errUnmarshal != nil {
		t.Fatalf("decode stored credentials: %v", errUnmarshal)
	}
	if stored.RefreshToken != "refresh-2" || stored.UserID != "user-42" || stored.Type != ProviderID {
		t.Fatalf("unexpected stored credentials: %+v", stored)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
	if _, errStat := os.Stat(callbackPath); !os.IsNotExist(errStat) {
		t.Fatalf("callback file was not removed: %v", errStat)
	}
}

func syntheticTraeJWT(t *testing.T, userID string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, errMarshal := json.Marshal(map[string]any{
		"data": map[string]string{"id": userID},
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	if errMarshal != nil {
		t.Fatalf("marshal JWT payload: %v", errMarshal)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestExtractCallbackDeviceInfo(t *testing.T) {
	// Case 1: direct query parameters
	values1 := url.Values{
		"BoundDeviceID": {"qwxe24l3old7ow"},
		"refreshToken":  {"refresh-abc"},
		"token":         {"token-xyz"},
	}
	did1, ref1, tok1 := extractCallbackDeviceInfo(values1)
	if did1 != "qwxe24l3old7ow" || ref1 != "refresh-abc" || tok1 != "token-xyz" {
		t.Errorf("case 1 failed: got did=%q, ref=%q, tok=%q", did1, ref1, tok1)
	}

	// Case 2: inside userJwt JSON
	userJwtJSON := `{"BoundDeviceID":"dev-from-jwt","ClientID":"ono9krqynydwx5","DeviceBindStatus":"BOUND","RefreshToken":"refresh-jwt","Token":"jwt-token"}`
	values2 := url.Values{
		"userJwt": {userJwtJSON},
	}
	did2, ref2, tok2 := extractCallbackDeviceInfo(values2)
	if did2 != "dev-from-jwt" || ref2 != "refresh-jwt" || tok2 != "jwt-token" {
		t.Errorf("case 2 failed: got did=%q, ref=%q, tok=%q", did2, ref2, tok2)
	}

	// Case 3: direct device_id fallback
	values3 := url.Values{
		"device_id":     {"custom-device-999"},
		"refresh_token": {"refresh-snake"},
	}
	did3, ref3, tok3 := extractCallbackDeviceInfo(values3)
	if did3 != "custom-device-999" || ref3 != "refresh-snake" || tok3 != "" {
		t.Errorf("case 3 failed: got did=%q, ref=%q, tok=%q", did3, ref3, tok3)
	}
}

func TestTraeLoginPollWithBoundDeviceID(t *testing.T) {
	authDir := t.TempDir()
	var claimedDeviceID string
	host := func(method string, raw []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			t.Fatalf("unexpected host method %q", method)
		}
		var request hostHTTPRequest
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			t.Fatalf("decode host request: %v", errUnmarshal)
		}
		switch {
		case strings.Contains(request.Request.URL, "GetLoginGuidance"):
			return pluginruntime.OK(pluginapi.HTTPResponse{
				StatusCode: http.StatusOK,
				Body:       []byte(`{"Result":{"LoginHost":"https://www.trae.cn"}}`),
			})
		case strings.Contains(request.Request.URL, "ExchangeToken"):
			return pluginruntime.OK(pluginapi.HTTPResponse{
				StatusCode: http.StatusOK,
				Body: []byte(`{"Result":{"Token":"` + syntheticTraeJWT(t, "user-bound") +
					`","RefreshToken":"refresh-bound-updated"}}`),
			})
		case strings.Contains(request.Request.URL, "checkin_credits"):
			claimedDeviceID = request.Request.Headers.Get("X-Device-Id")
			return pluginruntime.OK(pluginapi.HTTPResponse{
				StatusCode: http.StatusOK,
				Body:       []byte(`{"code":0,"msg":"success"}`),
			})
		default:
			t.Fatalf("unexpected URL %q", request.Request.URL)
			return nil, nil
		}
	}
	plugin := New(host)

	state := "state-bound-123"
	pollReq := authLoginPollRPCRequest{
		AuthLoginPollRequest: pluginapi.AuthLoginPollRequest{
			Provider: ProviderID,
			State:    state,
			Host:     pluginapi.HostConfigSummary{AuthDir: authDir},
		},
		HostCallbackID: "callback-bound",
	}
	pollRaw, _ := json.Marshal(pollReq)

	userJwtContent := `{"BoundDeviceID":"qwxe24l3old7ow","ClientID":"ono9krqynydwx5","DeviceBindStatus":"BOUND","RefreshToken":"refresh-bound","Token":"jwt-bound"}`
	callback := oauthCallbackFilePayload{
		Code:  "userJwt=" + url.QueryEscape(userJwtContent) + "&loginHost=" + url.QueryEscape("https://api.trae.com.cn"),
		State: state,
	}
	callbackData, _ := json.Marshal(callback)
	callbackPath := filepath.Join(authDir, traeCallbackPrefix+ProviderID+"-"+state+".oauth")
	if errWrite := os.WriteFile(callbackPath, callbackData, 0o600); errWrite != nil {
		t.Fatalf("write callback: %v", errWrite)
	}

	successEnvelope, errPoll := plugin.Handle(pluginabi.MethodAuthLoginPoll, pollRaw)
	if errPoll != nil {
		t.Fatalf("poll success: %v", errPoll)
	}
	success := decodeResult[pluginapi.AuthLoginPollResponse](t, successEnvelope)
	if success.Status != pluginapi.AuthLoginStatusSuccess {
		t.Fatalf("success status = %q, message = %q", success.Status, success.Message)
	}

	var stored credentials
	if errUnmarshal := json.Unmarshal(success.Auth.StorageJSON, &stored); errUnmarshal != nil {
		t.Fatalf("decode stored credentials: %v", errUnmarshal)
	}
	if stored.DeviceID != "qwxe24l3old7ow" {
		t.Fatalf("expected stored DeviceID qwxe24l3old7ow, got %q", stored.DeviceID)
	}
	if stored.RefreshToken != "refresh-bound-updated" {
		t.Fatalf("expected stored RefreshToken refresh-bound-updated, got %q", stored.RefreshToken)
	}
	if claimedDeviceID != "qwxe24l3old7ow" {
		t.Fatalf("expected checkin_credits X-Device-Id qwxe24l3old7ow, got %q", claimedDeviceID)
	}
	if metaDID, ok := success.Auth.Metadata["device_id"].(string); !ok || metaDID != "qwxe24l3old7ow" {
		t.Fatalf("expected metadata device_id qwxe24l3old7ow, got %v", success.Auth.Metadata["device_id"])
	}
}
