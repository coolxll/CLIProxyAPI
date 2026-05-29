package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// FetchModels fetches the Trae models available to the current auth.
func (e *TraeExecutor) FetchModels(ctx context.Context, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		return nil, err
	}
	e.replaceTraeDetailModelConfigs(auth, nil)

	now := time.Now().Unix()
	models, err := e.fetchModelsFromDetailParam(ctx, creds, auth, now)
	if err != nil {
		log.Warnf("trae get_detail_param failed, falling back to model_list: %v", err)
		models, err = e.fetchModelsFromModelList(ctx, creds, auth, now)
		if err != nil {
			return nil, err
		}
	}
	models = appendTraeNoThinkingModel(models, now)
	return models, nil
}

func (e *TraeExecutor) fetchModelsFromDetailParam(ctx context.Context, creds *traeauth.TraeCredentials, auth *cliproxyauth.Auth, now int64) ([]*registry.ModelInfo, error) {
	body := []byte(`{"function":"chat_v3","config_names":null,"need_prompt":false,"current_config_info":null,"poly_prompt":true,"mode_type":null,"agent_type":"builder_v3","ab_force_vids":null,"ab_autotest_advanced_mode":null,"access_type":0}`)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, traeDetailParamURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	setTraeCommonHeaders(httpReq.Header, creds)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("trae executor: close detail param response body error: %v", errClose)
		}
	}()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("get_detail_param API error (%d): %s", httpResp.StatusCode, string(b))
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	models, configs := parseTraeDetailParamWithConfigs(data, now)
	if len(models) == 0 {
		return nil, fmt.Errorf("get_detail_param returned no usable chat_completion configs")
	}
	e.replaceTraeDetailModelConfigs(auth, configs)
	models = appendTraeV1RawChatModels(models, now)
	models = appendTraeV3AgentModels(models, now)
	return models, nil
}

func (e *TraeExecutor) fetchModelsFromModelList(ctx context.Context, creds *traeauth.TraeCredentials, auth *cliproxyauth.Auth, now int64) ([]*registry.ModelInfo, error) {
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
	models := parseTraeModels(data, now)
	models = appendTraeV3AgentModels(models, now)
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

type traeThoughtToolCall struct {
	Name      string
	Arguments string
}

type traeThoughtParseResult struct {
	Content   string
	ToolCalls []traeThoughtToolCall
}

type traeThoughtToolParser struct {
	buffer string
}

type traeInlineToolCallParser struct {
	buffer string
}

const traeThoughtToolMarker = "<tool_call>"
const traeInlineToolCallMarker = "tool_calls="
const traeRunCommandStartMarker = "<run_command>"
const traeRunCommandEndMarker = "</run_command>"
const maxTraeThoughtToolBuffer = 8192
const maxTraeInlineToolBuffer = 65536

func (p *traeThoughtToolParser) Append(chunk string) traeThoughtParseResult {
	p.buffer += chunk
	var result traeThoughtParseResult

	for {
		idx := strings.Index(p.buffer, traeThoughtToolMarker)
		if idx < 0 {
			flushLen := len(p.buffer) - trailingToolMarkerPrefixLen(p.buffer)
			if flushLen > 0 {
				result.Content += p.buffer[:flushLen]
				p.buffer = p.buffer[flushLen:]
			}
			return result
		}

		if idx > 0 {
			result.Content += p.buffer[:idx]
			p.buffer = p.buffer[idx:]
		}

		closeIdx := strings.Index(p.buffer, "/>")
		if closeIdx < 0 {
			if len(p.buffer) > maxTraeThoughtToolBuffer {
				flushLen := len(p.buffer) - trailingToolMarkerPrefixLen(p.buffer)
				if flushLen > 0 {
					result.Content += p.buffer[:flushLen]
					p.buffer = p.buffer[flushLen:]
				}
			}
			return result
		}

		markup := p.buffer[:closeIdx+len("/>")]
		toolCall, ok := parseTraeThoughtToolMarkup(markup)
		if !ok {
			result.Content += markup
			p.buffer = p.buffer[closeIdx+len("/>"):]
			continue
		}
		result.ToolCalls = append(result.ToolCalls, toolCall)
		p.buffer = p.buffer[closeIdx+len("/>"):]
	}
}

func (p *traeThoughtToolParser) Flush() string {
	content := p.buffer
	p.buffer = ""
	return content
}

func (p *traeInlineToolCallParser) Append(chunk string) traeThoughtParseResult {
	p.buffer += chunk
	var result traeThoughtParseResult

	for {
		idx, marker := nextTraeInlineToolMarker(p.buffer)
		if idx < 0 {
			flushLen := len(p.buffer) - trailingInlineToolMarkerPrefixLen(p.buffer)
			if flushLen > 0 {
				result.Content += p.buffer[:flushLen]
				p.buffer = p.buffer[flushLen:]
			}
			return result
		}

		if marker == traeRunCommandStartMarker {
			if idx > 0 {
				result.Content += p.buffer[:idx]
				p.buffer = p.buffer[idx:]
			}
			closeIdx := strings.Index(p.buffer, traeRunCommandEndMarker)
			if closeIdx < 0 {
				if len(p.buffer) > maxTraeInlineToolBuffer {
					result.Content += p.buffer
					p.buffer = ""
				}
				return result
			}
			markup := p.buffer[:closeIdx+len(traeRunCommandEndMarker)]
			toolCall, ok := parseTraeRunCommandMarkup(markup)
			if !ok {
				result.Content += markup
			} else {
				result.ToolCalls = append(result.ToolCalls, toolCall)
			}
			p.buffer = p.buffer[closeIdx+len(traeRunCommandEndMarker):]
			continue
		}

		arrayStart := idx + len(traeInlineToolCallMarker)
		for arrayStart < len(p.buffer) && (p.buffer[arrayStart] == ' ' || p.buffer[arrayStart] == '\t' || p.buffer[arrayStart] == '\n' || p.buffer[arrayStart] == '\r') {
			arrayStart++
		}
		if arrayStart >= len(p.buffer) {
			if len(p.buffer) > maxTraeInlineToolBuffer {
				result.Content += p.buffer
				p.buffer = ""
			}
			return result
		}
		if p.buffer[arrayStart] != '[' {
			result.Content += p.buffer[:idx+len(traeInlineToolCallMarker)]
			p.buffer = p.buffer[idx+len(traeInlineToolCallMarker):]
			continue
		}

		toolCalls, consumed, complete := parseTraeInlineToolCalls(p.buffer[arrayStart:])
		if !complete {
			if len(p.buffer) > maxTraeInlineToolBuffer {
				result.Content += p.buffer
				p.buffer = ""
			}
			return result
		}
		if len(toolCalls) == 0 {
			result.Content += p.buffer[:arrayStart+consumed]
			p.buffer = p.buffer[arrayStart+consumed:]
			continue
		}

		prefix := p.buffer[:idx]
		result.Content += stripInlineToolLabel(prefix, toolCalls[0].Name)
		result.ToolCalls = append(result.ToolCalls, toolCalls...)
		p.buffer = p.buffer[arrayStart+consumed:]
		for strings.HasPrefix(p.buffer, "]") {
			p.buffer = p.buffer[1:]
		}
	}
}

func (p *traeInlineToolCallParser) Flush() string {
	content := p.buffer
	p.buffer = ""
	return content
}

func nextTraeInlineToolMarker(s string) (int, string) {
	toolCallsIdx := strings.Index(s, traeInlineToolCallMarker)
	runCommandIdx := strings.Index(s, traeRunCommandStartMarker)
	switch {
	case toolCallsIdx < 0 && runCommandIdx < 0:
		return -1, ""
	case toolCallsIdx < 0:
		return runCommandIdx, traeRunCommandStartMarker
	case runCommandIdx < 0:
		return toolCallsIdx, traeInlineToolCallMarker
	case runCommandIdx < toolCallsIdx:
		return runCommandIdx, traeRunCommandStartMarker
	default:
		return toolCallsIdx, traeInlineToolCallMarker
	}
}

func trailingToolMarkerPrefixLen(s string) int {
	return trailingMarkerPrefixLen(s, traeThoughtToolMarker)
}

func trailingInlineToolMarkerPrefixLen(s string) int {
	return trailingMarkerPrefixLen(s, traeInlineToolCallMarker, traeRunCommandStartMarker)
}

func trailingMarkerPrefixLen(s string, markers ...string) int {
	maxLen := 0
	for _, marker := range markers {
		if marker == "" {
			continue
		}
		markerMax := len(marker) - 1
		if len(s) < markerMax {
			markerMax = len(s)
		}
		for n := markerMax; n > 0; n-- {
			if strings.HasSuffix(s, marker[:n]) && n > maxLen {
				maxLen = n
			}
		}
	}
	return maxLen
}

func parseTraeInlineToolCalls(raw string) ([]traeThoughtToolCall, int, bool) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	var items []json.RawMessage
	if err := decoder.Decode(&items); err != nil {
		if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "unexpected EOF") {
			return nil, 0, false
		}
		return nil, 0, true
	}

	toolCalls := make([]traeThoughtToolCall, 0, len(items))
	for _, item := range items {
		toolCall, ok := parseTraeInlineToolCall(item)
		if ok {
			toolCalls = append(toolCalls, toolCall)
		}
	}
	return toolCalls, int(decoder.InputOffset()), true
}

func parseTraeInlineToolCall(raw json.RawMessage) (traeThoughtToolCall, bool) {
	var obj struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Function  struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return traeThoughtToolCall{}, false
	}

	name := firstNonEmpty(obj.Name, obj.Function.Name)
	if name == "" {
		return traeThoughtToolCall{}, false
	}
	args := firstNonEmptyRaw(obj.Arguments, obj.Function.Arguments)
	arguments := "{}"
	if len(args) > 0 && string(args) != "null" {
		arguments = compactTraeToolArguments(args)
	}
	return traeThoughtToolCall{Name: name, Arguments: arguments}, true
}

func firstNonEmptyRaw(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		if len(value) > 0 && string(value) != "null" {
			return value
		}
	}
	return nil
}

func compactTraeToolArguments(raw json.RawMessage) string {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if strings.TrimSpace(asString) == "" {
			return "{}"
		}
		return asString
	}
	if compacted, ok := compactJSONBytes(raw); ok {
		return compacted
	}
	return string(raw)
}

func parseTraeRunCommandMarkup(markup string) (traeThoughtToolCall, bool) {
	type runCommand struct {
		Command string `xml:"command"`
	}
	var rc runCommand
	if err := xml.Unmarshal([]byte(markup), &rc); err != nil {
		return traeThoughtToolCall{}, false
	}
	command := strings.TrimSpace(rc.Command)
	if command == "" {
		return traeThoughtToolCall{}, false
	}
	argsBytes, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		return traeThoughtToolCall{}, false
	}
	return traeThoughtToolCall{
		Name:      "Bash",
		Arguments: string(argsBytes),
	}, true
}

func traeToolSignature(name, arguments string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		arguments = "{}"
	}
	if gjson.Valid(arguments) {
		if compacted, ok := compactJSONBytes([]byte(arguments)); ok {
			arguments = compacted
		}
	}
	return name + "\x00" + arguments
}

func compactJSONBytes(raw []byte) (string, bool) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err == nil {
		return buf.String(), true
	}
	return "", false
}

func stripInlineToolLabel(prefix, toolName string) string {
	if toolName == "" {
		return prefix
	}
	trimmed := strings.TrimRight(prefix, " \t\r\n")
	if !strings.HasSuffix(trimmed, toolName) {
		return prefix
	}
	labelStart := len(trimmed) - len(toolName)
	if labelStart > 0 {
		prev := trimmed[labelStart-1]
		if (prev >= 'A' && prev <= 'Z') || (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') || prev == '_' || prev == '-' {
			return prefix
		}
	}
	return trimmed[:labelStart]
}

func buildTraeToolShimInstructions(openaiReq []byte) string {
	tools := gjson.GetBytes(openaiReq, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return ""
	}

	var lines []string
	for _, tool := range tools.Array() {
		name := firstNonEmpty(
			tool.Get("function.name").String(),
			tool.Get("name").String(),
		)
		if name == "" {
			continue
		}
		description := firstNonEmpty(
			tool.Get("function.description").String(),
			tool.Get("description").String(),
			"No description",
		)
		parameters := firstNonEmpty(
			tool.Get("function.parameters").Raw,
			tool.Get("input_schema").Raw,
			`{"type":"object","properties":{}}`,
		)
		lines = append(lines, fmt.Sprintf("- %s: %s parameters=%s", name, description, parameters))
	}
	if len(lines) == 0 {
		return ""
	}

	return strings.Join(append([]string{
		"External tool protocol:",
		"The following client-provided tools are available. If the user asks for information that requires one of these tools, call the matching tool before answering.",
		"To call a tool, output exactly one line in this format and no other text: tool_calls=[{\"name\":\"tool_name\",\"arguments\":{\"key\":\"value\"}}]",
		"Declared tools:",
	}, lines...), "\n")
}

func parseTraeThoughtToolMarkup(markup string) (traeThoughtToolCall, bool) {
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(markup, traeThoughtToolMarker), "/>"))
	if inner == "" || strings.HasPrefix(inner, "<") {
		return traeThoughtToolCall{}, false
	}

	xmlSnippet := "<" + inner + "/>"
	decoder := xml.NewDecoder(strings.NewReader(xmlSnippet))
	for {
		token, err := decoder.Token()
		if err != nil {
			return traeThoughtToolCall{}, false
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		args := make(map[string]string, len(start.Attr))
		for _, attr := range start.Attr {
			args[attr.Name.Local] = attr.Value
		}
		argBytes, err := json.Marshal(args)
		if err != nil {
			return traeThoughtToolCall{}, false
		}
		return traeThoughtToolCall{
			Name:      start.Name.Local,
			Arguments: string(argBytes),
		}, true
	}
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
			// Translate chunk back to standard OpenAI
			openaiChunks := sdktranslator.TranslateStream(ctx, streamOpts.SourceFormat, openaiFormat, req.Model, streamOpts.OriginalRequest, nil, chunk.Payload, &parseParam)
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

func (e *TraeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	protocol, upstreamModel := resolveTraeProtocol(baseModel, opts.Metadata)
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	creds, err := traeauth.CredentialsFromAuth(auth)
	if err != nil {
		return nil, fmt.Errorf("trae auth check failed: %w", err)
	}

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
		thoughtToolIndex := 0
		inlineToolIndex := 0
		hasToolCall := false
		finishReason := "stop"
		var thoughtToolParser traeThoughtToolParser
		var inlineContentToolParser traeInlineToolCallParser
		var inlineReasoningToolParser traeInlineToolCallParser
		emittedToolSignatures := make(map[string]struct{})
		shouldEmitToolCall := func(name, arguments string) bool {
			signature := traeToolSignature(name, arguments)
			if signature == "" {
				return true
			}
			if _, exists := emittedToolSignatures[signature]; exists {
				return false
			}
			emittedToolSignatures[signature] = struct{}{}
			return true
		}
		buildToolCalls := func(parsed []traeThoughtToolCall, nativePrefix, logContext string, nextNativeIndex *int) []openaiToolCall {
			toolCalls := make([]openaiToolCall, 0, len(parsed))
			for _, toolCall := range parsed {
				if !shouldEmitToolCall(toolCall.Name, toolCall.Arguments) {
					continue
				}
				nativeID := fmt.Sprintf("%s-%d", nativePrefix, *nextNativeIndex)
				(*nextNativeIndex)++
				state := traeToolState{
					SessionID:      build.SessionID,
					ConversationID: build.ConversationID,
					TaskID:         taskID,
					AgentRunID:     agentRunID,
					NativeID:       nativeID,
					Name:           toolCall.Name,
				}
				encodedID, errEncode := encodeTraeToolID(state)
				if errEncode != nil {
					log.Errorf("trae executor: encode %s tool id error: %v", logContext, errEncode)
					continue
				}
				hasToolCall = true
				toolCalls = append(toolCalls, openaiToolCall{
					Index: tcIndex,
					ID:    encodedID,
					Type:  "function",
					Function: openaiFunction{
						Name:      toolCall.Name,
						Arguments: toolCall.Arguments,
					},
				})
				tcIndex++
			}
			return toolCalls
		}

		inQueue := false
		queueDone := make(chan struct{})
		var queueDoneOnce sync.Once
		var queueHeartbeatStarted atomic.Bool
		closeQueueHeartbeat := func() {
			queueDoneOnce.Do(func() {
				close(queueDone)
			})
		}
		defer closeQueueHeartbeat()

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
				streamErr := traeStatusErr{code: http.StatusBadGateway, msg: message}
				helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
				reporter.PublishFailure(ctx, streamErr)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
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

			if evt == "request_wait_in_queue" {
				inQueue = true
				if queueHeartbeatStarted.CompareAndSwap(false, true) {
					go func() {
						ticker := time.NewTicker(15 * time.Second)
						defer ticker.Stop()
						var heartbeatTranslateParam any
						for {
							select {
							case <-ticker.C:
								heartbeatChunk := openaiChunk{
									ID:      chatID,
									Object:  "chat.completion.chunk",
									Created: time.Now().Unix(),
									Model:   req.Model,
									Choices: []openaiChoice{
										{
											Index: 0,
											Delta: openaiDelta{},
										},
									},
								}
								heartbeatJSON, _ := json.Marshal(heartbeatChunk)
								heartbeatBytes := []byte("data: " + string(heartbeatJSON))
								translatedChunks := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, heartbeatBytes, &heartbeatTranslateParam)
								for _, tc := range translatedChunks {
									select {
									case out <- cliproxyexecutor.StreamChunk{Payload: tc}:
									case <-queueDone:
										return
									case <-ctx.Done():
										return
									}
								}
							case <-queueDone:
								return
							case <-ctx.Done():
								return
							}
						}
					}()
				}
				continue
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

			var toolCalls []openaiToolCall
			if evt == "thought" {
				if val := gjson.Get(dataStr, "thought"); val.Exists() && val.Type == gjson.String {
					parsed := thoughtToolParser.Append(val.String())
					content += parsed.Content
					toolCalls = append(toolCalls, buildToolCalls(parsed.ToolCalls, "thought", "thought", &thoughtToolIndex)...)
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

			if content != "" {
				parsed := inlineContentToolParser.Append(content)
				content = parsed.Content
				toolCalls = append(toolCalls, buildToolCalls(parsed.ToolCalls, "inline", "inline", &inlineToolIndex)...)
			}
			if reasoning != "" {
				parsed := inlineReasoningToolParser.Append(reasoning)
				reasoning = parsed.Content
				if strings.TrimSpace(reasoning) == "" {
					reasoning = ""
				}
				toolCalls = append(toolCalls, buildToolCalls(parsed.ToolCalls, "inline", "inline reasoning", &inlineToolIndex)...)
			}

			toolName := ""
			toolPayload := ""
			nativeToolCallID := ""
			if evt == "tool_call" || gjson.Get(dataStr, "tool_name").Exists() || gjson.Get(dataStr, "toolcall_name").Exists() {
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

			if inQueue && (content != "" || reasoning != "" || toolName != "" || len(toolCalls) > 0) {
				inQueue = false
				closeQueueHeartbeat()
			}

			if content != "" || reasoning != "" || toolName != "" || len(toolCalls) > 0 {
				delta := openaiDelta{
					Content:          content,
					ReasoningContent: reasoning,
					ToolCalls:        toolCalls,
				}

				if toolName != "" && !shouldEmitToolCall(toolName, toolPayload) {
					toolName = ""
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
					delta.ToolCalls = append(delta.ToolCalls, openaiToolCall{
						Index: tcIndex,
						ID:    encodedID,
						Type:  "function",
						Function: openaiFunction{
							Name:      toolName,
							Arguments: toolPayload,
						},
					})
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
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
			return
		}

		emitTrailingDelta := func(content, reasoning string) bool {
			if content == "" && reasoning == "" {
				return true
			}
			trailingOpenAIChunk := openaiChunk{
				ID:      chatID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   req.Model,
				Choices: []openaiChoice{
					{
						Index: 0,
						Delta: openaiDelta{
							Content:          content,
							ReasoningContent: reasoning,
						},
					},
				},
			}

			trailingJSON, _ := json.Marshal(trailingOpenAIChunk)
			trailingChunkBytes := []byte("data: " + string(trailingJSON))

			translatedChunks := sdktranslator.TranslateStream(ctx, openaiFormat, from, req.Model, opts.OriginalRequest, nil, trailingChunkBytes, &translateParam)
			for _, tc := range translatedChunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: tc}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}

		if !emitTrailingDelta(thoughtToolParser.Flush(), "") {
			return
		}
		if !emitTrailingDelta(inlineContentToolParser.Flush(), "") {
			return
		}
		if !emitTrailingDelta("", inlineReasoningToolParser.Flush()) {
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
		reporter.EnsurePublished(ctx)
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
	return e.buildTraeV3CreateTaskRequest(auth, creds, upstreamModel, openaiReq, messages, opts)
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
			ToolCallName:         firstNonEmpty(state.Name, tm.Get("name").String()),
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
	openaiReq []byte,
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
	if toolInstructions := buildTraeToolShimInstructions(openaiReq); toolInstructions != "" {
		userPrompt = toolInstructions + "\n\nUser request:\n" + userPrompt
	}

	activeSessionID := strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
	activeConvID := strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
	modelConfig := traetranslator.ResolveModelConfig(upstreamModel)
	if detailConfig, ok := e.traeDetailModelConfig(auth, upstreamModel); ok {
		modelConfig.ModelName = detailConfig.ModelName
		modelConfig.ConfigName = detailConfig.ConfigName
	}
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
		v1Envelope := map[string]any{
			"model_name": modelConfig.ModelName,
			"message":    encrypted.Message,
		}
		// V1 raw chat accepts tools as plaintext in the outer envelope.
		if rawTools := gjson.GetBytes(openaiReq, "tools"); rawTools.Exists() && rawTools.IsArray() && len(rawTools.Array()) > 0 {
			v1Envelope["tools"] = json.RawMessage(rawTools.Raw)
		}
		requestBody, err = json.Marshal(v1Envelope)
	} else {
		targetURL = "https://trae-api-cn.mchost.guru/api/ide/v2/llm_raw_chat"
		extraHeaders.Set("X-App-Function", "utils")
		extraHeaders.Set("X-Ide-Function", "utils")
		extraHeaders.Set("x-ide-version-code", "20260401")
		// V2 raw chat does not support tools; omit them from the outer envelope.
		requestBody, err = json.Marshal(map[string]any{
			"model_name":      modelConfig.ModelName,
			"config_name":     modelConfig.ConfigName,
			"config_source":   1,
			"messages":        []any{},
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
	// V1 and V2 raw chat do not support tools inside the encrypted payload.
	// V1 passes tools as plaintext in the outer envelope; V2 does not support tools at all.
	if protocol == traeProtocolV1 || protocol == traeProtocolV2 {
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

func (e *TraeExecutor) replaceTraeDetailModelConfigs(auth *cliproxyauth.Auth, configs map[string]traeDetailModelConfig) {
	authID := traeAuthID(auth)
	if authID == "" {
		return
	}
	e.detailConfigMu.Lock()
	defer e.detailConfigMu.Unlock()
	if len(configs) == 0 {
		delete(e.detailConfigs, authID)
		return
	}
	if e.detailConfigs == nil {
		e.detailConfigs = make(map[string]map[string]traeDetailModelConfig)
	}
	copied := make(map[string]traeDetailModelConfig, len(configs))
	for key, config := range configs {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || strings.TrimSpace(config.ModelName) == "" || strings.TrimSpace(config.ConfigName) == "" {
			continue
		}
		copied[key] = traeDetailModelConfig{
			ModelName:  strings.TrimSpace(config.ModelName),
			ConfigName: strings.TrimSpace(config.ConfigName),
		}
	}
	if len(copied) == 0 {
		delete(e.detailConfigs, authID)
		return
	}
	e.detailConfigs[authID] = copied
}

func (e *TraeExecutor) traeDetailModelConfig(auth *cliproxyauth.Auth, configName string) (traeDetailModelConfig, bool) {
	authID := traeAuthID(auth)
	configName = strings.ToLower(strings.TrimSpace(configName))
	if authID == "" || configName == "" {
		return traeDetailModelConfig{}, false
	}
	e.detailConfigMu.RLock()
	defer e.detailConfigMu.RUnlock()
	configs := e.detailConfigs[authID]
	if len(configs) == 0 {
		return traeDetailModelConfig{}, false
	}
	config, ok := configs[configName]
	return config, ok
}

func traeAuthID(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return strings.TrimSpace(auth.ID)
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
			model.SupportedParameters = removeSupportedParameter(model.SupportedParameters, "tools")
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
	})
}

func removeSupportedParameter(parameters []string, parameter string) []string {
	filtered := parameters[:0]
	for _, current := range parameters {
		if strings.EqualFold(strings.TrimSpace(current), parameter) {
			continue
		}
		filtered = append(filtered, current)
	}
	return filtered
}

func appendTraeV3AgentModels(models []*registry.ModelInfo, now int64) []*registry.ModelInfo {
	v3Models := []struct {
		id          string
		displayName string
		context     int
	}{
		{"glm-4.7", "GLM-4.7", 16000},
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

func parseTraeDetailParam(data []byte, now int64) []*registry.ModelInfo {
	models, _ := parseTraeDetailParamWithConfigs(data, now)
	return models
}

func parseTraeDetailParamWithConfigs(data []byte, now int64) ([]*registry.ModelInfo, map[string]traeDetailModelConfig) {
	root := gjson.ParseBytes(data)
	configs := root.Get("config_info_list")
	if !configs.Exists() {
		configs = root.Get("data.config_info_list")
	}
	if !configs.Exists() || !configs.IsArray() {
		return nil, nil
	}

	models := make([]*registry.ModelInfo, 0, len(configs.Array()))
	detailConfigs := make(map[string]traeDetailModelConfig)
	seen := make(map[string]struct{})
	for _, item := range configs.Array() {
		if usage := item.Get("usage").String(); usage != "chat_completion" {
			continue
		}
		if !item.Get("config_switch").Bool() {
			continue
		}
		if item.Get("is_invisible_to_user").Bool() {
			continue
		}
		configName := strings.TrimSpace(item.Get("config_name").String())
		if configName == "" {
			continue
		}
		lower := strings.ToLower(configName)
		if strings.HasPrefix(lower, "custom_model_") || strings.HasPrefix(lower, "custom_claude") || strings.HasPrefix(lower, "custom_gemini") {
			continue
		}
		if strings.HasSuffix(lower, "-auto") || strings.HasSuffix(lower, "_auto") {
			continue
		}
		detail := item.Get("model_detail_list.0")
		modelName := strings.TrimSpace(detail.Get("model_name").String())
		if modelName == "" {
			continue
		}
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}

		displayConfig := item.Get("display_config")
		displayName := displayConfig.Get("display_name").String()
		if displayName == "" {
			displayName = configName
		}

		contextLength := int(detail.Get("prompt_max_tokens").Int())
		maxTokens := int(detail.Get("max_tokens").Int())
		if maxTokens <= 0 {
			maxTokens = 16000
		}

		model := &registry.ModelInfo{
			ID:                  configName,
			Object:              "model",
			Created:             now,
			OwnedBy:             "trae",
			Type:                "trae",
			DisplayName:         displayName,
			Name:                configName,
			ContextLength:       contextLength,
			MaxCompletionTokens: maxTokens,
			SupportedParameters: []string{"tools"},
		}
		if displayConfig.Get("multimodal").Bool() {
			model.SupportedInputModalities = []string{"text", "image"}
		}
		models = append(models, model)
		detailConfigs[lower] = traeDetailModelConfig{
			ModelName:  modelName,
			ConfigName: configName,
		}
	}
	return models, detailConfigs
}

func appendTraeV1RawChatModels(models []*registry.ModelInfo, now int64) []*registry.ModelInfo {
	v1Models := []struct {
		id          string
		displayName string
		context     int
	}{
		{"seed_m8", "Doubao 1.5 Pro", 28000},
		{"deepseek-R1", "DeepSeek Reasoner R1", 40000},
		{"deepseek-V3", "DeepSeek V3", 40000},
		{"deepseek-V3-0324", "DeepSeek V3 0324", 40000},
	}
	existing := make(map[string]struct{}, len(models))
	for _, m := range models {
		if m != nil {
			existing[strings.ToLower(strings.TrimSpace(m.ID))] = struct{}{}
		}
	}
	for _, v := range v1Models {
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
