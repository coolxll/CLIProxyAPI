package openrouter

import (
	"fmt"
	"sort"
	"strings"
)

// QualityTier represents the benchmark capability tier of a model.
type QualityTier string

const (
	TierSPlus   QualityTier = "S+"
	TierS       QualityTier = "S"
	TierAPlus   QualityTier = "A+"
	TierA       QualityTier = "A"
	TierAMinus  QualityTier = "A-"
	TierBPlus   QualityTier = "B+"
	TierB       QualityTier = "B"
	TierC       QualityTier = "C"
	TierUnknown QualityTier = "?"
)

// ModelQuality contains benchmark metadata and capability tags for a model.
type ModelQuality struct {
	ModelID        string      `json:"model_id"`
	Name           string      `json:"name"`
	Tier           QualityTier `json:"tier"`
	SWEBench       string      `json:"swe_bench,omitempty"`
	AAIntelligence float64     `json:"aa_intelligence,omitempty"`
	AASpeedTPS     float64     `json:"aa_speed_tps,omitempty"`
	Context        string      `json:"context,omitempty"`
	Tags           []string    `json:"tags,omitempty"`
}

// tierRank maps tiers to comparable integer weights (higher is better).
func tierRank(tier QualityTier) int {
	switch tier {
	case TierSPlus:
		return 100
	case TierS:
		return 90
	case TierAPlus:
		return 80
	case TierA:
		return 70
	case TierAMinus:
		return 60
	case TierBPlus:
		return 50
	case TierB:
		return 40
	case TierC:
		return 30
	default:
		return 10
	}
}

// knownModelRankings stores verified benchmark rankings derived from Artificial Analysis & SWE-bench.
var knownModelRankings = []ModelQuality{
	{
		ModelID:        "nvidia/nemotron-3-super-120b-a12b:free",
		Name:           "NVIDIA Nemotron 3 Super 120B",
		Tier:           TierS,
		SWEBench:       "60.5%",
		AAIntelligence: 36.0,
		AASpeedTPS:     449.5,
		Context:        "262k",
		Tags:           []string{"reasoning", "fast", "general"},
	},
	{
		ModelID:        "deepseek/deepseek-r1:free",
		Name:           "DeepSeek R1",
		Tier:           TierSPlus,
		SWEBench:       "79.8%",
		AAIntelligence: 55.0,
		Context:        "128k",
		Tags:           []string{"reasoning", "coding"},
	},
	{
		ModelID:        "deepseek/deepseek-chat:free",
		Name:           "DeepSeek V3",
		Tier:           TierSPlus,
		SWEBench:       "73.1%",
		AAIntelligence: 48.0,
		AASpeedTPS:     80.0,
		Context:        "128k",
		Tags:           []string{"coding", "general"},
	},
	{
		ModelID:        "minimax/minimax-m2.5:free",
		Name:           "MiniMax M2.5",
		Tier:           TierS,
		AAIntelligence: 41.9,
		AASpeedTPS:     92.8,
		Context:        "197k",
		Tags:           []string{"general", "fast"},
	},
	{
		ModelID:        "tencent/hy3:free",
		Name:           "Tencent Hy3",
		Tier:           TierS,
		AAIntelligence: 41.9,
		AASpeedTPS:     84.5,
		Context:        "262k",
		Tags:           []string{"general"},
	},
	{
		ModelID:        "google/gemma-4-31b-it:free",
		Name:           "Gemma 4 31B IT",
		Tier:           TierS,
		AAIntelligence: 39.2,
		AASpeedTPS:     35.5,
		Context:        "33k",
		Tags:           []string{"general", "reasoning"},
	},
	{
		ModelID: "google/gemma-4-26b-a4b-it:free",
		Name:    "Gemma 4 26B A4B IT",
		Tier:    TierS,
		Context: "128k",
		Tags:    []string{"general", "reasoning"},
	},
	{
		ModelID:        "qwen/qwen3-coder:free",
		Name:           "Qwen3 Coder 480B",
		Tier:           TierAPlus,
		SWEBench:       "70.6%",
		AAIntelligence: 45.0,
		AASpeedTPS:     69.9,
		Context:        "262k",
		Tags:           []string{"coding"},
	},
	{
		ModelID:        "openai/gpt-oss-120b:free",
		Name:           "GPT OSS 120B",
		Tier:           TierAPlus,
		SWEBench:       "60.0%",
		AAIntelligence: 33.3,
		AASpeedTPS:     221.0,
		Context:        "131k",
		Tags:           []string{"general", "fast"},
	},
	{
		ModelID:        "inclusionai/ling-2.6-1t:free",
		Name:           "Ling 2.6 1T",
		Tier:           TierAPlus,
		AAIntelligence: 33.6,
		Context:        "262k",
		Tags:           []string{"general"},
	},
	{
		ModelID:        "qwen/qwen-2.5-coder-32b-instruct:free",
		Name:           "Qwen 2.5 Coder 32B",
		Tier:           TierA,
		SWEBench:       "46.0%",
		AAIntelligence: 12.9,
		Context:        "32k",
		Tags:           []string{"coding"},
	},
	{
		ModelID:        "inclusionai/ling-2.6-flash:free",
		Name:           "Ling 2.6 Flash",
		Tier:           TierA,
		AAIntelligence: 26.2,
		AASpeedTPS:     206.6,
		Context:        "262k",
		Tags:           []string{"general", "fast"},
	},
	{
		ModelID:        "openai/gpt-oss-20b:free",
		Name:           "GPT OSS 20B",
		Tier:           TierA,
		SWEBench:       "42.0%",
		AAIntelligence: 24.5,
		AASpeedTPS:     277.7,
		Context:        "131k",
		Tags:           []string{"general", "fast"},
	},
	{
		ModelID:        "z-ai/glm-4.5-air:free",
		Name:           "GLM 4.5 Air",
		Tier:           TierA,
		AAIntelligence: 23.2,
		AASpeedTPS:     60.0,
		Context:        "128k",
		Tags:           []string{"general"},
	},
	{
		ModelID:        "qwen/qwen3-80b-instruct:free",
		Name:           "Qwen3 80B Instruct",
		Tier:           TierA,
		SWEBench:       "65.0%",
		AAIntelligence: 20.1,
		AASpeedTPS:     174.5,
		Context:        "262k",
		Tags:           []string{"general", "fast"},
	},
	{
		ModelID:        "nousresearch/hermes-3-llama-3.1-405b:free",
		Name:           "Hermes 3 405B",
		Tier:           TierA,
		AAIntelligence: 18.6,
		AASpeedTPS:     34.2,
		Context:        "131k",
		Tags:           []string{"general"},
	},
	{
		ModelID:  "meta-llama/llama-3.3-70b-instruct:free",
		Name:     "Llama 3.3 70B Instruct",
		Tier:     TierAMinus,
		SWEBench: "39.5%",
		Context:  "128k",
		Tags:     []string{"general", "coding"},
	},
	{
		ModelID: "nvidia/nemotron-3.5-lightning:free",
		Name:    "Nemotron 3.5 Lightning",
		Tier:    TierA,
		Context: "128k",
		Tags:    []string{"general", "fast"},
	},
	{
		ModelID:  "nvidia/nemotron-nano-30b:free",
		Name:     "Nemotron Nano 30B",
		Tier:     TierBPlus,
		SWEBench: "43.0%",
		Context:  "256k",
		Tags:     []string{"general"},
	},
	{
		ModelID:        "google/gemma-3-27b-it:free",
		Name:           "Gemma 3 27B IT",
		Tier:           TierB,
		AAIntelligence: 10.3,
		AASpeedTPS:     25.9,
		Context:        "131k",
		Tags:           []string{"general"},
	},
	{
		ModelID:        "meta-llama/llama-3.2-3b-instruct:free",
		Name:           "Llama 3.2 3B Instruct",
		Tier:           TierB,
		AAIntelligence: 9.7,
		AASpeedTPS:     51.8,
		Context:        "131k",
		Tags:           []string{"general", "fast"},
	},
	{
		ModelID:        "google/gemma-3-12b-it:free",
		Name:           "Gemma 3 12B IT",
		Tier:           TierB,
		AAIntelligence: 8.8,
		AASpeedTPS:     25.0,
		Context:        "33k",
		Tags:           []string{"general"},
	},
	{
		ModelID:        "liquid/lfm-2.5-1.2b-thinking:free",
		Name:           "LFM 2.5 1.2B Thinking",
		Tier:           TierB,
		AAIntelligence: 8.1,
		Context:        "33k",
		Tags:           []string{"reasoning"},
	},
	{
		ModelID:        "google/gemma-3-4b-it:free",
		Name:           "Gemma 3 4B IT",
		Tier:           TierC,
		AAIntelligence: 6.3,
		AASpeedTPS:     26.1,
		Context:        "33k",
		Tags:           []string{"general"},
	},
	{
		ModelID:        "google/gemma-3n-2b:free",
		Name:           "Gemma 3n 2B",
		Tier:           TierC,
		AAIntelligence: 4.8,
		AASpeedTPS:     60.9,
		Context:        "8k",
		Tags:           []string{"general"},
	},
}

// lookupQuality resolves model quality from static rankings or heuristic analysis.
func lookupQuality(modelID string) ModelQuality {
	clean := strings.ToLower(strings.TrimSpace(modelID))
	clean = strings.TrimPrefix(clean, "openrouter/")
	bare := strings.TrimSuffix(clean, ":free")

	for _, k := range knownModelRankings {
		kClean := strings.ToLower(k.ModelID)
		kBare := strings.TrimSuffix(kClean, ":free")
		if clean == kClean || clean == kBare || bare == kClean || bare == kBare {
			return k
		}
	}

	return deriveQualityHeuristic(modelID)
}

// deriveQualityHeuristic guesses quality metrics based on model name and parameter patterns.
func deriveQualityHeuristic(modelID string) ModelQuality {
	clean := strings.ToLower(strings.TrimSpace(modelID))

	var tags []string
	if strings.Contains(clean, "code") || strings.Contains(clean, "coder") {
		tags = append(tags, "coding")
	}
	if strings.Contains(clean, "r1") || strings.Contains(clean, "think") || strings.Contains(clean, "reason") {
		tags = append(tags, "reasoning")
	}
	if strings.Contains(clean, "flash") || strings.Contains(clean, "lightning") || strings.Contains(clean, "turbo") {
		tags = append(tags, "fast")
	}
	if len(tags) == 0 {
		tags = append(tags, "general")
	}

	tier := TierB
	switch {
	case strings.Contains(clean, "480b") || strings.Contains(clean, "405b") || strings.Contains(clean, "400b") ||
		strings.Contains(clean, "super") || strings.Contains(clean, "ultra") || strings.Contains(clean, "r1"):
		tier = TierS
	case strings.Contains(clean, "120b") || strings.Contains(clean, "70b") || strings.Contains(clean, "72b") ||
		strings.Contains(clean, "67b") || strings.Contains(clean, "32b") || strings.Contains(clean, "34b") ||
		strings.Contains(clean, "31b") || strings.Contains(clean, "26b"):
		tier = TierA
	case strings.Contains(clean, "14b") || strings.Contains(clean, "12b") || strings.Contains(clean, "8b") ||
		strings.Contains(clean, "9b") || strings.Contains(clean, "7b") || strings.Contains(clean, "27b"):
		tier = TierB
	case strings.Contains(clean, "1b") || strings.Contains(clean, "2b") || strings.Contains(clean, "3b") ||
		strings.Contains(clean, "4b") || strings.Contains(clean, "mini") || strings.Contains(clean, "nano"):
		tier = TierC
	}

	return ModelQuality{
		ModelID: modelID,
		Name:    modelID,
		Tier:    tier,
		Context: "32k",
		Tags:    tags,
	}
}

// formatModelDescription renders a human-readable quality badge for ModelInfo.Description.
func formatModelDescription(q ModelQuality) string {
	parts := []string{fmt.Sprintf("Tier: %s", q.Tier)}
	if q.SWEBench != "" {
		parts = append(parts, fmt.Sprintf("SWE: %s", q.SWEBench))
	}
	if q.AAIntelligence > 0 {
		parts = append(parts, fmt.Sprintf("AA Intel: %.1f", q.AAIntelligence))
	}
	if q.AASpeedTPS > 0 {
		parts = append(parts, fmt.Sprintf("%.0f t/s", q.AASpeedTPS))
	}
	if q.Context != "" {
		parts = append(parts, fmt.Sprintf("Context: %s", q.Context))
	}
	if len(q.Tags) > 0 {
		parts = append(parts, strings.Join(q.Tags, ", "))
	}
	prefix := fmt.Sprintf("[%s]", strings.Join(parts, " | "))
	if q.Name != "" && q.Name != q.ModelID {
		return fmt.Sprintf("%s %s", prefix, q.Name)
	}
	return prefix
}

// virtualModelKind classifies virtual intent routing aliases.
type virtualModelKind string

const (
	virtualKindNone      virtualModelKind = ""
	virtualKindAll       virtualModelKind = "all"
	virtualKindTierS     virtualModelKind = "tier_s"
	virtualKindTierA     virtualModelKind = "tier_a"
	virtualKindTierB     virtualModelKind = "tier_b"
	virtualKindCoding    virtualModelKind = "coding"
	virtualKindReasoning virtualModelKind = "reasoning"
	virtualKindFast      virtualModelKind = "fast"
)

// classifyVirtualModel checks if a model name is a virtual quality / intent alias.
func classifyVirtualModel(modelID string) virtualModelKind {
	clean := strings.ToLower(strings.TrimSpace(modelID))
	clean = strings.TrimPrefix(clean, "openrouter/")

	switch clean {
	case "free", "openrouter/free":
		return virtualKindAll
	case "free:s", "tier:s", "tier-s", "s":
		return virtualKindTierS
	case "free:a", "tier:a", "tier-a", "a":
		return virtualKindTierA
	case "free:b", "tier:b", "tier-b", "b":
		return virtualKindTierB
	case "free:coding", "free:code", "coding", "code":
		return virtualKindCoding
	case "free:reasoning", "free:thinking", "reasoning", "thinking":
		return virtualKindReasoning
	case "free:fast", "fast":
		return virtualKindFast
	default:
		return virtualKindNone
	}
}

// getRankedCandidates returns models matching the virtual criteria sorted by priority.
func getRankedCandidates(kind virtualModelKind) []string {
	var candidates []ModelQuality

	for _, m := range knownModelRankings {
		switch kind {
		case virtualKindAll:
			candidates = append(candidates, m)
		case virtualKindTierS:
			if m.Tier == TierSPlus || m.Tier == TierS {
				candidates = append(candidates, m)
			}
		case virtualKindTierA:
			if m.Tier == TierAPlus || m.Tier == TierA || m.Tier == TierAMinus {
				candidates = append(candidates, m)
			}
		case virtualKindTierB:
			if m.Tier == TierBPlus || m.Tier == TierB {
				candidates = append(candidates, m)
			}
		case virtualKindCoding:
			for _, tag := range m.Tags {
				if tag == "coding" {
					candidates = append(candidates, m)
					break
				}
			}
		case virtualKindReasoning:
			for _, tag := range m.Tags {
				if tag == "reasoning" {
					candidates = append(candidates, m)
					break
				}
			}
		case virtualKindFast:
			for _, tag := range m.Tags {
				if tag == "fast" {
					candidates = append(candidates, m)
					break
				}
			}
		}
	}

	// Sort candidates by tier rank descending, then AAIntelligence descending
	sort.SliceStable(candidates, func(i, j int) bool {
		rI, rJ := tierRank(candidates[i].Tier), tierRank(candidates[j].Tier)
		if rI != rJ {
			return rI > rJ
		}
		return candidates[i].AAIntelligence > candidates[j].AAIntelligence
	})

	result := make([]string, 0, len(candidates))
	for _, c := range candidates {
		result = append(result, c.ModelID)
	}
	return result
}

// resolveVirtualModel attempts to resolve a virtual intent alias to the highest-ranking active model.
func resolveVirtualModel(requestedModel string, tracker *cooldownTracker) (string, bool) {
	kind := classifyVirtualModel(requestedModel)
	if kind == virtualKindNone {
		return requestedModel, false
	}

	candidates := getRankedCandidates(kind)
	for _, c := range candidates {
		if tracker == nil || !tracker.isCoolingDown(c) {
			return c, true
		}
	}

	// If all candidates in this category are cooling down, fall back to openrouter/free
	return "openrouter/free", true
}

// qualityPreservingFallback finds the next best available model in the same or adjacent tier.
func qualityPreservingFallback(failedModel string, tracker *cooldownTracker) string {
	failedQuality := lookupQuality(failedModel)
	failedRank := tierRank(failedQuality.Tier)

	// Collect candidates sorted by proximity to the failed tier
	type candidateDiff struct {
		model string
		diff  int
		rank  int
	}

	var candidates []candidateDiff
	for _, m := range knownModelRankings {
		if strings.EqualFold(m.ModelID, failedModel) {
			continue
		}
		if tracker != nil && tracker.isCoolingDown(m.ModelID) {
			continue
		}
		mRank := tierRank(m.Tier)
		diff := mRank - failedRank
		if diff < 0 {
			diff = -diff
		}
		candidates = append(candidates, candidateDiff{
			model: m.ModelID,
			diff:  diff,
			rank:  mRank,
		})
	}

	// Sort candidates: smallest tier diff first, then highest rank
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].diff != candidates[j].diff {
			return candidates[i].diff < candidates[j].diff
		}
		return candidates[i].rank > candidates[j].rank
	})

	if len(candidates) > 0 {
		return candidates[0].model
	}

	return "openrouter/free"
}
