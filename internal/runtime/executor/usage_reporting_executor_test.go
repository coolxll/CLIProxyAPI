package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestLingmaExecutorStreamPublishesUsageWithoutUsageFrame(t *testing.T) {
	setupExecutorUsageQueue(t)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `data:{"headers":{"Content-Type":["application/json"]},"body":"{\"choices\":[{\"delta\":{\"content\":\"Pong\"},\"finish_reason\":\"stop\"}]}","statusCodeValue":200,"statusCode":"OK"}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))

	rawRequest := []byte(`{"model":"dashscope_qmodel","messages":[{"role":"user","content":"Ping"}],"stream":true}`)
	result, err := NewLingmaExecutor(nil).ExecuteStream(ctx, &cliproxyauth.Auth{
		Provider: "lingma",
		Metadata: map[string]any{
			"uid":             "test-user",
			"key":             "test-cosy-key",
			"organization_id": "test-org",
		},
	}, cliproxyexecutor.Request{
		Model:   "dashscope_qmodel",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream returned setup error: %v", err)
	}
	drainStream(t, result)

	got := waitForExecutorQueuedUsage(t, "lingma", "dashscope_qmodel")
	if got.Failed {
		t.Fatalf("queued usage failed = true, want false")
	}
	if got.Tokens.TotalTokens != 0 {
		t.Fatalf("queued usage total tokens = %d, want 0", got.Tokens.TotalTokens)
	}
}

func TestLingmaExecutorClaudeStreamPublishesFinalUsage(t *testing.T) {
	setupExecutorUsageQueue(t)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `data:{"headers":{"Content-Type":["application/json"]},"body":"{\"id\":\"chatcmpl-test\",\"model\":\"gm51model\",\"choices\":[{\"delta\":{\"content\":\"Pong\"},\"finish_reason\":null}],\"usage\":null}","statusCodeValue":200,"statusCode":"OK"}` + "\n\n" +
			`data:{"headers":{"Content-Type":["application/json"]},"body":"{\"id\":\"chatcmpl-test\",\"model\":\"gm51model\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":null}","statusCodeValue":200,"statusCode":"OK"}` + "\n\n" +
			`data:{"headers":{"Content-Type":["application/json"]},"body":"{\"id\":\"chatcmpl-test\",\"model\":\"gm51model\",\"choices\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"total_tokens\":18}}","statusCodeValue":200,"statusCode":"OK"}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))

	rawRequest := []byte(`{"model":"gm51model","messages":[{"role":"user","content":"Ping"}],"max_tokens":1024,"stream":true}`)
	result, err := NewLingmaExecutor(nil).ExecuteStream(ctx, &cliproxyauth.Auth{
		Provider: "lingma",
		Metadata: map[string]any{
			"uid":             "test-user",
			"key":             "test-cosy-key",
			"organization_id": "test-org",
		},
	}, cliproxyexecutor.Request{
		Model:   "gm51model",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream returned setup error: %v", err)
	}
	drainStream(t, result)

	got := waitForExecutorQueuedUsage(t, "lingma", "gm51model")
	if got.Failed {
		t.Fatalf("queued usage failed = true, want false")
	}
	if got.Tokens.TotalTokens != 18 {
		t.Fatalf("queued usage total tokens = %d, want 18", got.Tokens.TotalTokens)
	}
}

func TestLingmaExecutorClaudeNonStreamPublishesCacheUsage(t *testing.T) {
	setupExecutorUsageQueue(t)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `data:{"headers":{"Content-Type":["application/json"]},"body":"{\"id\":\"chatcmpl-test\",\"model\":\"gm51model\",\"choices\":[{\"delta\":{\"content\":\"Pong\"},\"finish_reason\":\"stop\"}],\"usage\":{\"input_tokens\":10,\"output_tokens\":4,\"cache_read_input_tokens\":3}}","statusCodeValue":200,"statusCode":"OK"}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))

	rawRequest := []byte(`{"model":"gm51model","messages":[{"role":"user","content":"Ping"}],"max_tokens":1024,"stream":false}`)
	resp, err := NewLingmaExecutor(nil).Execute(ctx, &cliproxyauth.Auth{
		Provider: "lingma",
		Metadata: map[string]any{
			"uid":             "test-user",
			"key":             "test-cosy-key",
			"organization_id": "test-org",
		},
	}, cliproxyexecutor.Request{
		Model:   "gm51model",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("response payload is empty")
	}

	got := waitForExecutorQueuedUsage(t, "lingma", "gm51model")
	if got.Failed {
		t.Fatalf("queued usage failed = true, want false")
	}
	if got.Tokens.CacheReadTokens != 3 {
		t.Fatalf("queued usage cache read tokens = %d, want 3", got.Tokens.CacheReadTokens)
	}
	if got.Tokens.CachedTokens != 3 {
		t.Fatalf("queued usage cached tokens = %d, want 3", got.Tokens.CachedTokens)
	}
}

func TestTraeExecutorStreamPublishesUsageWithoutTokenUsageFrame(t *testing.T) {
	setupExecutorUsageQueue(t)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := "data: {\"content\":\"hello\"}\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))

	rawRequest := []byte(`{"model":"doubao-seed-2.0-code","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	result, err := NewTraeExecutor(nil).ExecuteStream(ctx, &cliproxyauth.Auth{
		Provider: "trae",
		Attributes: map[string]string{
			"jwt_token": "not-a-real-jwt",
		},
	}, cliproxyexecutor.Request{
		Model:   "doubao-seed-2.0-code",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream returned setup error: %v", err)
	}
	drainStream(t, result)

	got := waitForExecutorQueuedUsage(t, "trae", "doubao-seed-2.0-code")
	if got.Failed {
		t.Fatalf("queued usage failed = true, want false")
	}
	if got.Tokens.TotalTokens != 0 {
		t.Fatalf("queued usage total tokens = %d, want 0", got.Tokens.TotalTokens)
	}
}

func TestTraeExecutorStreamErrorPublishesFailureUsage(t *testing.T) {
	setupExecutorUsageQueue(t)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := "event: error\n" +
			"data: {\"code\":4001,\"message\":\"failed to get summary config\"}\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))

	rawRequest := []byte(`{"model":"doubao-seed-2.0-code","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	result, err := NewTraeExecutor(nil).ExecuteStream(ctx, &cliproxyauth.Auth{
		Provider: "trae",
		Attributes: map[string]string{
			"jwt_token": "not-a-real-jwt",
		},
	}, cliproxyexecutor.Request{
		Model:   "doubao-seed-2.0-code",
		Payload: rawRequest,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: rawRequest,
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream returned setup error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("expected stream error from Trae error event")
	}

	got := waitForExecutorQueuedUsage(t, "trae", "doubao-seed-2.0-code")
	if !got.Failed {
		t.Fatalf("queued usage failed = false, want true")
	}
	if got.Fail.StatusCode != http.StatusBadGateway {
		t.Fatalf("queued usage fail status = %d, want %d", got.Fail.StatusCode, http.StatusBadGateway)
	}
	if !strings.Contains(got.Fail.Body, "trae error event 4001") {
		t.Fatalf("queued usage fail body = %q, want Trae error event", got.Fail.Body)
	}
}

func setupExecutorUsageQueue(t *testing.T) {
	t.Helper()

	prevQueueEnabled := redisqueue.Enabled()
	prevUsageEnabled := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
		redisqueue.SetUsageStatisticsEnabled(prevUsageEnabled)
	})
}

func drainStream(t *testing.T, result *cliproxyexecutor.StreamResult) {
	t.Helper()

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
	}
}

func waitForExecutorQueuedUsage(t *testing.T, wantProvider, wantModel string) executorQueuedUsagePayload {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items := redisqueue.PopOldest(20)
		for _, item := range items {
			got, ok := parseExecutorQueuedUsage(t, item)
			if !ok {
				continue
			}
			if got.Provider == wantProvider && got.Model == wantModel {
				return got
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for queued usage payload for provider=%q model=%q", wantProvider, wantModel)
	return executorQueuedUsagePayload{}
}

type executorQueuedUsagePayload struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Failed   bool   `json:"failed"`
	Tokens   struct {
		CachedTokens    int64 `json:"cached_tokens"`
		CacheReadTokens int64 `json:"cache_read_tokens"`
		TotalTokens     int64 `json:"total_tokens"`
	} `json:"tokens"`
	Fail struct {
		StatusCode int    `json:"status_code"`
		Body       string `json:"body"`
	} `json:"fail"`
}

func parseExecutorQueuedUsage(t *testing.T, payload []byte) (executorQueuedUsagePayload, bool) {
	t.Helper()

	var parsed executorQueuedUsagePayload
	if len(payload) == 0 {
		return parsed, false
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return parsed, false
	}
	if parsed.Provider == "" || parsed.Model == "" {
		return parsed, false
	}
	return parsed, true
}
