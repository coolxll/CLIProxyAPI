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

// nextFallbackModel picks the best replacement for a failed model from the live
// listing. It returns "" when no usable candidate is known, so callers fail
// honestly instead of routing to a model the account may not be able to reach.
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
			if clean == failedClean || classifyVirtualModel(clean) != virtualKindNone {
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

	// 2. Without a live listing there is no safe fallback: routing to a catalog
	// model this account may not have would silently substitute an unavailable
	// model, so report that nothing is available instead.
	return ""
}
