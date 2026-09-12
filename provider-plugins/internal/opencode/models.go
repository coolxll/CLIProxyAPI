package opencode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	aliasGPTRegex = regexp.MustCompile(`^gpt-?(\d+)(.*)`)

	// verifiedAvailableFreeModels contains the active free models confirmed to be operational on OpenCode Zen.
	verifiedAvailableFreeModels = map[string]struct{}{
		"big-pickle":                  {},
		"mimo-v2.5-free":              {},
		"nemotron-3.5-lightning-free": {},
		"nemotron-3-ultra-free":       {},
		"ling-3.0-flash-fin-free":     {},
	}
)

func isFreeModel(model string) bool {
	norm := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(norm, "/") {
		parts := strings.SplitN(norm, "/", 2)
		norm = parts[1]
	}
	if norm == "free" {
		return true
	}
	_, ok := verifiedAvailableFreeModels[norm]
	return ok
}

func resolveCloudZenModel(requestedModel string) string {
	clean := strings.TrimSpace(requestedModel)
	if clean == "" || clean == "free" || clean == "opencode/free" {
		return "big-pickle"
	}
	if strings.HasPrefix(clean, "opencode/") {
		clean = strings.TrimPrefix(clean, "opencode/")
	}
	clean = normalizeModelAlias(clean)
	return clean
}

func fetchModels(host hostRPC, creds credentials) ([]pluginapi.ModelInfo, error) {
	if creds.isCloudZen() {
		return fetchCloudZenModels(host, creds)
	}
	return fetchDaemonModels(host, creds)
}

type cloudZenModelsResponse struct {
	Object string `json:"object"`
	Data   []struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

func fetchCloudZenModels(host hostRPC, creds credentials) ([]pluginapi.ModelInfo, error) {
	urlStr := fmt.Sprintf("%s/v1/models", creds.baseURL())
	ids := deriveRequestIDs(nil, nil)
	key := creds.effectiveAPIKey()
	if key == "" {
		key = "public"
	}
	headers := buildCloudZenHeaders(creds, ids, key, false)

	resp, err := host.do(pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     urlStr,
		Headers: headers,
	})
	if err != nil {
		return nil, fmt.Errorf("OpenCode Zen model list request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenCode Zen model list returned HTTP %d", resp.StatusCode)
	}

	var parsed cloudZenModelsResponse
	if errUnmarshal := json.Unmarshal(resp.Body, &parsed); errUnmarshal != nil {
		return nil, fmt.Errorf("decode OpenCode Zen model list: %w", errUnmarshal)
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("OpenCode Zen model list is empty")
	}

	now := time.Now().Unix()
	var models []pluginapi.ModelInfo
	seen := make(map[string]struct{})

	addModel := func(id, ownedBy string, created int64) {
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		models = append(models, pluginapi.ModelInfo{
			ID:      id,
			Object:  "model",
			Created: created,
			OwnedBy: ownedBy,
		})
	}

	// Always expose opencode/free virtual alias
	addModel("opencode/free", "opencode", now)
	addModel("free", "opencode", now)

	for _, item := range parsed.Data {
		mID := strings.TrimSpace(item.ID)
		if mID == "" {
			continue
		}
		if creds.isFreeOnly() && !isFreeModel(mID) {
			continue
		}
		ownedBy := item.OwnedBy
		if ownedBy == "" {
			ownedBy = "opencode"
		}
		created := item.Created
		if created == 0 {
			created = now
		}

		prefixed := fmt.Sprintf("opencode/%s", mID)
		addModel(prefixed, ownedBy, created)
		addModel(mID, ownedBy, created)
	}

	if len(models) == 0 {
		return nil, fmt.Errorf("OpenCode Zen returned no usable models")
	}
	return models, nil
}

func fetchDaemonModels(host hostRPC, creds credentials) ([]pluginapi.ModelInfo, error) {
	urlStr := fmt.Sprintf("%s/config/providers", creds.baseURL())
	headers := buildAuthHeaders(creds)
	headers.Set("Accept", "application/json")

	resp, err := host.do(pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     urlStr,
		Headers: headers,
	})
	if err != nil {
		return nil, fmt.Errorf("OpenCode daemon model list request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenCode daemon model list returned HTTP %d", resp.StatusCode)
	}

	models, errParse := parseConfigProviders(resp.Body, creds.isFreeOnly())
	if errParse != nil {
		return nil, errParse
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("OpenCode daemon returned no usable models")
	}
	return models, nil
}

func parseConfigProviders(body []byte, freeOnly bool) ([]pluginapi.ModelInfo, error) {
	var resp ConfigProvidersResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Providers) == 0 {
		return nil, fmt.Errorf("no providers found in config")
	}

	var providersList []ProviderInfo
	if errArr := json.Unmarshal(resp.Providers, &providersList); errArr != nil {
		var providersMap map[string]ProviderInfo
		if errMap := json.Unmarshal(resp.Providers, &providersMap); errMap == nil {
			for id, p := range providersMap {
				if p.ID == "" {
					p.ID = id
				}
				providersList = append(providersList, p)
			}
		} else {
			return nil, fmt.Errorf("failed to decode providers: array=%v, map=%v", errArr, errMap)
		}
	}

	now := time.Now().Unix()
	var models []pluginapi.ModelInfo
	seen := make(map[string]struct{})

	addModel := func(id, ownedBy string, created int64) {
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		models = append(models, pluginapi.ModelInfo{
			ID:      id,
			Object:  "model",
			Created: created,
			OwnedBy: ownedBy,
		})
	}

	addModel("opencode/free", "opencode", now)
	addModel("free", "opencode", now)

	for _, p := range providersList {
		pID := p.ID
		if pID == "" {
			pID = "opencode"
		}
		for mID := range p.Models {
			if freeOnly && !isFreeModel(mID) {
				continue
			}
			id := fmt.Sprintf("%s/%s", pID, mID)
			addModel(id, pID, now)
			if pID == "opencode" {
				addModel(mID, pID, now)
			}
		}
	}

	return models, nil
}

func normalizeModelAlias(model string) string {
	clean := strings.TrimSpace(model)
	if m := aliasGPTRegex.FindStringSubmatch(clean); len(m) >= 3 {
		suffix := m[2]
		if suffix != "" && !strings.HasPrefix(suffix, "-") && !strings.HasPrefix(suffix, ".") {
			suffix = "-" + suffix
		}
		return fmt.Sprintf("gpt-%s%s", m[1], suffix)
	}
	return clean
}

func resolveModelRef(requestedModel string, availableModels []pluginapi.ModelInfo) OpenCodeModelRef {
	clean := strings.TrimSpace(requestedModel)
	if clean == "" || clean == "free" || clean == "opencode/free" {
		return OpenCodeModelRef{ProviderID: "opencode", ModelID: "big-pickle"}
	}

	var pID, mID string
	if strings.Contains(clean, "/") {
		parts := strings.SplitN(clean, "/", 2)
		pID = parts[0]
		mID = parts[1]
	} else {
		pID = "opencode"
		mID = clean
	}

	normMID := normalizeModelAlias(mID)
	candidates := []string{mID, normMID}

	for _, m := range availableModels {
		for _, c := range candidates {
			if m.ID == fmt.Sprintf("%s/%s", pID, c) || m.ID == c {
				if parts := strings.SplitN(m.ID, "/", 2); len(parts) == 2 {
					return OpenCodeModelRef{ProviderID: parts[0], ModelID: parts[1]}
				}
				return OpenCodeModelRef{ProviderID: pID, ModelID: m.ID}
			}
		}
	}

	for _, m := range availableModels {
		for _, c := range candidates {
			if strings.HasSuffix(m.ID, "/"+c+"-free") || strings.HasSuffix(m.ID, "/"+c) {
				if parts := strings.SplitN(m.ID, "/", 2); len(parts) == 2 {
					return OpenCodeModelRef{ProviderID: parts[0], ModelID: parts[1]}
				}
			}
		}
	}

	return OpenCodeModelRef{ProviderID: pID, ModelID: normMID}
}
