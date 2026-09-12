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

func fetchModels(host hostRPC, creds credentials) ([]pluginapi.ModelInfo, error) {
	urlStr := fmt.Sprintf("%s/models", creds.baseURL())
	headers := buildAuthHeaders(creds)

	resp, err := host.do(pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     urlStr,
		Headers: headers,
	})
	if err != nil {
		return nil, fmt.Errorf("OpenRouter model list request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenRouter model list returned HTTP %d", resp.StatusCode)
	}

	models, errParse := parseOpenRouterModels(resp.Body, creds.isFreeOnly())
	if errParse != nil {
		return nil, errParse
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("OpenRouter returned no usable models")
	}
	// Remember the live listing so virtual routers and auto-failover only ever
	// target models this account can actually reach.
	storeLiveModels(creds, models)
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

	// 1. Collect the upstream models this account can actually use (canonical
	// openrouter/ prefixed IDs only) and count the free ones.
	upstream := make([]pluginapi.ModelInfo, 0)
	freeUpstream := 0
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
		if isFree && classifyVirtualModel(mID) == virtualKindNone {
			freeUpstream++
		}

		canonicalID := mID
		if !strings.HasPrefix(canonicalID, "openrouter/") {
			canonicalID = fmt.Sprintf("openrouter/%s", canonicalID)
		}
		upstream = append(upstream, createModelInfo(canonicalID, now))
	}

	// 2. Advertise virtual intent routers only when this account has a free model
	// to route to; otherwise they would list models that cannot be served.
	if freeUpstream > 0 {
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
	}

	for _, info := range upstream {
		addModel(info)
	}

	return models, nil
}
