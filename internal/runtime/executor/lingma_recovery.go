package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	rand "math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/lingma"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	lingmahelpers "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/lingma/helpers"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	lingmaencoding "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/lingma"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	lingmaRecoveryDefaultAttempts = 3
	lingmaRecoveryMaxAttempts     = 5
	lingmaRecoveryDefaultDelay    = 200 * time.Millisecond
	lingmaRecoveryMaxDelay        = 30 * time.Second
)

type lingmaUpstreamPlan struct {
	ctx              context.Context
	executor         *LingmaExecutor
	auth             *cliproxyauth.Auth
	creds            *lingma.Credentials
	client           *http.Client
	baseBody         []byte
	model            string
	nextAttempt      int
	maxAttempts      int
	baseDelay        time.Duration
	nextRetryAfter   time.Duration
	fallbackApplied  bool
	recoveryEligible bool
}

func newLingmaUpstreamPlan(
	ctx context.Context,
	executor *LingmaExecutor,
	auth *cliproxyauth.Auth,
	creds *lingma.Credentials,
	client *http.Client,
	body []byte,
	model string,
	profile lingmaRequestProfile,
	fallback lingmaThinkingFallbackDecision,
) *lingmaUpstreamPlan {
	maxAttempts, baseDelay := lingmaRecoverySettings(executor.cfg)
	recoveryEligible := executor.cfg != nil && executor.cfg.LingmaThinkingFallback.Enabled && profile.LargeThinking && !fallback.Applied
	return &lingmaUpstreamPlan{
		ctx:              ctx,
		executor:         executor,
		auth:             auth,
		creds:            creds,
		client:           client,
		baseBody:         bytes.Clone(body),
		model:            model,
		maxAttempts:      maxAttempts,
		baseDelay:        baseDelay,
		fallbackApplied:  fallback.Applied,
		recoveryEligible: recoveryEligible,
	}
}

func lingmaRecoverySettings(cfg *config.Config) (int, time.Duration) {
	if cfg != nil && cfg.LingmaUpstreamRecovery.Disabled {
		return 1, lingmaRecoveryDefaultDelay
	}

	attempts := lingmaRecoveryDefaultAttempts
	if cfg != nil && cfg.LingmaUpstreamRecovery.MaxAttempts != 0 {
		attempts = cfg.LingmaUpstreamRecovery.MaxAttempts
	}
	if attempts < 1 {
		attempts = 1
	}
	if attempts > lingmaRecoveryMaxAttempts {
		attempts = lingmaRecoveryMaxAttempts
	}

	delay := lingmaRecoveryDefaultDelay
	if cfg != nil && strings.TrimSpace(cfg.LingmaUpstreamRecovery.BaseDelay) != "" {
		if parsed, err := time.ParseDuration(strings.TrimSpace(cfg.LingmaUpstreamRecovery.BaseDelay)); err == nil && parsed > 0 {
			delay = parsed
		}
	}
	return attempts, delay
}

func prepareLingmaAttemptBody(base []byte, attempt int, disableThinking bool) []byte {
	body := bytes.Clone(base)
	if attempt > 0 {
		requestID := uuid.NewString()
		updates := []struct {
			path  string
			value any
		}{
			{path: "request_id", value: requestID},
			{path: "chat_record_id", value: requestID},
			{path: "business.id", value: uuid.NewString()},
			{path: "business.begin_at", value: time.Now().UnixMilli()},
			{path: "is_retry", value: true},
		}
		for _, update := range updates {
			updated, err := sjson.SetBytes(body, update.path, update.value)
			if err != nil {
				return bytes.Clone(base)
			}
			body = updated
		}
	}
	if disableThinking {
		body = disableLingmaThinking(body)
	}
	return body
}

func (p *lingmaUpstreamPlan) open() (*http.Response, []byte, error) {
	for p.nextAttempt < p.maxAttempts {
		attempt := p.nextAttempt
		p.nextAttempt++
		if attempt > 0 {
			if err := lingmaRecoveryWait(p.ctx, lingmaRetryDelay(p.baseDelay, attempt-1, p.nextRetryAfter)); err != nil {
				return nil, nil, err
			}
			p.nextRetryAfter = 0
		}

		disableThinking := attempt > 0 && p.recoveryEligible
		body := prepareLingmaAttemptBody(p.baseBody, attempt, disableThinking)
		if disableThinking && !p.fallbackApplied {
			log.WithFields(log.Fields{
				"provider":     "lingma",
				"model":        p.model,
				"attempt":      attempt + 1,
				"max_attempts": p.maxAttempts,
			}).Warn("lingma in-request recovery applied; reasoning disabled")
			p.fallbackApplied = true
		}

		resp, err := p.do(body)
		if err != nil {
			helps.RecordAPIResponseError(p.ctx, p.executor.cfg, err)
			if p.canRetryTransport(err) {
				p.logRetry(attempt, 0, err)
				continue
			}
			return nil, body, err
		}

		helps.RecordAPIResponseMetadata(p.ctx, p.executor.cfg, resp.StatusCode, resp.Header.Clone())
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, body, nil
		}

		responseBody, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithFields(log.Fields{"provider": "lingma", "model": p.model}).Debugf("failed to close Lingma retry response: %v", errClose)
		}
		helps.AppendAPIResponseChunk(p.ctx, p.executor.cfg, responseBody)
		if lingmaShouldRetryStatus(resp.StatusCode) && p.hasNext() && p.ctx.Err() == nil {
			p.nextRetryAfter = parseLingmaRetryAfter(resp.Header.Get("Retry-After"), time.Now())
			p.logRetry(attempt, resp.StatusCode, errRead)
			continue
		}
		if errRead != nil {
			return nil, body, fmt.Errorf("lingma: failed to read error response body: %w", errRead)
		}
		return nil, body, statusErr{code: resp.StatusCode, msg: string(responseBody)}
	}
	return nil, nil, statusErr{code: http.StatusBadGateway, msg: "lingma upstream recovery exhausted"}
}

func (p *lingmaUpstreamPlan) do(body []byte) (*http.Response, error) {
	chatURL := lingmaChatURLForAgent(lingmaAgentIDFromBody(body, p.model))
	encodedBody := lingmaencoding.Encode(body)
	httpReq, err := http.NewRequestWithContext(p.ctx, http.MethodPost, chatURL, strings.NewReader(encodedBody))
	if err != nil {
		return nil, err
	}
	headers, err := lingma.BuildHeaders(p.creds, encodedBody, chatURL)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		httpReq.Header.Set(key, value)
	}
	if err = p.executor.PrepareRequest(httpReq, p.auth); err != nil {
		return nil, err
	}

	var authID, authLabel, authType, authValue string
	if p.auth != nil {
		authID = p.auth.ID
		authLabel = p.auth.Label
		authType, authValue = p.auth.AccountInfo()
	}
	helps.RecordAPIRequest(p.ctx, p.executor.cfg, helps.UpstreamRequestLog{
		URL:       chatURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  p.executor.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	return p.client.Do(httpReq)
}

func (p *lingmaUpstreamPlan) hasNext() bool {
	return p.nextAttempt < p.maxAttempts
}

func (p *lingmaUpstreamPlan) canRetryTransport(err error) bool {
	return p.hasNext() && p.ctx.Err() == nil && lingmaShouldRetryTransportError(err)
}

func (p *lingmaUpstreamPlan) canRetryIncomplete() bool {
	return p.hasNext() && p.ctx.Err() == nil
}

func (p *lingmaUpstreamPlan) logRetry(attempt, status int, err error) {
	fields := log.Fields{
		"provider":     "lingma",
		"model":        p.model,
		"attempt":      attempt + 1,
		"max_attempts": p.maxAttempts,
	}
	if status != 0 {
		fields["status"] = status
	}
	entry := log.WithFields(fields)
	if err != nil {
		entry.WithError(err).Warn("retrying transient Lingma upstream failure")
		return
	}
	entry.Warn("retrying transient Lingma upstream failure")
}

func lingmaRecoveryWait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func lingmaRetryDelay(baseDelay time.Duration, retryIndex int, retryAfter time.Duration) time.Duration {
	maxDelay := baseDelay
	if maxDelay > lingmaRecoveryMaxDelay {
		maxDelay = lingmaRecoveryMaxDelay
	}
	for i := 0; i < retryIndex && maxDelay < lingmaRecoveryMaxDelay; i++ {
		if maxDelay > lingmaRecoveryMaxDelay/2 {
			maxDelay = lingmaRecoveryMaxDelay
			break
		}
		maxDelay *= 2
	}
	delay := time.Duration(0)
	if maxDelay > 0 {
		delay = time.Duration(rand.Int64N(int64(maxDelay) + 1))
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay > lingmaRecoveryMaxDelay {
		return lingmaRecoveryMaxDelay
	}
	return delay
}

func parseLingmaRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func lingmaShouldRetryStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout,
		http.StatusTooEarly,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func lingmaShouldRetryTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"unexpected eof",
		"connection reset",
		"broken pipe",
		"server closed idle connection",
		"rst_stream",
		"stream error:",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func lingmaAggregateHasDone(data []byte) bool {
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if lingmahelpers.IsLingmaDone(line) {
			return true
		}
	}
	return false
}

func lingmaRetryableSSEError(raw []byte) error {
	payload := bytes.TrimSpace(raw)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(bytes.TrimPrefix(payload, []byte("data:")))
	}
	root := gjson.ParseBytes(payload)
	if body := root.Get("body"); body.Exists() && body.Type == gjson.String {
		root = gjson.Parse(body.String())
	}
	errorNode := root.Get("error")
	if !errorNode.Exists() {
		return nil
	}
	status := int(errorNode.Get("status").Int())
	if status == 0 {
		status = int(errorNode.Get("code").Int())
	}
	if status != 0 {
		if lingmaShouldRetryStatus(status) {
			return statusErr{code: status, msg: "lingma upstream returned a retryable SSE error"}
		}
		return nil
	}
	errorType := strings.ToLower(strings.TrimSpace(errorNode.Get("type").String()))
	for _, marker := range []string{"server", "internal", "overload", "rate", "timeout", "unavailable"} {
		if strings.Contains(errorType, marker) {
			status := http.StatusBadGateway
			if marker == "rate" {
				status = http.StatusTooManyRequests
			} else if marker == "timeout" {
				status = http.StatusGatewayTimeout
			}
			return statusErr{code: status, msg: "lingma upstream returned a retryable SSE error"}
		}
	}
	return nil
}

func lingmaAggregateRetryableSSEError(data []byte) error {
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if err := lingmaRetryableSSEError(line); err != nil {
			return err
		}
	}
	return nil
}
