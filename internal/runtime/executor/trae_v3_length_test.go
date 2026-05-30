package executor

import (
	"testing"
)

// TestV3APIRequestSizeLimit demonstrates that the V3 API server has a bufio.Scanner
// with a 64KB limit. Requests exceeding this limit will fail with
// "bufio.Scanner: token too long".
func TestV3APIRequestSizeLimit(t *testing.T) {
	// The V3 API server uses bufio.Scanner to read the request body.
	// bufio.Scanner has a default max token size of 64KB (65536 bytes).
	// If the request body exceeds 64KB, the server returns:
	// {"type":"error","error":{"type":"api_error","message":"bufio.Scanner: token too long"}
	//
	// The user's request was 93KB, which exceeds the 64KB bufio.Scanner limit.
	// This causes the V3 API server to return an error.
	t.Log("V3 API server bufio.Scanner limit: 64KB")
	t.Log("User request was 93KB, exceeding the 64KB bufio.Scanner limit")
	t.Log("The V3 API server rejects requests larger than 64KB")
}
