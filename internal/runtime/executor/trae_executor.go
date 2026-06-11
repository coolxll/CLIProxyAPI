package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	traetranslator "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/trae"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type traeStatusErr struct {
	code int
	msg  string
}

func (e traeStatusErr) Error() string {
	return e.msg
}

func (e traeStatusErr) StatusCode() int {
	return e.code
}

type TraeExecutor struct {
	cfg *config.Config

	translatorOnce sync.Once
	translator     *traetranslator.Translator
	translatorErr  error

	detailConfigMu sync.RWMutex
	detailConfigs  map[string]map[string]traeDetailModelConfig
}

type traeDetailModelConfig struct {
	ModelName  string
	ConfigName string
}

const (
	traeProtocolV1     = "v1"
	traeProtocolV2     = "v2"
	traeProtocolV3     = "v3"
	traeProtocolMeta   = "trae_protocol"
	traeModelNameMeta  = "trae_model_name"
	traeConfigMeta     = "trae_config_name"
	traeModelListURL   = "https://trae-api-cn.mchost.guru/api/ide/v1/model_list?type=llm_raw_chat"
	traeDetailParamURL = "https://trae-api-cn.mchost.guru/api/ide/v1/get_detail_param"
)

type traeRequestBuildResult struct {
	TargetURL        string
	RequestBody      []byte
	LogBody          []byte
	RequestPin       string
	RequestAt        int64
	SessionID        string
	ConversationID   string
	Protocol         string
	ExtraHeaders     http.Header
	IsToolCommit     bool
	RawResponseModel string
}

func NewTraeExecutor(cfg *config.Config) *TraeExecutor {
	return &TraeExecutor{cfg: cfg}
}

func (e *TraeExecutor) getTranslator() (*traetranslator.Translator, error) {
	e.translatorOnce.Do(func() {
		e.translator, e.translatorErr = traetranslator.NewTranslator()
	})
	return e.translator, e.translatorErr
}

func (e *TraeExecutor) Identifier() string {
	return "trae"
}

func (e *TraeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	return nil
}

func (e *TraeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented for trae")
}

func (e *TraeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func (e *TraeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if opts.SourceFormat == "" {
		return cliproxyexecutor.Response{}, fmt.Errorf("trae executor: source format is required")
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	_, upstreamModel := resolveTraeProtocol(baseModel, opts.Metadata)
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, upstreamModel, req.Payload, false)

	enc, err := helps.TokenizerForModel(upstreamModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("trae executor: tokenizer init failed: %w", err)
	}
	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("trae executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

func (e *TraeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	// Aggregate standard streaming chunks for non-streaming calls
	streamOpts := opts
	streamOpts.Stream = true
	// The Claude stream translator checks originalRequest.stream to decide between
	// streaming SSE output and non-streaming JSON. Force streaming so the translator
	// produces parseable SSE events (content_block_start/delta/stop) rather than a
	// single type:message JSON with empty content. Also handle missing stream flag
	// (Claude handler treats missing as non-streaming).
	if len(streamOpts.OriginalRequest) > 0 {
		streamVal := gjson.GetBytes(streamOpts.OriginalRequest, "stream")
		if !streamVal.Exists() || streamVal.Type == gjson.False {
			if modified, errSet := sjson.SetBytes(streamOpts.OriginalRequest, "stream", true); errSet == nil {
				streamOpts.OriginalRequest = modified
			}
		}
	}

	var aggregatedContent strings.Builder
	var aggregatedReasoning strings.Builder
	var toolCalls []openaiToolCall
	var finalModel string
	var chatID string
	var finalUsage openaiUsage
	var hasUsage bool
	var finalStopReason string
	var pendingToolCalls []openaiToolCall
	tcIndex := 0

	// Standard stream translator parameter
	openaiFormat := sdktranslator.FromString("openai")
	from := opts.SourceFormat

	res, err := e.ExecuteStream(ctx, auth, req, streamOpts)
	if err != nil {
		return resp, err
	}

	if res != nil {
		var parseParam any
		for chunk := range res.Chunks {
			if chunk.Err != nil {
				return resp, chunk.Err
			}
			payload := chunk.Payload

			// Extract JSON payloads from the chunk. For Claude format, the translator
			// produces SSE events (event:/data: lines) that need parsing. For other
			// formats (OpenAI, Gemini, Responses), translate back to OpenAI first.
			var jsonPayloads [][]byte
			if from == "claude" {
				isSSE := bytes.Contains(payload, []byte("\ndata:")) || bytes.HasPrefix(bytes.TrimSpace(payload), []byte("event:")) || bytes.HasPrefix(bytes.TrimSpace(payload), []byte("data:"))
				if isSSE {
					for _, line := range bytes.Split(payload, []byte("\n")) {
						trimmed := bytes.TrimSpace(line)
						if len(trimmed) == 0 || bytes.HasPrefix(trimmed, []byte("event:")) {
							continue
						}
						if bytes.HasPrefix(trimmed, []byte("data:")) {
							d := bytes.TrimSpace(trimmed[5:])
							if len(d) > 0 && gjson.ValidBytes(d) && string(d) != "[DONE]" {
								jsonPayloads = append(jsonPayloads, d)
							}
						}
					}
				} else if gjson.ValidBytes(payload) && string(payload) != "[DONE]" {
					jsonPayloads = append(jsonPayloads, payload)
				}
			} else {
				// Non-Claude formats: translate stream chunk back to OpenAI
				openaiChunks := sdktranslator.TranslateStream(ctx, from, openaiFormat, req.Model, streamOpts.OriginalRequest, nil, payload, &parseParam)
				for _, oc := range openaiChunks {
					d := string(oc)
					if strings.HasPrefix(d, "data:") {
						d = strings.TrimSpace(strings.TrimPrefix(d, "data:"))
					}
					if d != "[DONE]" && gjson.Valid(d) {
						jsonPayloads = append(jsonPayloads, []byte(d))
					}
				}
			}
			for _, jsonPayload := range jsonPayloads {

				root := gjson.ParseBytes(jsonPayload)

				// Common fields
				if id := root.Get("id").String(); id != "" && chatID == "" {
					chatID = id
				}
				if model := root.Get("model").String(); model != "" {
					finalModel = model
				}

				// OpenAI format: choices.0.delta.*
				if root.Get("choices").Exists() {
					if contentVal := root.Get("choices.0.delta.content"); contentVal.Exists() {
						aggregatedContent.WriteString(contentVal.String())
					}
					if reasoningVal := root.Get("choices.0.delta.reasoning_content"); reasoningVal.Exists() {
						aggregatedReasoning.WriteString(reasoningVal.String())
					}
					if tcVal := root.Get("choices.0.delta.tool_calls"); tcVal.Exists() && tcVal.IsArray() {
						for _, tc := range tcVal.Array() {
							idx := int(tc.Get("index").Int())
							found := false
							for i := range toolCalls {
								if toolCalls[i].Index == idx {
									if id := tc.Get("id").String(); id != "" {
										toolCalls[i].ID = id
									}
									if typ := tc.Get("type").String(); typ != "" {
										toolCalls[i].Type = typ
									}
									if name := tc.Get("function.name").String(); name != "" {
										toolCalls[i].Function.Name += name
									}
									if args := tc.Get("function.arguments").String(); args != "" {
										toolCalls[i].Function.Arguments += args
									}
									found = true
									break
								}
							}
							if !found {
								toolCalls = append(toolCalls, openaiToolCall{
									Index: idx,
									ID:    tc.Get("id").String(),
									Type:  tc.Get("type").String(),
									Function: openaiFunction{
										Name:      tc.Get("function.name").String(),
										Arguments: tc.Get("function.arguments").String(),
									},
								})
							}
						}
					}
				}

				// Claude format: SSE events (content_block_start/delta/stop, message_delta, message)
				switch root.Get("type").String() {
				case "message":
					root.Get("content").ForEach(func(_, block gjson.Result) bool {
						switch block.Get("type").String() {
						case "text":
							if t := block.Get("text").String(); t != "" {
								aggregatedContent.WriteString(t)
							}
						case "thinking":
							if t := block.Get("thinking").String(); t != "" {
								aggregatedReasoning.WriteString(t)
							}
						case "tool_use":
							toolCalls = append(toolCalls, openaiToolCall{
								Index: tcIndex,
								ID:    block.Get("id").String(),
								Type:  "function",
								Function: openaiFunction{
									Name:      block.Get("name").String(),
									Arguments: block.Get("input").Raw,
								},
							})
							tcIndex++
						}
						return true
					})
					if sr := root.Get("stop_reason").String(); sr != "" {
						finalStopReason = sr
					}
				case "content_block_start":
					block := root.Get("content_block")
					if block.Get("type").String() == "tool_use" {
						pendingToolCalls = append(pendingToolCalls, openaiToolCall{
							Index: tcIndex,
							ID:    block.Get("id").String(),
							Type:  "function",
							Function: openaiFunction{
								Name: block.Get("name").String(),
							},
						})
					}
				case "content_block_delta":
					delta := root.Get("delta")
					switch delta.Get("type").String() {
					case "text_delta":
						if t := delta.Get("text").String(); t != "" {
							aggregatedContent.WriteString(t)
						}
					case "thinking_delta":
						if t := delta.Get("thinking").String(); t != "" {
							aggregatedReasoning.WriteString(t)
						}
					case "input_json_delta":
						if len(pendingToolCalls) > 0 {
							last := &pendingToolCalls[len(pendingToolCalls)-1]
							last.Function.Arguments += delta.Get("partial_json").String()
						}
					}
				case "content_block_stop":
					// Finalize pending tool calls
					if len(pendingToolCalls) > 0 {
						last := pendingToolCalls[len(pendingToolCalls)-1]
						if last.Function.Arguments == "" {
							last.Function.Arguments = "{}"
						}
						toolCalls = append(toolCalls, last)
						pendingToolCalls = pendingToolCalls[:len(pendingToolCalls)-1]
						tcIndex++
					}
				case "message_delta":
					if sr := root.Get("delta.stop_reason").String(); sr != "" {
						finalStopReason = sr
					}
				}

				// Usage (both formats)
				usageVal := root.Get("usage")
				if !usageVal.Exists() {
					usageVal = root.Get("message.usage")
				}
				if usageVal.Exists() && (usageVal.Get("prompt_tokens").Int() > 0 || usageVal.Get("input_tokens").Int() > 0 || usageVal.Get("output_tokens").Int() > 0) {
					u := openAIUsageFromResult(usageVal)
					finalUsage = u
					hasUsage = true
				}
			} // end for _, jsonPayload
		}
	}

	// Fallback: if the model put the entire response in thinking blocks (no text block),
	// promote reasoning to content so the client gets a non-empty answer.
	// When the streaming fallback already promoted reasoning to a content delta,
	// content and reasoning will be identical — clear the duplicate from reasoning.
	if aggregatedContent.Len() == 0 && aggregatedReasoning.Len() > 0 && len(toolCalls) == 0 {
		aggregatedContent.WriteString(aggregatedReasoning.String())
		aggregatedReasoning.Reset()
	} else if aggregatedContent.Len() > 0 && aggregatedContent.String() == aggregatedReasoning.String() {
		aggregatedReasoning.Reset()
	}

	// Synthesize a standard OpenAI Chat Completion response
	type openAIMessage struct {
		Role             string           `json:"role"`
		Content          string           `json:"content"`
		ReasoningContent string           `json:"reasoning_content,omitempty"`
		ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
	}
	type openAIChoice struct {
		Index        int           `json:"index"`
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	}
	type openaiResponse struct {
		ID      string         `json:"id"`
		Object  string         `json:"object"`
		Created int64          `json:"created"`
		Model   string         `json:"model"`
		Choices []openAIChoice `json:"choices"`
		Usage   openaiUsage    `json:"usage"`
	}

	finishReason := "stop"
	if finalStopReason == "tool_use" || len(toolCalls) > 0 {
		finishReason = "tool_calls"
	} else if finalStopReason != "" {
		finishReason = mapUpstreamFinishReasonToOpenAI(finalStopReason)
	}
	if chatID == "" {
		chatID = fmt.Sprintf("chatcmpl-%s", strings.ReplaceAll(uuid.New().String(), "-", ""))
	}
	if finalModel == "" {
		finalModel = req.Model
	}

	openaiResp := openaiResponse{
		ID:      chatID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   finalModel,
		Choices: []openAIChoice{
			{
				Index: 0,
				Message: openAIMessage{
					Role:             "assistant",
					Content:          aggregatedContent.String(),
					ReasoningContent: aggregatedReasoning.String(),
					ToolCalls:        toolCalls,
				},
				FinishReason: finishReason,
			},
		},
	}
	if hasUsage {
		openaiResp.Usage = finalUsage
	}

	openaiRespBytes, err := json.Marshal(openaiResp)
	if err != nil {
		return resp, err
	}

	var translateParam any
	translatedNonStream := sdktranslator.TranslateNonStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, openaiRespBytes, &translateParam)

	resp = cliproxyexecutor.Response{
		Payload: translatedNonStream,
		Headers: res.Headers,
	}
	return resp, nil
}

func (e *TraeExecutor) buildTraeRequest(
	auth *cliproxyauth.Auth,
	creds *traeauth.TraeCredentials,
	protocol string,
	upstreamModel string,
	openaiReq []byte,
	messages []gjson.Result,
	isToolCommit bool,
	toolMessages []gjson.Result,
	opts cliproxyexecutor.Options,
) (*traeRequestBuildResult, error) {
	if isToolCommit && len(toolMessages) > 0 {
		return buildTraeToolCommitRequest(creds, toolMessages)
	}
	if protocol == traeProtocolV1 || protocol == traeProtocolV2 {
		return buildTraeRawChatRequest(protocol, upstreamModel, openaiReq, opts)
	}
	return e.buildTraeV3CreateTaskRequest(auth, creds, upstreamModel, openaiReq, messages, opts)
}
