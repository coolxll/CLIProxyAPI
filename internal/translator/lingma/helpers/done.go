package helpers

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// IsLingmaDone checks if a Lingma SSE chunk indicates the end of the stream.
// It handles both plain "[DONE]" and the double-JSON envelope {"body":"[DONE]"}.
func IsLingmaDone(raw []byte) bool {
	data := bytes.TrimSpace(raw)
	if bytes.HasPrefix(data, []byte("data:")) {
		data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("data:")))
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return true
	}
	root := gjson.ParseBytes(data)
	return root.Get("body").String() == "[DONE]"
}
