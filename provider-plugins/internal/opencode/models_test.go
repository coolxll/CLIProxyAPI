package opencode

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestIsFreeModel(t *testing.T) {
	tests := []struct {
		model    string
		wantFree bool
	}{
		{"opencode/big-pickle", true},
		{"big-pickle", true},
		{"deepseek-v4-flash-free", true},
		{"opencode/deepseek-v4-flash-free", true},
		{"kimi-k2.5-free", true},
		{"mimo-v2.5-free", true},
		{"nemotron-3.5-lightning-free", true},
		{"gpt-5-nano", false},
		{"claude-sonnet-4-6", false},
		{"opencode/gpt-5.4", false},
		{"claude-opus-4-6", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := isFreeModel(tt.model); got != tt.wantFree {
				t.Errorf("isFreeModel(%q) = %v, want %v", tt.model, got, tt.wantFree)
			}
		})
	}
}

func TestResolveCloudZenModel(t *testing.T) {
	tests := []struct {
		requested string
		want      string
	}{
		{"", "big-pickle"},
		{"free", "big-pickle"},
		{"opencode/free", "big-pickle"},
		{"opencode/big-pickle", "big-pickle"},
		{"deepseek-v4-flash-free", "deepseek-v4-flash-free"},
		{"opencode/deepseek-v4-flash-free", "deepseek-v4-flash-free"},
		{"gpt5nano", "gpt-5-nano"},
		{"opencode/gpt5nano", "gpt-5-nano"},
	}

	for _, tt := range tests {
		t.Run(tt.requested, func(t *testing.T) {
			if got := resolveCloudZenModel(tt.requested); got != tt.want {
				t.Errorf("resolveCloudZenModel(%q) = %q, want %q", tt.requested, got, tt.want)
			}
		})
	}
}

func TestStaticModels(t *testing.T) {
	models := staticModels()
	if len(models) == 0 {
		t.Fatalf("expected non-empty static models")
	}

	foundBigPickle := false
	foundBarePickle := false
	for _, m := range models {
		if m.ID == "opencode/big-pickle" {
			foundBigPickle = true
		}
		if m.ID == "big-pickle" {
			foundBarePickle = true
		}
	}
	if !foundBigPickle {
		t.Errorf("expected to find opencode/big-pickle in static models")
	}
	if !foundBarePickle {
		t.Errorf("expected to find bare big-pickle in static models")
	}
}

func TestFetchCloudZenModels(t *testing.T) {
	mockResponse := []byte(`{
		"object": "list",
		"data": [
			{"id": "deepseek-v4-flash-free", "object": "model", "created": 1789176681, "owned_by": "opencode"},
			{"id": "big-pickle", "object": "model", "created": 1789176681, "owned_by": "opencode"},
			{"id": "claude-sonnet-4-6", "object": "model", "created": 1789176681, "owned_by": "opencode"}
		]
	}`)

	host := hostRPC{
		call: func(method string, request []byte) ([]byte, error) {
			return json.Marshal(pluginapi.HTTPResponse{
				StatusCode: http.StatusOK,
				Body:       mockResponse,
			})
		},
	}

	creds := credentials{
		Type:      "opencode",
		ServerURL: "https://opencode.ai/zen",
	}

	models, err := fetchModels(host, creds)
	if err != nil {
		t.Fatalf("fetchModels() error: %v", err)
	}

	seen := make(map[string]bool)
	for _, m := range models {
		seen[m.ID] = true
	}

	if !seen["opencode/big-pickle"] || !seen["big-pickle"] {
		t.Errorf("expected big-pickle models to be present, got %+v", seen)
	}
	if !seen["opencode/deepseek-v4-flash-free"] || !seen["deepseek-v4-flash-free"] {
		t.Errorf("expected deepseek-v4-flash-free models to be present, got %+v", seen)
	}
	if seen["opencode/claude-sonnet-4-6"] || seen["claude-sonnet-4-6"] {
		t.Errorf("expected paid model claude-sonnet-4-6 to be filtered out, got %+v", seen)
	}
	if !seen["opencode/free"] || !seen["free"] {
		t.Errorf("expected opencode/free virtual alias to be present, got %+v", seen)
	}
}

func TestParseConfigProvidersArray(t *testing.T) {
	body := []byte(`{
		"providers": [
			{
				"id": "opencode",
				"name": "OpenCode",
				"models": {
					"kimi-k2.5-free": { "name": "Kimi K2.5 (Free)" },
					"big-pickle": { "name": "Big Pickle" },
					"claude-opus": { "name": "Claude Opus" }
				}
			},
			{
				"id": "custom",
				"models": {
					"free-model": { "name": "Free Custom" },
					"my-model": { "name": "Custom Model" }
				}
			}
		]
	}`)

	modelsFreeOnly, err := parseConfigProviders(body, true)
	if err != nil {
		t.Fatalf("parseConfigProviders(true) failed: %v", err)
	}

	mapFree := make(map[string]bool)
	for _, m := range modelsFreeOnly {
		mapFree[m.ID] = true
	}

	expectedFree := []string{"opencode/kimi-k2.5-free", "kimi-k2.5-free", "opencode/big-pickle", "big-pickle", "custom/free-model"}
	for _, exp := range expectedFree {
		if !mapFree[exp] {
			t.Errorf("expected free model %q to be present", exp)
		}
	}
	if mapFree["opencode/claude-opus"] || mapFree["custom/my-model"] {
		t.Errorf("expected paid models to be filtered out in freeOnly mode")
	}

	modelsAll, errAll := parseConfigProviders(body, false)
	if errAll != nil {
		t.Fatalf("parseConfigProviders(false) failed: %v", errAll)
	}
	mapAll := make(map[string]bool)
	for _, m := range modelsAll {
		mapAll[m.ID] = true
	}
	if !mapAll["opencode/claude-opus"] || !mapAll["custom/my-model"] {
		t.Errorf("expected paid models to be present when freeOnly is false")
	}
}
