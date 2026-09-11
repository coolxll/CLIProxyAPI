package lingma

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type storedCredentials struct {
	Name               string `json:"name"`
	UID                string `json:"uid"`
	AID                string `json:"aid"`
	OrganizationID     string `json:"organization_id"`
	UserType           string `json:"user_type"`
	Key                string `json:"key"`
	EncryptUserInfo    string `json:"encrypt_user_info"`
	EncryptUserInfoAlt string `json:"encryptUserInfo"`
	SecurityOAuthToken string `json:"security_oauth_token"`
	SecurityOAuthAlt   string `json:"securityOAuthToken"`
	RefreshToken       string `json:"refresh_token"`
	RefreshTokenAlt    string `json:"refreshToken"`
	ExpireTime         int64  `json:"expire_time"`
	ExpireTimeAlt      int64  `json:"expireTime"`
}

func loadCredentialsFromDir(dir string) (*credentials, error) {
	idFile := filepath.Join(dir, "id")
	userFile := filepath.Join(dir, "user")

	machineIDContent, err := readTrimmed(idFile)
	if err != nil {
		return nil, fmt.Errorf("read machine id file: %w", err)
	}
	machineID := parseMachineID(machineIDContent)

	userB64, err := readTrimmed(userFile)
	if err != nil {
		return nil, fmt.Errorf("read user file: %w", err)
	}

	userJSON, err := decryptUser(userB64, machineID)
	if err != nil {
		return nil, fmt.Errorf("decrypt user file: %w", err)
	}

	var user storedCredentials
	if err := json.Unmarshal(userJSON, &user); err != nil {
		return nil, fmt.Errorf("parse user json: %w", err)
	}

	secToken := user.SecurityOAuthToken
	if secToken == "" {
		secToken = user.SecurityOAuthAlt
	}
	encUserInfo := user.EncryptUserInfo
	if encUserInfo == "" {
		encUserInfo = user.EncryptUserInfoAlt
	}
	refreshToken := user.RefreshToken
	if refreshToken == "" {
		refreshToken = user.RefreshTokenAlt
	}
	expireTime := user.ExpireTime
	if expireTime == 0 {
		expireTime = user.ExpireTimeAlt
	}

	return &credentials{
		Type:               ProviderID,
		MachineID:          machineID,
		UID:                user.UID,
		CosyKey:            user.Key,
		EncryptUserInfo:    encUserInfo,
		UserType:           user.UserType,
		SecurityOAuthToken: secToken,
		RefreshToken:       refreshToken,
		ExpireTime:         expireTime,
		Name:               user.Name,
		OrganizationID:     user.OrganizationID,
	}, nil
}

func decryptUser(b64, machineID string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	if len(machineID) < 16 {
		return nil, fmt.Errorf("machineId too short: %d chars", len(machineID))
	}
	key := []byte(machineID[:16])

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes new cipher: %w", err)
	}

	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext not block-aligned: len=%d", len(ciphertext))
	}

	mode := cipher.NewCBCDecrypter(block, key)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	// PKCS7 unpadding
	if len(plaintext) == 0 {
		return nil, fmt.Errorf("empty plaintext after decryption")
	}
	pad := int(plaintext[len(plaintext)-1])
	if pad == 0 || pad > aes.BlockSize {
		return nil, fmt.Errorf("invalid pkcs7 padding: %d", pad)
	}
	for i := len(plaintext) - pad; i < len(plaintext); i++ {
		if plaintext[i] != byte(pad) {
			return nil, fmt.Errorf("invalid pkcs7 padding byte at %d", i)
		}
	}
	plaintext = plaintext[:len(plaintext)-pad]

	return plaintext, nil
}

func readTrimmed(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func parseMachineID(content string) string {
	machineID := strings.TrimSpace(content)
	if !strings.HasPrefix(machineID, "{") {
		return machineID
	}

	var legacy struct {
		MachineID string `json:"machine_id"`
	}
	if err := json.Unmarshal([]byte(machineID), &legacy); err == nil && legacy.MachineID != "" {
		return legacy.MachineID
	}
	return machineID
}
