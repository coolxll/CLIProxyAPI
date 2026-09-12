package openrouter

import (
	"sort"
	"strings"
	"sync"
	"time"
)

type cooldownTracker struct {
	mu        sync.RWMutex
	cooldowns map[string]time.Time
}

var globalCooldowns = &cooldownTracker{
	cooldowns: make(map[string]time.Time),
}

func (c *cooldownTracker) isCoolingDown(model string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	expiry, exists := c.cooldowns[strings.ToLower(strings.TrimSpace(model))]
	if !exists {
		return false
	}
	return time.Now().Before(expiry)
}

func (c *cooldownTracker) markCooldown(model string, duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if duration <= 0 {
		duration = 10 * time.Minute
	}
	c.cooldowns[strings.ToLower(strings.TrimSpace(model))] = time.Now().Add(duration)
}

func (c *cooldownTracker) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cooldowns = make(map[string]time.Time)
}

func (c *cooldownTracker) nextFallbackModel(failedModel string, availableModels []string) string {
	failedClean := strings.ToLower(strings.TrimSpace(failedModel))
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 1. If explicit availableModels are provided, filter and sort by quality proximity
	if len(availableModels) > 0 {
		failedQuality := lookupQuality(failedModel)
		failedRank := tierRank(failedQuality.Tier)

		type candidateScore struct {
			model string
			diff  int
			rank  int
		}
		var candidates []candidateScore
		for _, m := range availableModels {
			clean := strings.ToLower(strings.TrimSpace(m))
			if clean == failedClean {
				continue
			}
			if !c.isCoolingDown(clean) {
				mQuality := lookupQuality(clean)
				mRank := tierRank(mQuality.Tier)
				diff := mRank - failedRank
				if diff < 0 {
					diff = -diff
				}
				candidates = append(candidates, candidateScore{
					model: m,
					diff:  diff,
					rank:  mRank,
				})
			}
		}

		if len(candidates) > 0 {
			sort.SliceStable(candidates, func(i, j int) bool {
				if candidates[i].diff != candidates[j].diff {
					return candidates[i].diff < candidates[j].diff
				}
				return candidates[i].rank > candidates[j].rank
			})
			return candidates[0].model
		}
	}

	// 2. Otherwise use quality-preserving fallback from the known catalog
	if failedClean != "openrouter/free" && failedClean != "free" {
		fallback := qualityPreservingFallback(failedModel, c)
		if fallback != "" && !strings.EqualFold(fallback, failedClean) && !c.isCoolingDown(fallback) {
			return fallback
		}
	}

	// 3. Fallback to openrouter/free as last resort
	return "openrouter/free"
}
