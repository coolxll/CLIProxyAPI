package openrouter

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// testLiveModelsJSON is a minimal OpenRouter listing: free models that exist in the
// catalog, a free model without a catalog entry, and a paid model.
const testLiveModelsJSON = `{"data":[
  {"id":"nvidia/nemotron-3-super-120b-a12b:free","pricing":{"prompt":"0","completion":"0"}},
  {"id":"deepseek/deepseek-r1:free","pricing":{"prompt":"0","completion":"0"}},
  {"id":"deepseek/deepseek-chat:free","pricing":{"prompt":"0","completion":"0"}},
  {"id":"google/gemma-4-31b-it:free","pricing":{"prompt":"0","completion":"0"}},
  {"id":"vendor/mystery-model:free","pricing":{"prompt":"0","completion":"0"}},
  {"id":"openai/gpt-4o","pricing":{"prompt":"0.0000025","completion":"0.00001"}}
]}`

// testPaidOnlyModelsJSON is a listing without any free model.
const testPaidOnlyModelsJSON = `{"data":[
  {"id":"openai/gpt-4o","pricing":{"prompt":"0.0000025","completion":"0.00001"}}
]}`

func liveEntryFromIDs(ids ...string) liveModelEntry {
	entry := liveModelEntry{freeIDs: make(map[string]struct{}, len(ids))}
	for _, id := range ids {
		entry.freeIDs[normalizeModelID(id)] = struct{}{}
		entry.orderedFree = append(entry.orderedFree, normalizeModelID(id))
	}
	return entry
}

func TestNormalizeModelID(t *testing.T) {
	tests := map[string]string{
		"openrouter/deepseek/deepseek-r1:free": "deepseek/deepseek-r1:free",
		"DeepSeek/DeepSeek-R1:Free":            "deepseek/deepseek-r1:free",
		"  deepseek/deepseek-r1:free  ":        "deepseek/deepseek-r1:free",
		"":                                     "",
	}
	for input, want := range tests {
		if got := normalizeModelID(input); got != want {
			t.Errorf("normalizeModelID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestStoreLiveModelsFiltersVirtualAndPaid(t *testing.T) {
	resetLiveModelCache()
	creds := credentials{Type: "openrouter", APIKey: "sk-or-v1-store-test"}

	storeLiveModels(creds, []pluginapi.ModelInfo{
		{ID: "openrouter/free"},
		{ID: "openrouter/free:coding"},
		{ID: "openrouter/deepseek/deepseek-r1:free"},
		{ID: "openrouter/openai/gpt-4o"},
	})

	entry, err := liveFreeModels(hostRPC{}, creds)
	if err != nil {
		t.Fatalf("liveFreeModels failed: %v", err)
	}
	if _, ok := entry.freeIDs["deepseek/deepseek-r1:free"]; !ok {
		t.Errorf("expected live free model to be cached, got %v", entry.freeIDs)
	}
	if _, ok := entry.freeIDs["openrouter/free"]; ok {
		t.Errorf("virtual routers must not be cached as live models")
	}
	if _, ok := entry.freeIDs["openai/gpt-4o"]; ok {
		t.Errorf("paid models must not be cached as live free models")
	}
}

func TestResolveVirtualModel_LiveOnly(t *testing.T) {
	tracker := &cooldownTracker{cooldowns: make(map[string]time.Time)}

	// Tier S router picks the best live ranked model that is not cooling down.
	live := liveEntryFromIDs(
		"nvidia/nemotron-3-super-120b-a12b:free",
		"deepseek/deepseek-r1:free",
		"google/gemma-4-31b-it:free",
	)
	got, isVirtual := resolveVirtualModel("free:s", live, tracker)
	if !isVirtual || got == "" {
		t.Fatalf("expected a live Tier S candidate, got %q (virtual=%v)", got, isVirtual)
	}
	if quality := lookupQuality(got); quality.Tier != TierS && quality.Tier != TierSPlus {
		t.Errorf("expected Tier S/S+ model, got tier %s for %s", quality.Tier, got)
	}

	// A model absent from the live listing must never be selected.
	tracker.markCooldown(got, 10*time.Minute)
	got2, _ := resolveVirtualModel("free:s", live, tracker)
	if got2 == got || got2 == "" {
		t.Fatalf("expected a different live candidate after cooldown, got %q", got2)
	}
	if _, ok := live.freeIDs[normalizeModelID(got2)]; !ok {
		t.Errorf("resolved model %q is not in the live listing", got2)
	}

	// Category routers resolve against live models only.
	liveSmall := liveEntryFromIDs("deepseek/deepseek-chat:free")
	coding, _ := resolveVirtualModel("openrouter/free:coding", liveSmall, &cooldownTracker{cooldowns: make(map[string]time.Time)})
	if normalizeModelID(coding) != "deepseek/deepseek-chat:free" {
		t.Errorf("expected the only live coding model, got %q", coding)
	}
}

func TestResolveVirtualModel_NoLiveCandidates(t *testing.T) {
	tracker := &cooldownTracker{cooldowns: make(map[string]time.Time)}

	// Nothing live at all: virtual routers must be unresolvable, not guessed.
	got, isVirtual := resolveVirtualModel("free:s", liveEntryFromIDs(), tracker)
	if !isVirtual {
		t.Fatalf("expected virtual model to be recognized")
	}
	if got != "" {
		t.Errorf("expected no candidate, got %q", got)
	}

	// Every live candidate is cooling down: still no resolution.
	live := liveEntryFromIDs("nvidia/nemotron-3-super-120b-a12b:free")
	tracker.markCooldown("nvidia/nemotron-3-super-120b-a12b:free", 10*time.Minute)
	if got, _ := resolveVirtualModel("free:s", live, tracker); got != "" {
		t.Errorf("expected no candidate while cooling down, got %q", got)
	}

	// The catch-all router falls back to live free models without a catalog entry.
	got, _ = resolveVirtualModel("openrouter/free", liveEntryFromIDs("vendor/mystery-model:free"), tracker)
	if got != "vendor/mystery-model:free" {
		t.Errorf("expected the unranked live free model, got %q", got)
	}
}
