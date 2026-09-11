package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPostOAuthCallbackCreatesMissingAuthDir(t *testing.T) {

	authDir := filepath.Join(t.TempDir(), "missing-auth")
	state := "test-antigravity-state"
	RegisterOAuthSession(state, "antigravity")
	defer CompleteOAuthSession(state)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.POST("/v0/management/oauth-callback", h.PostOAuthCallback)

	body := `{"provider":"antigravity","redirect_url":"http://localhost:59788/oauth-callback?state=test-antigravity-state&code=test-code"}`
	req := httptest.NewRequest(http.MethodPost, "/v0/management/oauth-callback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	callbackPath := filepath.Join(authDir, ".oauth-antigravity-"+state+".oauth")
	data, errRead := os.ReadFile(callbackPath)
	if errRead != nil {
		t.Fatalf("expected callback file to be written: %v", errRead)
	}

	var payload oauthCallbackFilePayload
	if errUnmarshal := json.Unmarshal(data, &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode callback payload: %v", errUnmarshal)
	}
	if payload.State != state || payload.Code != "test-code" || payload.Error != "" {
		t.Fatalf("unexpected callback payload: %+v", payload)
	}
}

func TestGetOAuthCallbackWritesPluginProviderCallback(t *testing.T) {
	authDir := filepath.Join(t.TempDir(), "missing-auth")
	state := "test-geminicli-state"
	if errRegister := RegisterPluginOAuthSession(state, "gemini-cli", nil); errRegister != nil {
		t.Fatalf("register plugin oauth session: %v", errRegister)
	}
	defer CompleteOAuthSession(state)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.GET("/v0/management/oauth-callback", h.GetOAuthCallback)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/oauth-callback?state="+state+"&code=test-code", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	callbackPath := filepath.Join(authDir, ".oauth-gemini-cli-"+state+".oauth")
	data, errRead := os.ReadFile(callbackPath)
	if errRead != nil {
		t.Fatalf("expected callback file to be written: %v", errRead)
	}

	var payload oauthCallbackFilePayload
	if errUnmarshal := json.Unmarshal(data, &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode callback payload: %v", errUnmarshal)
	}
	if payload.State != state || payload.Code != "test-code" || payload.Error != "" {
		t.Fatalf("unexpected callback payload: %+v", payload)
	}
}

func TestGetOAuthCallbackDoesNotAliasPluginProvider(t *testing.T) {
	authDir := filepath.Join(t.TempDir(), "missing-auth")
	state := "test-openai-plugin-state"
	if errRegister := RegisterPluginOAuthSession(state, "openai", nil); errRegister != nil {
		t.Fatalf("register plugin oauth session: %v", errRegister)
	}
	defer CompleteOAuthSession(state)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.GET("/v0/management/oauth-callback", h.GetOAuthCallback)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/oauth-callback?state="+state+"&code=test-code", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	callbackPath := filepath.Join(authDir, ".oauth-openai-"+state+".oauth")
	if _, errRead := os.ReadFile(callbackPath); errRead != nil {
		t.Fatalf("expected plugin callback provider to stay openai: %v", errRead)
	}
	if _, errRead := os.ReadFile(filepath.Join(authDir, ".oauth-codex-"+state+".oauth")); errRead == nil {
		t.Fatal("unexpected codex callback file for openai plugin provider")
	}
}

func TestWriteOAuthCallbackFileForPendingSessionCreatesMissingAuthDirForCallbackProviders(t *testing.T) {
	// xAI uses device-code flow and no longer writes callback files.
	providers := []string{"anthropic", "codex", "gemini", "antigravity"}
	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			authDir := filepath.Join(t.TempDir(), "missing-auth")
			state := provider + "-state"
			RegisterOAuthSession(state, provider)
			defer CompleteOAuthSession(state)

			path, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, provider, state, "code-"+provider, "")
			if errWrite != nil {
				t.Fatalf("expected callback file write to succeed: %v", errWrite)
			}

			data, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatalf("expected callback file to be written: %v", errRead)
			}

			var payload oauthCallbackFilePayload
			if errUnmarshal := json.Unmarshal(data, &payload); errUnmarshal != nil {
				t.Fatalf("failed to decode callback payload: %v", errUnmarshal)
			}
			if payload.State != state || payload.Code != "code-"+provider || payload.Error != "" {
				t.Fatalf("unexpected callback payload: %+v", payload)
			}
		})
	}
}

func TestPostOAuthCallbackPluginWithHttpPort(t *testing.T) {
	receivedCallback := make(chan string, 1)
	receivedFollow := make(chan string, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/callback":
			receivedCallback <- r.URL.RawQuery
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html><script>window.is_select_account = true; window.select_account_info = '{"orgId":"org-test-123"}';</script></html>`))
		case "/auth/loginWithOrganization":
			receivedFollow <- r.URL.RawQuery
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html>OK</html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Extract port from srv.URL (http://127.0.0.1:port)
	u, _ := url.Parse(srv.URL)
	portStr := u.Port()
	var port int
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}

	state := "test-plugin-flow-state"
	metadata := map[string]any{
		"http_port": port,
	}
	if errRegister := RegisterPluginOAuthSession(state, "lingma-plugin", metadata); errRegister != nil {
		t.Fatalf("register plugin oauth session: %v", errRegister)
	}
	defer CompleteOAuthSession(state)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	router := gin.New()
	router.POST("/v0/management/oauth-callback", h.PostOAuthCallback)

	redirectURL := "http://127.0.0.1:1455/auth/callback?state=" + state + "&auth=test-auth-val&token=test-token-val"
	body := `{"provider":"lingma-plugin","redirect_url":"` + redirectURL + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v0/management/oauth-callback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	select {
	case q := <-receivedCallback:
		if !strings.Contains(q, "auth=test-auth-val") || !strings.Contains(q, "token=test-token-val") {
			t.Fatalf("unexpected callback query: %s", q)
		}
	default:
		t.Fatal("expected callback request to be forwarded to plugin")
	}

	select {
	case q := <-receivedFollow:
		if !strings.Contains(q, "organizationId=org-test-123") {
			t.Fatalf("unexpected follow query: %s", q)
		}
	default:
		t.Fatal("expected follow-up loginWithOrganization request to be sent")
	}
}

func TestPostOAuthCallbackTraeFragment(t *testing.T) {
	authDir := t.TempDir()
	state := "test-trae-state-123"
	if errRegister := RegisterPluginOAuthSession(state, "trae-plugin", nil); errRegister != nil {
		t.Fatalf("register plugin oauth session: %v", errRegister)
	}
	defer CompleteOAuthSession(state)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.POST("/v0/management/oauth-callback", h.PostOAuthCallback)

	redirectURL := "http://127.0.0.1:8317/v0/management/oauth-callback?provider=trae-plugin&state=" + state + "#refreshToken=refresh-token-xyz&loginHost=https%3A%2F%2Fapi.trae.com.cn"
	body := `{"provider":"trae-plugin","redirect_url":"` + redirectURL + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v0/management/oauth-callback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	callbackPath := filepath.Join(authDir, ".oauth-trae-plugin-"+state+".oauth")
	data, errRead := os.ReadFile(callbackPath)
	if errRead != nil {
		t.Fatalf("expected callback file to be written: %v", errRead)
	}

	var payload oauthCallbackFilePayload
	if errUnmarshal := json.Unmarshal(data, &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode callback payload: %v", errUnmarshal)
	}
	if payload.State != state || !strings.Contains(payload.Code, "refreshToken=refresh-token-xyz") {
		t.Fatalf("unexpected callback payload: %+v", payload)
	}
}

