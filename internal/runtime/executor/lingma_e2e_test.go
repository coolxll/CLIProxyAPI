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
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// loadLingmaTestAuth loads Lingma credentials from the local JSON auth file.
func loadLingmaTestAuth(t *testing.T) *cliproxyauth.Auth {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Join(wd, "..", "..", "..")

	var authFile string
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		t.Fatalf("read repo root: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "lingma-") && strings.HasSuffix(e.Name(), ".json") {
			authFile = filepath.Join(repoRoot, e.Name())
			break
		}
	}
	if authFile == "" {
		t.Skip("no lingma-*.json auth file found in repo root, skipping E2E test")
	}

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

func TestLingmaE2E_Chat_Streaming(t *testing.T) {
	auth := loadLingmaTestAuth(t)
	e := NewLingmaExecutor(nil)
	ctx := context.Background()

	// Try to fetch models first to select a valid one, or default to qwen-2.5-max
	modelName := "qwen-2.5-max"
	models, err := e.FetchModels(ctx, auth)
	if err == nil && len(models) > 0 {
		modelName = models[0].ID
	}
	t.Logf("Using model %q for streaming test", modelName)

	rawRequest := []byte(`{
		"model": "` + modelName + `",
		"messages": [
			{"role": "user", "content": "Reply with exactly: LINGMA_E2E_STREAM_OK"}
		],
		"stream": true
	}`)

	result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   modelName,
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var rawChunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		rawChunks = append(rawChunks, string(chunk.Payload))
	}

	t.Logf("Lingma streaming got %d raw chunks", len(rawChunks))
	var aggContent strings.Builder
	for _, c := range rawChunks {
		dataStr := c
		if strings.HasPrefix(dataStr, "data:") {
			dataStr = strings.TrimSpace(strings.TrimPrefix(dataStr, "data:"))
		}
		if dataStr == "[DONE]" || !gjson.Valid(dataStr) {
			continue
		}
		if val := gjson.Get(dataStr, "choices.0.delta.content"); val.Exists() && val.String() != "" {
			aggContent.WriteString(val.String())
		}
	}

	t.Logf("Lingma streaming content: %q", aggContent.String())
	if strings.TrimSpace(aggContent.String()) == "" {
		t.Fatal("Lingma streaming returned empty content")
	}
}

func TestLingmaE2E_Chat_NonStreaming(t *testing.T) {
	auth := loadLingmaTestAuth(t)
	e := NewLingmaExecutor(nil)
	ctx := context.Background()

	// Try to fetch models first to select a valid one, or default to qwen-2.5-max
	modelName := "qwen-2.5-max"
	models, err := e.FetchModels(ctx, auth)
	if err == nil && len(models) > 0 {
		modelName = models[0].ID
	}
	t.Logf("Using model %q for non-streaming test", modelName)

	rawRequest := []byte(`{
		"model": "` + modelName + `",
		"messages": [
			{"role": "user", "content": "Reply with exactly: LINGMA_E2E_OK"}
		],
		"stream": false
	}`)

	resp, err := e.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   modelName,
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          false,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	content := gjson.GetBytes(resp.Payload, "choices.0.message.content").String()
	t.Logf("Lingma non-streaming content: %q", content)

	if strings.TrimSpace(content) == "" {
		t.Fatal("Lingma non-streaming returned empty content")
	}
}
