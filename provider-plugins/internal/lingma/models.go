package lingma

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func (p *Plugin) modelsForAuth(raw []byte) ([]byte, error) {
	var req authModelRPCRequest
	if errUnmarshal := unmarshalRequest(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Lingma model request: %w", errUnmarshal)
	}
	creds, errCredentials := credentialsFromStorage(req.StorageJSON)
	if errCredentials != nil {
		return nil, errCredentials
	}
	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	modelURL := strings.TrimRight(p.configSnapshot().APIBaseURL, "/") + "/algo/api/v2/model/list"
	headers, errHeaders := buildHeaders(creds, "", modelURL, time.Now())
	if errHeaders != nil {
		return nil, errHeaders
	}
	applyCustomHeaders(headers, req.Attributes)
	resp, errDo := host.do(pluginapi.HTTPRequest{
		Method:    http.MethodGet,
		URL:       modelURL,
		Headers:   headers,
		Transport: pluginapi.HTTPTransportOptions{ForceHTTP11: p.configSnapshot().ForceHTTP11},
	})
	if errDo != nil {
		return nil, errDo
	}
	if resp.StatusCode != http.StatusOK {
		return nil, newStatusError(resp.StatusCode, "Lingma model list API error: "+safeUpstreamMessage(resp.Body))
	}
	return pluginOK(pluginapi.ModelResponse{
		Provider: ProviderID,
		Models:   parseModels(resp.Body, time.Now().Unix()),
	})
}

func parseModels(data []byte, now int64) []pluginapi.ModelInfo {
	root := gjson.ParseBytes(data)
	roots := []gjson.Result{root}
	if wrapped := root.Get("data"); wrapped.Exists() {
		roots = append(roots, wrapped)
	}
	if wrapped := root.Get("result"); wrapped.Exists() {
		roots = append(roots, wrapped)
	}

	models := make([]pluginapi.ModelInfo, 0)
	seen := make(map[string]struct{})
	appendModel := func(key, value gjson.Result) {
		modelID := firstModelString(value, "key", "modelId", "model_id", "modelName", "model_name", "id", "name")
		if modelID == "" && key.Type == gjson.String {
			modelID = strings.TrimSpace(key.String())
		}
		if modelID == "" {
			return
		}
		dedupeKey := strings.ToLower(modelID)
		if _, exists := seen[dedupeKey]; exists {
			return
		}
		seen[dedupeKey] = struct{}{}
		displayName := firstModelString(value, "display_name", "displayName", "modelName", "model_name", "name", "label")
		if displayName == "" {
			displayName = modelID
		}
		models = append(models, pluginapi.ModelInfo{
			ID:          modelID,
			Object:      "model",
			Created:     now,
			OwnedBy:     "alibaba",
			Type:        ProviderID,
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
		for _, category := range []string{"chat", "developer", "assistant", "inline"} {
			group := candidate.Get(category)
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

func firstModelString(value gjson.Result, keys ...string) string {
	for _, key := range keys {
		if candidate := value.Get(key); candidate.Exists() {
			if result := strings.TrimSpace(candidate.String()); result != "" {
				return result
			}
		}
	}
	return ""
}
