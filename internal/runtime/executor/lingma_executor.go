package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/lingma"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	lingmaencoding "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/lingma"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/lingma/helpers"
)

const (
	lingmaModelListURL                    = "https://lingma-api.tongyi.aliyun.com/algo/api/v2/model/list"
	lingmaLargeThinkingBodyWarningBytes   = 128 * 1024
	lingmaLargeThinkingToolHistoryWarning = 20
	lingmaThinkingFallbackDefaultTTL      = 2 * time.Minute
	lingmaThinkingFallbackMaxEntries      = 1024
	lingmaThinkingFallbackHeaderValue     = "lingma-thinking-disabled"
)

type lingmaRequestProfile struct {
	BodyBytes     int
	Messages      int
	ToolCalls     int
	ToolResults   int
	Tools         int
	LargeThinking bool
}

type lingmaThinkingFallbackDecision struct {
	Key      string
	Eligible bool
	Applied  bool
}

func inspectLingmaRequest(body []byte, modelName string) lingmaRequestProfile {
	profile := lingmaRequestProfile{BodyBytes: len(body)}
	if !strings.EqualFold(strings.TrimSpace(modelName), "gm51model") || len(body) == 0 || !gjson.ValidBytes(body) {
		return profile
	}

	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		profile.Messages = len(messages.Array())
		messages.ForEach(func(_, message gjson.Result) bool {
			if toolCalls := message.Get("tool_calls"); toolCalls.IsArray() {
				profile.ToolCalls += len(toolCalls.Array())
			}
			if strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "tool") {
				profile.ToolResults++
			}
			return true
		})
	}
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() {
		profile.Tools = len(tools.Array())
	}

	toolHistory := profile.ToolCalls + profile.ToolResults
	profile.LargeThinking = gjson.GetBytes(body, "model_config.is_reasoning").Bool() &&
		profile.BodyBytes >= lingmaLargeThinkingBodyWarningBytes &&
		toolHistory >= lingmaLargeThinkingToolHistoryWarning
	return profile
}

func warnLingmaLargeThinkingRequest(profile lingmaRequestProfile, modelName string) {
	if !profile.LargeThinking {
		return
	}
	log.WithFields(log.Fields{
		"provider": "lingma",
		"model":    modelName,
	}).Warnf(
		"lingma large thinking request may stall upstream body_bytes=%d messages=%d tool_calls=%d tool_results=%d tools=%d; consider reducing tool history or disabling reasoning",
		profile.BodyBytes,
		profile.Messages,
		profile.ToolCalls,
		profile.ToolResults,
		profile.Tools,
	)
}

func normalizeLingmaUpstreamError(err error, profile lingmaRequestProfile) error {
	if err == nil {
		return nil
	}

	var timeoutErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeoutErr) && timeoutErr.Timeout()) {
		message := "lingma upstream timeout while waiting for response data"
		if profile.LargeThinking {
			message += "; gm51model thinking may stall on large tool-call histories, reduce the history or set reasoning_effort to \"none\""
		}
		return statusErr{code: http.StatusGatewayTimeout, msg: message}
	}

	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || strings.TrimSpace(err.Error()) == "unexpected EOF" {
		return statusErr{code: http.StatusBadGateway, msg: "lingma upstream connection closed unexpectedly"}
	}
	return err
}

func lingmaThinkingFallbackKey(modelName, sourceFormat string, payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(strings.ToLower(strings.TrimSpace(modelName))))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strings.ToLower(strings.TrimSpace(sourceFormat))))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(payload)
	return hex.EncodeToString(hash.Sum(nil))
}

func disableLingmaThinking(body []byte) []byte {
	result, err := sjson.SetBytes(body, "model_config.is_reasoning", false)
	if err != nil {
		return body
	}
	result, err = sjson.SetBytes(result, "model_config.source", "")
	if err != nil {
		return body
	}
	result, err = sjson.SetBytes(result, "agent_id", helpers.AgentCommon)
	if err != nil {
		return body
	}
	return result
}

// lingmaChatURLForAgent builds the upstream SSE endpoint URL for the given
// agent_id. The URL's AgentId query param must match the body's agent_id, so
// callers resolve the final agent_id from the translated body (which may flip
// to agent_common when reasoning is disabled) and pass it here.
func lingmaChatURLForAgent(agentID string) string {
	return fmt.Sprintf("https://lingma-api.tongyi.aliyun.com/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=%s&Encode=1", agentID)
}

// lingmaAgentIDFromBody extracts the final agent_id from a translated Lingma
// request body. ApplyThinking and preserveLingmaClaudeCodeThinking only touch
// model_config.is_reasoning, so the agent_id set by the translator is the final
// value. Falls back to the model-name-derived agent_id if the body is missing
// the field (defensive; should not happen in practice).
func lingmaAgentIDFromBody(body []byte, modelName string) string {
	if v := gjson.GetBytes(body, "agent_id"); v.Exists() && v.String() != "" {
		return v.String()
	}
	return helpers.AgentID(modelName)
}

// newLingmaHTTPClient creates an HTTP client for Lingma, forcing HTTP/1.1 by
// default to avoid mid-stream HTTP/2 RST_STREAM errors from the upstream
// surfacing as raw "stream error: stream ID N; INTERNAL_ERROR" messages to
// clients. Set cfg.DisableHTTP11=true to opt out and negotiate HTTP/2 via ALPN
// as before.
func newLingmaHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	forceHTTP11 := cfg == nil || !cfg.DisableHTTP11
	return helps.NewHTTP11Client(ctx, cfg, auth, timeout, forceHTTP11)
}

// LingmaExecutor executes Lingma requests and owns bounded one-shot fallback state.
type LingmaExecutor struct {
	cfg              *config.Config
	thinkingFallback *helps.OneShotTTLSet
}

// NewLingmaExecutor creates a new Lingma executor.
func NewLingmaExecutor(cfg *config.Config) *LingmaExecutor {
	return &LingmaExecutor{
		cfg:              cfg,
		thinkingFallback: helps.NewOneShotTTLSet(lingmaThinkingFallbackMaxEntries),
	}
}

func (e *LingmaExecutor) applyThinkingFallback(body, sourcePayload []byte, modelName, sourceFormat string, profile lingmaRequestProfile) ([]byte, lingmaThinkingFallbackDecision) {
	decision := lingmaThinkingFallbackDecision{}
	if e == nil || e.cfg == nil || !e.cfg.LingmaThinkingFallback.Enabled || e.thinkingFallback == nil || !profile.LargeThinking {
		return body, decision
	}
	decision.Key = lingmaThinkingFallbackKey(modelName, sourceFormat, sourcePayload)
	decision.Eligible = decision.Key != ""
	if !decision.Eligible || !e.thinkingFallback.Consume(decision.Key) {
		return body, decision
	}
	decision.Applied = true
	log.WithFields(log.Fields{
		"provider": "lingma",
		"model":    modelName,
	}).Warn("lingma one-shot fallback applied; reasoning disabled for retried request")
	return disableLingmaThinking(body), decision
}

func (e *LingmaExecutor) rememberThinkingFallback(err error, decision lingmaThinkingFallbackDecision, profile lingmaRequestProfile, modelName string) {
	if e == nil || e.cfg == nil || !e.cfg.LingmaThinkingFallback.Enabled || e.thinkingFallback == nil ||
		!decision.Eligible || decision.Applied || !profile.LargeThinking || !errors.Is(err, context.Canceled) {
		return
	}
	ttl := lingmaThinkingFallbackDefaultTTL
	if configured := strings.TrimSpace(e.cfg.LingmaThinkingFallback.TTL); configured != "" {
		if parsed, errParse := time.ParseDuration(configured); errParse == nil && parsed > 0 {
			ttl = parsed
		}
	}
	if !e.thinkingFallback.Mark(decision.Key, ttl) {
		return
	}
	log.WithFields(log.Fields{
		"provider": "lingma",
		"model":    modelName,
	}).Warnf("lingma thinking fallback armed after pre-response cancellation ttl=%s fingerprint=%s", ttl, decision.Key[:12])
}

func lingmaResponseHeaders(headers http.Header, fallbackApplied bool) http.Header {
	result := headers.Clone()
	if fallbackApplied {
		if result == nil {
			result = make(http.Header)
		}
		result.Set(cliproxyexecutor.FallbackHeaderName, lingmaThinkingFallbackHeaderValue)
	}
	return result
}

// Identifier returns the executor identifier.
func (e *LingmaExecutor) Identifier() string { return "lingma" }

// PrepareRequest injects Lingma credentials into the outgoing HTTP request.
// Note: For Lingma, most headers are payload-dependent and are handled in the Execute methods.
func (e *LingmaExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil || auth == nil {
		return nil
	}
	util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	return nil
}

// HttpRequest executes a raw HTTP request with Lingma credentials.
func (e *LingmaExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("lingma executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := newLingmaHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming chat completion request to Lingma.
func (e *LingmaExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.SourceFormat == sdktranslator.Format(constant.OpenaiResponse) {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "lingma: openai-response format is not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	creds, err := e.getLingmaCreds(auth)
	if err != nil {
		return resp, err
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("lingma")

	// Lingma's chat endpoint is SSE-only; even non-streaming OpenAI requests are
	// sent upstream as a stream and aggregated by the response translator.
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	body, _ = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	body = preserveLingmaClaudeCodeThinking(body, req.Payload, from.String())
	profile := inspectLingmaRequest(body, baseModel)
	body, fallback := e.applyThinkingFallback(body, req.Payload, baseModel, from.String(), profile)
	if !fallback.Applied {
		warnLingmaLargeThinkingRequest(profile, baseModel)
	}

	// Build the URL from the final body's agent_id so the URL's AgentId query
	// param matches the body's agent_id (the translator may flip to agent_common
	// when reasoning is disabled).
	chatURL := lingmaChatURLForAgent(lingmaAgentIDFromBody(body, baseModel))

	// Final encoding after thinking application
	encodedBody := lingmaencoding.Encode(body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, strings.NewReader(encodedBody))
	if err != nil {
		return resp, err
	}

	headers, err := lingma.BuildHeaders(creds, encodedBody, chatURL)
	if err != nil {
		return resp, err
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}
	e.PrepareRequest(httpReq, auth)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       chatURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newLingmaHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		e.rememberThinkingFallback(err, fallback, profile, baseModel)
		return resp, normalizeLingmaUpstreamError(err, profile)
	}
	defer httpResp.Body.Close()

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return resp, fmt.Errorf("lingma: failed to read error response body: %w", err)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		return resp, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		if len(data) == 0 {
			e.rememberThinkingFallback(err, fallback, profile, baseModel)
		}
		return resp, normalizeLingmaUpstreamError(err, profile)
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, body, data, &param)
	// Extract usage from raw Lingma SSE data (not from translated output) to preserve
	// Lingma-format input_tokens which includes cached tokens, matching cpa-usage-keeper's
	// cost formula: promptTokens = inputTokens - cachedTokens.
	if detail, ok := helps.ParseLingmaStreamUsageFromAggregate(data); ok {
		reporter.PublishNonZero(ctx, detail)
	} else {
		detail := helps.ParseUsageForFormat(from.String(), out)
		// The translator (extractOpenAIUsage) already subtracted cached tokens
		// from Claude InputTokens. Restore them so cpa-usage-keeper's formula
		// promptTokens = inputTokens - cachedTokens works correctly.
		if shouldRestoreLingmaCachedTokens(from.String()) && detail.CachedTokens > 0 {
			detail.InputTokens += detail.CachedTokens
		}
		reporter.PublishNonZero(ctx, detail)
	}
	reporter.EnsurePublished(ctx)
	return cliproxyexecutor.Response{Payload: out, Headers: lingmaResponseHeaders(httpResp.Header, fallback.Applied)}, nil
}

// ExecuteStream performs a streaming chat completion request to Lingma.
func (e *LingmaExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.SourceFormat == sdktranslator.Format(constant.OpenaiResponse) {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "lingma: openai-response format is not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	creds, err := e.getLingmaCreds(auth)
	if err != nil {
		return nil, err
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("lingma")

	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	body, _ = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	body = preserveLingmaClaudeCodeThinking(body, req.Payload, from.String())
	profile := inspectLingmaRequest(body, baseModel)
	body, fallback := e.applyThinkingFallback(body, req.Payload, baseModel, from.String(), profile)
	if !fallback.Applied {
		warnLingmaLargeThinkingRequest(profile, baseModel)
	}

	// Build the URL from the final body's agent_id so the URL's AgentId query
	// param matches the body's agent_id (the translator may flip to agent_common
	// when reasoning is disabled).
	chatURL := lingmaChatURLForAgent(lingmaAgentIDFromBody(body, baseModel))

	// Final encoding after thinking application
	encodedBody := lingmaencoding.Encode(body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, strings.NewReader(encodedBody))
	if err != nil {
		return nil, err
	}

	headers, err := lingma.BuildHeaders(creds, encodedBody, chatURL)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}
	e.PrepareRequest(httpReq, auth)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       chatURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newLingmaHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		e.rememberThinkingFallback(err, fallback, profile, baseModel)
		return nil, normalizeLingmaUpstreamError(err, profile)
	}

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer httpResp.Body.Close()

		scanner := bufio.NewScanner(httpResp.Body)
		// Lingma responses can be large
		scanner.Buffer(nil, 5*1024*1024)

		var param any
		sawUpstreamData := false
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			sawUpstreamData = true
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseLingmaStreamUsage(line); ok {
				reporter.PublishNonZero(ctx, detail)
			}

			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, bytes.Clone(line), &param)
			for i := range chunks {
				if detail, ok := helps.ParseStreamUsageForFormat(from.String(), chunks[i]); ok {
					reporter.PublishNonZero(ctx, detail)
				}
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}

		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			if !sawUpstreamData {
				e.rememberThinkingFallback(errScan, fallback, profile, baseModel)
			}
			streamErr := normalizeLingmaUpstreamError(errScan, profile)
			reporter.PublishFailure(ctx, streamErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			return
		}
		if !sawUpstreamData {
			streamErr := statusErr{code: http.StatusBadGateway, msg: "lingma upstream connection closed before response data"}
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			reporter.PublishFailure(ctx, streamErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			return
		}

		doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, []byte("[DONE]"), &param)
		for i := range doneChunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: doneChunks[i]}:
			case <-ctx.Done():
				return
			}
		}
		reporter.EnsurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{Headers: lingmaResponseHeaders(httpResp.Header, fallback.Applied), Chunks: out}, nil
}

// CountTokens returns an approximate token count for Lingma requests.
// It converts the source request to OpenAI Chat format and uses a local tokenizer.
func (e *LingmaExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if opts.SourceFormat == "" {
		return cliproxyexecutor.Response{}, fmt.Errorf("lingma executor: source format is required")
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.Format(constant.OpenAI)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	enc, err := helps.TokenizerForModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("lingma executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("lingma executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

// Refresh triggers a credential refresh for Lingma.
func (e *LingmaExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	// Background refresh is handled by LingmaAuthenticator.Login during auto-refresh loop.
	// Here we can force a refresh if needed.
	creds, err := e.getLingmaCreds(auth)
	if err != nil {
		return auth, err
	}
	if err := lingma.ExchangeToken(creds); err != nil {
		return auth, err
	}
	// Update auth metadata
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["key"] = creds.CosyKey
	auth.Metadata["organization_id"] = creds.OrganizationID
	auth.UpdatedAt = time.Now()
	return auth, nil
}

// FetchModels fetches the available models from the Lingma API.
func (e *LingmaExecutor) FetchModels(ctx context.Context, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	creds, err := e.getLingmaCreds(auth)
	if err != nil {
		return nil, err
	}

	headers, err := lingma.BuildHeaders(creds, "", lingmaModelListURL)
	if err != nil {
		return nil, err
	}

	httpReq, _ := http.NewRequestWithContext(ctx, "GET", lingmaModelListURL, nil)
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("model list API error (%d): %s", resp.StatusCode, string(b))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return parseLingmaModels(data, time.Now().Unix()), nil
}

func parseLingmaModels(data []byte, now int64) []*registry.ModelInfo {
	root := gjson.ParseBytes(data)
	roots := []gjson.Result{root}
	if wrapped := root.Get("data"); wrapped.Exists() {
		roots = append(roots, wrapped)
	}
	if wrapped := root.Get("result"); wrapped.Exists() {
		roots = append(roots, wrapped)
	}

	models := make([]*registry.ModelInfo, 0)
	seen := make(map[string]struct{})
	appendModel := func(key, value gjson.Result) {
		modelID := firstLingmaModelString(value, "key", "modelId", "model_id", "modelName", "model_name", "id", "name")
		if modelID == "" && key.Type == gjson.String {
			modelID = key.String()
		}
		if modelID == "" {
			return
		}
		dedupeKey := strings.ToLower(modelID)
		if _, ok := seen[dedupeKey]; ok {
			return
		}
		seen[dedupeKey] = struct{}{}

		displayName := firstLingmaModelString(value, "display_name", "displayName", "modelName", "model_name", "name", "label")
		if displayName == "" {
			displayName = modelID
		}
		models = append(models, &registry.ModelInfo{
			ID:          modelID,
			Object:      "model",
			Created:     now,
			OwnedBy:     "alibaba",
			Type:        "lingma",
			DisplayName: displayName,
		})
	}

	for _, candidate := range roots {
		if candidate.IsArray() {
			candidate.ForEach(func(key, value gjson.Result) bool {
				appendModel(key, value)
				return true
			})
		}
		for _, cat := range []string{"chat", "developer", "assistant", "inline"} {
			group := candidate.Get(cat)
			if !group.Exists() {
				continue
			}
			group.ForEach(func(key, value gjson.Result) bool {
				appendModel(key, value)
				return true
			})
		}
	}

	return models
}

func preserveLingmaClaudeCodeThinking(body, source []byte, sourceFormat string) []byte {
	if !strings.EqualFold(strings.TrimSpace(sourceFormat), constant.Claude) {
		return body
	}
	if len(body) == 0 || !gjson.ValidBytes(body) || len(source) == 0 || !gjson.ValidBytes(source) {
		return body
	}

	enabled, ok := claudeCodeThinkingEnabled(source)
	if !ok {
		return body
	}
	result, err := sjson.SetBytes(body, "model_config.is_reasoning", enabled)
	if err != nil {
		return body
	}
	return result
}

func shouldRestoreLingmaCachedTokens(sourceFormat string) bool {
	return strings.EqualFold(strings.TrimSpace(sourceFormat), constant.Claude)
}

func claudeCodeThinkingEnabled(source []byte) (bool, bool) {
	if effort := gjson.GetBytes(source, "output_config.effort"); effort.Exists() && effort.Type == gjson.String {
		switch strings.ToLower(strings.TrimSpace(effort.String())) {
		case "none":
			return false, true
		case "off", "disabled":
			return false, true
		case "":
			// Empty effort is not a meaningful Claude Code thinking signal.
		default:
			return true, true
		}
	}

	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(source, "thinking.type").String()))
	switch thinkingType {
	case "disabled":
		return false, true
	case "enabled":
		if budget := gjson.GetBytes(source, "thinking.budget_tokens"); budget.Exists() && budget.Int() == 0 {
			return false, true
		}
		return true, true
	case "adaptive", "auto":
		return true, true
	default:
		return false, false
	}
}

func firstLingmaModelString(value gjson.Result, keys ...string) string {
	for _, key := range keys {
		if v := value.Get(key); v.Exists() {
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		}
	}
	return ""
}

func (e *LingmaExecutor) getLingmaCreds(auth *cliproxyauth.Auth) (*lingma.Credentials, error) {
	if auth == nil || auth.Metadata == nil {
		return nil, fmt.Errorf("lingma executor: missing auth metadata")
	}

	getString := func(key string) string {
		if v, ok := auth.Metadata[key].(string); ok {
			return v
		}
		return ""
	}

	creds := &lingma.Credentials{
		MachineID:          getString("machine_id"),
		UID:                getString("uid"),
		OrganizationID:     getString("organization_id"),
		CosyKey:            getString("key"),
		EncryptUserInfo:    getString("encrypt_user_info"),
		UserType:           getString("user_type"),
		SecurityOAuthToken: getString("security_oauth_token"),
	}

	if creds.UID == "" || creds.CosyKey == "" {
		return nil, fmt.Errorf("lingma executor: incomplete credentials in metadata")
	}

	return creds, nil
}
