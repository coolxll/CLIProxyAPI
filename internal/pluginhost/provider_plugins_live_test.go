//go:build cgo && (linux || darwin)

package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func runOptInLiveProviderSubtests(t *testing.T, host *Host) {
	t.Helper()
	liveProviders := []struct {
		id       string
		authPath string
		model    string
	}{
		{
			id:       "lingma-plugin",
			authPath: strings.TrimSpace(os.Getenv("CLIPROXY_LINGMA_PLUGIN_LIVE_AUTH_FILE")),
			model:    firstNonEmptyLive(os.Getenv("CLIPROXY_LINGMA_PLUGIN_LIVE_MODEL"), "gm51model"),
		},
		{
			id:       "trae-plugin",
			authPath: strings.TrimSpace(os.Getenv("CLIPROXY_TRAE_PLUGIN_LIVE_AUTH_FILE")),
			model:    firstNonEmptyLive(os.Getenv("CLIPROXY_TRAE_PLUGIN_LIVE_MODEL"), "glm-5"),
		},
	}
	hasLiveProvider := false
	for _, live := range liveProviders {
		hasLiveProvider = hasLiveProvider || live.authPath != ""
	}
	if !hasLiveProvider {
		return
	}

	for _, live := range liveProviders {
		if live.authPath == "" {
			continue
		}
		t.Run(live.id, func(t *testing.T) {
			auth := loadExplicitLiveAuth(t, host, live.id, live.authPath)
			runLiveNonStream(t, host, live.id, auth, live.model)
			runLiveStream(t, host, live.id, auth, live.model)
		})
	}
}

func loadExplicitLiveAuth(t *testing.T, host *Host, provider, path string) *coreauth.Auth {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read explicitly configured live auth file: %v", errRead)
	}
	var stored map[string]any
	if errUnmarshal := json.Unmarshal(raw, &stored); errUnmarshal != nil {
		t.Fatalf("decode explicitly configured live auth file: %v", errUnmarshal)
	}
	stored["type"] = provider
	pluginRaw, errMarshal := json.Marshal(stored)
	if errMarshal != nil {
		t.Fatalf("prepare live auth for shadow provider: %v", errMarshal)
	}
	auth, handled, errParse := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		Provider: provider,
		FileName: provider + "-live.json",
		RawJSON:  pluginRaw,
	})
	if errParse != nil {
		t.Fatalf("parse live auth: %v", errParse)
	}
	if !handled || auth == nil {
		t.Fatal("live auth was not handled by provider plugin")
	}
	return auth
}

func runLiveNonStream(t *testing.T, host *Host, provider string, auth *coreauth.Auth, model string) {
	t.Helper()
	adapter, errAdapter := host.executorAdapterForPlugin(provider)
	if errAdapter != nil {
		t.Fatalf("executor adapter: %v", errAdapter)
	}
	payload := []byte(`{"model":` + jsonStringLive(model) + `,"messages":[{"role":"user","content":"Reply with exactly: live-ok"}],"stream":false}`)
	response, errExecute := adapter.Execute(
		context.Background(),
		auth,
		coreexecutor.Request{Model: model, Payload: payload, Format: sdktranslator.FormatOpenAI},
		coreexecutor.Options{
			OriginalRequest: payload,
			SourceFormat:    sdktranslator.FormatOpenAI,
			ResponseFormat:  sdktranslator.FormatOpenAI,
		},
	)
	if errExecute != nil {
		t.Fatalf("live non-stream execution: %v", errExecute)
	}
	if len(bytes.TrimSpace(response.Payload)) == 0 || !json.Valid(response.Payload) {
		t.Fatal("live non-stream execution returned no valid JSON payload")
	}
}

func runLiveStream(t *testing.T, host *Host, provider string, auth *coreauth.Auth, model string) {
	t.Helper()
	adapter, errAdapter := host.executorAdapterForPlugin(provider)
	if errAdapter != nil {
		t.Fatalf("executor adapter: %v", errAdapter)
	}
	payload := []byte(`{"model":` + jsonStringLive(model) + `,"messages":[{"role":"user","content":"Reply with exactly: live-stream-ok"}],"stream":true}`)
	result, errExecute := adapter.ExecuteStream(
		context.Background(),
		auth,
		coreexecutor.Request{Model: model, Payload: payload, Format: sdktranslator.FormatOpenAI},
		coreexecutor.Options{
			Stream:          true,
			OriginalRequest: payload,
			SourceFormat:    sdktranslator.FormatOpenAI,
			ResponseFormat:  sdktranslator.FormatOpenAI,
		},
	)
	if errExecute != nil {
		t.Fatalf("live stream execution: %v", errExecute)
	}
	chunks := 0
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("live stream terminal error: %v", chunk.Err)
		}
		if len(bytes.TrimSpace(chunk.Payload)) > 0 {
			chunks++
		}
	}
	if chunks == 0 {
		t.Fatal("live stream execution returned no chunks")
	}
}

func jsonStringLive(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func firstNonEmptyLive(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
