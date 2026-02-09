package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/telemetry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type usageReporter struct {
	provider      string
	model         string
	authID        string
	authIndex     string
	apiKey        string
	source        string
	requestedAt   time.Time
	inputPayload  []byte
	outputPayload []byte
	respID        string
	finishReasons []string
	once          sync.Once
	mu            sync.Mutex
}

func newUsageReporter(ctx context.Context, provider, model string, auth *cliproxyauth.Auth) *usageReporter {
	apiKey := apiKeyFromContext(ctx)
	reporter := &usageReporter{
		provider:     provider,
		model:        model,
		requestedAt:  time.Now(),
		apiKey:       apiKey,
		source:       resolveUsageSource(auth, apiKey),
		inputPayload: telemetry.GetInputFromContext(ctx),
	}
	if auth != nil {
		reporter.authID = auth.ID
		reporter.authIndex = auth.EnsureIndex()
	}
	return reporter
}

// SetInput captures the input payload (prompt) for telemetry.
// This should ideally be the original, generic format request (e.g. OpenAI format)
// to ensure best readability in tracing dashboards.
func (r *usageReporter) SetInput(payload []byte) {
	if r == nil || len(payload) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Copy to prevent data races or modification issues
	r.inputPayload = bytes.Clone(payload)
}

// SetOutput captures the output payload (completion) for telemetry.
func (r *usageReporter) SetOutput(payload []byte) {
	if r == nil || len(payload) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outputPayload = bytes.Clone(payload)
}

// AppendOutput appends a chunk of output payload (completion) for telemetry in streaming mode.
func (r *usageReporter) AppendOutput(chunk []byte) {
	if r == nil || len(chunk) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outputPayload = append(r.outputPayload, chunk...)
}

// CaptureStreamChunk parses a translated stream chunk and appends its content to the completion.
// It supports common formats like OpenAI (delta.content).
func (r *usageReporter) CaptureStreamChunk(chunk []byte) {
	if r == nil || len(chunk) == 0 {
		return
	}

	// Strip SSE prefix if present
	trimmed := bytes.TrimSpace(chunk)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[5:])
	}
	if len(trimmed) == 0 || !gjson.ValidBytes(trimmed) {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Extract response ID if seen
	if id := gjson.GetBytes(trimmed, "id").String(); id != "" {
		r.respID = id
	}

	// Extract finish reason if seen
	if choices := gjson.GetBytes(trimmed, "choices"); choices.IsArray() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			if reason := choice.Get("finish_reason").String(); reason != "" {
				r.finishReasons = append(r.finishReasons, reason)
			}
			return true
		})
	}

	// Try to extract content from OpenAI format: choices[0].delta.content
	content := gjson.GetBytes(trimmed, "choices.0.delta.content").String()
	if content != "" {
		r.outputPayload = append(r.outputPayload, []byte(content)...)
		return
	}

	// Try Gemini format: candidates[0].content.parts[0].text
	content = gjson.GetBytes(trimmed, "candidates.0.content.parts.0.text").String()
	if content != "" {
		r.outputPayload = append(r.outputPayload, []byte(content)...)
		return
	}

	// Try Claude format: delta.text or content_block.text
	content = gjson.GetBytes(trimmed, "delta.text").String()
	if content == "" {
		content = gjson.GetBytes(trimmed, "content_block.text").String()
	}
	if content != "" {
		r.outputPayload = append(r.outputPayload, []byte(content)...)
		return
	}
}

func (r *usageReporter) publish(ctx context.Context, detail usage.Detail) {
	r.publishWithOutcome(ctx, detail, false)
}

func (r *usageReporter) publishFailure(ctx context.Context) {
	r.publishWithOutcome(ctx, usage.Detail{}, true)
}

func (r *usageReporter) trackFailure(ctx context.Context, errPtr *error) {
	if r == nil || errPtr == nil {
		return
	}
	if *errPtr != nil {
		r.publishFailure(ctx)
	}
}

func (r *usageReporter) publishWithOutcome(ctx context.Context, detail usage.Detail, failed bool) {
	if r == nil {
		return
	}
	if detail.TotalTokens == 0 {
		total := detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
		if total > 0 {
			detail.TotalTokens = total
		}
	}
	r.once.Do(func() {
		r.mu.Lock()
		outputPayload := r.outputPayload
		inputPayload := r.inputPayload
		capturedID := r.respID
		capturedReasons := r.finishReasons
		r.mu.Unlock()

		if ctx != nil {
			span := trace.SpanFromContext(ctx)
			if span.SpanContext().IsValid() {
				attrs := []attribute.KeyValue{
					attribute.String("gen_ai.system", r.provider),
					attribute.String("gen_ai.request.model", r.model),
					attribute.String("gen_ai.operation.name", "chat"),
					attribute.String("openinference.span.kind", "LLM"), // Phoenix specific category
					attribute.Int64("gen_ai.usage.input_tokens", int64(detail.InputTokens)),
					attribute.Int64("gen_ai.usage.output_tokens", int64(detail.OutputTokens)),
					attribute.Int64("gen_ai.usage.total_tokens", int64(detail.TotalTokens)),
				}

				if detail.ReasoningTokens > 0 {
					attrs = append(attrs, attribute.Int64("gen_ai.usage.reasoning_tokens", int64(detail.ReasoningTokens)))
				}
				if detail.CachedTokens > 0 {
					attrs = append(attrs, attribute.Int64("gen_ai.usage.cached_tokens", int64(detail.CachedTokens)))
				}

				// Record the input prompt if available.
				if len(inputPayload) > 0 {
					attrs = append(attrs, attribute.String("gen_ai.prompt", string(inputPayload)))

					// Extract request parameters
					if temp := gjson.GetBytes(inputPayload, "temperature"); temp.Exists() {
						attrs = append(attrs, attribute.Float64("gen_ai.request.temperature", temp.Float()))
					}
					if topP := gjson.GetBytes(inputPayload, "top_p"); topP.Exists() {
						attrs = append(attrs, attribute.Float64("gen_ai.request.top_p", topP.Float()))
					}
					if topK := gjson.GetBytes(inputPayload, "top_k"); topK.Exists() {
						attrs = append(attrs, attribute.Int64("gen_ai.request.top_k", topK.Int()))
					}
					if presP := gjson.GetBytes(inputPayload, "presence_penalty"); presP.Exists() {
						attrs = append(attrs, attribute.Float64("gen_ai.request.presence_penalty", presP.Float()))
					}
					if freqP := gjson.GetBytes(inputPayload, "frequency_penalty"); freqP.Exists() {
						attrs = append(attrs, attribute.Float64("gen_ai.request.frequency_penalty", freqP.Float()))
					}
					if maxTokens := gjson.GetBytes(inputPayload, "max_tokens"); maxTokens.Exists() {
						attrs = append(attrs, attribute.Int64("gen_ai.request.max_tokens", maxTokens.Int()))
					}

					// Phoenix specific: structured messages for better UI
					// Support OpenAI 'messages' and Gemini 'contents'
					if messages := gjson.GetBytes(inputPayload, "messages"); messages.IsArray() {
						attrs = append(attrs, attribute.String("llm.input_messages", messages.Raw))
					} else if contents := gjson.GetBytes(inputPayload, "contents"); contents.IsArray() {
						attrs = append(attrs, attribute.String("llm.input_messages", contents.Raw))
					}

					// OpenInference/Phoenix specific: primary input value for dashboard columns
					attrs = append(attrs, attribute.String("input.value", string(inputPayload)))
				}

				// Record the output completion if available.
				if len(outputPayload) > 0 {
					attrs = append(attrs, attribute.String("gen_ai.completion", string(outputPayload)))

					// OpenInference/Phoenix specific: primary output value for dashboard columns
					var outputValue string
					if outputPayload[0] == '{' {
						// Try to extract text content for cleaner dashboard display
						res := gjson.GetBytes(outputPayload, "choices.0.message.content")
						if !res.Exists() {
							res = gjson.GetBytes(outputPayload, "candidates.0.content.parts.0.text")
						}
						if !res.Exists() {
							res = gjson.GetBytes(outputPayload, "content.0.text")
						}

						if res.Exists() {
							outputValue = res.String()
						} else {
							outputValue = string(outputPayload)
						}
					} else {
						outputValue = string(outputPayload)
					}
					attrs = append(attrs, attribute.String("output.value", outputValue))

					// Extract response ID (prefer captured streaming ID)
					respID := capturedID
					if respID == "" {
						respID = gjson.GetBytes(outputPayload, "id").String()
					}
					if respID != "" {
						attrs = append(attrs, attribute.String("gen_ai.response.id", respID))
					}

					// Extract finish reasons (prefer captured streaming reasons)
					reasons := capturedReasons
					var lastMessage gjson.Result
					if len(reasons) == 0 {
						if choices := gjson.GetBytes(outputPayload, "choices"); choices.IsArray() {
							choices.ForEach(func(_, choice gjson.Result) bool {
								if reason := choice.Get("finish_reason").String(); reason != "" {
									reasons = append(reasons, reason)
								}
								if msg := choice.Get("message"); msg.Exists() {
									lastMessage = msg
								} else if delta := choice.Get("delta"); delta.Exists() {
									lastMessage = delta
								}
								return true
							})
						}
					}
					if len(reasons) > 0 {
						attrs = append(attrs, attribute.StringSlice("gen_ai.response.finish_reasons", reasons))
					}

					// Phoenix specific: structured output message
					if lastMessage.Exists() {
						attrs = append(attrs, attribute.String("llm.output_messages", "["+lastMessage.Raw+"]"))
					} else if len(outputPayload) > 0 && outputPayload[0] != '{' {
						// Streaming case where outputPayload is plain text, create a simple message array for Phoenix
						payloadJSON, _ := json.Marshal(string(outputPayload))
						msgObj := fmt.Sprintf(`[{"role":"assistant","content":%s}]`, string(payloadJSON))
						attrs = append(attrs, attribute.String("llm.output_messages", msgObj))
					}

					// Try to extract response model if it differs from request model
					if respModel := gjson.GetBytes(outputPayload, "model").String(); respModel != "" {
						attrs = append(attrs, attribute.String("gen_ai.response.model", respModel))
					} else if respModel = gjson.GetBytes(outputPayload, "response.model").String(); respModel != "" {
						attrs = append(attrs, attribute.String("gen_ai.response.model", respModel))
					}
				}

				// Record user context
				if r.source != "" {
					attrs = append(attrs, attribute.String("user.id", r.source))
				} else if r.apiKey != "" {
					attrs = append(attrs, attribute.String("user.id", util.AnonymizeString(r.apiKey)))
				}

				span.SetAttributes(attrs...)
			}
		}

		usage.PublishRecord(ctx, usage.Record{
			Provider:    r.provider,
			Model:       r.model,
			Source:      r.source,
			APIKey:      r.apiKey,
			AuthID:      r.authID,
			AuthIndex:   r.authIndex,
			RequestedAt: r.requestedAt,
			Failed:      failed,
			Detail:      detail,
		})
	})
}

// ensurePublished guarantees that a usage record is emitted exactly once.
func (r *usageReporter) ensurePublished(ctx context.Context) {
	if r == nil {
		return
	}
	// Use publishWithOutcome with empty detail to ensure OTel attributes are also set
	r.publishWithOutcome(ctx, usage.Detail{}, false)
}

func apiKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	if v, exists := ginCtx.Get("apiKey"); exists {
		switch value := v.(type) {
		case string:
			return value
		case fmt.Stringer:
			return value.String()
		default:
			return fmt.Sprintf("%v", value)
		}
	}
	return ""
}

func resolveUsageSource(auth *cliproxyauth.Auth, ctxAPIKey string) string {
	if auth != nil {
		provider := strings.TrimSpace(auth.Provider)
		if strings.EqualFold(provider, "gemini-cli") {
			if id := strings.TrimSpace(auth.ID); id != "" {
				return id
			}
		}
		if strings.EqualFold(provider, "vertex") {
			if auth.Metadata != nil {
				if projectID, ok := auth.Metadata["project_id"].(string); ok {
					if trimmed := strings.TrimSpace(projectID); trimmed != "" {
						return trimmed
					}
				}
				if project, ok := auth.Metadata["project"].(string); ok {
					if trimmed := strings.TrimSpace(project); trimmed != "" {
						return trimmed
					}
				}
			}
		}
		if typ, value := auth.AccountInfo(); value != "" {
			if typ == "api_key" {
				return util.AnonymizeString(value)
			}
			return strings.TrimSpace(value)
		}
		if auth.Metadata != nil {
			if email, ok := auth.Metadata["email"].(string); ok {
				if trimmed := strings.TrimSpace(email); trimmed != "" {
					return trimmed
				}
			}
		}
		if auth.Attributes != nil {
			if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
				return util.AnonymizeString(key)
			}
		}
	}
	if trimmed := strings.TrimSpace(ctxAPIKey); trimmed != "" {
		return util.AnonymizeString(trimmed)
	}
	return ""
}

func parseCodexUsage(data []byte) (usage.Detail, bool) {
	usageNode := gjson.ParseBytes(data).Get("response.usage")
	if !usageNode.Exists() {
		return usage.Detail{}, false
	}
	detail := usage.Detail{
		InputTokens:  usageNode.Get("input_tokens").Int(),
		OutputTokens: usageNode.Get("output_tokens").Int(),
		TotalTokens:  usageNode.Get("total_tokens").Int(),
	}
	if cached := usageNode.Get("input_tokens_details.cached_tokens"); cached.Exists() {
		detail.CachedTokens = cached.Int()
	}
	if reasoning := usageNode.Get("output_tokens_details.reasoning_tokens"); reasoning.Exists() {
		detail.ReasoningTokens = reasoning.Int()
	}
	return detail, true
}

func parseOpenAIUsage(data []byte) usage.Detail {
	usageNode := gjson.ParseBytes(data).Get("usage")
	if !usageNode.Exists() {
		return usage.Detail{}
	}
	inputNode := usageNode.Get("prompt_tokens")
	if !inputNode.Exists() {
		inputNode = usageNode.Get("input_tokens")
	}
	outputNode := usageNode.Get("completion_tokens")
	if !outputNode.Exists() {
		outputNode = usageNode.Get("output_tokens")
	}
	detail := usage.Detail{
		InputTokens:  inputNode.Int(),
		OutputTokens: outputNode.Int(),
		TotalTokens:  usageNode.Get("total_tokens").Int(),
	}
	cached := usageNode.Get("prompt_tokens_details.cached_tokens")
	if !cached.Exists() {
		cached = usageNode.Get("input_tokens_details.cached_tokens")
	}
	if cached.Exists() {
		detail.CachedTokens = cached.Int()
	}
	reasoning := usageNode.Get("completion_tokens_details.reasoning_tokens")
	if !reasoning.Exists() {
		reasoning = usageNode.Get("output_tokens_details.reasoning_tokens")
	}
	if reasoning.Exists() {
		detail.ReasoningTokens = reasoning.Int()
	}
	return detail
}

func parseOpenAIStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	usageNode := gjson.GetBytes(payload, "usage")
	if !usageNode.Exists() {
		return usage.Detail{}, false
	}
	detail := usage.Detail{
		InputTokens:  usageNode.Get("prompt_tokens").Int(),
		OutputTokens: usageNode.Get("completion_tokens").Int(),
		TotalTokens:  usageNode.Get("total_tokens").Int(),
	}
	if cached := usageNode.Get("prompt_tokens_details.cached_tokens"); cached.Exists() {
		detail.CachedTokens = cached.Int()
	}
	if reasoning := usageNode.Get("completion_tokens_details.reasoning_tokens"); reasoning.Exists() {
		detail.ReasoningTokens = reasoning.Int()
	}
	return detail, true
}

func parseClaudeUsage(data []byte) usage.Detail {
	usageNode := gjson.ParseBytes(data).Get("usage")
	if !usageNode.Exists() {
		return usage.Detail{}
	}
	detail := usage.Detail{
		InputTokens:  usageNode.Get("input_tokens").Int(),
		OutputTokens: usageNode.Get("output_tokens").Int(),
		CachedTokens: usageNode.Get("cache_read_input_tokens").Int(),
	}
	if detail.CachedTokens == 0 {
		// fall back to creation tokens when read tokens are absent
		detail.CachedTokens = usageNode.Get("cache_creation_input_tokens").Int()
	}
	detail.TotalTokens = detail.InputTokens + detail.OutputTokens
	return detail
}

func parseClaudeStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	usageNode := gjson.GetBytes(payload, "usage")
	if !usageNode.Exists() {
		return usage.Detail{}, false
	}
	detail := usage.Detail{
		InputTokens:  usageNode.Get("input_tokens").Int(),
		OutputTokens: usageNode.Get("output_tokens").Int(),
		CachedTokens: usageNode.Get("cache_read_input_tokens").Int(),
	}
	if detail.CachedTokens == 0 {
		detail.CachedTokens = usageNode.Get("cache_creation_input_tokens").Int()
	}
	detail.TotalTokens = detail.InputTokens + detail.OutputTokens
	return detail, true
}

func parseGeminiFamilyUsageDetail(node gjson.Result) usage.Detail {
	detail := usage.Detail{
		InputTokens:     node.Get("promptTokenCount").Int(),
		OutputTokens:    node.Get("candidatesTokenCount").Int(),
		ReasoningTokens: node.Get("thoughtsTokenCount").Int(),
		TotalTokens:     node.Get("totalTokenCount").Int(),
		CachedTokens:    node.Get("cachedContentTokenCount").Int(),
	}
	if detail.TotalTokens == 0 {
		detail.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	return detail
}

func parseGeminiCLIUsage(data []byte) usage.Detail {
	usageNode := gjson.ParseBytes(data)
	node := usageNode.Get("response.usageMetadata")
	if !node.Exists() {
		node = usageNode.Get("response.usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}
	}
	return parseGeminiFamilyUsageDetail(node)
}

func parseGeminiUsage(data []byte) usage.Detail {
	usageNode := gjson.ParseBytes(data)
	node := usageNode.Get("usageMetadata")
	if !node.Exists() {
		node = usageNode.Get("usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}
	}
	return parseGeminiFamilyUsageDetail(node)
}

func parseGeminiStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	node := gjson.GetBytes(payload, "usageMetadata")
	if !node.Exists() {
		node = gjson.GetBytes(payload, "usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}, false
	}
	return parseGeminiFamilyUsageDetail(node), true
}

func parseGeminiCLIStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	node := gjson.GetBytes(payload, "response.usageMetadata")
	if !node.Exists() {
		node = gjson.GetBytes(payload, "usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}, false
	}
	return parseGeminiFamilyUsageDetail(node), true
}

func parseAntigravityUsage(data []byte) usage.Detail {
	usageNode := gjson.ParseBytes(data)
	node := usageNode.Get("response.usageMetadata")
	if !node.Exists() {
		node = usageNode.Get("usageMetadata")
	}
	if !node.Exists() {
		node = usageNode.Get("usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}
	}
	return parseGeminiFamilyUsageDetail(node)
}

func parseAntigravityStreamUsage(line []byte) (usage.Detail, bool) {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	node := gjson.GetBytes(payload, "response.usageMetadata")
	if !node.Exists() {
		node = gjson.GetBytes(payload, "usageMetadata")
	}
	if !node.Exists() {
		node = gjson.GetBytes(payload, "usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}, false
	}
	return parseGeminiFamilyUsageDetail(node), true
}

var stopChunkWithoutUsage sync.Map

func rememberStopWithoutUsage(traceID string) {
	stopChunkWithoutUsage.Store(traceID, struct{}{})
	time.AfterFunc(10*time.Minute, func() { stopChunkWithoutUsage.Delete(traceID) })
}

// FilterSSEUsageMetadata removes usageMetadata from SSE events that are not
// terminal (finishReason != "stop"). Stop chunks are left untouched. This
// function is shared between aistudio and antigravity executors.
func FilterSSEUsageMetadata(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}

	lines := bytes.Split(payload, []byte("\n"))
	modified := false
	foundData := false
	for idx, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		foundData = true
		dataIdx := bytes.Index(line, []byte("data:"))
		if dataIdx < 0 {
			continue
		}
		rawJSON := bytes.TrimSpace(line[dataIdx+5:])
		traceID := gjson.GetBytes(rawJSON, "traceId").String()
		if isStopChunkWithoutUsage(rawJSON) && traceID != "" {
			rememberStopWithoutUsage(traceID)
			continue
		}
		if traceID != "" {
			if _, ok := stopChunkWithoutUsage.Load(traceID); ok && hasUsageMetadata(rawJSON) {
				stopChunkWithoutUsage.Delete(traceID)
				continue
			}
		}

		cleaned, changed := StripUsageMetadataFromJSON(rawJSON)
		if !changed {
			continue
		}
		var rebuilt []byte
		rebuilt = append(rebuilt, line[:dataIdx]...)
		rebuilt = append(rebuilt, []byte("data:")...)
		if len(cleaned) > 0 {
			rebuilt = append(rebuilt, ' ')
			rebuilt = append(rebuilt, cleaned...)
		}
		lines[idx] = rebuilt
		modified = true
	}
	if !modified {
		if !foundData {
			// Handle payloads that are raw JSON without SSE data: prefix.
			trimmed := bytes.TrimSpace(payload)
			cleaned, changed := StripUsageMetadataFromJSON(trimmed)
			if !changed {
				return payload
			}
			return cleaned
		}
		return payload
	}
	return bytes.Join(lines, []byte("\n"))
}

// StripUsageMetadataFromJSON drops usageMetadata unless finishReason is present (terminal).
// It handles both formats:
// - Aistudio: candidates.0.finishReason
// - Antigravity: response.candidates.0.finishReason
func StripUsageMetadataFromJSON(rawJSON []byte) ([]byte, bool) {
	jsonBytes := bytes.TrimSpace(rawJSON)
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return rawJSON, false
	}

	// Check for finishReason in both aistudio and antigravity formats
	finishReason := gjson.GetBytes(jsonBytes, "candidates.0.finishReason")
	if !finishReason.Exists() {
		finishReason = gjson.GetBytes(jsonBytes, "response.candidates.0.finishReason")
	}
	terminalReason := finishReason.Exists() && strings.TrimSpace(finishReason.String()) != ""

	usageMetadata := gjson.GetBytes(jsonBytes, "usageMetadata")
	if !usageMetadata.Exists() {
		usageMetadata = gjson.GetBytes(jsonBytes, "response.usageMetadata")
	}

	// Terminal chunk: keep as-is.
	if terminalReason {
		return rawJSON, false
	}

	// Nothing to strip
	if !usageMetadata.Exists() {
		return rawJSON, false
	}

	// Remove usageMetadata from both possible locations
	cleaned := jsonBytes
	var changed bool

	if usageMetadata = gjson.GetBytes(cleaned, "usageMetadata"); usageMetadata.Exists() {
		// Rename usageMetadata to cpaUsageMetadata in the message_start event of Claude
		cleaned, _ = sjson.SetRawBytes(cleaned, "cpaUsageMetadata", []byte(usageMetadata.Raw))
		cleaned, _ = sjson.DeleteBytes(cleaned, "usageMetadata")
		changed = true
	}

	if usageMetadata = gjson.GetBytes(cleaned, "response.usageMetadata"); usageMetadata.Exists() {
		// Rename usageMetadata to cpaUsageMetadata in the message_start event of Claude
		cleaned, _ = sjson.SetRawBytes(cleaned, "response.cpaUsageMetadata", []byte(usageMetadata.Raw))
		cleaned, _ = sjson.DeleteBytes(cleaned, "response.usageMetadata")
		changed = true
	}

	return cleaned, changed
}

func hasUsageMetadata(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	if gjson.GetBytes(jsonBytes, "usageMetadata").Exists() {
		return true
	}
	if gjson.GetBytes(jsonBytes, "response.usageMetadata").Exists() {
		return true
	}
	return false
}

func isStopChunkWithoutUsage(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	finishReason := gjson.GetBytes(jsonBytes, "candidates.0.finishReason")
	if !finishReason.Exists() {
		finishReason = gjson.GetBytes(jsonBytes, "response.candidates.0.finishReason")
	}
	trimmed := strings.TrimSpace(finishReason.String())
	if !finishReason.Exists() || trimmed == "" {
		return false
	}
	return !hasUsageMetadata(jsonBytes)
}

func jsonPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	return trimmed
}
