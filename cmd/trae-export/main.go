package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
)

const (
	defaultOutPath      = "trae_import.json"
	TRAE_AUTH_CLIENT_ID = "ono9krqynydwx5"
	TRAE_CN_API_HOST    = "https://api.trae.com.cn"
)

func main() {
	var outPath string
	var manualName string
	var envPath string
	var jwtToken string
	var machineID string
	var deviceID string
	var workspacePath string
	var refreshToken string
	var doRefresh bool
	var loginHost string

	flag.StringVar(&outPath, "out", defaultOutPath, "Output JSON file path")
	flag.StringVar(&manualName, "name", "", "Manual name label (auto-generated if empty)")
	flag.StringVar(&envPath, "env", "", "Optional .env file path to read TRAE_* values from")
	flag.StringVar(&jwtToken, "jwt-token", "", "Trae JWT token override")
	flag.StringVar(&machineID, "machine-id", "", "Trae machine id override")
	flag.StringVar(&deviceID, "device-id", "", "Trae device id override")
	flag.StringVar(&workspacePath, "workspace-path", "", "Optional workspace path for Trae agent payloads")
	flag.StringVar(&refreshToken, "refresh-token", "", "Trae refresh token for token refresh")
	flag.BoolVar(&doRefresh, "refresh", false, "Refresh access token using refresh token from .env or -refresh-token")
	flag.StringVar(&loginHost, "login-host", "", "Trae API host for token exchange (default: "+TRAE_CN_API_HOST+")")
	flag.Parse()

	// Collect env values
	envValues, err := collectEnvValues(envPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Handle token refresh
	if doRefresh || refreshToken != "" {
		rt := firstNonEmpty(refreshToken, os.Getenv("TRAE_REFRESH_TOKEN"), envValues["TRAE_REFRESH_TOKEN"])
		host := firstNonEmpty(loginHost, os.Getenv("TRAE_LOGIN_HOST"), envValues["TRAE_LOGIN_HOST"], TRAE_CN_API_HOST)

		if rt == "" {
			fmt.Fprintln(os.Stderr, "Error: No refresh token available. Use -refresh-token or set TRAE_REFRESH_TOKEN in .env")
			os.Exit(1)
		}

		fmt.Println("⏳ Refreshing access token...")
		result, err := refreshAccessToken(host, rt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error refreshing token: %v\n", err)
			os.Exit(1)
		}

		jwtToken = result.AccessToken
		if result.RefreshToken != "" {
			envValues["TRAE_REFRESH_TOKEN"] = result.RefreshToken
		}

		// Update .env file if it exists
		if envPath != "" {
			if err := updateEnvFile(envPath, map[string]string{
				"TRAE_JWT_TOKEN":     result.AccessToken,
				"TRAE_REFRESH_TOKEN": result.RefreshToken,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to update .env: %v\n", err)
			} else {
				fmt.Printf("✅ Updated .env: %s\n", envPath)
			}
		}

		fmt.Printf("✅ Token refreshed, expires: %s\n", result.ExpiresAt.Format(time.RFC3339))
	}

	// Resolve credentials
	jwtToken = firstNonEmpty(jwtToken, os.Getenv("TRAE_JWT_TOKEN"), envValues["TRAE_JWT_TOKEN"])
	machineID = firstNonEmpty(machineID, os.Getenv("TRAE_MACHINE_ID"), envValues["TRAE_MACHINE_ID"], readFirstTrimmed(machineIDCandidates()...))
	deviceID = firstNonEmpty(deviceID, os.Getenv("TRAE_DEVICE_ID"), envValues["TRAE_DEVICE_ID"], readFirstTrimmed(deviceIDCandidates()...))
	workspacePath = firstNonEmpty(workspacePath, os.Getenv("TRAE_WORKSPACE_PATH"), envValues["TRAE_WORKSPACE_PATH"])

	creds, err := traeauth.ParseTraeCredentials(jwtToken, machineID, deviceID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintln(os.Stderr, "Provide credentials with -jwt-token or TRAE_JWT_TOKEN in the environment/.env file.")
		os.Exit(1)
	}

	finalName := strings.TrimSpace(manualName)
	if finalName == "" {
		finalName = defaultName(creds.UserID, creds.MachineID)
	}

	exportData := map[string]any{
		"type":       "trae",
		"jwt_token":  creds.JWTToken,
		"machine_id": creds.MachineID,
		"device_id":  creds.DeviceID,
		"user_id":    creds.UserID,
		"name":       finalName,
	}
	if workspacePath != "" {
		exportData["workspace_path"] = workspacePath
	}
	if expiresAt, ok := jwtExpiresAt(creds.JWTToken); ok {
		exportData["expires_at"] = expiresAt
	}

	outBytes, err := json.MarshalIndent(exportData, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}

	if outPath == defaultOutPath {
		outPath = sanitizeFileComponent(finalName) + ".json"
	}
	if err := os.WriteFile(outPath, outBytes, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output file: %v\n", err)
		os.Exit(1)
	}

	absOut, _ := filepath.Abs(outPath)
	fmt.Printf("\nSuccessfully generated importable JSON at: %s\n", absOut)
	fmt.Printf("Import Name: %s\n", finalName)
	fmt.Printf("JWT Token: %s\n", maskSecret(creds.JWTToken))
	fmt.Printf("Machine ID: %s\n", maskSecret(creds.MachineID))
	fmt.Printf("Device ID: %s\n", maskSecret(creds.DeviceID))
}

// TokenRefreshResult holds the result of a token refresh
type TokenRefreshResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// refreshAccessToken refreshes the access token using the refresh token
func refreshAccessToken(host, refreshToken string) (*TokenRefreshResult, error) {
	payload := map[string]string{
		"ClientID":     TRAE_AUTH_CLIENT_ID,
		"ClientSecret": "-",
		"UserID":       "",
		"RefreshToken": refreshToken,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(host, "/") + "/cloudide/api/v3/trae/oauth/ExchangeToken"
	req, err := http.NewRequest("POST", url, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 1000)]))
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	accessToken := extractString(result, [][]string{
		{"Result", "Token"},
		{"Result", "AccessToken"},
		{"Result", "accessToken"},
		{"result", "accessToken"},
		{"result", "access_token"},
		{"data", "accessToken"},
		{"data", "access_token"},
		{"accessToken"},
		{"access_token"},
		{"data", "token"},
		{"Token"},
		{"token"},
	})

	newRefresh := extractString(result, [][]string{
		{"Result", "RefreshToken"},
		{"Result", "refreshToken"},
		{"result", "refreshToken"},
		{"result", "refresh_token"},
		{"data", "refreshToken"},
		{"data", "refresh_token"},
		{"refreshToken"},
		{"refresh_token"},
	})

	if accessToken == "" {
		return nil, fmt.Errorf("no access token in response: %s", string(body[:min(len(body), 500)]))
	}

	// Parse JWT expiry
	var expiresAt time.Time
	if exp, ok := jwtExpiresAt(accessToken); ok {
		expiresAt = exp
	}

	return &TokenRefreshResult{
		AccessToken:  accessToken,
		RefreshToken: newRefresh,
		ExpiresAt:    expiresAt,
	}, nil
}

// extractString extracts a string value from nested map using path candidates
func extractString(data map[string]any, paths [][]string) string {
	for _, path := range paths {
		val := data
		for i, key := range path {
			v, ok := val[key]
			if !ok {
				break
			}
			if i == len(path)-1 {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			} else if m, ok := v.(map[string]any); ok {
				val = m
			} else {
				break
			}
		}
	}
	return ""
}

func updateEnvFile(path string, updates map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read env file: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	updated := make(map[string]bool)
	var newLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip comment lines
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			newLines = append(newLines, line)
			continue
		}
		// Strip "export " prefix for matching
		keyPart := trimmed
		exportPrefix := ""
		if strings.HasPrefix(keyPart, "export ") {
			exportPrefix = "export "
			keyPart = strings.TrimPrefix(keyPart, "export ")
		}
		parts := strings.SplitN(keyPart, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			if val, ok := updates[key]; ok {
				newLines = append(newLines, exportPrefix+key+"="+val)
				updated[key] = true
				continue
			}
		}
		newLines = append(newLines, line)
	}

	for key, val := range updates {
		if !updated[key] {
			newLines = append(newLines, key+"="+val)
		}
	}

	if err := os.WriteFile(path, []byte(strings.Join(newLines, "\n")), 0600); err != nil {
		return fmt.Errorf("write env file: %w", err)
	}
	return nil
}

func collectEnvValues(explicitPath string) (map[string]string, error) {
	values := make(map[string]string)
	explicitPath = strings.TrimSpace(explicitPath)
	for _, path := range envCandidates(explicitPath) {
		fileValues, err := readEnvFile(path)
		if err != nil {
			if explicitPath != "" && samePath(path, explicitPath) {
				return nil, fmt.Errorf("read env file %q: %w", explicitPath, err)
			}
			continue
		}
		for key, value := range fileValues {
			if _, exists := values[key]; !exists {
				values[key] = value
			}
		}
	}
	return values, nil
}

func envCandidates(explicitPath string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}

	add(explicitPath)
	if wd, err := os.Getwd(); err == nil {
		add(filepath.Join(wd, ".env"))
	}
	return out
}

func readEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := parseEnvLine(line)
		if ok {
			values[key] = value
		}
	}
	return values, nil
}

func parseEnvLine(line string) (string, string, bool) {
	line = strings.TrimPrefix(line, "\ufeff")
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	idx := strings.Index(line, "=")
	if idx <= 0 {
		return "", "", false
	}
	key := strings.TrimPrefix(strings.TrimSpace(line[:idx]), "\ufeff")
	value := strings.TrimSpace(line[idx+1:])
	if key == "" {
		return "", "", false
	}
	if len(value) >= 2 {
		quote := value[0]
		if (quote == '"' || quote == '\'') && value[len(value)-1] == quote {
			value = value[1 : len(value)-1]
			if quote == '"' {
				value = strings.ReplaceAll(value, `\"`, `"`)
				value = strings.ReplaceAll(value, `\\`, `\`)
			}
			return key, value, true
		}
	}
	if hash := strings.Index(value, " #"); hash >= 0 {
		value = strings.TrimSpace(value[:hash])
	}
	return key, value, true
}

func samePath(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr == nil {
		left = leftAbs
	}
	if rightErr == nil {
		right = rightAbs
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func machineIDCandidates() []string {
	var candidates []string
	home, _ := os.UserHomeDir()
	addMachineIDFiles := func(base string) {
		if base == "" {
			return
		}
		candidates = append(candidates,
			filepath.Join(base, "machineid"),
			filepath.Join(base, "machine_id"),
		)
	}

	switch runtime.GOOS {
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			addMachineIDFiles(filepath.Join(appdata, "Trae CN"))
			addMachineIDFiles(filepath.Join(appdata, "Trae"))
		}
	case "darwin":
		addMachineIDFiles(filepath.Join(home, "Library", "Application Support", "Trae CN"))
		addMachineIDFiles(filepath.Join(home, "Library", "Application Support", "Trae"))
	default:
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			addMachineIDFiles(filepath.Join(xdg, "Trae CN"))
			addMachineIDFiles(filepath.Join(xdg, "Trae"))
		}
		addMachineIDFiles(filepath.Join(home, ".config", "Trae CN"))
		addMachineIDFiles(filepath.Join(home, ".config", "Trae"))
	}
	addMachineIDFiles(filepath.Join(home, ".trae-cn"))
	addMachineIDFiles(filepath.Join(home, ".trae"))
	return candidates
}

func deviceIDCandidates() []string {
	var candidates []string
	home, _ := os.UserHomeDir()
	addDeviceIDFiles := func(base string) {
		if base == "" {
			return
		}
		candidates = append(candidates,
			filepath.Join(base, "deviceid"),
			filepath.Join(base, "device_id"),
		)
	}

	switch runtime.GOOS {
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			addDeviceIDFiles(filepath.Join(appdata, "Trae CN"))
			addDeviceIDFiles(filepath.Join(appdata, "Trae"))
		}
	case "darwin":
		addDeviceIDFiles(filepath.Join(home, "Library", "Application Support", "Trae CN"))
		addDeviceIDFiles(filepath.Join(home, "Library", "Application Support", "Trae"))
	default:
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			addDeviceIDFiles(filepath.Join(xdg, "Trae CN"))
			addDeviceIDFiles(filepath.Join(xdg, "Trae"))
		}
		addDeviceIDFiles(filepath.Join(home, ".config", "Trae CN"))
		addDeviceIDFiles(filepath.Join(home, ".config", "Trae"))
	}
	addDeviceIDFiles(filepath.Join(home, ".trae-cn"))
	addDeviceIDFiles(filepath.Join(home, ".trae"))
	return candidates
}

func readFirstTrimmed(paths ...string) string {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if value := strings.TrimSpace(string(data)); value != "" {
			return value
		}
	}
	return ""
}

func jwtExpiresAt(jwtToken string) (time.Time, bool) {
	payload, ok := jwtPayload(jwtToken)
	if !ok {
		return time.Time{}, false
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, false
	}
	raw := strings.TrimSpace(claims.Exp.String())
	if raw == "" {
		return time.Time{}, false
	}
	exp, err := claims.Exp.Int64()
	if err != nil || exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(exp, 0), true
}

func jwtPayload(jwtToken string) ([]byte, bool) {
	parts := strings.Split(strings.TrimSpace(jwtToken), ".")
	if len(parts) < 2 {
		return nil, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err == nil {
		return decoded, true
	}
	segment := parts[1]
	if l := len(segment) % 4; l > 0 {
		segment += strings.Repeat("=", 4-l)
	}
	decoded, err = base64.URLEncoding.DecodeString(segment)
	return decoded, err == nil
}

func defaultName(userID, machineID string) string {
	accountPart := "local"
	if trimmed := strings.TrimSpace(userID); trimmed != "" && trimmed != "0" {
		accountPart = trimmed
		if len(accountPart) > 8 {
			accountPart = accountPart[len(accountPart)-8:]
		}
	}
	machineSuffix := strings.TrimSpace(machineID)
	if len(machineSuffix) > 4 {
		machineSuffix = machineSuffix[len(machineSuffix)-4:]
	}
	if machineSuffix == "" {
		return "trae-" + accountPart
	}
	return fmt.Sprintf("trae-%s-%s", accountPart, machineSuffix)
}

func sanitizeFileComponent(value string) string {
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
	if value == "" {
		return "trae"
	}
	return value
}

func maskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(empty)"
	}
	runes := []rune(value)
	if len(runes) <= 8 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:4]) + "..." + string(runes[len(runes)-4:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
