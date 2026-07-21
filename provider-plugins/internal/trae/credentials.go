package trae

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	traeAuthClientID = "ono9krqynydwx5"
	traeAPIHost      = "https://api.trae.com.cn"
)

// credentialsFromStorage parses and validates credential JSON from storage.
func credentialsFromStorage(raw []byte) (credentials, error) {
	var creds credentials
	if len(raw) == 0 {
		return creds, fmt.Errorf("Trae credential storage is empty")
	}
	if err := json.Unmarshal(raw, &creds); err != nil {
		return creds, fmt.Errorf("decode Trae credential storage: %w", err)
	}
	if err := validateCredentials(creds); err != nil {
		return creds, err
	}
	return creds, nil
}

// refreshToken exchanges the refresh token for a new access token.
func refreshToken(host hostRPC, creds *credentials, apiHost string) error {
	if creds == nil {
		return fmt.Errorf("Trae credentials are nil")
	}
	if creds.RefreshToken == "" {
		return fmt.Errorf("Trae credential missing refresh_token")
	}

	payload := map[string]string{
		"ClientID":     traeAuthClientID,
		"ClientSecret": "-",
		"UserID":       "",
		"RefreshToken": creds.RefreshToken,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal refresh request: %w", err)
	}

	url := strings.TrimRight(apiHost, "/") + "/cloudide/api/v3/trae/oauth/ExchangeToken"
	resp, err := host.do(pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     url,
		Headers: http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"application/json"}},
		Body:    payloadBytes,
	})
	if err != nil {
		return fmt.Errorf("refresh token request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refresh token HTTP %d: %s", resp.StatusCode, safeUpstreamMessage(resp.Body))
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return fmt.Errorf("parse refresh response: %w", err)
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
		return fmt.Errorf("no access token in refresh response")
	}

	creds.JWTToken = accessToken
	if newRefresh != "" {
		creds.RefreshToken = newRefresh
	}

	// Extract user ID from JWT if not already set
	if creds.UserID == "" {
		if uid, ok := userIDFromJWT(accessToken); ok {
			creds.UserID = uid
		}
	}

	return nil
}

// extractString extracts a string value from nested map using path candidates.
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

// userIDFromJWT extracts the user ID from a JWT token.
func userIDFromJWT(jwtToken string) (string, bool) {
	parts := strings.Split(jwtToken, ".")
	if len(parts) != 3 {
		return "", false
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", false
	}

	for _, key := range []string{"data.id", "user_id", "uid", "sub"} {
		if strings.Contains(key, ".") {
			parts := strings.Split(key, ".")
			if len(parts) == 2 {
				if data, ok := claims[parts[0]].(map[string]any); ok {
					if id, ok := data[parts[1]].(string); ok && id != "" {
						return id, true
					}
				}
			}
		} else {
			if id, ok := claims[key].(string); ok && id != "" {
				return id, true
			}
		}
	}

	return "", false
}

// jwtExpiresAt extracts the expiry time from a JWT token.
func jwtExpiresAt(jwtToken string) (time.Time, bool) {
	parts := strings.Split(jwtToken, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, false
	}

	if exp, ok := claims["exp"].(float64); ok {
		return time.Unix(int64(exp), 0), true
	}

	return time.Time{}, false
}

// credentialExpiry returns the credential expiry time.
func credentialExpiry(creds credentials, now time.Time) time.Time {
	if exp, ok := jwtExpiresAt(creds.JWTToken); ok {
		return exp
	}
	return now.Add(1 * time.Hour)
}

// nextRefreshTime returns when the credential should be refreshed.
func nextRefreshTime(creds credentials, now time.Time) time.Time {
	expiry := credentialExpiry(creds, now)
	next := expiry.Add(-5 * time.Minute)
	if !next.After(now) {
		return now.Add(1 * time.Minute)
	}
	return next
}

// safeUpstreamMessage extracts a readable error message from upstream response body.
func safeUpstreamMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		if len(body) > 256 {
			return string(body[:256])
		}
		return string(body)
	}
	if msg, ok := result["message"].(string); ok && msg != "" {
		return msg
	}
	if msg, ok := result["error"].(string); ok && msg != "" {
		return msg
	}
	if len(body) > 256 {
		return string(body[:256])
	}
	return string(body)
}
