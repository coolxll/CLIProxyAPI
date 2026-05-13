package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
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
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	lingmaChatURL      = "https://lingma-api.tongyi.aliyun.com/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
	lingmaModelListURL = "https://lingma-api.tongyi.aliyun.com/algo/api/v2/model/list"
)

// LingmaExecutor is a stateless executor for the Lingma API.
type LingmaExecutor struct {
	cfg *config.Config
}

// NewLingmaExecutor creates a new Lingma executor.
func NewLingmaExecutor(cfg *config.Config) *LingmaExecutor {
	return &LingmaExecutor{cfg: cfg}
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
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming chat completion request to Lingma.
func (e *LingmaExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, lingmaChatURL, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}

	headers, err := lingma.BuildHeaders(creds, string(body), lingmaChatURL)
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
		URL:       lingmaChatURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
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
		return resp, err
	}
	defer httpResp.Body.Close()

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		return resp, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, body, data, &param)
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

// ExecuteStream performs a streaming chat completion request to Lingma.
func (e *LingmaExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, lingmaChatURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	headers, err := lingma.BuildHeaders(creds, string(body), lingmaChatURL)
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
		URL:       lingmaChatURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
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
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)

			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, bytes.Clone(line), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}

		doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, []byte("[DONE]"), &param)
		for i := range doneChunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: doneChunks[i]}:
			case <-ctx.Done():
				return
			}
		}

		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// CountTokens returns the token count (placeholder for Lingma).
func (e *LingmaExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("CountTokens not implemented for Lingma")
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
