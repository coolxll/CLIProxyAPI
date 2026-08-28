package main

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestJWTExpiresAt(t *testing.T) {
	payload := []byte(`{"exp":1893456000,"data":{"id":123}}`)
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"

	got, ok := jwtExpiresAt(token)
	if !ok {
		t.Fatal("expected expiration to be parsed")
	}
	want := time.Unix(1893456000, 0)
	if !got.Equal(want) {
		t.Fatalf("expires_at = %s, want %s", got, want)
	}
}

func TestDefaultName(t *testing.T) {
	got := defaultName("1234567890", "machine-abcd")
	if got != "trae-34567890-abcd" {
		t.Fatalf("defaultName = %q", got)
	}
}
