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

	// When modelA fails, fallback should prefer closer tier modelB over unknown general router
	fallback := tracker.nextFallbackModel(modelA, available)
	if fallback != modelB {
		t.Errorf("expected modelB (%s) as tier-preserving fallback, got %q", modelB, fallback)
	}

	// When modelB is also in cooldown, should pick openrouter/free as last resort
	tracker.markCooldown(modelB, 10*time.Minute)
	fallback2 := tracker.nextFallbackModel(modelA, available)
	if fallback2 != "openrouter/free" {
		t.Errorf("expected openrouter/free as last resort fallback, got %q", fallback2)
	}
}
