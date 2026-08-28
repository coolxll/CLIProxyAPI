package trae

import (
	"bytes"
	"testing"
	"time"
)

func TestTraeRoundTrip(t *testing.T) {
	plaintext := []byte("Hello Trae GCM Encryption with XOR Obfuscation!")
	requestAt := time.Now().Unix()

	encrypted, err := EncryptMessageAt(plaintext, requestAt)
	if err != nil {
		t.Fatalf("EncryptMessageAt failed: %v", err)
	}

	if encrypted.Message == "" {
		t.Error("expected non-empty encrypted message")
	}
	if len(encrypted.RequestPin) != 16 { // Hex representation of 8 bytes is 16 characters
		t.Errorf("expected 16 hex chars for request pin, got %d", len(encrypted.RequestPin))
	}
	if encrypted.RequestAt != requestAt {
		t.Errorf("expected requestAt to match %d, got %d", requestAt, encrypted.RequestAt)
	}

	decrypted, err := DecryptMessage(encrypted.Message, encrypted.RequestPin, encrypted.RequestAt)
	if err != nil {
		t.Fatalf("DecryptMessage failed: %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Errorf("decrypted message %q does not match original plaintext %q", string(decrypted), string(plaintext))
	}
}

func TestTraeTamperDetection(t *testing.T) {
	plaintext := []byte("Sensitive Trae Command Payload")
	requestAt := time.Now().Unix()

	encrypted, err := EncryptMessageAt(plaintext, requestAt)
	if err != nil {
		t.Fatalf("EncryptMessageAt failed: %v", err)
	}

	// 1. Wrong request pin
	wrongPin := "0000000000000000"
	if wrongPin == encrypted.RequestPin {
		wrongPin = "1111111111111111"
	}
	_, err = DecryptMessage(encrypted.Message, wrongPin, encrypted.RequestAt)
	if err == nil {
		t.Error("expected decryption to fail with a wrong request pin, but it succeeded")
	}

	// 2. Wrong requestAt timestamp
	_, err = DecryptMessage(encrypted.Message, encrypted.RequestPin, encrypted.RequestAt+1)
	if err == nil {
		t.Error("expected decryption to fail with a wrong requestAt timestamp, but it succeeded")
	}
}
