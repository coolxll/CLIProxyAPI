package openrouter

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// liveModelCacheTTL bounds how long a successful upstream listing is reused when
// resolving virtual routers and auto-failover candidates.
const liveModelCacheTTL = 5 * time.Minute

type liveModelEntry struct {
	freeIDs     map[string]struct{}
	orderedFree []string
	fetchedAt   time.Time
}

var liveModels = struct {
	mu      sync.RWMutex
	entries map[string]liveModelEntry
}{entries: make(map[string]liveModelEntry)}

// normalizeModelID lowercases a model ID and strips the openrouter/ prefix so
// live listing IDs can be matched against the bare IDs used by the catalog.
func normalizeModelID(id string) string {
	clean := strings.ToLower(strings.TrimSpace(id))
	return strings.TrimPrefix(clean, "openrouter/")
}

// storeLiveModels records the free models of a successful listing so request
// time routing only ever targets models this account can actually reach.
func storeLiveModels(creds credentials, models []pluginapi.ModelInfo) {
	freeIDs := make(map[string]struct{}, len(models))
	ordered := make([]string, 0, len(models))
	for _, model := range models {
		if classifyVirtualModel(model.ID) != virtualKindNone {
			continue
		}
		if !isFreeModel(model.ID) {
			continue
		}
		normalized := normalizeModelID(model.ID)
		if normalized == "" {
			continue
		}
		if _, exists := freeIDs[normalized]; exists {
			continue
		}
		freeIDs[normalized] = struct{}{}
		ordered = append(ordered, normalized)
	}
	sort.Strings(ordered)

	liveModels.mu.Lock()
	defer liveModels.mu.Unlock()
	liveModels.entries[stableAuthID(creds)] = liveModelEntry{
		freeIDs:     freeIDs,
		orderedFree: ordered,
		fetchedAt:   time.Now(),
	}
}

// liveFreeModels returns the cached free models for an auth, refreshing them from
// the upstream listing when the cache is missing or stale.
func liveFreeModels(host hostRPC, creds credentials) (liveModelEntry, error) {
	key := stableAuthID(creds)
	liveModels.mu.RLock()
	entry, cached := liveModels.entries[key]
	liveModels.mu.RUnlock()
	if cached && time.Since(entry.fetchedAt) < liveModelCacheTTL {
		return entry, nil
	}

	if _, err := fetchModels(host, creds); err != nil {
		return liveModelEntry{}, err
	}

	liveModels.mu.RLock()
	defer liveModels.mu.RUnlock()
	if entry, ok := liveModels.entries[key]; ok {
		return entry, nil
	}
	return liveModelEntry{freeIDs: map[string]struct{}{}}, nil
}

// resetLiveModelCache drops every cached listing.
func resetLiveModelCache() {
	liveModels.mu.Lock()
	defer liveModels.mu.Unlock()
	liveModels.entries = make(map[string]liveModelEntry)
}

// resolveVirtualModel maps a virtual router request to a concrete model present in
// the live listing. Candidates keep the curated catalog order; the catch-all
// router additionally accepts live free models without a catalog entry in
// deterministic name order. It returns ("", true) when the request is virtual but
// no live candidate is currently usable.
func resolveVirtualModel(requested string, live liveModelEntry, tracker *cooldownTracker) (string, bool) {
	kind := classifyVirtualModel(requested)
	if kind == virtualKindNone {
		return requested, false
	}

	candidates := getRankedCandidates(kind)
	if kind == virtualKindAll {
		candidates = append(candidates, extraLiveCandidates(live, candidates)...)
	}
	for _, candidate := range candidates {
		if _, exists := live.freeIDs[normalizeModelID(candidate)]; !exists {
			continue
		}
		if tracker != nil && tracker.isCoolingDown(candidate) {
			continue
		}
		return candidate, true
	}
	return "", true
}

// extraLiveCandidates returns live free models that have no catalog entry.
func extraLiveCandidates(live liveModelEntry, ranked []string) []string {
	rankedSet := make(map[string]struct{}, len(ranked))
	for _, id := range ranked {
		rankedSet[normalizeModelID(id)] = struct{}{}
	}
	extras := make([]string, 0, len(live.orderedFree))
	for _, id := range live.orderedFree {
		if _, exists := rankedSet[id]; exists {
			continue
		}
		extras = append(extras, id)
	}
	return extras
}

// resolveRequestedModel maps the client requested model to the model that is
// actually sent upstream. Virtual routers and cooled-down models resolve against
// the live listing so the plugin never substitutes a model the account cannot use.
func resolveRequestedModel(host hostRPC, creds credentials, requested string) (string, error) {
	target := cleanModelID(requested)
	virtual := classifyVirtualModel(target) != virtualKindNone
	coolingDown := globalCooldowns.isCoolingDown(target)
	if !virtual && !(coolingDown && creds.isAutoFailover()) {
		return target, nil
	}

	live, err := liveFreeModels(host, creds)
	if err != nil {
		return "", fmt.Errorf("resolve OpenRouter model %q: %w", requested, err)
	}

	if virtual {
		resolved, isVirtual := resolveVirtualModel(target, live, globalCooldowns)
		if isVirtual {
			if resolved == "" {
				return "", fmt.Errorf("OpenRouter virtual model %q has no available free model for this account", requested)
			}
			return resolved, nil
		}
	}

	if coolingDown && creds.isAutoFailover() {
		fallback := globalCooldowns.nextFallbackModel(target, live.orderedFree)
		if fallback == "" {
			return "", fmt.Errorf("OpenRouter model %q is cooling down and no live free fallback is available", target)
		}
		return fallback, nil
	}
	return target, nil
}

// nextLiveFallback returns the best live free fallback for a failed model.
func nextLiveFallback(host hostRPC, creds credentials, failedModel string) (string, error) {
	live, err := liveFreeModels(host, creds)
	if err != nil {
		return "", err
	}
	fallback := globalCooldowns.nextFallbackModel(failedModel, live.orderedFree)
	if fallback == "" {
		return "", fmt.Errorf("no live free fallback available for %q", failedModel)
	}
	return fallback, nil
}
