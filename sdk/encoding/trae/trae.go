package trae

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// TraeKeyHex is the hex key used by Trae for AES-GCM encryption.
const TraeKeyHex = "6195f24ca4d430f8a4833de7db8dac37d148a084e7464a351ffa68585c16b955"

// EncryptedMessage represents the structure of an encrypted payload for Trae backend.
type EncryptedMessage struct {
	Message    string `json:"message"`
	RequestPin string `json:"requestPin"`
	RequestAt  int64  `json:"requestAt"`
}

// EncryptMessage encrypts a plaintext using the current time as RequestAt.
func EncryptMessage(plaintext []byte) (*EncryptedMessage, error) {
	return EncryptMessageAt(plaintext, time.Now().Unix())
}

// EncryptMessageAt encrypts a plaintext using a specified timestamp for RequestAt.
func EncryptMessageAt(plaintext []byte, requestAt int64) (*EncryptedMessage, error) {
	key, errDec := hex.DecodeString(TraeKeyHex)
	if errDec != nil {
		return nil, fmt.Errorf("decode trae key: %w", errDec)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid trae key length: %d", len(key))
	}

	pin := make([]byte, 8)
	if _, errPin := io.ReadFull(rand.Reader, pin); errPin != nil {
		return nil, fmt.Errorf("generate request pin: %w", errPin)
	}
	for i := 0; i < len(pin); i++ {
		key[i] ^= pin[i]
	}

	nonce := make([]byte, 12)
	if _, errNonce := io.ReadFull(rand.Reader, nonce); errNonce != nil {
		return nil, fmt.Errorf("generate gcm nonce: %w", errNonce)
	}

	block, errCipher := aes.NewCipher(key)
	if errCipher != nil {
		return nil, fmt.Errorf("new aes cipher: %w", errCipher)
	}
	gcm, errGCM := cipher.NewGCM(block)
	if errGCM != nil {
		return nil, fmt.Errorf("new gcm: %w", errGCM)
	}

	aad := []byte(strconv.FormatInt(requestAt, 10))
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)
	payload := append(append([]byte{}, nonce...), ciphertext...)

	return &EncryptedMessage{
		Message:    addLineBreaks(base64.StdEncoding.EncodeToString(payload), 76),
		RequestPin: hex.EncodeToString(pin),
		RequestAt:  requestAt,
	}, nil
}

// addLineBreaks inserts a newline character every lineLen characters.
// This prevents the bufio.Scanner on the V3 API server from treating
// the entire base64 string as a single token exceeding 64KB.
func addLineBreaks(s string, lineLen int) string {
	if len(s) <= lineLen {
		return s
	}
	var buf strings.Builder
	buf.Grow(len(s) + len(s)/lineLen)
	for i := 0; i < len(s); i += lineLen {
		end := i + lineLen
		if end > len(s) {
			end = len(s)
		}
		buf.WriteString(s[i:end])
		if end < len(s) {
			buf.WriteByte('\n')
		}
	}
	return buf.String()
}

// DecryptMessage decrypts the base64 encoded message using requestPin and requestAt timestamp.
func DecryptMessage(messageB64, requestPin string, requestAt int64) ([]byte, error) {
	key, errDec := hex.DecodeString(TraeKeyHex)
	if errDec != nil {
		return nil, fmt.Errorf("decode trae key: %w", errDec)
	}
	pin, errPin := hex.DecodeString(requestPin)
	if errPin != nil {
		return nil, fmt.Errorf("decode request pin: %w", errPin)
	}
	if len(pin) != 8 {
		return nil, fmt.Errorf("invalid request pin length: %d", len(pin))
	}
	for i := 0; i < len(pin); i++ {
		key[i] ^= pin[i]
	}

	data, errB64 := base64.StdEncoding.DecodeString(messageB64)
	if errB64 != nil {
		return nil, fmt.Errorf("base64 decode trae message: %w", errB64)
	}
	if len(data) < 12+16 {
		return nil, fmt.Errorf("trae ciphertext too short: %d", len(data))
	}

	block, errCipher := aes.NewCipher(key)
	if errCipher != nil {
		return nil, fmt.Errorf("new aes cipher: %w", errCipher)
	}
	gcm, errGCM := cipher.NewGCM(block)
	if errGCM != nil {
		return nil, fmt.Errorf("new gcm: %w", errGCM)
	}

	nonce := data[:12]
	ciphertext := data[12:]
	aad := []byte(strconv.FormatInt(requestAt, 10))

	decrypted, errOpen := gcm.Open(nil, nonce, ciphertext, aad)
	if errOpen != nil {
		return nil, fmt.Errorf("gcm decrypt failed: %w", errOpen)
	}
	return decrypted, nil
}
