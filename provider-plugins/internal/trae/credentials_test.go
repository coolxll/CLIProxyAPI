package trae

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestCredentialsFromStorage(t *testing.T) {
	data, err := os.ReadFile("../../testdata/trae/credential_valid.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	creds, err := credentialsFromStorage(data)
	if err != nil {
		t.Fatalf("parse credentials: %v", err)
	}

	if creds.Type != "trae-plugin" {
		t.Errorf("expected type trae-plugin, got %s", creds.Type)
	}
	if creds.UserID != "test-user-123" {
		t.Errorf("expected user_id test-user-123, got %s", creds.UserID)
	}
	if creds.MachineID != "test-machine-id" {
		t.Errorf("expected machine_id test-machine-id, got %s", creds.MachineID)
	}
	if creds.DeviceID != "test-device-id" {
		t.Errorf("expected device_id test-device-id, got %s", creds.DeviceID)
	}
}

func TestCredentialsFromStorageEmpty(t *testing.T) {
	_, err := credentialsFromStorage([]byte{})
	if err == nil {
		t.Fatal("expected error for empty storage")
	}
}

func TestCredentialsFromStorageInvalid(t *testing.T) {
	_, err := credentialsFromStorage([]byte(`{"type":"trae-plugin"}`))
	if err == nil {
		t.Fatal("expected error for missing required fields")
	}
}

func TestValidateCredentials(t *testing.T) {
	tests := []struct {
		name    string
		creds   credentials
		wantErr bool
	}{
		{
			name: "valid",
			creds: credentials{
				JWTToken:  "token",
				MachineID: "machine",
				DeviceID:  "device",
			},
			wantErr: false,
		},
		{
			name: "missing_jwt",
			creds: credentials{
				MachineID: "machine",
				DeviceID:  "device",
			},
			wantErr: true,
		},
		{
			name: "missing_machine",
			creds: credentials{
				JWTToken: "token",
				DeviceID: "device",
			},
			wantErr: true,
		},
		{
			name: "missing_device",
			creds: credentials{
				JWTToken:  "token",
				MachineID: "machine",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCredentials(tt.creds)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateCredentials() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRefreshTokenViaHostCallback(t *testing.T) {
	responseData, err := os.ReadFile("../../testdata/trae/token_refresh_response.json")
	if err != nil {
		t.Fatalf("read response fixture: %v", err)
	}

	var capturedURL string
	host := func(method string, raw []byte) ([]byte, error) {
		if method == pluginabi.MethodHostLog {
			return pluginruntime.OK(struct{}{})
		}
		if method != pluginabi.MethodHostHTTPDo {
			t.Fatalf("unexpected host method %q", method)
		}
		var req hostHTTPRequest
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			t.Fatalf("decode host request: %v", errUnmarshal)
		}
		capturedURL = req.Request.URL
		return pluginruntime.OK(pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       responseData,
		})
	}

	creds := credentials{
		JWTToken:     "old-jwt-token",
		MachineID:    "test-machine",
		DeviceID:     "test-device",
		RefreshToken: "old-refresh-token",
	}

	rpc := hostRPC{call: host, callbackID: "test-callback"}
	err = refreshToken(rpc, &creds, traeAPIHost)
	if err != nil {
		t.Fatalf("refresh token: %v", err)
	}

	if !strings.Contains(capturedURL, "/cloudide/api/v3/trae/oauth/ExchangeToken") {
		t.Errorf("unexpected URL: %s", capturedURL)
	}
	if creds.JWTToken == "old-jwt-token" {
		t.Error("JWT token was not updated")
	}
	if creds.RefreshToken != "new-refresh-token" {
		t.Errorf("expected refresh_token new-refresh-token, got %s", creds.RefreshToken)
	}
}

func TestRefreshTokenMissingRefreshToken(t *testing.T) {
	creds := credentials{
		JWTToken:  "token",
		MachineID: "machine",
		DeviceID:  "device",
	}

	rpc := hostRPC{}
	err := refreshToken(rpc, &creds, traeAPIHost)
	if err == nil {
		t.Fatal("expected error for missing refresh_token")
	}
}

func TestRefreshTokenHTTPError(t *testing.T) {
	host := func(method string, raw []byte) ([]byte, error) {
		return pluginruntime.OK(pluginapi.HTTPResponse{
			StatusCode: http.StatusUnauthorized,
			Body:       []byte(`{"message":"invalid refresh token"}`),
		})
	}

	creds := credentials{
		JWTToken:     "token",
		MachineID:    "machine",
		DeviceID:     "device",
		RefreshToken: "bad-token",
	}

	rpc := hostRPC{call: host}
	err := refreshToken(rpc, &creds, traeAPIHost)
	if err == nil {
		t.Fatal("expected error for HTTP 401")
	}
	if !strings.Contains(err.Error(), "invalid refresh token") {
		t.Errorf("error should contain upstream message: %v", err)
	}
}

func TestJWTExpiresAt(t *testing.T) {
	// JWT with exp=1735689600 (2025-01-01 00:00:00 UTC)
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3MzU2ODk2MDB9.test"
	exp, ok := jwtExpiresAt(jwt)
	if !ok {
		t.Fatal("failed to extract expiry")
	}

	expected := time.Unix(1735689600, 0)
	if !exp.Equal(expected) {
		t.Errorf("expected expiry %v, got %v", expected, exp)
	}
}

func TestJWTExpiresAtInvalid(t *testing.T) {
	_, ok := jwtExpiresAt("not-a-jwt")
	if ok {
		t.Error("expected failure for invalid JWT")
	}
}

func TestUserIDFromJWT(t *testing.T) {
	// JWT with data.id="test-user-123"
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJkYXRhIjp7ImlkIjoidGVzdC11c2VyLTEyMyJ9fQ.test"
	userID, ok := userIDFromJWT(jwt)
	if !ok {
		t.Fatal("failed to extract user ID")
	}
	if userID != "test-user-123" {
		t.Errorf("expected user_id test-user-123, got %s", userID)
	}
}

func TestUserIDFromJWTInvalid(t *testing.T) {
	_, ok := userIDFromJWT("not-a-jwt")
	if ok {
		t.Error("expected failure for invalid JWT")
	}
}

func TestCredentialExpiry(t *testing.T) {
	creds := credentials{
		JWTToken: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3MzU2ODk2MDB9.test",
	}
	now := time.Now()
	exp := credentialExpiry(creds, now)

	expected := time.Unix(1735689600, 0)
	if !exp.Equal(expected) {
		t.Errorf("expected expiry %v, got %v", expected, exp)
	}
}

func TestCredentialExpiryNoJWT(t *testing.T) {
	creds := credentials{}
	now := time.Now()
	exp := credentialExpiry(creds, now)

	expected := now.Add(1 * time.Hour)
	diff := exp.Sub(expected)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("expected expiry around %v, got %v", expected, exp)
	}
}

func TestNextRefreshTime(t *testing.T) {
	creds := credentials{
		JWTToken: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3MzU2ODk2MDB9.test",
	}
	now := time.Unix(1735686000, 0) // 1 hour before expiry
	next := nextRefreshTime(creds, now)

	// Should refresh 5 minutes before expiry
	expected := time.Unix(1735689300, 0)
	if !next.Equal(expected) {
		t.Errorf("expected next refresh %v, got %v", expected, next)
	}
}

func TestNextRefreshTimeExpired(t *testing.T) {
	creds := credentials{
		JWTToken: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3MzU2ODk2MDB9.test",
	}
	now := time.Unix(1735689700, 0) // After expiry
	next := nextRefreshTime(creds, now)

	expected := now.Add(1 * time.Minute)
	diff := next.Sub(expected)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("expected next refresh around %v, got %v", expected, next)
	}
}

func TestStableAuthID(t *testing.T) {
	creds := credentials{
		JWTToken:  "token",
		MachineID: "machine",
		UserID:    "user123",
	}
	id := stableAuthID(creds)
	if id == "" {
		t.Error("stableAuthID returned empty string")
	}
	if !strings.HasPrefix(id, "trae-plugin:") {
		t.Errorf("expected prefix trae-plugin:, got %s", id)
	}
}

func TestNormalizedFileName(t *testing.T) {
	tests := []struct {
		candidate string
		account   string
		want      string
	}{
		{"test.json", "user123", "test.json"},
		{"", "user123", "trae-plugin-user123.json"},
		{"", "user@domain", "trae-plugin-user-domain.json"},
		{"", "", "trae-plugin-account.json"},
	}

	for _, tt := range tests {
		got := normalizedFileName(tt.candidate, tt.account)
		if got != tt.want {
			t.Errorf("normalizedFileName(%q, %q) = %q, want %q", tt.candidate, tt.account, got, tt.want)
		}
	}
}

func TestExtractString(t *testing.T) {
	data := map[string]any{
		"Result": map[string]any{
			"Token": "value1",
		},
		"data": map[string]any{
			"token": "value2",
		},
	}

	result := extractString(data, [][]string{
		{"Result", "Token"},
		{"data", "token"},
	})
	if result != "value1" {
		t.Errorf("expected value1, got %s", result)
	}

	data2 := map[string]any{
		"data": map[string]any{
			"token": "value2",
		},
	}
	result2 := extractString(data2, [][]string{
		{"Result", "Token"},
		{"data", "token"},
	})
	if result2 != "value2" {
		t.Errorf("expected value2, got %s", result2)
	}
}

func TestSafeUpstreamMessage(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{"empty", []byte{}, ""},
		{"json_message", []byte(`{"message":"error occurred"}`), "error occurred"},
		{"json_error", []byte(`{"error":"something failed"}`), "something failed"},
		{"plain_text", []byte("plain error"), "plain error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeUpstreamMessage(tt.body)
			if got != tt.want {
				t.Errorf("safeUpstreamMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

// hostDoResponder creates a HostCall that responds to HTTP do requests.
func hostDoResponder(t *testing.T, respond func(pluginapi.HTTPRequest) pluginapi.HTTPResponse) HostCall {
	t.Helper()
	return func(method string, raw []byte) ([]byte, error) {
		if method == pluginabi.MethodHostLog {
			return pluginruntime.OK(struct{}{})
		}
		if method != pluginabi.MethodHostHTTPDo {
			t.Fatalf("host method = %q", method)
		}
		var req hostHTTPRequest
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			t.Fatalf("decode host request: %v", errUnmarshal)
		}
		return pluginruntime.OK(respond(req.Request))
	}
}
