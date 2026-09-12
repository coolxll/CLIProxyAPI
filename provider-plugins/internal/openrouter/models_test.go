package openrouter

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestIsFreeModel(t *testing.T) {
	tests := []struct {
		model    string
		wantFree bool
	}{
		{"openrouter/free", true},
		{"free", true},
		{"free:s", true},
		{"openrouter/free:s", true},
		{"free:coding", true},
		{"google/gemma-4-31b-it:free", true},
		{"openrouter/google/gemma-4-31b-it:free", true},
		{"nvidia/nemotron-3.5-lightning:free", true},
		{"anthropic/claude-3.5-sonnet", false},
		{"openai/gpt-4o", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := isFreeModel(tt.model); got != tt.wantFree {
				t.Errorf("isFreeModel(%q) = %v, want %v", tt.model, got, tt.wantFree)
			}
		})
	}
}

func TestCleanModelID(t *testing.T) {
	tests := []struct {
		requested string
		want      string
	}{
		{"", "openrouter/free"},
		{"free", "openrouter/free"},
		{"openrouter/free", "openrouter/free"},
		{"free:s", "openrouter/free:s"},
		{"openrouter/free:s", "openrouter/free:s"},
		{"free:coding", "openrouter/free:coding"},
		{"free:code", "openrouter/free:coding"},
		{"free:reasoning", "openrouter/free:reasoning"},
		{"free:fast", "openrouter/free:fast"},
		{"openrouter/google/gemma-4-31b-it:free", "google/gemma-4-31b-it:free"},
		{"google/gemma-4-31b-it:free", "google/gemma-4-31b-it:free"},
	}

	for _, tt := range tests {
		t.Run(tt.requested, func(t *testing.T) {
			if got := cleanModelID(tt.requested); got != tt.want {
				t.Errorf("cleanModelID(%q) = %q, want %q", tt.requested, got, tt.want)
			}
		})
	}
}

func TestStaticModels(t *testing.T) {
	models := staticModels()
	if len(models) == 0 {
		t.Fatalf("expected non-empty static models")
	}

	foundFreeRouter := false
	foundTierS := false
	foundQualityBadge := false

	for _, m := range models {
		if m.ID == "openrouter/free" {
			foundFreeRouter = true
		}
		if m.ID == "openrouter/free:s" || m.ID == "free:s" {
			foundTierS = true
		}
		if m.ID == "google/gemma-4-31b-it:free" && strings.Contains(m.Description, "Tier: S") {
			foundQualityBadge = true
		}
	}
	if !foundFreeRouter {
		t.Errorf("expected openrouter/free in static models")
	}
	if !foundTierS {
		t.Errorf("expected free:s virtual router in static models")
	}
	if !foundQualityBadge {
		t.Errorf("expected quality badge in static model description")
	}
}

func TestParseOpenRouterModels(t *testing.T) {
	mockJSON := []byte(`{
		"data": [
			{
				"id": "google/gemma-4-31b-it:free",
				"pricing": {"prompt": "0", "completion": "0"}
			},
			{
				"id": "openai/gpt-4o",
				"pricing": {"prompt": "0.000005", "completion": "0.000015"}
			},
			{
				"id": "openrouter/free",
				"pricing": {"prompt": "0", "completion": "0"}
			}
		]
	}`)

	models, err := parseOpenRouterModels(mockJSON, true)
	if err != nil {
		t.Fatalf("parseOpenRouterModels error: %v", err)
	}

	modelMap := make(map[string]bool)
	for _, m := range models {
		modelMap[m.ID] = true
	}

	if !modelMap["google/gemma-4-31b-it:free"] {
		t.Errorf("expected google/gemma-4-31b-it:free to be present")
	}
	if !modelMap["openrouter/free"] {
		t.Errorf("expected openrouter/free to be present")
	}
	if modelMap["openai/gpt-4o"] {
		t.Errorf("expected openai/gpt-4o to be filtered out when freeOnly is true")
	}
}

func TestFetchModelsMock(t *testing.T) {
	mockResponse := []byte(`{
		"data": [
			{"id": "nvidia/nemotron-3.5-lightning:free", "pricing": {"prompt": "0", "completion": "0"}}
		]
	}`)

	host := hostRPC{
		call: func(method string, request []byte) ([]byte, error) {
			return pluginruntime.OK(pluginapi.HTTPResponse{
				StatusCode: http.StatusOK,
				Body:       mockResponse,
			})
		},
	}

	creds := credentials{
		Type:   "openrouter",
		APIKey: "sk-or-v1-test",
	}

	models, err := fetchModels(host, creds)
	if err != nil {
		t.Fatalf("fetchModels failed: %v", err)
	}

	seen := make(map[string]bool)
	for _, m := range models {
		seen[m.ID] = true
	}

	if !seen["nvidia/nemotron-3.5-lightning:free"] {
		t.Errorf("expected nvidia/nemotron-3.5-lightning:free to be in model list")
	}
}
