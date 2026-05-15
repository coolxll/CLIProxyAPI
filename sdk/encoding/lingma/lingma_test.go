package lingma

import (
	"bytes"
	"testing"
)

func TestLingmaEncodeDecode(t *testing.T) {
	testCases := []struct {
		name      string
		plaintext string
	}{
		{"empty", ""},
		{"short", "hello"},
		{"json", `{"model":"dashscope_qmodel","messages":[{"role":"user","content":"hello"}]}`},
		{"long", `Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat.`},
		{"symbols", `!@#$%^&*()_+-=[]{}|;':",./<>?`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			plaintext := []byte(tc.plaintext)

			encoded := Encode(plaintext)

			decoded, err := Decode(encoded)
			if err != nil {
				t.Fatalf("Decode failed: %v", err)
			}

			if !bytes.Equal(plaintext, decoded) {
				t.Errorf("Mismatch. \nExpected: %s\nGot: %s", plaintext, decoded)
			}
		})
	}
}

// Ensure the specific Lingma structure works correctly
func TestLingmaSpecials(t *testing.T) {
	// A sample token string typical of lingma tokens
	token := "abc123def456ghi789jkl012mno345pqr678stu901vwx234yz"

	encoded := Encode([]byte(token))
	if len(encoded) == 0 {
		t.Fatal("encoded is empty")
	}

	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if string(decoded) != token {
		t.Errorf("decoded %q != original %q", decoded, token)
	}
}
