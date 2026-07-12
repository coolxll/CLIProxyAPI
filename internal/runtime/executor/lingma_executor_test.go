package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/lingma/helpers"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	lingmaencoding "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/lingma"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type lingmaTestRoundTripper func(*http.Request) (*http.Response, error)

func (f lingmaTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestLingmaExecutorExecuteReturns501ForOpenAIResponseFormat(t *testing.T) {
	e := &LingmaExecutor{}
	req := cliproxyexecutor.Request{Model: "test-model", Payload: []byte(`{}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.Format("openai-response")}
	_, err := e.Execute(context.Background(), nil, req, opts)
	if err == nil {
		t.Fatal("expected error for openai-response format, got nil")
	}
	statusErr, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T: %v", err, err)
	}
	if statusErr.code != http.StatusNotImplemented {
		t.Fatalf("status code = %d, want %d", statusErr.code, http.StatusNotImplemented)
	}
}

func TestLingmaExecutorExecuteStreamReturns501ForOpenAIResponseFormat(t *testing.T) {
	e := &LingmaExecutor{}
	req := cliproxyexecutor.Request{Model: "test-model", Payload: []byte(`{}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.Format("openai-response")}
	_, err := e.ExecuteStream(context.Background(), nil, req, opts)
	if err == nil {
		t.Fatal("expected error for openai-response format, got nil")
	}
	statusErr, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T: %v", err, err)
	}
	if statusErr.code != http.StatusNotImplemented {
		t.Fatalf("status code = %d, want %d", statusErr.code, http.StatusNotImplemented)
	}
}

func TestLingmaExecutorCountTokensReturnsApproximation(t *testing.T) {
	e := &LingmaExecutor{}
	payload := []byte(`{"model":"test","messages":[{"role":"user","content":"Hello, how are you?"}],"stream":false}`)
	req := cliproxyexecutor.Request{Model: "test-model", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.Format("claude")}
	resp, err := e.CountTokens(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("CountTokens returned empty payload")
	}
}

func TestPreserveLingmaClaudeCodeThinkingAdaptiveEffort(t *testing.T) {
	body := []byte(`{"model_config":{"is_reasoning":false}}`)
	source := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`)

	out := preserveLingmaClaudeCodeThinking(body, source, "claude")

	if got := gjson.GetBytes(out, "model_config.is_reasoning").Bool(); !got {
		t.Fatalf("model_config.is_reasoning = %v, want true; body=%s", got, string(out))
	}
}

func TestPreserveLingmaClaudeCodeThinkingDisabled(t *testing.T) {
	body := []byte(`{"model_config":{"is_reasoning":true}}`)
	source := []byte(`{"thinking":{"type":"disabled"}}`)

	out := preserveLingmaClaudeCodeThinking(body, source, "claude")

	if got := gjson.GetBytes(out, "model_config.is_reasoning").Bool(); got {
		t.Fatalf("model_config.is_reasoning = %v, want false; body=%s", got, string(out))
	}
}

func TestParseLingmaModelsCategorizedResponse(t *testing.T) {
	raw := []byte(`{
		"chat": [
			{"key": "dashscope_qmodel", "display_name": "DashScope QModel"}
		],
		"developer": {
			"dev": {"modelId": "dev_model", "displayName": "Developer Model"}
		}
	}`)

	models := parseLingmaModels(raw, 123)
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].ID != "dashscope_qmodel" || models[0].DisplayName != "DashScope QModel" {
		t.Fatalf("models[0] = %#v", models[0])
	}
	if models[1].ID != "dev_model" || models[1].DisplayName != "Developer Model" {
		t.Fatalf("models[1] = %#v", models[1])
	}
}

func TestParseLingmaModelsArrayResponse(t *testing.T) {
	raw := []byte(`[
		{"modelName": "qwen-2.5-max"},
		{"id": "qwen-2.5-max"},
		{"model_id": "qwen-coder", "name": "Qwen Coder"}
	]`)

	models := parseLingmaModels(raw, 123)
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].ID != "qwen-2.5-max" {
		t.Fatalf("models[0].ID = %q, want qwen-2.5-max", models[0].ID)
	}
	if models[1].ID != "qwen-coder" || models[1].DisplayName != "Qwen Coder" {
		t.Fatalf("models[1] = %#v", models[1])
	}
}

// TestLingmaChatURLAgentIDMatchesBody verifies that the URL's AgentId query
// param matches the body's agent_id after translation, for both reasoning-on
// (agent_chat) and reasoning-off (agent_common) requests. The translator may
// flip agent_id to agent_common when reasoning is disabled; the URL builder
// must follow so URL and body never disagree.
func TestLingmaChatURLAgentIDMatchesBody(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		payload   string
		wantAgent string
	}{
		{
			name:      "reasoning_off_forces_agent_common",
			model:     "gm51model",
			payload:   `{"model":"gm51model","messages":[{"role":"user","content":"hi"}],"stream":true,"reasoning_effort":"none"}`,
			wantAgent: "agent_common",
		},
		{
			name:      "reasoning_on_keeps_agent_chat",
			model:     "gm51model",
			payload:   `{"model":"gm51model","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			wantAgent: "agent_chat",
		},
		{
			name:      "agent_common_model_stays_agent_common",
			model:     "kmodel",
			payload:   `{"model":"kmodel","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			wantAgent: "agent_common",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from := sdktranslator.FormatOpenAI
			to := sdktranslator.FromString("lingma")
			body := sdktranslator.TranslateRequest(from, to, tc.model, []byte(tc.payload), true)
			// ApplyThinking runs after translation in the executor; it must not
			// change agent_id (only model_config.is_reasoning). Run it to mirror
			// the real pipeline and assert agent_id is stable.
			body, _ = thinking.ApplyThinking(body, tc.model, from.String(), to.String(), "lingma")

			bodyAgent := gjson.GetBytes(body, "agent_id").String()
			if bodyAgent != tc.wantAgent {
				t.Fatalf("body agent_id = %q, want %q", bodyAgent, tc.wantAgent)
			}

			// The executor builds the URL from the final body's agent_id.
			chatURL := lingmaChatURLForAgent(lingmaAgentIDFromBody(body, tc.model))
			u, err := url.Parse(chatURL)
			if err != nil {
				t.Fatalf("parse chatURL: %v", err)
			}
			urlAgent := u.Query().Get("AgentId")
			if urlAgent != bodyAgent {
				t.Fatalf("URL AgentId = %q, body agent_id = %q; they must match", urlAgent, bodyAgent)
			}
		})
	}
}

// TestLingmaAgentIDFromBodyFallback asserts the defensive fallback: when the
// body lacks an agent_id field, we derive it from the model name so the URL
// still gets a valid AgentId rather than empty.
func TestLingmaAgentIDFromBodyFallback(t *testing.T) {
	got := lingmaAgentIDFromBody([]byte(`{"model_config":{}}`), "gm51model")
	if got != "agent_chat" {
		t.Fatalf("lingmaAgentIDFromBody fallback = %q, want agent_chat", got)
	}
	got = lingmaAgentIDFromBody([]byte(`{"agent_id":"agent_common"}`), "gm51model")
	if got != "agent_common" {
		t.Fatalf("lingmaAgentIDFromBody = %q, want agent_common", got)
	}
}

func TestNormalizeLingmaUpstreamError(t *testing.T) {
	t.Run("unexpected EOF", func(t *testing.T) {
		err := normalizeLingmaUpstreamError(io.ErrUnexpectedEOF, lingmaRequestProfile{})
		status, ok := err.(interface{ StatusCode() int })
		if !ok || status.StatusCode() != http.StatusBadGateway {
			t.Fatalf("error = %T %v, want status %d", err, err, http.StatusBadGateway)
		}
		if err.Error() != "lingma upstream connection closed unexpectedly" {
			t.Fatalf("message = %q", err.Error())
		}
	})

	t.Run("large thinking timeout includes advice", func(t *testing.T) {
		err := normalizeLingmaUpstreamError(context.DeadlineExceeded, lingmaRequestProfile{LargeThinking: true})
		status, ok := err.(interface{ StatusCode() int })
		if !ok || status.StatusCode() != http.StatusGatewayTimeout {
			t.Fatalf("error = %T %v, want status %d", err, err, http.StatusGatewayTimeout)
		}
		if !strings.Contains(err.Error(), `reasoning_effort to "none"`) {
			t.Fatalf("message missing mitigation advice: %q", err.Error())
		}
	})

	t.Run("context canceled remains cancellation", func(t *testing.T) {
		err := normalizeLingmaUpstreamError(context.Canceled, lingmaRequestProfile{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	})
}

func TestInspectLingmaRequestDetectsLargeThinkingToolHistory(t *testing.T) {
	body := translatedLargeLingmaThinkingBody(t)

	profile := inspectLingmaRequest(body, "gm51model")
	if !profile.LargeThinking {
		t.Fatalf("profile = %#v, want large thinking request", profile)
	}
	if profile.ToolCalls != 10 || profile.ToolResults != 10 || profile.Messages != 21 || profile.Tools != 1 {
		t.Fatalf("unexpected profile counts: %#v", profile)
	}
}

func TestLingmaThinkingFallbackAppliesOnceAfterCancellation(t *testing.T) {
	cfg := &config.Config{}
	cfg.LingmaThinkingFallback.Enabled = true
	cfg.LingmaThinkingFallback.TTL = "2m"
	executor := NewLingmaExecutor(cfg)
	auth := &cliproxyauth.Auth{
		Provider: "lingma",
		Metadata: map[string]any{
			"uid": "test-user",
			"key": "test-key",
		},
	}
	payload := largeLingmaThinkingSourcePayload(t)
	req := cliproxyexecutor.Request{Model: "gm51model", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload, Stream: true}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := executor.ExecuteStream(canceledCtx, auth, req, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first ExecuteStream error = %v, want context canceled", err)
	}

	secondBody, secondHeaders := executeCapturedLingmaStream(t, executor, auth, req, opts)
	if got := secondHeaders.Get(cliproxyexecutor.FallbackHeaderName); got != lingmaThinkingFallbackHeaderValue {
		t.Fatalf("fallback header = %q, want %q", got, lingmaThinkingFallbackHeaderValue)
	}
	if got := gjson.GetBytes(secondBody, "agent_id").String(); got != helpers.AgentCommon {
		t.Fatalf("second agent_id = %q, want %q", got, helpers.AgentCommon)
	}
	if gjson.GetBytes(secondBody, "model_config.is_reasoning").Bool() {
		t.Fatalf("second request kept reasoning enabled: %s", secondBody)
	}
	if got := gjson.GetBytes(secondBody, "model_config.source").String(); got != "" {
		t.Fatalf("second model_config.source = %q, want empty", got)
	}

	thirdBody, thirdHeaders := executeCapturedLingmaStream(t, executor, auth, req, opts)
	if got := thirdHeaders.Get(cliproxyexecutor.FallbackHeaderName); got != "" {
		t.Fatalf("third fallback header = %q, want empty", got)
	}
	if got := gjson.GetBytes(thirdBody, "agent_id").String(); got != "agent_chat" {
		t.Fatalf("third agent_id = %q, want agent_chat", got)
	}
	if !gjson.GetBytes(thirdBody, "model_config.is_reasoning").Bool() {
		t.Fatalf("third request did not restore reasoning: %s", thirdBody)
	}
}

func TestLingmaRecoverySettingsDefaultsAndClamp(t *testing.T) {
	attempts, delay := lingmaRecoverySettings(nil)
	if attempts != lingmaRecoveryDefaultAttempts || delay != lingmaRecoveryDefaultDelay {
		t.Fatalf("defaults = (%d, %s), want (%d, %s)", attempts, delay, lingmaRecoveryDefaultAttempts, lingmaRecoveryDefaultDelay)
	}

	cfg := &config.Config{}
	cfg.LingmaUpstreamRecovery.MaxAttempts = 99
	cfg.LingmaUpstreamRecovery.BaseDelay = "5ms"
	attempts, delay = lingmaRecoverySettings(cfg)
	if attempts != lingmaRecoveryMaxAttempts || delay != 5*time.Millisecond {
		t.Fatalf("clamped = (%d, %s), want (%d, 5ms)", attempts, delay, lingmaRecoveryMaxAttempts)
	}

	cfg.LingmaUpstreamRecovery.Disabled = true
	attempts, _ = lingmaRecoverySettings(cfg)
	if attempts != 1 {
		t.Fatalf("disabled attempts = %d, want 1", attempts)
	}
}

func TestLingmaExecuteRetries503AndRefreshesIdentity(t *testing.T) {
	executor := newLingmaRecoveryTestExecutor()
	var attempts [][]byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", lingmaTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		attempts = append(attempts, decodeLingmaTestRequest(t, req))
		if len(attempts) == 1 {
			return lingmaTestResponse(http.StatusServiceUnavailable, "busy"), nil
		}
		return lingmaTestResponse(http.StatusOK, lingmaOpenAIContentFrame("recovered")+lingmaTestDoneFrame), nil
	}))

	payload := []byte(`{"model":"gm51model","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	resp, err := executor.Execute(ctx, lingmaTestAuth(), cliproxyexecutor.Request{Model: "gm51model", Payload: payload}, cliproxyexecutor.Options{
		OriginalRequest: payload,
		SourceFormat:    sdktranslator.FormatOpenAI,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("Execute returned empty payload")
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(attempts))
	}
	firstRequestID := gjson.GetBytes(attempts[0], "request_id").String()
	secondRequestID := gjson.GetBytes(attempts[1], "request_id").String()
	if firstRequestID == "" || secondRequestID == "" || firstRequestID == secondRequestID {
		t.Fatalf("request IDs were not refreshed: first=%q second=%q", firstRequestID, secondRequestID)
	}
	if gjson.GetBytes(attempts[0], "business.id").String() == gjson.GetBytes(attempts[1], "business.id").String() {
		t.Fatal("business.id was not refreshed")
	}
	if beginAt := gjson.GetBytes(attempts[1], "business.begin_at").Int(); beginAt <= 0 {
		t.Fatalf("retry business.begin_at = %d, want positive timestamp", beginAt)
	}
	if gjson.GetBytes(attempts[0], "session_id").String() != gjson.GetBytes(attempts[1], "session_id").String() {
		t.Fatal("session_id changed across recovery attempts")
	}
	if !gjson.GetBytes(attempts[1], "is_retry").Bool() {
		t.Fatal("retry attempt did not set is_retry=true")
	}
}

func TestLingmaRetryAfterAndJitter(t *testing.T) {
	now := time.Date(2026, time.July, 12, 0, 0, 0, 0, time.UTC)
	if got := parseLingmaRetryAfter("3", now); got != 3*time.Second {
		t.Fatalf("delta Retry-After = %s, want 3s", got)
	}
	if got := parseLingmaRetryAfter(now.Add(5*time.Second).Format(http.TimeFormat), now); got != 5*time.Second {
		t.Fatalf("date Retry-After = %s, want 5s", got)
	}
	for i := 0; i < 32; i++ {
		delay := lingmaRetryDelay(200*time.Millisecond, 0, 0)
		if delay < 0 || delay > 200*time.Millisecond {
			t.Fatalf("jitter delay = %s, want [0, 200ms]", delay)
		}
	}
	if got := lingmaRetryDelay(200*time.Millisecond, 0, 750*time.Millisecond); got != 750*time.Millisecond {
		t.Fatalf("Retry-After delay = %s, want 750ms", got)
	}
	if got := lingmaRetryDelay(20*time.Second, 4, time.Minute); got != lingmaRecoveryMaxDelay {
		t.Fatalf("capped delay = %s, want %s", got, lingmaRecoveryMaxDelay)
	}
}

func TestLingmaRetryableSSEErrorClassification(t *testing.T) {
	for _, line := range []string{
		`data:{"body":"{\"error\":{\"type\":\"server_error\",\"message\":\"temporary\"}}"}`,
		`data:{"body":"{\"error\":{\"type\":\"rate_limit_error\",\"message\":\"temporary\"}}"}`,
		`data:{"body":"{\"error\":{\"code\":429,\"message\":\"temporary\"}}"}`,
	} {
		if err := lingmaRetryableSSEError([]byte(line)); err == nil {
			t.Fatalf("SSE error should be retryable: %s", line)
		}
	}
	for _, line := range []string{
		`data:{"body":"{\"error\":{\"type\":\"invalid_request_error\",\"message\":\"bad request\"}}"}`,
		`data:{"body":"{\"error\":{\"code\":400,\"message\":\"bad request\"}}"}`,
	} {
		if err := lingmaRetryableSSEError([]byte(line)); err != nil {
			t.Fatalf("SSE error should not be retryable: %s", line)
		}
	}
}

func TestLingmaExecuteStreamRetries503BeforeReturning(t *testing.T) {
	executor := newLingmaRecoveryTestExecutor()
	attempts := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", lingmaTestRoundTripper(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return lingmaTestResponse(http.StatusServiceUnavailable, "busy"), nil
		}
		return lingmaTestResponse(http.StatusOK, lingmaOpenAIContentFrame("recovered")+lingmaTestDoneFrame), nil
	}))

	result, err := executor.ExecuteStream(ctx, lingmaTestAuth(), lingmaOpenAIStreamRequest(), lingmaOpenAIStreamOptions())
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	chunks := collectLingmaTestChunks(t, result)
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	assertNoLingmaTestStreamError(t, chunks)
}

func TestLingmaExecuteRetriesTransportEOF(t *testing.T) {
	executor := newLingmaRecoveryTestExecutor()
	attempts := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", lingmaTestRoundTripper(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, io.EOF
		}
		return lingmaTestResponse(http.StatusOK, lingmaOpenAIContentFrame("recovered")+lingmaTestDoneFrame), nil
	}))

	payload := []byte(`{"model":"gm51model","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	_, err := executor.Execute(ctx, lingmaTestAuth(), cliproxyexecutor.Request{Model: "gm51model", Payload: payload}, cliproxyexecutor.Options{
		OriginalRequest: payload,
		SourceFormat:    sdktranslator.FormatOpenAI,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestLingmaExecuteRetriesIncompleteAggregate(t *testing.T) {
	executor := newLingmaRecoveryTestExecutor()
	attempts := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", lingmaTestRoundTripper(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return lingmaTestResponse(http.StatusOK, lingmaOpenAIContentFrame("incomplete")), nil
		}
		return lingmaTestResponse(http.StatusOK, lingmaOpenAIContentFrame("recovered")+lingmaTestDoneFrame), nil
	}))

	payload := []byte(`{"model":"gm51model","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	resp, err := executor.Execute(ctx, lingmaTestAuth(), cliproxyexecutor.Request{Model: "gm51model", Payload: payload}, cliproxyexecutor.Options{
		OriginalRequest: payload,
		SourceFormat:    sdktranslator.FormatOpenAI,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("Execute returned empty payload")
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestLingmaLargeThinkingRecoveryDisablesReasoning(t *testing.T) {
	executor := newLingmaRecoveryTestExecutor()
	executor.cfg.LingmaThinkingFallback.Enabled = true
	var attempts [][]byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", lingmaTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		attempts = append(attempts, decodeLingmaTestRequest(t, req))
		if len(attempts) == 1 {
			return lingmaTestResponse(http.StatusServiceUnavailable, "busy"), nil
		}
		return lingmaTestResponse(http.StatusOK, lingmaTestDoneFrame), nil
	}))

	payload := largeLingmaThinkingSourcePayload(t)
	result, err := executor.ExecuteStream(ctx, lingmaTestAuth(), cliproxyexecutor.Request{Model: "gm51model", Payload: payload}, cliproxyexecutor.Options{
		OriginalRequest: payload,
		SourceFormat:    sdktranslator.FormatOpenAI,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	assertNoLingmaTestStreamError(t, collectLingmaTestChunks(t, result))
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(attempts))
	}
	if got := gjson.GetBytes(attempts[1], "agent_id").String(); got != helpers.AgentCommon {
		t.Fatalf("retry agent_id = %q, want %q", got, helpers.AgentCommon)
	}
	if gjson.GetBytes(attempts[1], "model_config.is_reasoning").Bool() {
		t.Fatal("retry kept reasoning enabled")
	}
	if got := gjson.GetBytes(attempts[1], "model_config.source").String(); got != "" {
		t.Fatalf("retry model_config.source = %q, want empty", got)
	}
	if got := result.Headers.Get(cliproxyexecutor.FallbackHeaderName); got != lingmaThinkingFallbackHeaderValue {
		t.Fatalf("fallback header = %q, want %q", got, lingmaThinkingFallbackHeaderValue)
	}
}

func TestLingmaExecuteStreamRetriesEmptyOutputEOF(t *testing.T) {
	executor := newLingmaRecoveryTestExecutor()
	attempts := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", lingmaTestRoundTripper(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return lingmaTestResponse(http.StatusOK, `data:{"body":"{}"}`+"\n"), nil
		}
		return lingmaTestResponse(http.StatusOK, lingmaOpenAIContentFrame("recovered")+lingmaTestDoneFrame), nil
	}))

	result, err := executor.ExecuteStream(ctx, lingmaTestAuth(), lingmaOpenAIStreamRequest(), lingmaOpenAIStreamOptions())
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	chunks := collectLingmaTestChunks(t, result)
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	assertNoLingmaTestStreamError(t, chunks)
}

func TestLingmaExecuteStreamRetriesTransientSSEErrorBeforeOutput(t *testing.T) {
	executor := newLingmaRecoveryTestExecutor()
	attempts := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", lingmaTestRoundTripper(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return lingmaTestResponse(http.StatusOK, `data:{"body":"{\"error\":{\"type\":\"server_error\",\"message\":\"temporary\"}}"}`+"\n"), nil
		}
		return lingmaTestResponse(http.StatusOK, lingmaOpenAIContentFrame("recovered")+lingmaTestDoneFrame), nil
	}))

	result, err := executor.ExecuteStream(ctx, lingmaTestAuth(), lingmaOpenAIStreamRequest(), lingmaOpenAIStreamOptions())
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	chunks := collectLingmaTestChunks(t, result)
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	assertNoLingmaTestStreamError(t, chunks)
}

func TestLingmaExecuteStreamDoesNotRetryAfterOutput(t *testing.T) {
	executor := newLingmaRecoveryTestExecutor()
	attempts := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", lingmaTestRoundTripper(func(*http.Request) (*http.Response, error) {
		attempts++
		body := io.MultiReader(
			strings.NewReader(lingmaOpenAIContentFrame("partial")),
			iotest.ErrReader(io.ErrUnexpectedEOF),
		)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(body)}, nil
	}))

	result, err := executor.ExecuteStream(ctx, lingmaTestAuth(), lingmaOpenAIStreamRequest(), lingmaOpenAIStreamOptions())
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	chunks := collectLingmaTestChunks(t, result)
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	var sawPayload, sawError bool
	for _, chunk := range chunks {
		sawPayload = sawPayload || len(chunk.Payload) > 0
		sawError = sawError || chunk.Err != nil
	}
	if !sawPayload || !sawError {
		t.Fatalf("chunks = %#v, want partial payload followed by terminal error", chunks)
	}
}

func TestLingmaExecuteStreamDoneIgnoresTrailingReadError(t *testing.T) {
	executor := newLingmaRecoveryTestExecutor()
	attempts := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", lingmaTestRoundTripper(func(*http.Request) (*http.Response, error) {
		attempts++
		body := io.MultiReader(
			strings.NewReader(lingmaOpenAIContentFrame("complete")+lingmaTestDoneFrame),
			iotest.ErrReader(io.ErrUnexpectedEOF),
		)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(body)}, nil
	}))

	result, err := executor.ExecuteStream(ctx, lingmaTestAuth(), lingmaOpenAIStreamRequest(), lingmaOpenAIStreamOptions())
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	chunks := collectLingmaTestChunks(t, result)
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	assertNoLingmaTestStreamError(t, chunks)
}

func TestLingmaExecuteStreamParentCancellationDoesNotRetry(t *testing.T) {
	executor := newLingmaRecoveryTestExecutor()
	attempts := 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", lingmaTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		attempts++
		return nil, req.Context().Err()
	}))

	_, err := executor.ExecuteStream(ctx, lingmaTestAuth(), lingmaOpenAIStreamRequest(), lingmaOpenAIStreamOptions())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecuteStream error = %v, want context canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestLingmaExecuteStreamRecoveryExhaustionReturns502(t *testing.T) {
	executor := newLingmaRecoveryTestExecutor()
	attempts := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", lingmaTestRoundTripper(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, io.EOF
	}))

	_, err := executor.ExecuteStream(ctx, lingmaTestAuth(), lingmaOpenAIStreamRequest(), lingmaOpenAIStreamOptions())
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("error = %T %v, want status %d", err, err, http.StatusBadGateway)
	}
	if attempts != lingmaRecoveryDefaultAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, lingmaRecoveryDefaultAttempts)
	}
}

func newLingmaRecoveryTestExecutor() *LingmaExecutor {
	cfg := &config.Config{}
	cfg.LingmaUpstreamRecovery.MaxAttempts = 3
	cfg.LingmaUpstreamRecovery.BaseDelay = "1ns"
	return NewLingmaExecutor(cfg)
}

func lingmaTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "lingma",
		Metadata: map[string]any{
			"uid": "test-user",
			"key": "test-key",
		},
	}
}

func lingmaOpenAIStreamRequest() cliproxyexecutor.Request {
	payload := []byte(`{"model":"gm51model","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	return cliproxyexecutor.Request{Model: "gm51model", Payload: payload}
}

func lingmaOpenAIStreamOptions() cliproxyexecutor.Options {
	request := lingmaOpenAIStreamRequest()
	return cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: request.Payload, Stream: true}
}

func lingmaTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func lingmaOpenAIContentFrame(content string) string {
	return `data:{"body":"{\"id\":\"chatcmpl-test\",\"model\":\"gm51model\",\"choices\":[{\"delta\":{\"content\":\"` + content + `\"},\"finish_reason\":null}],\"usage\":null}"}` + "\n"
}

func decodeLingmaTestRequest(t *testing.T, req *http.Request) []byte {
	t.Helper()
	encoded, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read encoded request: %v", err)
	}
	decoded, err := lingmaencoding.Decode(string(encoded))
	if err != nil {
		t.Fatalf("decode Lingma request: %v", err)
	}
	return decoded
}

func collectLingmaTestChunks(t *testing.T, result *cliproxyexecutor.StreamResult) []cliproxyexecutor.StreamChunk {
	t.Helper()
	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range result.Chunks {
		chunks = append(chunks, chunk)
	}
	return chunks
}

func assertNoLingmaTestStreamError(t *testing.T, chunks []cliproxyexecutor.StreamChunk) {
	t.Helper()
	for _, chunk := range chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
	}
}

func executeCapturedLingmaStream(t *testing.T, executor *LingmaExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([]byte, http.Header) {
	t.Helper()
	var captured []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", lingmaTestRoundTripper(func(httpReq *http.Request) (*http.Response, error) {
		encoded, errRead := io.ReadAll(httpReq.Body)
		if errRead != nil {
			t.Fatalf("read encoded request: %v", errRead)
		}
		decoded, errDecode := lingmaencoding.Decode(string(encoded))
		if errDecode != nil {
			t.Fatalf("decode Lingma request: %v", errDecode)
		}
		captured = decoded
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`data:{"body":"[DONE]"}` + "\n")),
		}, nil
	}))
	result, err := executor.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range result.Chunks {
	}
	return captured, result.Headers
}

func largeLingmaThinkingSourcePayload(t *testing.T) []byte {
	t.Helper()
	messages := make([]map[string]any, 0, 21)
	for i := 0; i < 10; i++ {
		messages = append(messages,
			map[string]any{"role": "assistant", "tool_calls": []map[string]any{{"id": "call", "type": "function", "function": map[string]any{"name": "tool", "arguments": `{}`}}}},
			map[string]any{"role": "tool", "tool_call_id": "call", "content": "ok"},
		)
	}
	messages = append(messages, map[string]any{"role": "user", "content": strings.Repeat("x", lingmaLargeThinkingBodyWarningBytes)})
	payload, err := json.Marshal(map[string]any{
		"model":            "gm51model",
		"stream":           true,
		"reasoning_effort": "medium",
		"messages":         messages,
		"tools":            []map[string]any{{"type": "function", "function": map[string]any{"name": "tool"}}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return payload
}

func translatedLargeLingmaThinkingBody(t *testing.T) []byte {
	t.Helper()
	payload := largeLingmaThinkingSourcePayload(t)
	body := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAI, sdktranslator.FromString("lingma"), "gm51model", payload, true)
	body, _ = thinking.ApplyThinking(body, "gm51model", sdktranslator.FormatOpenAI.String(), "lingma", "lingma")
	return body
}

func TestLingmaExecuteStreamUnexpectedEOFDoesNotEmitDone(t *testing.T) {
	assertLingmaStreamBodyError(t, io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF)), "lingma upstream connection closed unexpectedly")
}

func TestLingmaExecuteStreamEmptyEOFDoesNotEmitDone(t *testing.T) {
	assertLingmaStreamBodyError(t, http.NoBody, "lingma upstream connection closed before response data")
}

func assertLingmaStreamBodyError(t *testing.T, body io.ReadCloser, wantMessage string) {
	t.Helper()
	cfg := &config.Config{}
	cfg.LingmaUpstreamRecovery.Disabled = true
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", lingmaTestRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
		}, nil
	}))
	auth := &cliproxyauth.Auth{
		Provider: "lingma",
		Metadata: map[string]any{
			"uid": "test-user",
			"key": "test-key",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gm51model",
		Payload: []byte(`{"model":"gm51model","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: req.Payload, Stream: true}

	result, err := NewLingmaExecutor(cfg).ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range result.Chunks {
		chunks = append(chunks, chunk)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %#v, want one terminal error chunk", chunks)
	}
	if chunks[0].Err == nil {
		t.Fatalf("chunk = %#v, want terminal error", chunks[0])
	}
	status, ok := chunks[0].Err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("error = %T %v, want status %d", chunks[0].Err, chunks[0].Err, http.StatusBadGateway)
	}
	if chunks[0].Err.Error() != wantMessage {
		t.Fatalf("message = %q, want %q", chunks[0].Err.Error(), wantMessage)
	}
	if len(chunks[0].Payload) != 0 {
		t.Fatalf("unexpected payload before terminal error: %q", chunks[0].Payload)
	}
}
