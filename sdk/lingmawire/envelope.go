package lingmawire

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// StripSSEPrefix strips the "data:" or "event:" prefix and surrounding whitespace
// from an SSE line. If the line is an "event:" line or empty, returns nil.
func StripSSEPrefix(raw []byte) []byte {
	data := bytes.TrimSpace(raw)
	if bytes.HasPrefix(data, []byte("event:")) {
		return nil
	}
	if bytes.HasPrefix(data, []byte("data:")) {
		data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("data:")))
	}
	return data
}

// IsDone checks if a Lingma SSE chunk indicates the end of the stream.
// It handles plain "[DONE]", "data: [DONE]", and the double-JSON envelope {"body":"[DONE]"}.
func IsDone(raw []byte) bool {
	data := StripSSEPrefix(raw)
	if len(data) == 0 {
		return false
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return true
	}
	if !gjson.ValidBytes(data) {
		return false
	}
	root := gjson.ParseBytes(data)
	return root.Get("body").String() == "[DONE]"
}

// UnwrapBody unwraps a double-JSON envelope if present:
// e.g. {"body": "{\"choices\":...}"} or {"body": {...}}
// Returns the unwrapped inner payload bytes, or the trimmed bytes if not wrapped.
func UnwrapBody(raw []byte) []byte {
	data := StripSSEPrefix(raw)
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return data
	}
	root := gjson.ParseBytes(data)
	if bodyNode := root.Get("body"); bodyNode.Exists() {
		if bodyNode.Type == gjson.String {
			innerStr := bodyNode.String()
			if gjson.Valid(innerStr) {
				return []byte(innerStr)
			}
		} else if bodyNode.IsObject() {
			return []byte(bodyNode.Raw)
		}
	}
	return data
}
