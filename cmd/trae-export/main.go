package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

const (
	defaultOutPath      = "trae_import.json"
	TRAE_AUTH_CLIENT_ID = "ono9krqynydwx5"
	TRAE_CN_API_HOST    = "https://api.trae.com.cn"
	TRAE_CN_AUTH_HOST   = "https://www.trae.cn"
	CALLBACK_PATH       = "/authorize"
	OAUTH_TIMEOUT       = 10 * time.Minute
)

func main() {
	var outPath string
	var authsDir string
	var configPath string
	var manualName string
	var jwtToken string
	var machineID string
	var deviceID string
	var workspacePath string
	var refreshToken string
	var doRefresh bool
	var loginHost string
	var doLogin bool
	var pluginOutput bool

	flag.StringVar(&outPath, "out", defaultOutPath, "Output JSON file path (default: <auth-dir>/<name>.json)")
	flag.StringVar(&authsDir, "auths-dir", "", "Directory for auth JSON files (overrides config auth-dir)")
	flag.StringVar(&configPath, "config", "", "Config file path (default: config.yaml in working dir)")
	flag.StringVar(&manualName, "name", "", "Manual name label (auto-generated if empty)")
	flag.StringVar(&jwtToken, "jwt-token", "", "Trae JWT token override")
	flag.StringVar(&machineID, "machine-id", "", "Trae machine id override")
	flag.StringVar(&deviceID, "device-id", "", "Trae device id override")
	flag.StringVar(&workspacePath, "workspace-path", "", "Optional workspace path for Trae agent payloads")
	flag.StringVar(&refreshToken, "refresh-token", "", "Trae refresh token for token refresh")
	flag.BoolVar(&doRefresh, "refresh", false, "Refresh access token using -refresh-token or TRAE_REFRESH_TOKEN env")
	flag.StringVar(&loginHost, "login-host", "", "Trae API host for token exchange (default: "+TRAE_CN_API_HOST+")")
	flag.BoolVar(&doLogin, "login", false, "Interactive OAuth login via browser")
	flag.BoolVar(&pluginOutput, "plugin", false, "Export for the trae-plugin shadow provider")
	flag.Parse()

	// Resolve the auth directory from config (or -auths-dir override), so the
	// generated importable JSON lands directly where the proxy reads auth files.
	authsDirOverridden := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "auths-dir" {
			authsDirOverridden = true
		}
	})

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	if strings.TrimSpace(configPath) == "" {
		configPath = filepath.Join(wd, "config.yaml")
	}
	// Load the config read-only: we only need cfg.AuthDir, so parse the bytes
	// directly via ParseConfigBytes (which never persists to disk) instead of
	// LoadConfigOptional (which hashes and writes back a plaintext
	// remote-management secret-key). A missing config file is tolerated — the
	// tool falls back to the default auth dir — so it stays self-contained.
	cfg := &config.Config{}
	if data, errRead := os.ReadFile(configPath); errRead == nil {
		if parsed, errParse := config.ParseConfigBytes(data); errParse == nil {
			cfg = parsed
		} else {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse config file %s: %v (using default auth dir)\n", configPath, errParse)
		}
	} else if !os.IsNotExist(errRead) {
		fmt.Fprintf(os.Stderr, "Warning: failed to read config file %s: %v (using default auth dir)\n", configPath, errRead)
	}

	if !authsDirOverridden {
		authsDir = cfg.AuthDir
	} else if strings.TrimSpace(authsDir) != "" && !strings.HasPrefix(strings.TrimSpace(authsDir), "~") && !filepath.IsAbs(authsDir) {
		authsDir = filepath.Join(wd, authsDir)
	}
	if authsDir, err = util.ResolveAuthDir(authsDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to resolve auth directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(authsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to create auth directory %s: %v\n", authsDir, err)
		os.Exit(1)
	}

	// Resolve credentials (before login so runOAuthLogin can reuse them)
	jwtToken = firstNonEmpty(jwtToken, os.Getenv("TRAE_JWT_TOKEN"))
	machineID = firstNonEmpty(machineID, os.Getenv("TRAE_MACHINE_ID"), readFirstTrimmed(machineIDCandidates()...))
	deviceID = firstNonEmpty(deviceID, os.Getenv("TRAE_DEVICE_ID"), readFirstTrimmed(deviceIDCandidates()...))
	workspacePath = firstNonEmpty(workspacePath, os.Getenv("TRAE_WORKSPACE_PATH"))

	// Handle OAuth login
	if doLogin {
		result, err := runOAuthLogin(machineID, deviceID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
			os.Exit(1)
		}
		jwtToken = result.AccessToken
		if result.RefreshToken != "" {
			refreshToken = result.RefreshToken
		}
		fmt.Printf("✅ Logged in as %s (%s)\n", result.Nickname, result.Email)
		fmt.Printf("✅ Token expires: %s\n", result.ExpiresAt.Format(time.RFC3339))
	}

	// Handle token refresh (only when explicitly requested, not after login)
	if doRefresh || (!doLogin && refreshToken != "") {
		rt := firstNonEmpty(refreshToken, os.Getenv("TRAE_REFRESH_TOKEN"))
		host := firstNonEmpty(loginHost, os.Getenv("TRAE_LOGIN_HOST"), TRAE_CN_API_HOST)

		if rt == "" {
			fmt.Fprintln(os.Stderr, "Error: No refresh token available. Use -refresh-token or set TRAE_REFRESH_TOKEN env")
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
			refreshToken = result.RefreshToken
		}

		fmt.Printf("✅ Token refreshed, expires: %s\n", result.ExpiresAt.Format(time.RFC3339))
	}

	creds, err := traeauth.ParseTraeCredentials(jwtToken, machineID, deviceID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintln(os.Stderr, "Provide credentials with -jwt-token or TRAE_JWT_TOKEN env var.")
		os.Exit(1)
	}

	finalName := strings.TrimSpace(manualName)
	if finalName == "" {
		finalName = defaultName(creds.UserID, creds.MachineID)
	}

	providerType := "trae"
	if pluginOutput {
		providerType = "trae-plugin"
	}
	exportData := map[string]any{
		"type":       providerType,
		"jwt_token":  creds.JWTToken,
		"machine_id": creds.MachineID,
		"device_id":  creds.DeviceID,
		"user_id":    creds.UserID,
		"name":       finalName,
	}
	if workspacePath != "" {
		exportData["workspace_path"] = workspacePath
	}
	if refreshToken != "" {
		exportData["refresh_token"] = refreshToken
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
		// Default: write directly into the resolved auth directory using the
		// sanitized name, so the file is immediately usable by the proxy.
		outPath = filepath.Join(authsDir, sanitizeFileComponent(finalName)+".json")
	} else if strings.HasPrefix(outPath, "~") {
		// A -out path beginning with ~ is expanded to the user's home directory,
		// matching how authsDir is resolved, so os.WriteFile does not receive a
		// literal "~/..." path.
		if expanded, errExpand := util.ResolveAuthDir(outPath); errExpand == nil {
			outPath = expanded
		} else {
			fmt.Fprintf(os.Stderr, "Error: failed to resolve output path %s: %v\n", outPath, errExpand)
			os.Exit(1)
		}
	} else if !filepath.IsAbs(outPath) {
		// Relative -out paths are resolved against the auth directory so they
		// still land inside the auth tree by default.
		outPath = filepath.Join(authsDir, outPath)
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

// OAuthLoginResult holds the result of a successful OAuth login.
type OAuthLoginResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Email        string
	Nickname     string
	LoginHost    string
	LoginRegion  string
}

// runOAuthLogin performs the full browser-based OAuth login flow.
func runOAuthLogin(machineID, deviceID string) (*OAuthLoginResult, error) {
	// 1. Find a free port for the callback server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("find free port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	callbackURL := fmt.Sprintf("http://127.0.0.1:%d%s", port, CALLBACK_PATH)

	// 2. Generate login trace ID
	loginTraceID, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("generate trace id: %w", err)
	}

	// 3. Resolve login host
	host := resolveLoginHost(loginTraceID)

	// 4. Build verification URL
	verificationURL := buildVerificationURL(host, loginTraceID, callbackURL, machineID, deviceID)

	// 5. Start callback server
	type callbackResult struct {
		RefreshToken  string
		LoginHost     string
		LoginRegion   string
		CloudideToken string
	}
	resultCh := make(chan callbackResult, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(CALLBACK_PATH, func(w http.ResponseWriter, r *http.Request) {
		qs := r.URL.Query()
		if qs.Get("isRedirect") != "true" {
			// Serve JS to convert hash to query params
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!doctype html><html><body><h2>Processing...</h2>`+
				`<script>(function(){if(location.hash&&location.hash.length>1){`+
				`location.replace(location.origin+location.pathname+"?"+location.hash.slice(1));`+
				`}})();</script></body></html>`)
			return
		}

		rt := qs.Get("refreshToken")
		if rt == "" {
			http.Error(w, "Missing refreshToken", http.StatusBadRequest)
			errCh <- fmt.Errorf("callback missing refreshToken")
			return
		}

		lh := qs.Get("loginHost")
		if lh == "" {
			lh = qs.Get("host")
		}
		region := qs.Get("userRegion")
		if region == "" {
			region = inferRegion(lh)
		}

		resultCh <- callbackResult{
			RefreshToken:  rt,
			LoginHost:     lh,
			LoginRegion:   region,
			CloudideToken: qs.Get("cloudideToken"),
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body><h2>Login successful! You may close this tab.</h2></body></html>`)
	})

	server := &http.Server{Handler: mux}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("callback server: %w", err)
		}
	}()

	// 6. Print URL and open browser
	fmt.Printf("\n🔗 Open this URL in your browser to login:\n\n%s\n\n", verificationURL)
	fmt.Printf("Waiting for callback on port %d (timeout: %s)...\n", port, OAUTH_TIMEOUT)

	if err := browser.OpenURL(verificationURL); err != nil {
		fmt.Printf("⚠️  Could not open browser automatically: %v\n", err)
	}

	// 7. Wait for callback
	select {
	case cb := <-resultCh:
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)

		loginHost := firstNonEmpty(cb.LoginHost, host)

		// 8. Exchange token
		fmt.Println("⏳ Exchanging token...")
		tokenResult, err := refreshAccessToken(loginHost, cb.RefreshToken)
		if err != nil {
			return nil, fmt.Errorf("exchange token: %w", err)
		}

		accessToken := tokenResult.AccessToken
		refreshToken := tokenResult.RefreshToken
		if refreshToken == "" {
			refreshToken = cb.RefreshToken
		}

		// 9. Get user info
		email, nickname := "", ""
		if accessToken != "" {
			var infoErr error
			email, nickname, infoErr = getUserInfo(loginHost, accessToken)
			if infoErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not fetch user info: %v\n", infoErr)
			}
		}

		return &OAuthLoginResult{
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			ExpiresAt:    tokenResult.ExpiresAt,
			Email:        email,
			Nickname:     nickname,
			LoginHost:    loginHost,
			LoginRegion:  cb.LoginRegion,
		}, nil

	case err := <-errCh:
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		return nil, err

	case <-time.After(OAUTH_TIMEOUT):
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		return nil, fmt.Errorf("login timed out after %s", OAUTH_TIMEOUT)
	}
}

// buildVerificationURL constructs the Trae OAuth authorization URL.
func buildVerificationURL(host, loginTraceID, callbackURL, machineID, deviceID string) string {
	params := url.Values{
		"login_version":     {"1"},
		"auth_from":         {"trae"},
		"login_channel":     {"native_ide"},
		"plugin_version":    {"2.3.33255"},
		"auth_type":         {"local"},
		"client_id":         {TRAE_AUTH_CLIENT_ID},
		"redirect":          {"0"},
		"login_trace_id":    {loginTraceID},
		"auth_callback_url": {callbackURL},
		"x_device_type":     {"desktop"},
		"x_os_version":      {runtime.GOOS},
		"x_device_brand":    {""},
		"x_app_version":     {"3.5.54"},
		"x_app_type":        {"stable"},
		"x_env":             {"production"},
	}
	if machineID != "" {
		params.Set("machine_id", machineID)
		params.Set("x_machine_id", machineID)
	}
	if deviceID != "" {
		params.Set("device_id", deviceID)
		params.Set("x_device_id", deviceID)
	}
	parsed, _ := url.Parse(host)
	if parsed == nil || parsed.Host == "" {
		host = TRAE_CN_AUTH_HOST
	}
	origin := host
	if parsed != nil && parsed.Host != "" {
		origin = parsed.Scheme + "://" + parsed.Host
	}
	return origin + "/authorization?" + params.Encode()
}

// resolveLoginHost asks Trae guidance endpoints for the browser login host.
func resolveLoginHost(loginTraceID string) string {
	endpoints := []string{
		TRAE_CN_API_HOST + "/cloudide/api/v3/trae/GetLoginGuidance",
		TRAE_CN_AUTH_HOST + "/cloudide/api/v3/trae/GetLoginGuidance",
	}
	payload, _ := json.Marshal(map[string]string{
		"loginTraceID":   loginTraceID,
		"login_trace_id": loginTraceID,
	})

	client := &http.Client{Timeout: 15 * time.Second}
	for _, endpoint := range endpoints {
		req, err := http.NewRequest("POST", endpoint, bytes.NewReader(payload))
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Trae/1.0.0 trae-export")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		body, err := io.ReadAll(resp.Body)
		if closeErr := resp.Body.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close response body: %v\n", closeErr)
		}
		if err != nil {
			continue
		}

		if resp.StatusCode != http.StatusOK {
			continue
		}

		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			continue
		}

		loginHost := extractString(result, [][]string{
			{"Result", "LoginHost"},
			{"Result", "loginHost"},
			{"result", "LoginHost"},
			{"result", "loginHost"},
			{"data", "loginHost"},
			{"LoginHost"},
			{"loginHost"},
		})
		if loginHost != "" {
			return loginHost
		}
	}
	return TRAE_CN_AUTH_HOST
}

// getUserInfo fetches user info (email, nickname) from the Trae API.
func getUserInfo(host, accessToken string) (email, nickname string, err error) {
	payload := []byte(`{}`)
	req, err := http.NewRequest("POST", host+"/cloudide/api/v3/trae/GetUserInfo", bytes.NewReader(payload))
	if err != nil {
		return "", "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-cloudide-token", accessToken)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close response body: %v\n", closeErr)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", fmt.Errorf("parse response: %w", err)
	}

	email = extractString(result, [][]string{
		{"Result", "NonPlainTextEmail"},
		{"Result", "Email"},
		{"Result", "email"},
		{"result", "email"},
		{"data", "email"},
		{"email"},
	})

	nickname = extractString(result, [][]string{
		{"Result", "ScreenName"},
		{"Result", "Nickname"},
		{"Result", "nickname"},
		{"Result", "Name"},
		{"result", "nickname"},
		{"result", "name"},
		{"data", "nickname"},
		{"data", "name"},
		{"nickname"},
		{"name"},
	})

	return email, nickname, nil
}

// inferRegion infers the region code from a login host.
func inferRegion(host string) string {
	host = strings.ToLower(host)
	if strings.Contains(host, "trae.com.cn") || strings.Contains(host, "trae.cn") {
		return "cn"
	}
	if strings.Contains(host, "sg") {
		return "sg"
	}
	if strings.Contains(host, "us") {
		return "us"
	}
	return "cn"
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
