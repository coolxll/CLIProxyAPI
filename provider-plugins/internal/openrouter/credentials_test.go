package openrouter

import (
	"testing"
)

func TestValidateCredentials(t *testing.T) {
	tests := []struct {
		name    string
		creds   credentials
		wantErr bool
	}{
		{
			name: "valid openrouter-plugin with api key",
			creds: credentials{
				Type:   "openrouter-plugin",
				APIKey: "sk-or-v1-test",
			},
			wantErr: false,
		},
		{
			name: "valid openrouter bare type",
			creds: credentials{
				Type:   "openrouter",
				APIKey: "sk-or-v1-test",
			},
			wantErr: false,
		},
		{
			name: "missing api key",
			creds: credentials{
				Type: "openrouter",
			},
			wantErr: true,
		},
		{
			name: "invalid type",
			creds: credentials{
				Type:   "unknown-type",
				APIKey: "sk-or-v1-test",
			},
			wantErr: true,
		},
		{
			name: "invalid base url",
			creds: credentials{
				Type:    "openrouter",
				APIKey:  "sk-or-v1-test",
				BaseURL: "ftp://localhost",
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

func TestBuildAuthHeaders(t *testing.T) {
	creds := credentials{
		Type:   "openrouter",
		APIKey: "sk-or-v1-my-key",
	}

	headers := buildAuthHeaders(creds)
	if got := headers.Get("Authorization"); got != "Bearer sk-or-v1-my-key" {
		t.Errorf("expected 'Bearer sk-or-v1-my-key', got %q", got)
	}
	if got := headers.Get("HTTP-Referer"); got != defaultOpenRouterHTTPReferer {
		t.Errorf("expected default HTTP-Referer, got %q", got)
	}
	if got := headers.Get("X-Title"); got != defaultOpenRouterXTitle {
		t.Errorf("expected default X-Title, got %q", got)
	}
}
