package lingma

import (
	"strings"

	"github.com/tidwall/gjson"
)

// nativeParseLingmaModelsForTest is a copy of the native parseLingmaModels
// function for golden parity testing. This avoids importing the native package
// which would create a circular dependency. The function is kept in sync with
// internal/runtime/executor/lingma_executor.go:601-661.
//
// The only difference from the plugin parseModels is that this function does
// NOT trim key.String() when extracting the fallback model ID.
func nativeParseLingmaModelsForTest(data []byte, now int64) []nativeModelInfo {
	root := gjson.ParseBytes(data)
	roots := []gjson.Result{root}
	if wrapped := root.Get("data"); wrapped.Exists() {
		roots = append(roots, wrapped)
	}
	if wrapped := root.Get("result"); wrapped.Exists() {
		roots = append(roots, wrapped)
	}

	models := make([]nativeModelInfo, 0)
	seen := make(map[string]struct{})
	appendModel := func(key, value gjson.Result) {
		modelID := nativeFirstModelString(value, "key", "modelId", "model_id", "modelName", "model_name", "id", "name")
		if modelID == "" && key.Type == gjson.String {
			// NATIVE BEHAVIOR: does NOT trim
			modelID = key.String()
		}
		if modelID == "" {
			return
		}
		dedupeKey := strings.ToLower(modelID)
		if _, ok := seen[dedupeKey]; ok {
			return
		}
		seen[dedupeKey] = struct{}{}

		displayName := nativeFirstModelString(value, "display_name", "displayName", "modelName", "model_name", "name", "label")
		if displayName == "" {
			displayName = modelID
		}
		models = append(models, nativeModelInfo{
			ID:          modelID,
			Object:      "model",
			Created:     now,
			OwnedBy:     "alibaba",
			Type:        "lingma",
			DisplayName: displayName,
		})
	}

	for _, candidate := range roots {
		if candidate.IsArray() {
			candidate.ForEach(func(key, value gjson.Result) bool {
				appendModel(key, value)
				return true
			})
		}
		for _, cat := range []string{"chat", "developer", "assistant", "inline"} {
			group := candidate.Get(cat)
			if !group.Exists() {
				continue
			}
			group.ForEach(func(key, value gjson.Result) bool {
				appendModel(key, value)
				return true
			})
		}
	}

	return models
}

type nativeModelInfo struct {
	ID          string
	Object      string
	Created     int64
	OwnedBy     string
	Type        string
	DisplayName string
}

func nativeFirstModelString(value gjson.Result, keys ...string) string {
	for _, key := range keys {
		if candidate := value.Get(key); candidate.Exists() {
			if result := strings.TrimSpace(candidate.String()); result != "" {
				return result
			}
		}
	}
	return ""
}
