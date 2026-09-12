package opencode

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	defaultOpenCodeCloudURL  = "https://opencode.ai/zen"
	defaultOpenCodeDaemonURL = "http://127.0.0.1:10001"
	defaultOpenCodeServerURL = defaultOpenCodeCloudURL
)

type credentials struct {
	Type        string `json:"type"`
	ServerURL   string `json:"server_url,omitempty"`
	Password    string `json:"password,omitempty"`
	APIKey      string `json:"api_key,omitempty"`
	ZenKey      string `json:"zen_key,omitempty"`
	Anonymous   *bool  `json:"anonymous,omitempty"`
	Mode        string `json:"mode,omitempty"` // "cloud", "zen", "daemon", "local"
	Name        string `json:"name,omitempty"`
	Model       string `json:"model,omitempty"`
	AutoCleanup *bool  `json:"auto_cleanup,omitempty"`
	Protocol    string `json:"protocol,omitempty"` // "opencode" or "openai"
}

func (c credentials) isAutoCleanup() bool {
	if c.AutoCleanup == nil {
		return true
	}
	return *c.AutoCleanup
}

func (c credentials) isCloudZen() bool {
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	if mode == "daemon" || mode == "local" {
		return false
	}
	if mode == "cloud" || mode == "zen" {
		return true
	}
	raw := strings.TrimSpace(c.ServerURL)
	if raw == "" || strings.HasPrefix(raw, "https://opencode.ai") {
		return true
	}
	if strings.Contains(raw, "127.0.0.1") || strings.Contains(raw, "localhost") {
		return false
	}
	return true
}

func (c credentials) isDaemon() bool {
	return !c.isCloudZen()
}

func (c credentials) effectiveAPIKey() string {
	if key := strings.TrimSpace(c.APIKey); key != "" {
		return key
	}
	if key := strings.TrimSpace(c.ZenKey); key != "" {
		return key
	}
	return ""
}

func (c credentials) isAnonymous() bool {
	if c.Anonymous != nil {
		return *c.Anonymous
	}
	key := c.effectiveAPIKey()
	return key == "" || key == "public"
}

func (c credentials) baseURL() string {
	raw := strings.TrimSpace(c.ServerURL)
	if raw == "" {
		if c.isDaemon() {
			return defaultOpenCodeDaemonURL
		}
		return defaultOpenCodeCloudURL
	}
	return strings.TrimRight(raw, "/")
}

func validateCredentials(creds credentials) error {
	t := strings.ToLower(strings.TrimSpace(creds.Type))
	if t != ProviderID && t != "opencode" {
		return fmt.Errorf("unsupported credential type %q; expected %q or %q", creds.Type, ProviderID, "opencode")
	}
	rawURL := strings.TrimSpace(creds.ServerURL)
	if rawURL != "" {
		parsed, err := url.Parse(rawURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("invalid server_url %q: must be http or https URL", creds.ServerURL)
		}
	}
	return nil
}

func buildAuthHeaders(creds credentials) http.Header {
	headers := make(http.Header)
	password := strings.TrimSpace(creds.Password)
	apiKey := creds.effectiveAPIKey()

	if password != "" {
		token := base64.StdEncoding.EncodeToString([]byte("opencode:" + password))
		headers.Set("Authorization", "Basic "+token)
	} else if apiKey != "" {
		headers.Set("Authorization", "Bearer "+apiKey)
	} else if creds.isCloudZen() {
		headers.Set("Authorization", "Bearer public")
	}
	return headers
}

func credentialsFromStorage(raw []byte) (credentials, error) {
	var creds credentials
	if len(raw) == 0 {
		return creds, fmt.Errorf("OpenCode credential storage is empty")
	}
	if errUnmarshal := json.Unmarshal(raw, &creds); errUnmarshal != nil {
		return creds, fmt.Errorf("decode OpenCode credential storage: %w", errUnmarshal)
	}
	if errValidate := validateCredentials(creds); errValidate != nil {
		return creds, errValidate
	}
	return creds, nil
}

func compactJSON(raw []byte) (string, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "", fmt.Errorf("compact OpenCode credential storage: %w", err)
	}
	return buf.String(), nil
}

func accountLabel(creds credentials) string {
	if strings.TrimSpace(creds.Name) != "" {
		return strings.TrimSpace(creds.Name)
	}
	raw := creds.baseURL()
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return "default"
}

func stableAuthID(creds credentials) string {
	seed := fmt.Sprintf("%s|%s|%s", creds.baseURL(), creds.Name, creds.effectiveAPIKey())
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
