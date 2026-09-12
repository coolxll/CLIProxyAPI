package openrouter

import (
	"testing"
	"time"
)

func TestCooldownTracker(t *testing.T) {
	tracker := &cooldownTracker{
		cooldowns: make(map[string]time.Time),
	}

	modelA := "google/gemma-4-31b-it:free"
	if tracker.isCoolingDown(modelA) {
		t.Errorf("modelA should not be in cooldown initially")
	}

	tracker.markCooldown(modelA, 50*time.Millisecond)
	if !tracker.isCoolingDown(modelA) {
		t.Errorf("modelA should be in cooldown after markCooldown")
	}

	time.Sleep(60 * time.Millisecond)
	if tracker.isCoolingDown(modelA) {
		t.Errorf("modelA should have expired from cooldown")
	}
}

func TestNextFallbackModel(t *testing.T) {
	tracker := &cooldownTracker{
		cooldowns: make(map[string]time.Time),
	}

	modelA := "google/gemma-4-31b-it:free"         // Tier S
	modelB := "nvidia/nemotron-3.5-lightning:free" // Tier A
	available := []string{modelA, modelB, "openrouter/free"}

	// When modelA fails, fallback should prefer the live closer-tier modelB.
	fallback := tracker.nextFallbackModel(modelA, available)
	if fallback != modelB {
		t.Errorf("expected modelB (%s) as tier-preserving fallback, got %q", modelB, fallback)
	}

	// The failed model itself is never returned.
	if tracker.nextFallbackModel(modelB, available) == modelB {
		t.Errorf("fallback must not return the failed model")
	}

	// When no live candidate is left, there is no fallback: callers must fail
	// instead of routing to a catalog model this account may not have.
	tracker.markCooldown(modelB, 10*time.Minute)
	if fallback2 := tracker.nextFallbackModel(modelA, available); fallback2 != "" {
		t.Errorf("expected no fallback, got %q", fallback2)
	}
	if fallback3 := tracker.nextFallbackModel(modelA, nil); fallback3 != "" {
		t.Errorf("expected no fallback without a live listing, got %q", fallback3)
	}
}
