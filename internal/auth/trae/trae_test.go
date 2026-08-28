package trae

import (
	"encoding/base64"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestParseTraeCredentials(t *testing.T) {
	// A mock JWT payload with data.id
	payload := `{"data":{"id":1234567890},"user_id":2222,"uid":3333,"sub":"4444"}`
	jwtPayloadB64 := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mockJWT := "header." + jwtPayloadB64 + ".signature"

	creds, err := ParseTraeCredentials(mockJWT, "mach-123", "dev-456")
	if err != nil {
		t.Fatalf("ParseTraeCredentials failed: %v", err)
	}

	if creds.MachineID != "mach-123" {
		t.Errorf("expected machine_id to be %q, got %q", "mach-123", creds.MachineID)
	}
	if creds.DeviceID != "dev-456" {
		t.Errorf("expected device_id to be %q, got %q", "dev-456", creds.DeviceID)
	}
	if creds.UserID != "1234567890" {
		t.Errorf("expected user_id to be parsed as %q, got %q", "1234567890", creds.UserID)
	}
}

func TestParseTraeCredentials_Fallback(t *testing.T) {
	payload := `{"data":{"id":1234567890}}`
	jwtPayloadB64 := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mockJWT := "header." + jwtPayloadB64 + ".signature"

	creds, err := ParseTraeCredentials(mockJWT, "", "  ")
	if err != nil {
		t.Fatalf("ParseTraeCredentials failed: %v", err)
	}

	if creds.MachineID != "2569994131757818" {
		t.Errorf("expected fallback machine_id, got %q", creds.MachineID)
	}
	if creds.DeviceID != "2569994131757818" {
		t.Errorf("expected fallback device_id, got %q", creds.DeviceID)
	}
}

func TestCredentialsFromAuth(t *testing.T) {
	// Mock string user_id payload
	payload := `{"user_id":"987654321","uid":"3333"}`
	jwtPayloadB64 := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mockJWT := "header." + jwtPayloadB64 + ".signature"

	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"jwt_token":      mockJWT,
			"machine_id":     "fixed-machine-id",
			"device_id":      "fixed-device-id",
			"workspace_path": "C:\\Workspace\\Test",
		},
	}

	creds, err := CredentialsFromAuth(auth)
	if err != nil {
		t.Fatalf("CredentialsFromAuth failed: %v", err)
	}

	if creds.MachineID != "fixed-machine-id" || creds.DeviceID != "fixed-device-id" {
		t.Errorf("ids mismatch: machine=%q, device=%q", creds.MachineID, creds.DeviceID)
	}
	if creds.UserID != "987654321" {
		t.Errorf("expected user_id to be %q, got %q", "987654321", creds.UserID)
	}

	wpath := WorkspacePathFromAuth(auth, "C:\\Fallback")
	if wpath != "C:\\Workspace\\Test" {
		t.Errorf("expected workspace_path %q, got %q", "C:\\Workspace\\Test", wpath)
	}
}
