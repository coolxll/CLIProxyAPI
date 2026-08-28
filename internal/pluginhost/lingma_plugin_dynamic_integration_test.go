//go:build cgo && (linux || darwin) && providerplugins

package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestLingmaDynamicLibraryLoadsAndExecutesThroughHostCallbacks(t *testing.T) {
	pluginDir := buildLingmaPluginLibrary(t)
	enabled := true
	host := New()
	t.Cleanup(host.ShutdownAll)
	host.ApplyConfig(context.Background(), &config.Config{
		AuthDir: t.TempDir(),
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     pluginDir,
			Configs: map[string]config.PluginInstanceConfig{
				"lingma-plugin": {Enabled: &enabled},
			},
		},
	})
	if !host.PluginRegistered("lingma-plugin") {
		t.Fatal("lingma-plugin was not registered from its dynamic library")
	}
	if !host.HasAuthProvider("lingma-plugin") || !host.HasExecutorCandidateProvider("lingma-plugin") {
		t.Fatal("lingma-plugin did not expose auth and executor capabilities")
	}

	auth, handled, errParse := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		Provider: "lingma-plugin",
		FileName: "lingma-plugin.json",
		RawJSON: []byte(`{
			"type":"lingma-plugin",
			"machine_id":"synthetic-machine-id-123456",
			"uid":"synthetic-user",
			"organization_id":"synthetic-org",
			"key":"synthetic-cosy-key",
			"security_oauth_token":"synthetic-oauth-token",
			"encrypt_user_info":"synthetic-encrypted-user",
			"user_type":"synthetic",
			"name":"Synthetic Lingma"
		}`),
	})
	if errParse != nil || !handled || auth == nil {
		t.Fatalf("parse Lingma plugin auth handled=%t auth=%#v error=%v", handled, auth, errParse)
	}

	transport := lingmaPluginRoundTripper(func(req *http.Request) (*http.Response, error) {
		if !strings.Contains(req.URL.Host, "lingma") {
			return nil, fmt.Errorf("unexpected Lingma plugin URL %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(lingmaPluginSSE("Lingma plugin response"))),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
	adapter, errAdapter := host.executorAdapterForPlugin("lingma-plugin")
	if errAdapter != nil {
		t.Fatalf("Lingma plugin executor adapter: %v", errAdapter)
	}
	payload := []byte(`{"model":"gm51model","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	response, errExecute := adapter.Execute(
		ctx,
		auth,
		coreexecutor.Request{Model: "gm51model", Payload: payload, Format: sdktranslator.FormatOpenAI},
		coreexecutor.Options{
			OriginalRequest: payload,
			SourceFormat:    sdktranslator.FormatOpenAI,
			ResponseFormat:  sdktranslator.FormatOpenAI,
		},
	)
	if errExecute != nil {
		t.Fatalf("execute through Lingma plugin: %v", errExecute)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.message.content").String(); got != "Lingma plugin response" {
		t.Fatalf("content = %q; payload=%s", got, response.Payload)
	}
}

func buildLingmaPluginLibrary(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "lingma-plugin-v0.2.0"+pluginExtension(runtime.GOOS))
	command := exec.Command("go", "build", "-buildmode=c-shared", "-o", outputPath, "./cmd/lingma")
	command.Dir = filepath.Join(repositoryRoot, "provider-plugins")
	command.Env = append(os.Environ(), "CGO_ENABLED=1")
	if output, errBuild := command.CombinedOutput(); errBuild != nil {
		t.Fatalf("build Lingma dynamic plugin: %v\n%s", errBuild, output)
	}
	_ = os.Remove(strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".h")
	return outputDir
}

type lingmaPluginRoundTripper func(*http.Request) (*http.Response, error)

func (fn lingmaPluginRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func lingmaPluginSSE(content string) string {
	inner, _ := json.Marshal(map[string]any{
		"id":    "chatcmpl-lingma-plugin",
		"model": "gm51model",
		"choices": []any{map[string]any{
			"delta":         map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14},
	})
	envelope, _ := json.Marshal(map[string]string{"body": string(inner)})
	return "data:" + string(envelope) + "\n\ndata: [DONE]\n\n"
}
