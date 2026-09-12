package openrouter

import (
	"strings"
	"testing"
	"time"
)

func TestLookupQuality_KnownModels(t *testing.T) {
	qNemotron := lookupQuality("nvidia/nemotron-3-super-120b-a12b:free")
	if qNemotron.Tier != TierS {
		t.Errorf("got tier %s, want %s", qNemotron.Tier, TierS)
	}
	if qNemotron.SWEBench != "60.5%" {
		t.Errorf("got SWEBench %s, want 60.5%%", qNemotron.SWEBench)
	}

	qR1 := lookupQuality("openrouter/deepseek/deepseek-r1:free")
	if qR1.Tier != TierSPlus {
		t.Errorf("got tier %s, want %s", qR1.Tier, TierSPlus)
	}

	qQwen := lookupQuality("qwen/qwen3-coder:free")
	if qQwen.Tier != TierAPlus {
		t.Errorf("got tier %s, want %s", qQwen.Tier, TierAPlus)
	}
	hasCodingTag := false
	for _, tag := range qQwen.Tags {
		if tag == "coding" {
			hasCodingTag = true
			break
		}
	}
	if !hasCodingTag {
		t.Errorf("expected coding tag on qwen3-coder")
	}
}

func TestDeriveQualityHeuristic(t *testing.T) {
	q1 := deriveQualityHeuristic("custom/megacoder-70b-v2:free")
	if q1.Tier != TierA {
		t.Errorf("got tier %s, want %s", q1.Tier, TierA)
	}
	hasCoding := false
	for _, tag := range q1.Tags {
		if tag == "coding" {
			hasCoding = true
			break
		}
	}
	if !hasCoding {
		t.Errorf("expected coding tag on megacoder")
	}

	q2 := deriveQualityHeuristic("tinymodel-2b-instruct:free")
	if q2.Tier != TierC {
		t.Errorf("got tier %s, want %s", q2.Tier, TierC)
	}
}

func TestFormatModelDescription(t *testing.T) {
	q := ModelQuality{
		ModelID:        "test/model",
		Name:           "Test Model",
		Tier:           TierS,
		SWEBench:       "65.0%",
		AAIntelligence: 40.5,
		AASpeedTPS:     120.0,
		Context:        "128k",
		Tags:           []string{"coding", "fast"},
	}
	desc := formatModelDescription(q)
	if !strings.Contains(desc, "Tier: S") {
		t.Errorf("missing Tier: S in %s", desc)
	}
	if !strings.Contains(desc, "SWE: 65.0%") {
		t.Errorf("missing SWE in %s", desc)
	}
	if !strings.Contains(desc, "AA Intel: 40.5") {
		t.Errorf("missing AA Intel in %s", desc)
	}
	if !strings.Contains(desc, "coding, fast") {
		t.Errorf("missing tags in %s", desc)
	}
}

func TestClassifyVirtualModel(t *testing.T) {
	tests := []struct {
		model string
		want  virtualModelKind
	}{
		{"free:s", virtualKindTierS},
		{"openrouter/free:s", virtualKindTierS},
		{"tier:s", virtualKindTierS},
		{"free:a", virtualKindTierA},
		{"free:code", virtualKindCoding},
		{"openrouter/free:coding", virtualKindCoding},
		{"free:thinking", virtualKindReasoning},
		{"free:reasoning", virtualKindReasoning},
		{"free:fast", virtualKindFast},
		{"free", virtualKindAll},
		{"openrouter/free", virtualKindAll},
		{"google/gemma-4-31b-it:free", virtualKindNone},
	}

	for _, tt := range tests {
		got := classifyVirtualModel(tt.model)
		if got != tt.want {
			t.Errorf("classifyVirtualModel(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestResolveVirtualModel(t *testing.T) {
	tracker := &cooldownTracker{cooldowns: make(map[string]time.Time)}

	// Test free:s resolves to a Tier S/S+ model
	modelS, isVirtual := resolveVirtualModel("free:s", tracker)
	if !isVirtual {
		t.Fatalf("expected isVirtual to be true")
	}
	qS := lookupQuality(modelS)
	if qS.Tier != TierS && qS.Tier != TierSPlus {
		t.Errorf("expected Tier S or S+, got %s for model %s", qS.Tier, modelS)
	}

	// Mark top model in cooldown
	tracker.markCooldown(modelS, 10*time.Minute)
	modelS2, _ := resolveVirtualModel("free:s", tracker)
	if modelS2 == modelS {
		t.Errorf("expected different model after marking %s cooling down", modelS)
	}
	qS2 := lookupQuality(modelS2)
	if qS2.Tier != TierS && qS2.Tier != TierSPlus {
		t.Errorf("expected Tier S or S+ for fallback, got %s for model %s", qS2.Tier, modelS2)
	}

	// Test free:coding resolves to a model with coding tag
	modelCode, isVirtualCode := resolveVirtualModel("openrouter/free:coding", tracker)
	if !isVirtualCode {
		t.Fatalf("expected isVirtualCode to be true")
	}
	qCode := lookupQuality(modelCode)
	hasCoding := false
	for _, tag := range qCode.Tags {
		if tag == "coding" {
			hasCoding = true
			break
		}
	}
	if !hasCoding {
		t.Errorf("expected coding tag for %s, tags: %v", modelCode, qCode.Tags)
	}
}

func TestQualityPreservingFallback(t *testing.T) {
	tracker := &cooldownTracker{cooldowns: make(map[string]time.Time)}

	failedModel := "google/gemma-4-31b-it:free" // Tier S
	tracker.markCooldown(failedModel, 10*time.Minute)

	fallback := qualityPreservingFallback(failedModel, tracker)
	if fallback == failedModel {
		t.Errorf("fallback returned same failed model")
	}

	qFallback := lookupQuality(fallback)
	// Must be Tier S or S+ (same tier priority)
	if qFallback.Tier != TierS && qFallback.Tier != TierSPlus {
		t.Errorf("expected Tier S or S+ preserving fallback, got %s for %s", qFallback.Tier, fallback)
	}
}
