package lingma

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestLingmaCredentialDecryption(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}

	machineID := "machine-1234567890abcdef"
	idFile := filepath.Join(cacheDir, "id")
	if err := os.WriteFile(idFile, []byte(machineID), 0600); err != nil {
		t.Fatalf("write id: %v", err)
	}

	rawUserJSON := `{
		"key": "test-cosy-key",
		"uid": "test-lingma-uid",
		"encryptUserInfo": "encrypted-info",
		"securityOAuthToken": "oauth-token-val",
		"refreshToken": "refresh-token-val",
		"expireTime": 1893456000000,
		"userType": "personal",
		"name": "developer"
	}`

	// Encrypt using AES-128-CBC with key=machineID[:16], IV=key
	key := []byte(machineID[:16])
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}

	// PKCS7 padding
	blockSize := block.BlockSize()
	padding := blockSize - len(rawUserJSON)%blockSize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	plaintext := append([]byte(rawUserJSON), padtext...)

	ciphertext := make([]byte, len(plaintext))
	mode := cipher.NewCBCEncrypter(block, key)
	mode.CryptBlocks(ciphertext, plaintext)

	encryptedBase64 := base64.StdEncoding.EncodeToString(ciphertext)
	userFile := filepath.Join(cacheDir, "user")
	if err := os.WriteFile(userFile, []byte(encryptedBase64), 0600); err != nil {
		t.Fatalf("write user: %v", err)
	}

	// Test loadCredentialsFromDir
	creds, err := loadCredentialsFromDir(cacheDir)
	if err != nil {
		t.Fatalf("loadCredentialsFromDir error: %v", err)
	}
	if creds == nil {
		t.Fatalf("creds is nil")
	}

	if creds.MachineID != machineID {
		t.Errorf("machineID = %q, want %q", creds.MachineID, machineID)
	}
	if creds.UID != "test-lingma-uid" {
		t.Errorf("UID = %q, want %q", creds.UID, "test-lingma-uid")
	}
	if creds.CosyKey != "test-cosy-key" {
		t.Errorf("CosyKey = %q, want %q", creds.CosyKey, "test-cosy-key")
	}
	if creds.SecurityOAuthToken != "oauth-token-val" {
		t.Errorf("SecurityOAuthToken = %q, want %q", creds.SecurityOAuthToken, "oauth-token-val")
	}
	if creds.Name != "developer" {
		t.Errorf("Name = %q, want %q", creds.Name, "developer")
	}
	if creds.Type != ProviderID {
		t.Errorf("Type = %q, want %q", creds.Type, ProviderID)
	}
}

func TestLingmaOAuthState(t *testing.T) {
	state1, err1 := newLingmaOAuthState()
	if err1 != nil {
		t.Fatalf("new state 1: %v", err1)
	}
	state2, err2 := newLingmaOAuthState()
	if err2 != nil {
		t.Fatalf("new state 2: %v", err2)
	}
	if state1 == "" || state2 == "" {
		t.Errorf("empty state")
	}
	if state1 == state2 {
		t.Errorf("states should be unique: %s == %s", state1, state2)
	}
	if len(state1) != 32 {
		t.Errorf("state length = %d, want 32", len(state1))
	}
}

func TestLingmaPollUnknownSession(t *testing.T) {
	plugin := New(nil)
	req := authLoginPollRPCRequest{
		AuthLoginPollRequest: pluginapi.AuthLoginPollRequest{
			Provider: ProviderID,
			State:    "nonexistent-state",
		},
	}
	raw, _ := json.Marshal(req)
	respBytes, err := plugin.pollLogin(raw)
	if err != nil {
		t.Fatalf("pollLogin error: %v", err)
	}

	var resp struct {
		Result pluginapi.AuthLoginPollResponse `json:"result"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Result.Status != pluginapi.AuthLoginStatusError {
		t.Errorf("status = %q, want %q", resp.Result.Status, pluginapi.AuthLoginStatusError)
	}
}

func TestLingmaPollPendingSession(t *testing.T) {
	plugin := New(nil)
	tempWorkDir := t.TempDir()

	state := "test-state-123"
	plugin.oauthSessions[state] = &lingmaOAuthSession{
		state:     state,
		workDir:   tempWorkDir,
		expiresAt: time.Now().Add(time.Hour),
	}

	req := authLoginPollRPCRequest{
		AuthLoginPollRequest: pluginapi.AuthLoginPollRequest{
			Provider: ProviderID,
			State:    state,
		},
	}
	raw, _ := json.Marshal(req)
	respBytes, err := plugin.pollLogin(raw)
	if err != nil {
		t.Fatalf("pollLogin error: %v", err)
	}

	var resp struct {
		Result pluginapi.AuthLoginPollResponse `json:"result"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Result.Status != pluginapi.AuthLoginStatusPending {
		t.Errorf("status = %q, want %q", resp.Result.Status, pluginapi.AuthLoginStatusPending)
	}
}

func TestLingmaBinaryResolverEnv(t *testing.T) {
	tempFile, err := os.CreateTemp("", "lingma-fake-*")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer os.Remove(tempFile.Name())
	_ = tempFile.Chmod(0755)
	_ = tempFile.Close()

	t.Setenv("LINGMA_SERVICE_BINARY", tempFile.Name())

	found, err := FindLingmaServiceBinary()
	if err != nil {
		t.Fatalf("resolver error: %v", err)
	}
	if found != tempFile.Name() {
		t.Errorf("found = %q, want %q", found, tempFile.Name())
	}
}
