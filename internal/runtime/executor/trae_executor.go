package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	traetranslator "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/trae"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	traeenc "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/trae"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type traeToolState struct {
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	TaskID         string `json:"task_id"`
	AgentRunID     string `json:"agent_run_id"`
	NativeID       string `json:"native_id"`
	Name           string `json:"name"`
}

func encodeTraeToolID(state traeToolState) (string, error) {
	b, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	return "trae_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeTraeToolID(id string) (traeToolState, error) {
	var state traeToolState
	raw := strings.TrimPrefix(id, "trae_")
	if raw == id {
		return state, fmt.Errorf("invalid trae tool_call_id prefix")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return state, err
	}
	if state.SessionID == "" || state.ConversationID == "" || state.TaskID == "" || state.AgentRunID == "" || state.NativeID == "" {
		return state, fmt.Errorf("incomplete trae tool state")
	}
	return state, nil
}

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
}

const (
	traeProtocolV1    = "v1"
	traeProtocolV2    = "v2"
	traeProtocolV3    = "v3"
	traeProtocolMeta  = "trae_protocol"
	traeModelNameMeta = "trae_model_name"
	traeConfigMeta    = "trae_config_name"
	traeModelListURL  = "https://trae-api-cn.mchost.guru/api/ide/v1/model_list?type=llm_raw_chat"
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

// FetchModels fetches the Trae models available to the current auth.
func (e *TraeExecutor) FetchModels(ctx context.Context, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, traeModelListURL, nil)
	if err != nil {
		return nil, err
	}
	setTraeCommonHeaders(httpReq.Header, creds)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("trae executor: close model list response body error: %v", errClose)
		}
	}()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("model list API error (%d): %s", httpResp.StatusCode, string(b))
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	models := parseTraeModels(data, time.Now().Unix())
	models = appendTraeNoThinkingModel(models, time.Now().Unix())
	models = appendTraeV3AgentModels(models, time.Now().Unix())
	return models, nil
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

type openaiDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          string           `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
}

type openaiToolCall struct {
	Index    int            `json:"index"`
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"`
	Function openaiFunction `json:"function,omitempty"`
}

type openaiFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openaiChoice struct {
	Index        int         `json:"index"`
	Delta        openaiDelta `json:"delta"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

type openaiChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openaiChoice `json:"choices"`
	Usage   *openaiUsage   `json:"usage,omitempty"`
}

type openaiUsage struct {
	PromptTokens            int64                    `json:"prompt_tokens"`
	CompletionTokens        int64                    `json:"completion_tokens"`
	TotalTokens             int64                    `json:"total_tokens"`
	PromptTokensDetails     *openaiPromptDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *openaiCompletionDetails `json:"completion_tokens_details,omitempty"`
}

type openaiPromptDetails struct {
	CachedTokens int64 `json:"cached_tokens,omitempty"`
}

type openaiCompletionDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`
}

func (e *TraeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	// Aggregate standard streaming chunks for non-streaming calls
	streamOpts := opts
	streamOpts.Stream = true

	res, err := e.ExecuteStream(ctx, auth, req, streamOpts)
	if err != nil {
		return resp, err
	}

	var aggregatedContent strings.Builder
	var aggregatedReasoning strings.Builder
	var toolCalls []openaiToolCall
	var finalModel string
	var chatID string
	var finalUsage openaiUsage
	var hasUsage bool

	// Standard stream translator parameter
	openaiFormat := sdktranslator.FromString("openai")
	from := opts.SourceFormat

	if res != nil {
		var parseParam any
		for chunk := range res.Chunks {
			if chunk.Err != nil {
				return resp, chunk.Err
			}
			// Translate chunk back to standard OpenAI
			openaiChunks := sdktranslator.TranslateStream(ctx, from, openaiFormat, req.Model, opts.OriginalRequest, nil, chunk.Payload, &parseParam)
			for _, oc := range openaiChunks {
				dataStr := string(oc)
				if strings.HasPrefix(dataStr, "data:") {
					dataStr = strings.TrimSpace(strings.TrimPrefix(dataStr, "data:"))
				}
				if dataStr == "[DONE]" || !gjson.Valid(dataStr) {
					continue
				}

				if id := gjson.Get(dataStr, "id").String(); id != "" && chatID == "" {
					chatID = id
				}
				if model := gjson.Get(dataStr, "model").String(); model != "" {
					finalModel = model
				}

				if contentVal := gjson.Get(dataStr, "choices.0.delta.content"); contentVal.Exists() {
					aggregatedContent.WriteString(contentVal.String())
				}
				if reasoningVal := gjson.Get(dataStr, "choices.0.delta.reasoning_content"); reasoningVal.Exists() {
					aggregatedReasoning.WriteString(reasoningVal.String())
				}
				if usageVal := gjson.Get(dataStr, "usage"); usageVal.Exists() {
					finalUsage = openAIUsageFromResult(usageVal)
					hasUsage = true
				}

				if tcVal := gjson.Get(dataStr, "choices.0.delta.tool_calls"); tcVal.Exists() && tcVal.IsArray() {
					for _, tc := range tcVal.Array() {
						toolCalls = append(toolCalls, openaiToolCall{
							Index: int(tc.Get("index").Int()),
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
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
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

type TraeToolCallResult struct {
	AgentRunID           string `json:"agent_run_id"`
	ToolCallID           string `json:"toolcall_id"`
	ToolCallName         string `json:"toolcall_name"`
	ToolCallResp         string `json:"toolcall_resp"`
	ToolCallStatus       string `json:"toolcall_status"`
	ToolCallErrorMessage string `json:"toolcall_error_message"`
	IsTruncated          *bool  `json:"is_truncated"`
}

type TraeCommitPayload struct {
	ConversationID  string               `json:"conversation_id"`
	TaskID          string               `json:"task_id"`
	UserID          string               `json:"user_id"`
	ToolcallResults []TraeToolCallResult `json:"toolcall_results"`
	ExtraContext    any                  `json:"extra_context"`
	RequestSeq      int                  `json:"request_seq"`
	QueueID         any                  `json:"queue_id"`
	AccessType      int                  `json:"access_type"`
	IsRemoteReq     bool                 `json:"is_remote_req"`
}

func (e *TraeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		return nil, fmt.Errorf("trae auth check failed: %w", err)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	protocol, upstreamModel := resolveTraeProtocol(baseModel, opts.Metadata)
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	from := opts.SourceFormat
	openaiFormat := sdktranslator.FromString("openai")
	openaiReq := sdktranslator.TranslateRequest(from, openaiFormat, upstreamModel, req.Payload, true)
	messages := gjson.GetBytes(openaiReq, "messages").Array()

	isToolCommit := false
	var toolMessages []gjson.Result
	if protocol == traeProtocolV3 {
		for i := len(messages) - 1; i >= 0; i-- {
			role := messages[i].Get("role").String()
			if role == "tool" {
				toolMessages = append([]gjson.Result{messages[i]}, toolMessages...)
				isToolCommit = true
			} else if isToolCommit {
				break
			}
		}
	}

	build, err := e.buildTraeRequest(auth, creds, protocol, upstreamModel, openaiReq, messages, isToolCommit, toolMessages, opts)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, build.TargetURL, bytes.NewReader(build.RequestBody))
	if err != nil {
		return nil, err
	}

	// Request settings matching reverse-engineered headers
	httpReq.Header.Set("Content-Type", "application/json")
	setTraeCommonHeaders(httpReq.Header, creds)
	httpReq.Header.Set("X-Ide-Session-Id", build.SessionID)
	httpReq.Header.Set("X-Request-Pin", build.RequestPin)
	httpReq.Header.Set("X-Requested-At", strconv.FormatInt(build.RequestAt, 10))
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	for name, values := range build.ExtraHeaders {
		for _, value := range values {
			httpReq.Header.Set(name, value)
		}
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       build.TargetURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      build.LogBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("trae executor: close response body error: %v", errClose)
		}
		return nil, traeStatusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("trae executor: close response body error: %v", errClose)
			}
		}()

		chatID := fmt.Sprintf("chatcmpl-%s", strings.ReplaceAll(uuid.New().String(), "-", ""))

		// First, yield standard opening OpenAI assistant role delta
		initOpenAIChunk := openaiChunk{
			ID:      chatID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   req.Model,
			Choices: []openaiChoice{
				{
					Index: 0,
					Delta: openaiDelta{
						Role: "assistant",
					},
				},
			},
		}

		initJSON, _ := json.Marshal(initOpenAIChunk)
		initChunk := []byte("data: " + string(initJSON))

		var translateParam any
		firstTranslated := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, initChunk, &translateParam)
		for _, ft := range firstTranslated {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: ft}:
			case <-ctx.Done():
				return
			}
		}

		scanner := bufio.NewScanner(httpResp.Body)
		taskID := "unknown"
		agentRunID := "unknown"
		tcIndex := 0
		hasToolCall := false
		finishReason := "stop"

		var currentEvent string
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)

			trimmedLine := bytes.TrimSpace(line)
			if len(trimmedLine) == 0 {
				continue
			}

			if bytes.HasPrefix(trimmedLine, []byte("event:")) {
				currentEvent = strings.TrimSpace(string(bytes.TrimPrefix(trimmedLine, []byte("event:"))))
				continue
			}

			if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
				continue
			}

			dataBytes := bytes.TrimSpace(bytes.TrimPrefix(trimmedLine, []byte("data:")))
			if len(dataBytes) == 0 {
				continue
			}

			dataStr := string(dataBytes)
			if !gjson.Valid(dataStr) {
				continue
			}

			evt := currentEvent
			currentEvent = ""

			if evt == "error" {
				message := firstNonEmpty(
					gjson.Get(dataStr, "message").String(),
					gjson.Get(dataStr, "error").String(),
					dataStr,
				)
				code := gjson.Get(dataStr, "code").String()
				if code != "" {
					message = fmt.Sprintf("trae error event %s: %s", code, message)
				} else {
					message = fmt.Sprintf("trae error event: %s", message)
				}
				helps.RecordAPIResponseError(ctx, e.cfg, errors.New(message))
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: traeStatusErr{code: http.StatusBadGateway, msg: message}}:
				case <-ctx.Done():
				}
				return
			}

			if evt == "task_created" {
				if tID := gjson.Get(dataStr, "task_id").String(); tID != "" {
					taskID = tID
				}
				if aRunID := gjson.Get(dataStr, "agent_run_id").String(); aRunID != "" {
					agentRunID = aRunID
				}
			}

			content := ""
			if val := gjson.Get(dataStr, "choices.0.delta.content"); val.Exists() && val.Type == gjson.String {
				content = val.String()
			} else if val := gjson.Get(dataStr, "response"); val.Exists() && val.Type == gjson.String {
				content = val.String()
			} else if val := gjson.Get(dataStr, "content"); val.Exists() && val.Type == gjson.String {
				content = val.String()
			} else if val := gjson.Get(dataStr, "text"); val.Exists() && val.Type == gjson.String {
				content = val.String()
			}

			reasoning := ""
			if val := gjson.Get(dataStr, "choices.0.delta.reasoning_content"); val.Exists() && val.Type == gjson.String {
				reasoning = val.String()
			} else if val := gjson.Get(dataStr, "reasoning_content"); val.Exists() && val.Type == gjson.String {
				reasoning = val.String()
			}
			if evt == "thought" {
				if val := gjson.Get(dataStr, "thought"); val.Exists() && val.Type == gjson.String {
					content += val.String()
				}
			}

			if evt == "token_usage" {
				usageData := openAIUsageFromResult(gjson.Parse(dataStr))
				reporter.Publish(ctx, usageDetailFromOpenAIUsage(usageData))
				usageOpenAIChunk := openaiChunk{
					ID:      chatID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []openaiChoice{},
					Usage:   &usageData,
				}
				usageJSON, _ := json.Marshal(usageOpenAIChunk)
				usageChunkBytes := []byte("data: " + string(usageJSON))
				translatedChunks := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, usageChunkBytes, &translateParam)
				for _, tc := range translatedChunks {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: tc}:
					case <-ctx.Done():
						return
					}
				}
				continue
			}

			if evt == "agent_event" || gjson.Get(dataStr, "event").String() == "agent_event" {
				payloadVal := gjson.Get(dataStr, "payload")
				if payloadVal.Exists() {
					payloadStr := payloadVal.String()
					if gjson.Valid(payloadStr) {
						if msgVal := gjson.Get(payloadStr, "message"); msgVal.Exists() && msgVal.Type == gjson.String {
							content += msgVal.String()
						}
					}
				}
			}

			toolName := ""
			toolPayload := ""
			nativeToolCallID := ""
			if evt == "tool_call" || gjson.Get(dataStr, "tool_name").Exists() {
				toolName = firstNonEmpty(
					gjson.Get(dataStr, "tool_name").String(),
					gjson.Get(dataStr, "toolcall_name").String(),
				)
				toolPayload = firstNonEmpty(
					gjson.Get(dataStr, "arguments").String(),
					gjson.Get(dataStr, "toolcall_payload").String(),
				)
				nativeToolCallID = gjson.Get(dataStr, "toolcall_id").String()
			}
			if val := gjson.Get(dataStr, "finish_reason"); val.Exists() && val.String() != "" {
				finishReason = val.String()
			}

			if content != "" || reasoning != "" || toolName != "" {
				delta := openaiDelta{
					Content:          content,
					ReasoningContent: reasoning,
				}

				if toolName != "" {
					hasToolCall = true
					state := traeToolState{
						SessionID:      build.SessionID,
						ConversationID: build.ConversationID,
						TaskID:         taskID,
						AgentRunID:     agentRunID,
						NativeID:       nativeToolCallID,
						Name:           toolName,
					}
					encodedID, _ := encodeTraeToolID(state)
					delta.ToolCalls = []openaiToolCall{
						{
							Index: tcIndex,
							ID:    encodedID,
							Type:  "function",
							Function: openaiFunction{
								Name:      toolName,
								Arguments: toolPayload,
							},
						},
					}
					tcIndex++
				}

				syntheticOpenAIChunk := openaiChunk{
					ID:      chatID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []openaiChoice{
						{
							Index: 0,
							Delta: delta,
						},
					},
				}

				chunkJSON, _ := json.Marshal(syntheticOpenAIChunk)
				chunkBytes := []byte("data: " + string(chunkJSON))

				translatedChunks := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, chunkBytes, &translateParam)
				for _, tc := range translatedChunks {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: tc}:
					case <-ctx.Done():
						return
					}
				}
			}
		}

		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
			return
		}

		// Stream termination chunk
		if hasToolCall {
			finishReason = "tool_calls"
		}

		terminalOpenAIChunk := openaiChunk{
			ID:      chatID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   req.Model,
			Choices: []openaiChoice{
				{
					Index:        0,
					Delta:        openaiDelta{},
					FinishReason: finishReason,
				},
			},
		}

		terminalJSON, _ := json.Marshal(terminalOpenAIChunk)
		terminalChunkBytes := []byte("data: " + string(terminalJSON))

		translatedChunks := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, terminalChunkBytes, &translateParam)
		for _, tc := range translatedChunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: tc}:
			case <-ctx.Done():
				return
			}
		}

		// Standard [DONE] block
		doneChunks := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, []byte("data: [DONE]"), &translateParam)
		for _, dc := range doneChunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: dc}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
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
	return e.buildTraeV3CreateTaskRequest(auth, creds, upstreamModel, messages, opts)
}

func buildTraeToolCommitRequest(creds *traeauth.TraeCredentials, toolMessages []gjson.Result) (*traeRequestBuildResult, error) {
	var toolcallResults []TraeToolCallResult
	var firstState traeToolState
	for idx, tm := range toolMessages {
		tcID := tm.Get("tool_call_id").String()
		state, err := decodeTraeToolID(tcID)
		if err != nil {
			return nil, fmt.Errorf("decode tool call id: %w", err)
		}
		if idx == 0 {
			firstState = state
		}
		toolcallResults = append(toolcallResults, TraeToolCallResult{
			AgentRunID:           state.AgentRunID,
			ToolCallID:           state.NativeID,
			ToolCallName:         tm.Get("name").String(),
			ToolCallResp:         tm.Get("content").String(),
			ToolCallStatus:       "success",
			ToolCallErrorMessage: "",
			IsTruncated:          nil,
		})
	}

	commitPayload := TraeCommitPayload{
		ConversationID:  firstState.ConversationID,
		TaskID:          firstState.TaskID,
		UserID:          creds.UserID,
		ToolcallResults: toolcallResults,
		RequestSeq:      1,
		IsRemoteReq:     false,
	}
	plainBytes, err := json.Marshal(commitPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal trae tool commit payload: %w", err)
	}
	encrypted, err := traeenc.EncryptMessage(plainBytes)
	if err != nil {
		return nil, fmt.Errorf("encrypt trae tool commit payload: %w", err)
	}
	return &traeRequestBuildResult{
		TargetURL:      "https://trae-api-cn.mchost.guru/api/agent/v3/commit_toolcall_result",
		RequestBody:    []byte(encrypted.Message),
		LogBody:        plainBytes,
		RequestPin:     encrypted.RequestPin,
		RequestAt:      encrypted.RequestAt,
		SessionID:      firstState.SessionID,
		ConversationID: firstState.ConversationID,
		Protocol:       traeProtocolV3,
		IsToolCommit:   true,
	}, nil
}

func (e *TraeExecutor) buildTraeV3CreateTaskRequest(
	auth *cliproxyauth.Auth,
	creds *traeauth.TraeCredentials,
	upstreamModel string,
	messages []gjson.Result,
	opts cliproxyexecutor.Options,
) (*traeRequestBuildResult, error) {
	userPrompt := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Get("role").String() == "user" {
			userPrompt = openAIMessageText(messages[i])
			break
		}
	}

	activeSessionID := strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
	activeConvID := strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
	modelConfig := traetranslator.ResolveModelConfig(upstreamModel)
	if modelName := metadataString(opts.Metadata, traeModelNameMeta); modelName != "" {
		modelConfig.ModelName = modelName
	}
	if configName := metadataString(opts.Metadata, traeConfigMeta); configName != "" {
		modelConfig.ConfigName = configName
	}

	workspacePath := traeauth.WorkspacePathFromAuth(auth, "")
	if workspacePath == "" {
		if pwd, errWd := os.Getwd(); errWd == nil {
			workspacePath = pwd
		} else {
			workspacePath = "C:\\Workspace\\Personal"
		}
	}

	trans, err := e.getTranslator()
	if err != nil {
		return nil, err
	}
	plainBytes, err := trans.BuildV3CreateTaskPayload(
		modelConfig.ModelName,
		modelConfig.ConfigName,
		userPrompt,
		activeSessionID,
		activeConvID,
		creds.UserID,
		creds.DeviceID,
		workspacePath,
	)
	if err != nil {
		return nil, fmt.Errorf("build trae task payload: %w", err)
	}
	encrypted, err := traeenc.EncryptMessage(plainBytes)
	if err != nil {
		return nil, fmt.Errorf("encrypt trae task payload: %w", err)
	}
	return &traeRequestBuildResult{
		TargetURL:      "https://trae-api-cn.mchost.guru/api/agent/v3/create_agent_task",
		RequestBody:    []byte(encrypted.Message),
		LogBody:        plainBytes,
		RequestPin:     encrypted.RequestPin,
		RequestAt:      encrypted.RequestAt,
		SessionID:      activeSessionID,
		ConversationID: activeConvID,
		Protocol:       traeProtocolV3,
	}, nil
}

func buildTraeRawChatRequest(protocol, upstreamModel string, openaiReq []byte, opts cliproxyexecutor.Options) (*traeRequestBuildResult, error) {
	modelConfig := traetranslator.ResolveRawChatModelConfig(upstreamModel, protocol)
	if modelName := metadataString(opts.Metadata, traeModelNameMeta); modelName != "" {
		modelConfig.ModelName = modelName
	}
	if configName := metadataString(opts.Metadata, traeConfigMeta); configName != "" {
		modelConfig.ConfigName = configName
	}

	innerPayload := buildTraeRawChatInnerPayload(openaiReq, protocol)
	innerBytes, err := json.Marshal(innerPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal trae raw chat payload: %w", err)
	}
	encrypted, err := traeenc.EncryptMessage(innerBytes)
	if err != nil {
		return nil, fmt.Errorf("encrypt trae raw chat payload: %w", err)
	}

	sessionID := strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
	convID := strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
	var targetURL string
	var requestBody []byte
	extraHeaders := make(http.Header)
	if protocol == traeProtocolV1 {
		targetURL = "https://trae-api-cn.mchost.guru/api/ide/v1/llm_raw_chat"
		requestBody, err = json.Marshal(map[string]any{
			"model_name": modelConfig.ModelName,
			"message":    encrypted.Message,
		})
	} else {
		targetURL = "https://trae-api-cn.mchost.guru/api/ide/v2/llm_raw_chat"
		extraHeaders.Set("X-App-Function", "utils")
		extraHeaders.Set("X-Ide-Function", "utils")
		extraHeaders.Set("x-ide-version-code", "20260401")
		tools := any(nil)
		if rawTools := gjson.GetBytes(openaiReq, "tools"); rawTools.Exists() {
			tools = json.RawMessage(rawTools.Raw)
		}
		requestBody, err = json.Marshal(map[string]any{
			"model_name":      modelConfig.ModelName,
			"config_name":     modelConfig.ConfigName,
			"config_source":   1,
			"messages":        []any{},
			"tools":           tools,
			"session_id":      sessionID,
			"conversation_id": convID,
			"message":         encrypted.Message,
		})
	}
	if err != nil {
		return nil, fmt.Errorf("marshal trae %s raw chat envelope: %w", protocol, err)
	}

	return &traeRequestBuildResult{
		TargetURL:      targetURL,
		RequestBody:    requestBody,
		LogBody:        innerBytes,
		RequestPin:     encrypted.RequestPin,
		RequestAt:      encrypted.RequestAt,
		SessionID:      sessionID,
		ConversationID: convID,
		Protocol:       protocol,
		ExtraHeaders:   extraHeaders,
	}, nil
}

func buildTraeRawChatInnerPayload(openaiReq []byte, protocol string) any {
	messages := buildTraeRawChatMessages(openaiReq)
	if protocol != traeProtocolV1 {
		return messages
	}
	if rawTools := gjson.GetBytes(openaiReq, "tools"); rawTools.Exists() && rawTools.IsArray() && len(rawTools.Array()) > 0 {
		return map[string]any{
			"messages": messages,
			"tools":    json.RawMessage(rawTools.Raw),
		}
	}
	return messages
}

func buildTraeRawChatMessages(openaiReq []byte) []map[string]any {
	openAIMessages := gjson.GetBytes(openaiReq, "messages").Array()
	messages := make([]map[string]any, 0, len(openAIMessages))
	for _, msg := range openAIMessages {
		role := firstNonEmpty(msg.Get("role").String(), "user")
		if role == "tool" {
			role = "user"
		}
		text := openAIMessageText(msg)
		messages = append(messages, map[string]any{
			"role": role,
			"content": []map[string]string{
				{
					"type": "text",
					"text": text,
				},
			},
		})
	}
	return messages
}

func openAIMessageText(message gjson.Result) string {
	content := message.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var builder strings.Builder
		for _, part := range content.Array() {
			text := firstNonEmpty(
				part.Get("text").String(),
				part.Get("text_content").String(),
				part.Get("input_text").String(),
			)
			if text == "" {
				continue
			}
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(text)
		}
		return builder.String()
	}
	return content.String()
}

func resolveTraeProtocol(model string, metadata map[string]any) (string, string) {
	model = strings.TrimSpace(model)
	if protocol := normalizeTraeProtocol(metadataString(metadata, traeProtocolMeta)); protocol != "" {
		return protocol, stripTraeProtocolPrefix(model)
	}

	// Strip the case-insensitive provider prefix "trae/" if present
	if stripped, ok := stripCaseInsensitivePrefix(model, "trae/"); ok {
		model = stripped
	}

	for _, candidate := range []struct {
		prefix   string
		protocol string
	}{
		{"trae-v1/", traeProtocolV1},
		{"raw-v1/", traeProtocolV1},
		{"v1/", traeProtocolV1},
		{"trae-v2/", traeProtocolV2},
		{"raw-v2/", traeProtocolV2},
		{"v2/", traeProtocolV2},
		{"trae-v3/", traeProtocolV3},
		{"agent/", traeProtocolV3},
		{"v3/", traeProtocolV3},
	} {
		if stripped, ok := stripCaseInsensitivePrefix(model, candidate.prefix); ok {
			return candidate.protocol, stripped
		}
	}
	if isTraeV1RawChatModel(model) {
		return traeProtocolV1, model
	}
	if isTraeV2RawChatModel(model) {
		return traeProtocolV2, model
	}
	return traeProtocolV3, model
}

func isTraeV1RawChatModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "seed_m8", "deepseek-r1", "deepseek-v3", "deepseek-v3-0324":
		return true
	default:
		return false
	}
}

func isTraeV2RawChatModel(model string) bool {
	key := strings.ToLower(strings.TrimSpace(model))
	return key == "no_thinking_model"
}

func stripTraeProtocolPrefix(model string) string {
	for _, prefix := range []string{"trae-v1/", "raw-v1/", "v1/", "trae-v2/", "raw-v2/", "v2/", "trae-v3/", "agent/", "v3/"} {
		if stripped, ok := stripCaseInsensitivePrefix(model, prefix); ok {
			return stripped
		}
	}
	return model
}

func stripCaseInsensitivePrefix(value, prefix string) (string, bool) {
	if len(value) < len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		return value, false
	}
	return value[len(prefix):], true
}

func normalizeTraeProtocol(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "1", "v1", "raw-v1", "llm_raw_chat_v1":
		return traeProtocolV1
	case "2", "v2", "raw-v2", "llm_raw_chat", "llm_raw_chat_v2":
		return traeProtocolV2
	case "3", "v3", "agent", "builder", "builder_v3", "create_agent_task":
		return traeProtocolV3
	default:
		return ""
	}
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func setTraeCommonHeaders(header http.Header, creds *traeauth.TraeCredentials) {
	header.Set("Authorization", "Cloud-IDE-JWT "+creds.JWTToken)
	header.Set("X-App-Id", "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8")
	header.Set("x-app-version", "default")
	header.Set("x-ide-version-code", "20260508")
	header.Set("x-app-version-code", "20260401")
	header.Set("x-device-brand", "Lenovo")
	header.Set("x-device-cpu", "AMD")
	header.Set("x-device-id", creds.DeviceID)
	header.Set("x-machine-id", creds.MachineID)
	header.Set("x-os-version", "Linux")
	header.Set("x-device-type", "linux")
	header.Set("x-ide-version", "3.3.55")
	header.Set("x-ide-version-type", "stable")
	header.Set("request-traffic-type", "prod")
	header.Set("get-svc", "1")
}

func parseTraeModels(data []byte, now int64) []*registry.ModelInfo {
	root := gjson.ParseBytes(data)
	configs := root.Get("model_configs")
	if !configs.Exists() {
		configs = root.Get("data.model_configs")
	}
	if !configs.Exists() || !configs.IsArray() {
		return nil
	}

	models := make([]*registry.ModelInfo, 0, len(configs.Array()))
	seen := make(map[string]struct{})
	for _, item := range configs.Array() {
		if status := item.Get("status"); status.Exists() && !status.Bool() {
			continue
		}
		id := strings.TrimSpace(item.Get("name").String())
		if id == "" {
			continue
		}
		if strings.EqualFold(id, "Doubao_1_5_thinking_pro") {
			continue
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		displayName := firstNonEmpty(item.Get("display_name").String(), item.Get("displayName").String(), id)
		contextLength := int(item.Get("prompt_max_tokens").Int())
		if contextLength <= 0 {
			contextLength = int(item.Get("context_length").Int())
		}
		model := &registry.ModelInfo{
			ID:                  id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                "trae",
			DisplayName:         displayName,
			Name:                id,
			ContextLength:       contextLength,
			MaxCompletionTokens: 65536,
			SupportedParameters: []string{"tools"},
		}
		models = append(models, model)
	}
	return models
}

func appendTraeNoThinkingModel(models []*registry.ModelInfo, now int64) []*registry.ModelInfo {
	for _, model := range models {
		if model != nil && strings.EqualFold(strings.TrimSpace(model.ID), "no_thinking_model") {
			return models
		}
	}
	return append(models, &registry.ModelInfo{
		ID:                  "no_thinking_model",
		Object:              "model",
		Created:             now,
		OwnedBy:             "trae",
		Type:                "trae",
		DisplayName:         "Trae No Thinking Model",
		Name:                "no_thinking_model",
		ContextLength:       40000,
		MaxCompletionTokens: 65536,
		SupportedParameters: []string{"tools"},
	})
}

func appendTraeV3AgentModels(models []*registry.ModelInfo, now int64) []*registry.ModelInfo {
	v3Models := []struct {
		id          string
		displayName string
		context     int
	}{
		{"glm-5", "GLM-5", 16000},
		{"glm-5.1", "GLM-5.1", 16000},
		{"DeepSeek-V4-Pro", "DeepSeek V4 Pro", 16000},
		{"DeepSeek-V4-Flash", "DeepSeek V4 Flash", 16000},
		{"kimi-k2.6", "Kimi K2.6", 16000},
		{"qwen-3.6-plus", "Qwen 3.6 Plus", 16000},
	}
	existing := make(map[string]struct{}, len(models))
	for _, m := range models {
		if m != nil {
			existing[strings.ToLower(strings.TrimSpace(m.ID))] = struct{}{}
		}
	}
	for _, v := range v3Models {
		if _, ok := existing[strings.ToLower(v.id)]; ok {
			continue
		}
		models = append(models, &registry.ModelInfo{
			ID:                  v.id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                "trae",
			DisplayName:         v.displayName,
			Name:                v.id,
			ContextLength:       v.context,
			MaxCompletionTokens: 65536,
			SupportedParameters: []string{"tools"},
		})
	}
	return models
}

func openAIUsageFromResult(result gjson.Result) openaiUsage {
	promptTokens := traeUsageInt(result, "prompt_tokens")
	completionTokens := traeUsageInt(result, "completion_tokens")
	totalTokens := traeUsageInt(result, "total_tokens")
	if totalTokens == 0 {
		totalTokens = promptTokens + completionTokens
	}

	cacheReadTokens := traeUsageInt(result, "cache_read_input_tokens")
	cacheCreationTokens := traeUsageInt(result, "cache_creation_input_tokens")
	reasoningTokens := traeUsageInt(result, "reasoning_tokens")

	usageData := openaiUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
	}
	if cachedTokens := cacheReadTokens + cacheCreationTokens; cachedTokens > 0 {
		usageData.PromptTokensDetails = &openaiPromptDetails{CachedTokens: cachedTokens}
	}
	if reasoningTokens > 0 {
		usageData.CompletionTokensDetails = &openaiCompletionDetails{ReasoningTokens: reasoningTokens}
	}
	return usageData
}

func usageDetailFromOpenAIUsage(usageData openaiUsage) cliproxyusage.Detail {
	detail := cliproxyusage.Detail{
		InputTokens:  usageData.PromptTokens,
		OutputTokens: usageData.CompletionTokens,
		TotalTokens:  usageData.TotalTokens,
	}
	if usageData.PromptTokensDetails != nil {
		detail.CachedTokens = usageData.PromptTokensDetails.CachedTokens
	}
	if usageData.CompletionTokensDetails != nil {
		detail.ReasoningTokens = usageData.CompletionTokensDetails.ReasoningTokens
	}
	return detail
}

func traeUsageInt(result gjson.Result, key string) int64 {
	if val := result.Get(key); val.Exists() {
		return val.Int()
	}
	if val := result.Get(key + "_total"); val.Exists() {
		return val.Int()
	}
	return 0
}
