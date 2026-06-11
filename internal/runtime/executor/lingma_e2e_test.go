//go:build e2e

package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func lingmaE2ETarget(t *testing.T) (executorE2ETarget, bool) {
	t.Helper()
	authFile := findLingmaTestAuthFile(t)
	if authFile == "" {
		t.Log("no Lingma auth JSON found; skipping Lingma target")
		return executorE2ETarget{}, false
	}
	auth := loadLingmaTestAuthFromFile(t, authFile)
	e := NewLingmaExecutor(nil)
	modelName := strings.TrimSpace(os.Getenv("LINGMA_E2E_MODEL"))
	if modelName == "" {
		models, err := e.FetchModels(context.Background(), auth)
		if err == nil && len(models) > 0 {
			modelName = models[0].ID
		}
	}
	if modelName == "" {
		modelName = "qwen-2.5-max"
	}
	return executorE2ETarget{
		Name:                       "lingma/" + modelName,
		Executor:                   e,
		Auth:                       auth,
		Model:                      modelName,
		SourceFormat:               sdktranslator.FromString("openai"),
		SupportsClaudeTools:        true,
		SupportsToolResultFollowUp: true,
		RequiresTraeToolID:         false,
	}, true
}

// loadLingmaTestAuth loads Lingma credentials from the local JSON auth file.
func loadLingmaTestAuth(t *testing.T) *cliproxyauth.Auth {
	t.Helper()
	authFile := findLingmaTestAuthFile(t)
	if authFile == "" {
		t.Skip("no Lingma auth JSON found; set LINGMA_E2E_AUTH_FILE or place lingma-*.json in auths/")
	}
	return loadLingmaTestAuthFromFile(t, authFile)
}

func findLingmaTestAuthFile(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Join(wd, "..", "..", "..")

	authFile := strings.TrimSpace(os.Getenv("LINGMA_E2E_AUTH_FILE"))
	if authFile != "" && !filepath.IsAbs(authFile) {
		authFile = filepath.Join(repoRoot, authFile)
	}
	if authFile != "" {
		return authFile
	}

	for _, dir := range []string{filepath.Join(repoRoot, "auths"), repoRoot} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "lingma-") && strings.HasSuffix(e.Name(), ".json") {
				return filepath.Join(dir, e.Name())
			}
		}
	}
	return ""
}

func loadLingmaTestAuthFromFile(t *testing.T, authFile string) *cliproxyauth.Auth {
	t.Helper()
	raw, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}

	var authData struct {
		MachineID          string `json:"machine_id"`
		UID                string `json:"uid"`
		OrganizationID     string `json:"organization_id"`
		Key                string `json:"key"`
		EncryptUserInfo    string `json:"encrypt_user_info"`
		UserType           string `json:"user_type"`
		SecurityOAuthToken string `json:"security_oauth_token"`
		Name               string `json:"name"`
	}
	if err := json.Unmarshal(raw, &authData); err != nil {
		t.Fatalf("parse auth json: %v", err)
	}

	return &cliproxyauth.Auth{
		Provider: "lingma",
		Metadata: map[string]any{
			"machine_id":           authData.MachineID,
			"uid":                  authData.UID,
			"organization_id":      authData.OrganizationID,
			"key":                  authData.Key,
			"encrypt_user_info":    authData.EncryptUserInfo,
			"user_type":            authData.UserType,
			"security_oauth_token": authData.SecurityOAuthToken,
		},
	}
}

func TestLingmaE2E_FetchModels(t *testing.T) {
	auth := loadLingmaTestAuth(t)
	e := NewLingmaExecutor(nil)
	ctx := context.Background()

	models, err := e.FetchModels(ctx, auth)
	if err != nil {
		t.Fatalf("FetchModels error: %v", err)
	}

	t.Logf("Lingma fetched %d models:", len(models))
	for _, m := range models {
		t.Logf("  Model: ID=%s, DisplayName=%s", m.ID, m.DisplayName)
	}

	if len(models) == 0 {
		t.Fatal("Lingma returned 0 models")
	}
}
