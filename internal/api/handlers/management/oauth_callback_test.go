package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestGetOAuthCallbackServesPluginFragmentBridge(t *testing.T) {
	state := "plugin-fragment-state"
	if errRegister := RegisterPluginOAuthSession(state, "trae-plugin", nil); errRegister != nil {
		t.Fatalf("register plugin session: %v", errRegister)
	}
	t.Cleanup(func() { CompleteOAuthSession(state) })

	h := &Handler{cfg: &config.Config{AuthDir: t.TempDir()}}
	router := gin.New()
	router.GET("/v0/management/oauth-callback", h.GetOAuthCallback)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/oauth-callback?provider=trae-plugin&state="+state, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "window.location.href") {
		t.Fatalf("callback response does not contain fragment bridge: %s", recorder.Body.String())
	}
}

func TestPostOAuthCallbackPersistsPluginFragmentAsCode(t *testing.T) {
	authDir := t.TempDir()
	state := "plugin-fragment-post-state"
	if errRegister := RegisterPluginOAuthSession(state, "trae-plugin", nil); errRegister != nil {
		t.Fatalf("register plugin session: %v", errRegister)
	}
	t.Cleanup(func() { CompleteOAuthSession(state) })

	h := &Handler{cfg: &config.Config{AuthDir: authDir}}
	router := gin.New()
	router.POST("/v0/management/oauth-callback", h.PostOAuthCallback)

	body := `{"redirect_url":"http://127.0.0.1/v0/management/oauth-callback?provider=trae-plugin&state=` + state + `#refreshToken=refresh-1&loginHost=https%3A%2F%2Fapi.trae.com.cn"}`
	req := httptest.NewRequest(http.MethodPost, "/v0/management/oauth-callback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	path := filepath.Join(authDir, ".oauth-trae-plugin-"+state+".oauth")
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read callback file: %v", errRead)
	}
	var payload oauthCallbackFilePayload
	if errUnmarshal := json.Unmarshal(data, &payload); errUnmarshal != nil {
		t.Fatalf("decode callback file: %v", errUnmarshal)
	}
	if payload.Code != "refreshToken=refresh-1&loginHost=https%3A%2F%2Fapi.trae.com.cn" {
		t.Fatalf("code = %q", payload.Code)
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
	providers := []string{"anthropic", "codex", "gemini", "antigravity", "xai"}
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
