package lingma

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/sjson"
)

const maxRecoveryDelay = 30 * time.Second

type upstreamPlan struct {
	plugin           *Plugin
	host             hostRPC
	credentials      credentials
	attributes       map[string]string
	config           pluginConfig
	baseBody         []byte
	model            string
	profile          requestProfile
	nextAttempt      int
	maxAttempts      int
	baseDelay        time.Duration
	nextRetryAfter   time.Duration
	fallbackApplied  bool
	recoveryEligible bool
}

func newUpstreamPlan(plugin *Plugin, host hostRPC, creds credentials, attributes map[string]string, body []byte, model string, profile requestProfile, fallback fallbackDecision) *upstreamPlan {
	config := plugin.configSnapshot()
	maxAttempts := config.RecoveryMaxAttempts
	if config.RecoveryDisabled {
		maxAttempts = 1
	}
	return &upstreamPlan{
		plugin:           plugin,
		host:             host,
		credentials:      creds,
		attributes:       attributes,
		config:           config,
		baseBody:         bytes.Clone(body),
		model:            model,
		profile:          profile,
		maxAttempts:      maxAttempts,
		baseDelay:        config.RecoveryBaseDelay,
		fallbackApplied:  fallback.Applied,
		recoveryEligible: config.ThinkingFallback && profile.LargeThinking && !fallback.Applied,
	}
}

func (p *upstreamPlan) do() (pluginapi.HTTPResponse, []byte, error) {
	for p.hasNext() {
		body, errPrepare := p.prepareNextAttempt()
		if errPrepare != nil {
			return pluginapi.HTTPResponse{}, nil, errPrepare
		}
		request, errRequest := encodeChatRequest(p.credentials, p.config, body, p.model, p.attributes)
		if errRequest != nil {
			return pluginapi.HTTPResponse{}, body, errRequest
		}
		response, errDo := p.host.do(request)
		if errDo != nil {
			if p.hasNext() && shouldRetryTransportError(errDo) {
				p.logRetry(errDo, 0)
				continue
			}
			return pluginapi.HTTPResponse{}, body, errDo
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, body, nil
		}
		if shouldRetryStatus(response.StatusCode) && p.hasNext() {
			p.nextRetryAfter = parseRetryAfter(response.Headers.Get("Retry-After"), time.Now())
			p.logRetry(nil, response.StatusCode)
			continue
		}
		return pluginapi.HTTPResponse{}, body, newStatusError(response.StatusCode, safeUpstreamMessage(response.Body))
	}
	return pluginapi.HTTPResponse{}, nil, newStatusError(http.StatusBadGateway, "Lingma upstream recovery exhausted")
}

func (p *upstreamPlan) openStream() (hostHTTPStreamResponse, []byte, error) {
	for p.hasNext() {
		body, errPrepare := p.prepareNextAttempt()
		if errPrepare != nil {
			return hostHTTPStreamResponse{}, nil, errPrepare
		}
		request, errRequest := encodeChatRequest(p.credentials, p.config, body, p.model, p.attributes)
		if errRequest != nil {
			return hostHTTPStreamResponse{}, body, errRequest
		}
		response, errDo := p.host.doStream(request)
		if errDo != nil {
			if p.hasNext() && shouldRetryTransportError(errDo) {
				p.logRetry(errDo, 0)
				continue
			}
			return hostHTTPStreamResponse{}, body, errDo
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, body, nil
		}
		errorBody, errDrain := p.drainStream(response.StreamID)
		p.host.closeHTTPStream(response.StreamID)
		if shouldRetryStatus(response.StatusCode) && p.hasNext() {
			p.nextRetryAfter = parseRetryAfter(response.Headers.Get("Retry-After"), time.Now())
			p.logRetry(errDrain, response.StatusCode)
			continue
		}
		if errDrain != nil {
			return hostHTTPStreamResponse{}, body, errDrain
		}
		return hostHTTPStreamResponse{}, body, newStatusError(response.StatusCode, safeUpstreamMessage(errorBody))
	}
	return hostHTTPStreamResponse{}, nil, newStatusError(http.StatusBadGateway, "Lingma upstream recovery exhausted")
}

func (p *upstreamPlan) drainStream(streamID string) ([]byte, error) {
	var body []byte
	for {
		chunk, errRead := p.host.readHTTPStream(streamID)
		if errRead != nil {
			return body, errRead
		}
		body = append(body, chunk.Payload...)
		if chunk.Error != "" {
			return body, errors.New(chunk.Error)
		}
		if chunk.Done {
			return body, nil
		}
	}
}

func (p *upstreamPlan) prepareNextAttempt() ([]byte, error) {
	attempt := p.nextAttempt
	p.nextAttempt++
	if attempt > 0 {
		delay := retryDelay(p.baseDelay, attempt-1, p.nextRetryAfter)
		p.nextRetryAfter = 0
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	disable := attempt > 0 && p.recoveryEligible
	body := prepareAttemptBody(p.baseBody, attempt, disable)
	if disable && !p.fallbackApplied {
		p.fallbackApplied = true
		p.host.log("warn", "Lingma in-request recovery disabled reasoning", map[string]any{
			"provider":     ProviderID,
			"model":        p.model,
			"attempt":      attempt + 1,
			"max_attempts": p.maxAttempts,
		})
	}
	return body, nil
}

func prepareAttemptBody(base []byte, attempt int, disable bool) []byte {
	body := bytes.Clone(base)
	if attempt > 0 {
		updates := []struct {
			path  string
			value any
		}{
			{path: "request_id", value: uuid.NewString()},
			{path: "chat_record_id", value: uuid.NewString()},
			{path: "business.id", value: uuid.NewString()},
			{path: "business.begin_at", value: time.Now().UnixMilli()},
			{path: "is_retry", value: true},
		}
		for _, update := range updates {
			updated, errSet := sjson.SetBytes(body, update.path, update.value)
			if errSet != nil {
				return bytes.Clone(base)
			}
			body = updated
		}
	}
	if disable {
		body = disableThinking(body)
	}
	return body
}

func (p *upstreamPlan) hasNext() bool {
	return p != nil && p.nextAttempt < p.maxAttempts
}

func (p *upstreamPlan) logRetry(err error, status int) {
	fields := map[string]any{
		"provider":     ProviderID,
		"model":        p.model,
		"attempt":      p.nextAttempt,
		"max_attempts": p.maxAttempts,
	}
	if status != 0 {
		fields["status"] = status
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	p.host.log("warn", "Retrying transient Lingma upstream failure", fields)
}

func retryDelay(base time.Duration, retryIndex int, retryAfter time.Duration) time.Duration {
	maximum := base
	if maximum > maxRecoveryDelay {
		maximum = maxRecoveryDelay
	}
	for index := 0; index < retryIndex && maximum < maxRecoveryDelay; index++ {
		if maximum > maxRecoveryDelay/2 {
			maximum = maxRecoveryDelay
			break
		}
		maximum *= 2
	}
	delay := time.Duration(0)
	if maximum > 0 {
		delay = time.Duration(rand.Int64N(int64(maximum) + 1))
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay > maxRecoveryDelay {
		return maxRecoveryDelay
	}
	return delay
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, errParse := strconv.Atoi(value); errParse == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	when, errParse := http.ParseTime(value)
	if errParse != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func shouldRetryStatus(status int) bool {
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

func shouldRetryTransportError(err error) bool {
	if err == nil || isContextCancellation(err) {
		return false
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

func formatRecoveryError(status int, body []byte) error {
	return fmt.Errorf("Lingma upstream returned %d: %s", status, safeUpstreamMessage(body))
}
