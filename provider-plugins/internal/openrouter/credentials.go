package openrouter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	defaultOpenRouterBaseURL     = "https://openrouter.ai/api/v1"
	defaultOpenRouterHTTPReferer = "https://github.com/router-for-me/CLIProxyAPI"
	defaultOpenRouterXTitle      = "CLIProxyAPI"
)

var unsafeFileCharacter = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

type credentials struct {
	Type         string `json:"type"`
	APIKey       string `json:"api_key,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	Name         string `json:"name,omitempty"`
	AutoFailover *bool  `json:"auto_failover,omitempty"`
	FreeOnly     *bool  `json:"free_only,omitempty"`
	HTTPReferer  string `json:"http_referer,omitempty"`
	XTitle       string `json:"x_title,omitempty"`
}

func (c credentials) baseURL() string {
	raw := strings.TrimSpace(c.BaseURL)
	if raw == "" {
		return defaultOpenRouterBaseURL
	}
	return strings.TrimRight(raw, "/")
}

func (c credentials) isAutoFailover() bool {
	if c.AutoFailover == nil {
		return true
	}
	return *c.AutoFailover
}

func (c credentials) isFreeOnly() bool {
	if c.FreeOnly == nil {
		return true
	}
	return *c.FreeOnly
}

func (c credentials) httpReferer() string {
	if r := strings.TrimSpace(c.HTTPReferer); r != "" {
		return r
	}
	return defaultOpenRouterHTTPReferer
}

func (c credentials) xTitle() string {
	if t := strings.TrimSpace(c.XTitle); t != "" {
		return t
	}
	return defaultOpenRouterXTitle
}

func validateCredentials(creds credentials) error {
	t := strings.ToLower(strings.TrimSpace(creds.Type))
	if t != ProviderID && t != "openrouter" {
		return fmt.Errorf("unsupported credential type %q; expected %q or %q", creds.Type, ProviderID, "openrouter")
	}
	rawURL := strings.TrimSpace(creds.BaseURL)
	if rawURL != "" {
		parsed, err := url.Parse(rawURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("invalid base_url %q: must be http or https URL", creds.BaseURL)
		}
	}
	if strings.TrimSpace(creds.APIKey) == "" {
		return fmt.Errorf("OpenRouter API key is required (obtain a free key from https://openrouter.ai/settings/keys)")
	}
	return nil
}

func buildAuthHeaders(creds credentials) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json, text/event-stream")
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(creds.APIKey))
	headers.Set("HTTP-Referer", creds.httpReferer())
	headers.Set("X-Title", creds.xTitle())
	return headers
}

func credentialsFromStorage(raw []byte) (credentials, error) {
	var creds credentials
	if len(raw) == 0 {
		return creds, fmt.Errorf("OpenRouter credential storage is empty")
	}
	if errUnmarshal := json.Unmarshal(raw, &creds); errUnmarshal != nil {
		return creds, fmt.Errorf("decode OpenRouter credential storage: %w", errUnmarshal)
	}
	if errValidate := validateCredentials(creds); errValidate != nil {
		return creds, errValidate
	}
	return creds, nil
}

func compactJSON(raw []byte) (string, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "", fmt.Errorf("compact OpenRouter credential storage: %w", err)
	}
	return buf.String(), nil
}

func accountLabel(creds credentials) string {
	if strings.TrimSpace(creds.Name) != "" {
		return strings.TrimSpace(creds.Name)
	}
	key := strings.TrimSpace(creds.APIKey)
	if len(key) > 8 {
		return fmt.Sprintf("or-...%s", key[len(key)-4:])
	}
	return "default"
}

func stableAuthID(creds credentials) string {
	seed := fmt.Sprintf("%s|%s|%s", creds.baseURL(), creds.Name, creds.APIKey)
	hash := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%s:%s", ProviderID, hex.EncodeToString(hash[:8]))
}

func normalizedFileName(fileName, account string) string {
	clean := strings.TrimSpace(fileName)
	if clean != "" {
		return clean
	}
	safeAccount := unsafeFileCharacter.ReplaceAllString(strings.TrimSpace(account), "-")
	safeAccount = strings.Trim(safeAccount, "-")
	if safeAccount == "" {
		safeAccount = "account"
	}
	return fmt.Sprintf("%s-%s.json", ProviderID, safeAccount)
}
