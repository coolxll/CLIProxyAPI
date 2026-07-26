//go:build cgo && (linux || darwin)

package pluginhost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const dynamicPluginTestVersion = "0.2.0"

func TestLingmaAndTraeDynamicLibrariesLoadAndExecuteThroughHostCallbacks(t *testing.T) {
	pluginDir := buildProviderPluginLibraries(t)
	enabled := true
	authDir := t.TempDir()
	host := New()
	t.Cleanup(host.ShutdownAll)
	host.ApplyConfig(context.Background(), &config.Config{
		AuthDir: authDir,
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     pluginDir,
			Configs: map[string]config.PluginInstanceConfig{
				"lingma-plugin": {Enabled: &enabled},
				"trae-plugin":   {Enabled: &enabled},
			},
		},
	})

	for _, pluginID := range []string{"lingma-plugin", "trae-plugin"} {
		if !host.PluginRegistered(pluginID) {
			t.Fatalf("plugin %s was not registered from its dynamic library", pluginID)
		}
		if !host.HasAuthProvider(pluginID) || !host.HasExecutorCandidateProvider(pluginID) {
			t.Fatalf("plugin %s did not expose auth and executor capabilities", pluginID)
		}
	}

	lingmaAuth := parseDynamicPluginAuth(t, host, "lingma-plugin", []byte(`{
		"type":"lingma-plugin",
		"machine_id":"synthetic-machine-id-123456",
		"uid":"synthetic-user",
		"organization_id":"synthetic-org",
		"key":"synthetic-cosy-key",
		"security_oauth_token":"synthetic-oauth-token",
		"encrypt_user_info":"synthetic-encrypted-user",
		"user_type":"synthetic",
		"name":"Synthetic Lingma"
	}`))
	traeAuth := parseDynamicPluginAuth(t, host, "trae-plugin", []byte(`{
		"type":"trae-plugin",
		"jwt_token":"synthetic.jwt.token",
		"machine_id":"synthetic-machine",
		"device_id":"synthetic-device",
		"user_id":"synthetic-user",
		"name":"Synthetic Trae"
	}`))
	t.Run("trae-browser-login", func(t *testing.T) {
		runTraeDynamicLogin(t, host, authDir)
	})

	transport := dynamicProviderRoundTripper(func(req *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.Contains(req.URL.Host, "lingma"):
			body = dynamicLingmaSSE("Lingma dynamic response")
		case strings.Contains(req.URL.Host, "trae"):
			body = "event: task_created\n" +
				"data: {\"task_id\":\"task-1\",\"agent_run_id\":\"run-1\"}\n\n" +
				"data: {\"response\":\"Trae dynamic response\"}\n\n" +
				"event: token_usage\n" +
				"data: {\"prompt_tokens\":7,\"completion_tokens\":4,\"total_tokens\":11}\n\n"
		default:
			return nil, fmt.Errorf("unexpected dynamic plugin URL %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)

	tests := []struct {
		pluginID string
		auth     *coreauth.Auth
		model    string
		want     string
	}{
		{pluginID: "lingma-plugin", auth: lingmaAuth, model: "gm51model", want: "Lingma dynamic response"},
		{pluginID: "trae-plugin", auth: traeAuth, model: "glm-5", want: "Trae dynamic response"},
	}
	for _, test := range tests {
		t.Run(test.pluginID, func(t *testing.T) {
			adapter, errAdapter := host.executorAdapterForPlugin(test.pluginID)
			if errAdapter != nil {
				t.Fatalf("executor adapter: %v", errAdapter)
			}
			payload := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"stream":false}`, test.model))
			response, errExecute := adapter.Execute(
				ctx,
				test.auth,
				coreexecutor.Request{Model: test.model, Payload: payload, Format: sdktranslator.FormatOpenAI},
				coreexecutor.Options{
					OriginalRequest: payload,
					SourceFormat:    sdktranslator.FormatOpenAI,
					ResponseFormat:  sdktranslator.FormatOpenAI,
				},
			)
			if errExecute != nil {
				t.Fatalf("execute: %v", errExecute)
			}
			if got := gjson.GetBytes(response.Payload, "choices.0.message.content").String(); got != test.want {
				t.Fatalf("content = %q, want %q; payload=%s", got, test.want, response.Payload)
			}
		})
	}
	t.Run("trae-stream-cancellation", func(t *testing.T) {
		runTraeDynamicStreamCancellation(t, host, traeAuth)
	})
	runOptInLiveProviderSubtests(t, host)
	t.Run("active-stream-shutdown", func(t *testing.T) {
		runDynamicActiveStreamShutdown(t, host, lingmaAuth, traeAuth)
	})
}

func runTraeDynamicLogin(t *testing.T, host *Host, authDir string) {
	t.Helper()
	jwt := dynamicSyntheticTraeJWT(t, "dynamic-login-user")
	transport := dynamicProviderRoundTripper(func(req *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.Contains(req.URL.Path, "GetLoginGuidance"):
			body = `{"Result":{"LoginHost":"https://www.trae.cn"}}`
		case strings.Contains(req.URL.Path, "ExchangeToken"):
			body = `{"Result":{"Token":` + quotedJSON(jwt) + `,"RefreshToken":"dynamic-refresh-2"}}`
		default:
			return nil, fmt.Errorf("unexpected Trae login URL %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
	start, handled, errStart := host.StartLogin(ctx, "trae-plugin", "http://127.0.0.1:8317/v0/management/oauth-callback")
	if errStart != nil || !handled {
		t.Fatalf("start Trae plugin login handled=%t error=%v", handled, errStart)
	}
	if start.State == "" || !strings.Contains(start.URL, "auth_callback_url=") {
		t.Fatalf("invalid Trae plugin login response: %+v", start)
	}
	callbackData, errMarshal := json.Marshal(map[string]string{
		"code":  "refreshToken=dynamic-refresh-1&loginHost=https%3A%2F%2Fapi.trae.com.cn",
		"state": start.State,
	})
	if errMarshal != nil {
		t.Fatalf("marshal Trae callback: %v", errMarshal)
	}
	callbackPath := filepath.Join(authDir, ".oauth-trae-plugin-"+start.State+".oauth")
	if errWrite := os.WriteFile(callbackPath, callbackData, 0o600); errWrite != nil {
		t.Fatalf("write Trae callback: %v", errWrite)
	}
	poll, handled, errPoll := host.PollLogin(ctx, "trae-plugin", start.State, start.Metadata)
	if errPoll != nil || !handled {
		t.Fatalf("poll Trae plugin login handled=%t error=%v", handled, errPoll)
	}
	if poll.Status != pluginapi.AuthLoginStatusSuccess || poll.Auth.Provider != "trae-plugin" {
		t.Fatalf("invalid Trae plugin login poll response: %+v", poll)
	}
	if strings.Contains(string(poll.Auth.StorageJSON), "dynamic-refresh-1") {
		t.Fatal("Trae plugin persisted the rotated-out refresh token")
	}
}

func dynamicSyntheticTraeJWT(t *testing.T, userID string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, errMarshal := json.Marshal(map[string]any{
		"data": map[string]string{"id": userID},
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	if errMarshal != nil {
		t.Fatalf("marshal dynamic JWT: %v", errMarshal)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func quotedJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func runTraeDynamicStreamCancellation(t *testing.T, host *Host, auth *coreauth.Auth) {
	t.Helper()
	closed := make(chan struct{})
	var closeOnce sync.Once
	var streamBody strings.Builder
	for index := 0; index < 128; index++ {
		fmt.Fprintf(&streamBody, "data: {\"response\":\"chunk-%03d\"}\n\n", index)
	}
	transport := dynamicProviderRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: &notifyingReadCloser{
				Reader: strings.NewReader(streamBody.String()),
				close: func() {
					closeOnce.Do(func() { close(closed) })
				},
			},
			Request: req,
		}, nil
	})
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), "cliproxy.roundtripper", transport))
	adapter, errAdapter := host.executorAdapterForPlugin("trae-plugin")
	if errAdapter != nil {
		t.Fatalf("executor adapter: %v", errAdapter)
	}
	payload := []byte(`{"model":"glm-5","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	result, errExecute := adapter.ExecuteStream(
		ctx,
		auth,
		coreexecutor.Request{Model: "glm-5", Payload: payload, Format: sdktranslator.FormatOpenAI},
		coreexecutor.Options{
			Stream:          true,
			OriginalRequest: payload,
			SourceFormat:    sdktranslator.FormatOpenAI,
			ResponseFormat:  sdktranslator.FormatOpenAI,
		},
	)
	if errExecute != nil {
		t.Fatalf("execute stream: %v", errExecute)
	}

	time.Sleep(100 * time.Millisecond)
	cancel()
	for range result.Chunks {
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Trae upstream body was not closed after downstream cancellation")
	}
}

func runDynamicActiveStreamShutdown(t *testing.T, host *Host, lingmaAuth, traeAuth *coreauth.Auth) {
	t.Helper()
	var bodiesMu sync.Mutex
	var bodies []*blockingReadCloser
	transport := dynamicProviderRoundTripper(func(req *http.Request) (*http.Response, error) {
		body := newBlockingReadCloser()
		bodiesMu.Lock()
		bodies = append(bodies, body)
		bodiesMu.Unlock()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       body,
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
	streams := make([]*coreexecutor.StreamResult, 0, 2)
	for _, item := range []struct {
		provider string
		auth     *coreauth.Auth
		model    string
	}{
		{provider: "lingma-plugin", auth: lingmaAuth, model: "gm51model"},
		{provider: "trae-plugin", auth: traeAuth, model: "glm-5"},
	} {
		adapter, errAdapter := host.executorAdapterForPlugin(item.provider)
		if errAdapter != nil {
			t.Fatalf("%s executor adapter: %v", item.provider, errAdapter)
		}
		payload := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"stream":true}`, item.model))
		result, errExecute := adapter.ExecuteStream(
			ctx,
			item.auth,
			coreexecutor.Request{Model: item.model, Payload: payload, Format: sdktranslator.FormatOpenAI},
			coreexecutor.Options{
				Stream:          true,
				OriginalRequest: payload,
				SourceFormat:    sdktranslator.FormatOpenAI,
				ResponseFormat:  sdktranslator.FormatOpenAI,
			},
		)
		if errExecute != nil {
			t.Fatalf("%s execute stream: %v", item.provider, errExecute)
		}
		streams = append(streams, result)
	}
	for _, stream := range streams {
		go func(chunks <-chan coreexecutor.StreamChunk) {
			for range chunks {
			}
		}(stream.Chunks)
	}

	shutdownDone := make(chan struct{})
	go func() {
		host.ShutdownAll()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
	case <-time.After(5 * time.Second):
		t.Fatal("host shutdown blocked with active provider plugin streams")
	}
	bodiesMu.Lock()
	defer bodiesMu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("active upstream body count = %d, want 2", len(bodies))
	}
	for index, body := range bodies {
		select {
		case <-body.closed:
		default:
			t.Fatalf("active upstream body %d was not closed during shutdown", index)
		}
	}
}

func buildProviderPluginLibraries(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	pluginModule := filepath.Join(repositoryRoot, "provider-plugins")
	outputDir := t.TempDir()
	for _, pluginID := range []string{"lingma", "trae"} {
		outputPath := filepath.Join(outputDir, pluginID+"-plugin-v"+dynamicPluginTestVersion+pluginExtension(runtime.GOOS))
		command := exec.Command("go", "build", "-buildmode=c-shared", "-o", outputPath, "./cmd/"+pluginID)
		command.Dir = pluginModule
		command.Env = append(os.Environ(), "CGO_ENABLED=1")
		if output, errBuild := command.CombinedOutput(); errBuild != nil {
			t.Fatalf("build %s dynamic plugin: %v\n%s", pluginID, errBuild, output)
		}
		_ = os.Remove(strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".h")
	}
	return outputDir
}

func parseDynamicPluginAuth(t *testing.T, host *Host, provider string, raw []byte) *coreauth.Auth {
	t.Helper()
	auth, handled, errParse := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		Provider: provider,
		FileName: provider + ".json",
		RawJSON:  raw,
	})
	if errParse != nil {
		t.Fatalf("parse %s auth: %v", provider, errParse)
	}
	if !handled || auth == nil {
		t.Fatalf("parse %s auth handled=%t auth=%#v", provider, handled, auth)
	}
	return auth
}

type dynamicProviderRoundTripper func(*http.Request) (*http.Response, error)

func (fn dynamicProviderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type notifyingReadCloser struct {
	io.Reader
	close func()
}

type blockingReadCloser struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closed: make(chan struct{})}
}

func (r *blockingReadCloser) Read(_ []byte) (int, error) {
	<-r.closed
	return 0, io.EOF
}

func (r *blockingReadCloser) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

func (r *notifyingReadCloser) Close() error {
	if r != nil && r.close != nil {
		r.close()
	}
	return nil
}

func dynamicLingmaSSE(content string) string {
	inner, _ := json.Marshal(map[string]any{
		"id":    "chatcmpl-dynamic",
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
