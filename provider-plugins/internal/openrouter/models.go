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
		id:          "openrouter/free:s",
		aliases:     []string{"free:s", "tier:s"},
		displayName: "OpenRouter Free (Tier S Flagship)",
		description: "[Tier: S+/S | Flagship] Virtual router auto-routing to highest-performing free models (Nemotron 3 Super, DeepSeek R1, MiniMax M2.5, etc.)",
	},
	{
		id:          "openrouter/free:a",
		aliases:     []string{"free:a", "tier:a"},
		displayName: "OpenRouter Free (Tier A Workhorse)",
		description: "[Tier: A+/A | Workhorse] Virtual router auto-routing to balanced free models (Qwen 32B Coder, GPT OSS 120B, Llama 3.3 70B, etc.)",
	},
	{
		id:          "openrouter/free:coding",
		aliases:     []string{"free:coding", "free:code"},
		displayName: "OpenRouter Free (Coding Priority)",
		description: "[Category: Coding] Virtual router auto-routing to top-rated coding models (Qwen Coder 480B/32B, DeepSeek, etc.)",
	},
	{
		id:          "openrouter/free:reasoning",
		aliases:     []string{"free:reasoning", "free:thinking"},
		displayName: "OpenRouter Free (Reasoning Priority)",
		description: "[Category: Reasoning] Virtual router auto-routing to deep thinking & reasoning models (DeepSeek R1, Nemotron Reasoning, etc.)",
	},
	{
		id:          "openrouter/free:fast",
		aliases:     []string{"free:fast", "fast"},
		displayName: "OpenRouter Free (Speed Priority)",
		description: "[Category: Fast TPS] Virtual router auto-routing to ultra-fast free models (>100 tokens/sec)",
	},
	{
		id:          "openrouter/free",
		aliases:     []string{"free"},
		displayName: "OpenRouter Free (All Tiers Auto-Router)",
		description: "[Dynamic Free Router] Auto-routes across all available free models with quality-preserving failover",
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
	now := time.Now().Unix()
	rawIDs := []string{
		"openrouter/free",
		"google/gemma-4-31b-it:free",
		"google/gemma-4-26b-a4b-it:free",
		"nvidia/nemotron-3.5-lightning:free",
		"nvidia/nemotron-3-super-120b-a12b:free",
		"nvidia/nemotron-3-ultra-550b-a55b:free",
		"nex-agi/nex-n2.5-pro:free",
		"nex-agi/nex-n2.5-mini:free",
		"inclusionai/ling-3.0-flash-fin:free",
		"inclusionai/ling-3.0-flash-vl:free",
		"liquid/lfm-2.5-2.6b:free",
		"thinkingmachines/inkling:free",
		"poolside/laguna-s-2.1:free",
		"cohere/north-mini-code:free",
		"meta-llama/llama-3.3-70b-instruct:free",
		"deepseek/deepseek-r1:free",
		"deepseek/deepseek-chat:free",
		"qwen/qwen-2.5-coder-32b-instruct:free",
		"mistralai/mistral-7b-instruct:free",
	}

	models := make([]pluginapi.ModelInfo, 0, len(rawIDs)*3+len(virtualModels)*2)
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

	// 1. Add virtual intent routers first
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
		for _, alias := range vm.aliases {
			addModel(pluginapi.ModelInfo{
				ID:          alias,
				Object:      "model",
				Created:     now,
				OwnedBy:     "openrouter",
				DisplayName: vm.displayName,
				Description: vm.description,
				Type:        "router",
			})
		}
	}

	// 2. Add concrete models
	for _, id := range rawIDs {
		info := createModelInfo(id, now)
		addModel(info)
		if !strings.HasPrefix(id, "openrouter/") {
			infoWithPrefix := createModelInfo(fmt.Sprintf("openrouter/%s", id), now)
			addModel(infoWithPrefix)
		}
		if strings.HasSuffix(id, ":free") {
			bareNoFree := strings.TrimSuffix(id, ":free")
			infoBare := createModelInfo(bareNoFree, now)
			addModel(infoBare)
			infoBarePrefix := createModelInfo(fmt.Sprintf("openrouter/%s", bareNoFree), now)
			addModel(infoBarePrefix)
		}
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
		return staticModels(), nil
	}

	models, errParse := parseOpenRouterModels(resp.Body, creds.isFreeOnly())
	if errParse != nil || len(models) == 0 {
		return staticModels(), nil
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

	// 1. Add virtual intent routers first
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
		for _, alias := range vm.aliases {
			addModel(pluginapi.ModelInfo{
				ID:          alias,
				Object:      "model",
				Created:     now,
				OwnedBy:     "openrouter",
				DisplayName: vm.displayName,
				Description: vm.description,
				Type:        "router",
			})
		}
	}

	// 2. Add parsed upstream models with quality info
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

		info := createModelInfo(mID, now)
		addModel(info)
		if !strings.HasPrefix(mID, "openrouter/") {
			infoWithPrefix := createModelInfo(fmt.Sprintf("openrouter/%s", mID), now)
			addModel(infoWithPrefix)
		}
		if strings.HasSuffix(mID, ":free") {
			bareNoFree := strings.TrimSuffix(mID, ":free")
			infoBare := createModelInfo(bareNoFree, now)
			addModel(infoBare)
			infoBarePrefix := createModelInfo(fmt.Sprintf("openrouter/%s", bareNoFree), now)
			addModel(infoBarePrefix)
		}
	}

	return models, nil
}
