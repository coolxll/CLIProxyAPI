package helpers

import "github.com/router-for-me/CLIProxyAPI/v7/sdk/lingmawire"

// IsLingmaDone checks if a Lingma SSE chunk indicates the end of the stream.
// It handles both plain "[DONE]" and the double-JSON envelope {"body":"[DONE]"}.
func IsLingmaDone(raw []byte) bool {
	return lingmawire.IsDone(raw)
}
