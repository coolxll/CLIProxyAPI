package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func main() {
	var outPath string
	var manualName string

	flag.StringVar(&outPath, "out", "lingma_import.json", "Output JSON file path")
	flag.StringVar(&manualName, "name", "", "Manual name label (auto-generated if empty)")
	flag.Parse()

	dir, err := findAuthDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Found Lingma auth files in: %s\n", dir)

	machineID, err := readTrimmed(filepath.Join(dir, "id"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading id file: %v\n", err)
		os.Exit(1)
	}

	userB64, err := readTrimmed(filepath.Join(dir, "user"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading user file: %v\n", err)
		os.Exit(1)
	}

	if machineID == "" || userB64 == "" {
		fmt.Fprintf(os.Stderr, "Error: machine ID or user content is empty\n")
		os.Exit(1)
	}

	// Decrypt user info
	userJSON, err := decryptUser(userB64, machineID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error decrypting user file: %v\n", err)
		os.Exit(1)
	}

	var user struct {
		Name               string `json:"name"`
		UID                string `json:"uid"`
		OrganizationID     string `json:"organization_id"`
		UserType           string `json:"user_type"`
		Key                string `json:"key"`
		EncryptUserInfo    string `json:"encrypt_user_info"`
		SecurityOAuthToken string `json:"security_oauth_token"`
		RefreshToken       string `json:"refresh_token"`
		ExpireTime         int64  `json:"expire_time"`
	}
	if err := json.Unmarshal(userJSON, &user); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing user JSON: %v\n", err)
		os.Exit(1)
	}

	finalName := manualName
	if finalName == "" {
		accountPart := maskName(user.Name)
		if accountPart == "" {
			accountPart = user.UID
			if len(accountPart) > 8 {
				accountPart = accountPart[len(accountPart)-8:]
			}
		}
		if accountPart == "" {
			accountPart = "local"
		}
		machineSuffix := ""
		if len(machineID) > 4 {
			machineSuffix = machineID[len(machineID)-4:]
		} else {
			machineSuffix = machineID
		}
		finalName = fmt.Sprintf("lingma-%s-%s", accountPart, machineSuffix)
	}

	var expiresAt time.Time
	if user.ExpireTime > 0 {
		if user.ExpireTime > 32503680000 {
			expiresAt = time.UnixMilli(user.ExpireTime)
		} else {
			expiresAt = time.Unix(user.ExpireTime, 0)
		}
	} else {
		expiresAt = time.Now().Add(24 * time.Hour)
	}

	exportData := map[string]interface{}{
		"type":                 "lingma",
		"machine_id":           machineID,
		"uid":                  user.UID,
		"organization_id":      user.OrganizationID,
		"key":                  user.Key,
		"security_oauth_token": user.SecurityOAuthToken,
		"encrypt_user_info":    user.EncryptUserInfo,
		"user_type":            user.UserType,
		"expire_time":          user.ExpireTime,
		"name":                 finalName,
		"expires_at":           expiresAt,
	}
	outBytes, err := json.MarshalIndent(exportData, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}

	if outPath == "lingma_import.json" {
		// If user didn't change the default, use the automatic name
		outPath = sanitizeLingmaFileComponent(finalName) + ".json"
	}

	if err := os.WriteFile(outPath, outBytes, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output file: %v\n", err)
		os.Exit(1)
	}

	absOut, _ := filepath.Abs(outPath)
	fmt.Printf("Successfully generated importable JSON at: %s\n", absOut)
	fmt.Printf("Import Name: %s\n", finalName)
}

func findAuthDir() (string, error) {
	candidates := authDirCandidates()
	for _, dir := range candidates {
		idFile := filepath.Join(dir, "id")
		userFile := filepath.Join(dir, "user")
		if _, err := os.Stat(idFile); err == nil {
			if _, err := os.Stat(userFile); err == nil {
				return dir, nil
			}
		}
	}
	return "", fmt.Errorf("lingma auth files not found, searched %d locations", len(candidates))
}

func authDirCandidates() []string {
	var candidates []string
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates,
			filepath.Join(home, "Library", "Application Support", "lingma", "SharedClientCache", "cache"),
		)
	case "linux":
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			candidates = append(candidates, filepath.Join(xdg, "lingma", "SharedClientCache", "cache"))
		}
		candidates = append(candidates,
			filepath.Join(home, ".config", "lingma", "SharedClientCache", "cache"),
		)
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			candidates = append(candidates, filepath.Join(appdata, "lingma", "SharedClientCache", "cache"))
		}
	}

	// Fallbacks
	candidates = append(candidates,
		filepath.Join(home, ".lingma", "vscode", "sharedClientCache", "cache"),
		filepath.Join(home, ".lingma", "core"), // Some versions use this
	)

	return candidates
}

func decryptUser(b64, machineID string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}

	if len(machineID) < 16 {
		return nil, fmt.Errorf("machineId too short")
	}
	key := []byte(machineID[:16])

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext not block-aligned")
	}

	mode := cipher.NewCBCDecrypter(block, key)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	// PKCS7 unpadding
	if len(plaintext) == 0 {
		return nil, fmt.Errorf("empty plaintext")
	}
	pad := int(plaintext[len(plaintext)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(plaintext) {
		return nil, fmt.Errorf("invalid padding")
	}
	for i := len(plaintext) - pad; i < len(plaintext); i++ {
		if plaintext[i] != byte(pad) {
			return nil, fmt.Errorf("invalid padding byte")
		}
	}
	return plaintext[:len(plaintext)-pad], nil
}

func maskName(name string) string {
	if name == "" {
		return ""
	}
	runes := []rune(name)
	if len(runes) <= 1 {
		return name + "*"
	}
	// For Chinese names or short names, keep the first char and add stars
	return string(runes[0]) + "**"
}

func sanitizeLingmaFileComponent(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, ".")
	value = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r >= 0x4e00 {
			return r
		}
		return '-'
	}, value)
	value = strings.Trim(value, "-_.")
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	return value
}

func readTrimmed(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
