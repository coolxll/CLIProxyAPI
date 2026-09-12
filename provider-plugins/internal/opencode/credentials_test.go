package opencode

import (
	"encoding/base64"
	"testing"
)

func TestValidateCredentials(t *testing.T) {
	tests := []struct {
		name    string
		creds   credentials
		wantErr bool
	}{
		{
			name: "valid opencode-plugin daemon",
			creds: credentials{
				Type:      "opencode-plugin",
				ServerURL: "http://127.0.0.1:10001",
			},
			wantErr: false,
		},
		{
			name: "valid opencode cloud zen",
			creds: credentials{
				Type:      "opencode",
				ServerURL: "https://opencode.ai/zen",
			},
			wantErr: false,
		},
		{
			name: "valid with default url",
			creds: credentials{
				Type: "opencode-plugin",
			},
			wantErr: false,
		},
		{
			name: "invalid type",
			creds: credentials{
				Type: "unknown-type",
			},
			wantErr: true,
		},
		{
			name: "invalid server url",
			creds: credentials{
				Type:      "opencode-plugin",
				ServerURL: "ftp://localhost",
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

func TestCloudZenVsDaemonDetection(t *testing.T) {
	tests := []struct {
		name        string
		creds       credentials
		wantCloud   bool
		wantBaseURL string
	}{
		{
			name:        "empty url defaults to cloud zen",
			creds:       credentials{Type: "opencode"},
			wantCloud:   true,
			wantBaseURL: "https://opencode.ai/zen",
		},
		{
			name:        "explicit cloud zen url",
			creds:       credentials{Type: "opencode", ServerURL: "https://opencode.ai/zen"},
			wantCloud:   true,
			wantBaseURL: "https://opencode.ai/zen",
		},
		{
			name:        "explicit 127.0.0.1 is daemon",
			creds:       credentials{Type: "opencode", ServerURL: "http://127.0.0.1:10001"},
			wantCloud:   false,
			wantBaseURL: "http://127.0.0.1:10001",
		},
		{
			name:        "mode daemon overrides",
			creds:       credentials{Type: "opencode", Mode: "daemon"},
			wantCloud:   false,
			wantBaseURL: "http://127.0.0.1:10001",
		},
		{
			name:        "mode zen overrides",
			creds:       credentials{Type: "opencode", Mode: "zen", ServerURL: "http://127.0.0.1:10001"},
			wantCloud:   true,
			wantBaseURL: "http://127.0.0.1:10001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.creds.isCloudZen(); got != tt.wantCloud {
				t.Errorf("isCloudZen() = %v, want %v", got, tt.wantCloud)
			}
			if got := tt.creds.baseURL(); got != tt.wantBaseURL {
				t.Errorf("baseURL() = %v, want %v", got, tt.wantBaseURL)
			}
		})
	}
}

func TestBuildAuthHeaders(t *testing.T) {
	t.Run("basic auth with password", func(t *testing.T) {
		creds := credentials{
			Type:      "opencode-plugin",
			ServerURL: "http://127.0.0.1:10001",
			Password:  "secret-password",
		}
		headers := buildAuthHeaders(creds)
		auth := headers.Get("Authorization")
		expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:secret-password"))
		if auth != expected {
			t.Errorf("got %q, want %q", auth, expected)
		}
	})

	t.Run("bearer auth with api key", func(t *testing.T) {
		creds := credentials{
			Type:   "opencode-plugin",
			APIKey: "sk-my-api-key",
		}
		headers := buildAuthHeaders(creds)
		auth := headers.Get("Authorization")
		expected := "Bearer sk-my-api-key"
		if auth != expected {
			t.Errorf("got %q, want %q", auth, expected)
		}
	})

	t.Run("bearer auth with zen key alias", func(t *testing.T) {
		creds := credentials{
			Type:   "opencode-plugin",
			ZenKey: "sk-my-zen-key",
		}
		headers := buildAuthHeaders(creds)
		auth := headers.Get("Authorization")
		expected := "Bearer sk-my-zen-key"
		if auth != expected {
			t.Errorf("got %q, want %q", auth, expected)
		}
	})

	t.Run("cloud zen defaults to public bearer when no key provided", func(t *testing.T) {
		creds := credentials{
			Type: "opencode-plugin",
		}
		headers := buildAuthHeaders(creds)
		if got := headers.Get("Authorization"); got != "Bearer public" {
			t.Errorf("got %q, want 'Bearer public'", got)
		}
	})
}
