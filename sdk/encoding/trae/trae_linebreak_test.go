package trae

import (
	"bufio"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestLargePayloadBase64LineBreaks verifies that encrypted payloads larger than 64KB
// produce base64 strings with line breaks, so that bufio.Scanner on the server side
// can read them without "token too long" errors.
func TestLargePayloadBase64LineBreaks(t *testing.T) {
	// Simulate a large payload (similar to the 93KB request that caused the error)
	largePayload := []byte(strings.Repeat("x", 93000))

	encrypted, err := EncryptMessage(largePayload)
	if err != nil {
		t.Fatalf("EncryptMessage failed: %v", err)
	}

	// Verify the base64 string has line breaks
	if !strings.Contains(encrypted.Message, "\n") {
		t.Error("expected base64 string to contain line breaks for large payloads")
	}

	// Verify no single line exceeds 64KB
	lines := strings.Split(encrypted.Message, "\n")
	for i, line := range lines {
		if len(line) > 65536 {
			t.Errorf("line %d is %d bytes, exceeds bufio.Scanner 64KB limit", i, len(line))
		}
	}

	// Verify round-trip: decrypt should still work
	decrypted, err := DecryptMessage(encrypted.Message, encrypted.RequestPin, encrypted.RequestAt)
	if err != nil {
		t.Fatalf("DecryptMessage failed: %v", err)
	}
	if !bytes.Equal(largePayload, decrypted) {
		t.Error("decrypted message does not match original")
	}
}

// TestLargePayloadScannerRead verifies that a bufio.Scanner can read the encrypted
// base64 string without "token too long" errors.
func TestLargePayloadScannerRead(t *testing.T) {
	// Create a large payload
	largePayload := []byte(strings.Repeat("x", 93000))

	encrypted, err := EncryptMessage(largePayload)
	if err != nil {
		t.Fatalf("EncryptMessage failed: %v", err)
	}

	// Simulate the V3 API server reading the request body with bufio.Scanner
	scanner := bufio.NewScanner(strings.NewReader(encrypted.Message))
	lineCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 65536 {
			t.Errorf("scanner read line of %d bytes, exceeds 64KB limit", len(line))
		}
		lineCount++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("bufio.Scanner error: %v", err)
	}

	if lineCount == 0 {
		t.Error("expected at least one line from scanner")
	}
	t.Logf("Scanner read %d lines, max line size OK", lineCount)
}

// TestLargePayloadHTTPServer verifies that an HTTP server using bufio.Scanner
// can read a large encrypted request body without errors.
func TestLargePayloadHTTPServer(t *testing.T) {
	// Create a mock V3 API server that reads the request body with bufio.Scanner
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scanner := bufio.NewScanner(r.Body)
		var allLines []string
		for scanner.Scan() {
			line := scanner.Text()
			if len(line) > 65536 {
				t.Errorf("server scanner read line of %d bytes, exceeds 64KB limit", len(line))
			}
			allLines = append(allLines, line)
		}
		if err := scanner.Err(); err != nil {
			t.Errorf("server scanner error: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	// Create a large payload
	largePayload := []byte(strings.Repeat("x", 93000))
	encrypted, err := EncryptMessage(largePayload)
	if err != nil {
		t.Fatalf("EncryptMessage failed: %v", err)
	}

	// Send the request
	req, err := http.NewRequest("POST", server.URL, strings.NewReader(encrypted.Message))
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
}

// TestSmallPayloadNoLineBreaks verifies that small payloads don't get line breaks.
func TestSmallPayloadNoLineBreaks(t *testing.T) {
	smallPayload := []byte("hello")
	encrypted, err := EncryptMessage(smallPayload)
	if err != nil {
		t.Fatalf("EncryptMessage failed: %v", err)
	}

	if strings.Contains(encrypted.Message, "\n") {
		t.Error("expected small payload base64 to not contain line breaks")
	}

	// Verify round-trip
	decrypted, err := DecryptMessage(encrypted.Message, encrypted.RequestPin, encrypted.RequestAt)
	if err != nil {
		t.Fatalf("DecryptMessage failed: %v", err)
	}
	if !bytes.Equal(smallPayload, decrypted) {
		t.Error("decrypted message does not match original")
	}
}
