package openrouter

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

var virtualModels = []struct {
	id          string
	aliases     []string
	displayName string
	description string
}{
	{
		id:          "openrouter/free",
		aliases:     []string{"free"},
		displayName: "OpenRouter Free (Auto-Router)",
		description: "[Dynamic Free Router] Auto-routes across all available free models with quality-preserving failover",
	},
	{
		id:          "openrouter/free:coding",
		aliases:     []string{"free:coding", "free:code"},
		displayName: "OpenRouter Free (Coding Priority)",
		description: "[Category: Coding] Auto-routes to top free coding models (e.g. Qwen 2.5 Coder 32B)",
	},
	{
		id:          "openrouter/free:reasoning",
		aliases:     []string{"free:reasoning", "free:thinking"},
		displayName: "OpenRouter Free (Reasoning Priority)",
		description: "[Category: Reasoning] Auto-routes to top free reasoning models (e.g. DeepSeek R1)",
	},
	{
		id:          "openrouter/free:fast",
		aliases:     []string{"free:fast", "fast"},
		displayName: "OpenRouter Free (Speed Priority)",
		description: "[Category: Fast TPS] Auto-routes to ultra-fast free models",
	},
}

func isFreeModel(modelID string) bool {
	clean := strings.ToLower(strings.TrimSpace(modelID))
	if classifyVirtualModel(clean) != virtualKindNone {
		return true
	}
	return strings.HasSuffix(clean, ":free") || clean == "openrouter/free" || clean == "free"
}

func cleanModelID(requestedModel string) string {
	clean := strings.TrimSpace(requestedModel)
	if clean == "" || clean == "free" || clean == "openrouter/free" {
		return "openrouter/free"
	}
	if kind := classifyVirtualModel(clean); kind != virtualKindNone {
		switch kind {
		case virtualKindTierS:
			return "openrouter/free:s"
		case virtualKindTierA:
			return "openrouter/free:a"
		case virtualKindTierB:
			return "openrouter/free:b"
		case virtualKindCoding:
			return "openrouter/free:coding"
		case virtualKindReasoning:
			return "openrouter/free:reasoning"
		case virtualKindFast:
			return "openrouter/free:fast"
		default:
			return "openrouter/free"
		}
	}
	if strings.HasPrefix(clean, "openrouter/") {
		trimmed := strings.TrimPrefix(clean, "openrouter/")
		if strings.Contains(trimmed, "/") || strings.HasSuffix(trimmed, ":free") {
			return trimmed
		}
	}
	return clean
}

func createModelInfo(id string, now int64) pluginapi.ModelInfo {
	q := lookupQuality(id)
	displayName := q.Name
	if displayName == "" {
		displayName = id
	}
	desc := formatModelDescription(q)

	return pluginapi.ModelInfo{
		ID:          id,
		Object:      "model",
		Created:     now,
		OwnedBy:     "openrouter",
		DisplayName: displayName,
		Description: desc,
		Type:        "chat",
	}
}

func staticModels() []pluginapi.ModelInfo {
	return nil
}

func fallbackFreeModels(now int64) []pluginapi.ModelInfo {
	models := make([]pluginapi.ModelInfo, 0, len(virtualModels)+6)
	for _, vm := range virtualModels {
		models = append(models, pluginapi.ModelInfo{
			ID:          vm.id,
			Object:      "model",
			Created:     now,
			OwnedBy:     "openrouter",
			DisplayName: vm.displayName,
			Description: vm.description,
			Type:        "router",
		})
	}
	topFreeIDs := []string{
		"openrouter/deepseek/deepseek-r1:free",
		"openrouter/deepseek/deepseek-chat:free",
		"openrouter/qwen/qwen-2.5-coder-32b-instruct:free",
		"openrouter/meta-llama/llama-3.3-70b-instruct:free",
		"openrouter/google/gemma-4-31b-it:free",
		"openrouter/nvidia/nemotron-3.5-lightning:free",
	}
	for _, id := range topFreeIDs {
		models = append(models, createModelInfo(id, now))
	}
	return models
}

func fetchModels(host hostRPC, creds credentials) ([]pluginapi.ModelInfo, error) {
	urlStr := fmt.Sprintf("%s/models", creds.baseURL())
	headers := buildAuthHeaders(creds)

	resp, err := host.do(pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     urlStr,
		Headers: headers,
	})
	if err != nil || resp.StatusCode != http.StatusOK {
		return fallbackFreeModels(time.Now().Unix()), nil
	}

	models, errParse := parseOpenRouterModels(resp.Body, creds.isFreeOnly())
	if errParse != nil || len(models) == 0 {
		return fallbackFreeModels(time.Now().Unix()), nil
	}
	return models, nil
}

func parseOpenRouterModels(body []byte, freeOnly bool) ([]pluginapi.ModelInfo, error) {
	parsed := gjson.GetBytes(body, "data")
	if !parsed.Exists() || !parsed.IsArray() {
		return nil, fmt.Errorf("invalid OpenRouter models response")
	}

	now := time.Now().Unix()
	var models []pluginapi.ModelInfo
	seen := make(map[string]struct{})

	addModel := func(info pluginapi.ModelInfo) {
		if info.ID == "" {
			return
		}
		if _, exists := seen[info.ID]; exists {
			return
		}
		seen[info.ID] = struct{}{}
		models = append(models, info)
	}

	// 1. Add virtual intent routers (canonical IDs only)
	for _, vm := range virtualModels {
		addModel(pluginapi.ModelInfo{
			ID:          vm.id,
			Object:      "model",
			Created:     now,
			OwnedBy:     "openrouter",
			DisplayName: vm.displayName,
			Description: vm.description,
			Type:        "router",
		})
	}

	// 2. Add parsed upstream models with quality info (canonical openrouter/ prefixed IDs only)
	for _, item := range parsed.Array() {
		mID := strings.TrimSpace(item.Get("id").String())
		if mID == "" {
			continue
		}

		promptPrice := item.Get("pricing.prompt").String()
		compPrice := item.Get("pricing.completion").String()
		isFree := strings.HasSuffix(mID, ":free") || mID == "openrouter/free" || (promptPrice == "0" && compPrice == "0")

		if freeOnly && !isFree {
			continue
		}

		canonicalID := mID
		if !strings.HasPrefix(canonicalID, "openrouter/") {
			canonicalID = fmt.Sprintf("openrouter/%s", canonicalID)
		}
		info := createModelInfo(canonicalID, now)
		addModel(info)
	}

	return models, nil
}
