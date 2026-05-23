package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseEnvLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantKey   string
		wantValue string
		wantOK    bool
	}{
		{name: "plain", line: "TRAE_MACHINE_ID=machine-1", wantKey: "TRAE_MACHINE_ID", wantValue: "machine-1", wantOK: true},
		{name: "double quoted", line: `TRAE_JWT_TOKEN="header.payload.sig"`, wantKey: "TRAE_JWT_TOKEN", wantValue: "header.payload.sig", wantOK: true},
		{name: "single quoted", line: `TRAE_DEVICE_ID='device-1'`, wantKey: "TRAE_DEVICE_ID", wantValue: "device-1", wantOK: true},
		{name: "export", line: "export TRAE_WORKSPACE_PATH=C:\\Workspace\\App", wantKey: "TRAE_WORKSPACE_PATH", wantValue: "C:\\Workspace\\App", wantOK: true},
		{name: "comment", line: "# TRAE_JWT_TOKEN=skip", wantOK: false},
		{name: "inline comment", line: "TRAE_DEVICE_ID=device-1 # local", wantKey: "TRAE_DEVICE_ID", wantValue: "device-1", wantOK: true},
		{name: "bom", line: "\ufeffTRAE_JWT_TOKEN=token-1", wantKey: "TRAE_JWT_TOKEN", wantValue: "token-1", wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKey, gotValue, gotOK := parseEnvLine(tt.line)
			if gotOK != tt.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotKey != tt.wantKey || gotValue != tt.wantValue {
				t.Fatalf("got (%q, %q), want (%q, %q)", gotKey, gotValue, tt.wantKey, tt.wantValue)
			}
		})
	}
}

func TestCollectEnvValuesExplicitMissingReturnsError(t *testing.T) {
	_, err := collectEnvValues(filepath.Join(t.TempDir(), "missing.env"))
	if err == nil {
		t.Fatal("expected explicit missing env file to return error")
	}
	if !strings.Contains(err.Error(), "missing.env") {
		t.Fatalf("expected missing path in error, got %v", err)
	}
}

func TestCollectEnvValuesExplicitFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("TRAE_JWT_TOKEN=token-1\nTRAE_MACHINE_ID=machine-1\n"), 0600); err != nil {
		t.Fatal(err)
	}

	values, err := collectEnvValues(path)
	if err != nil {
		t.Fatalf("collectEnvValues failed: %v", err)
	}
	if values["TRAE_JWT_TOKEN"] != "token-1" || values["TRAE_MACHINE_ID"] != "machine-1" {
		t.Fatalf("unexpected env values: %#v", values)
	}
}

func TestJWTExpiresAt(t *testing.T) {
	payload := []byte(`{"exp":1893456000,"data":{"id":123}}`)
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"

	got, ok := jwtExpiresAt(token)
	if !ok {
		t.Fatal("expected expiration to be parsed")
	}
	want := time.Unix(1893456000, 0)
	if !got.Equal(want) {
		t.Fatalf("expires_at = %s, want %s", got, want)
	}
}

func TestDefaultName(t *testing.T) {
	got := defaultName("1234567890", "machine-abcd")
	if got != "trae-34567890-abcd" {
		t.Fatalf("defaultName = %q", got)
	}
}
