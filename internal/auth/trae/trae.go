package trae

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TraeCredentials represents the configuration required to communicate with Trae.
type TraeCredentials struct {
	JWTToken  string `json:"jwt_token"`
	MachineID string `json:"machine_id"`
	DeviceID  string `json:"device_id"`
	UserID    string `json:"user_id"`
}

// CredentialsFromAuth parses the Auth record and maps it to TraeCredentials.
func CredentialsFromAuth(auth *cliproxyauth.Auth) (*TraeCredentials, error) {
	if auth == nil {
		return nil, errors.New("missing trae auth")
	}
	return ParseTraeCredentials(
		authString(auth, "jwt_token"),
		authString(auth, "machine_id"),
		authString(auth, "device_id"),
	)
}

// WorkspacePathFromAuth extracts the workspace path from the Auth record or returns a fallback.
func WorkspacePathFromAuth(auth *cliproxyauth.Auth, fallback string) string {
	if auth == nil {
		return fallback
	}
	if v := authString(auth, "workspace_path"); v != "" {
		return v
	}
	return fallback
}

// ParseTraeCredentials validates the auth components and extracts the UserID.
func ParseTraeCredentials(jwtToken, machineID, deviceID string) (*TraeCredentials, error) {
	jwtToken = strings.TrimSpace(jwtToken)
	machineID = strings.TrimSpace(machineID)
	deviceID = strings.TrimSpace(deviceID)
	if jwtToken == "" {
		return nil, errors.New("missing trae auth configuration (jwt_token)")
	}
	// Fallback to strictly fixed, constant IDs if not provided to match user constraints. No dynamic/random generation.
	if machineID == "" {
		machineID = "2569994131757818"
	}
	if deviceID == "" {
		deviceID = "2569994131757818"
	}
	return &TraeCredentials{
		JWTToken:  jwtToken,
		MachineID: machineID,
		DeviceID:  deviceID,
		UserID:    userIDFromJWT(jwtToken),
	}, nil
}

func authString(auth *cliproxyauth.Auth, key string) string {
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes[key]); v != "" {
			return v
		}
	}
	if auth.Metadata != nil {
		if val, ok := auth.Metadata[key]; ok {
			switch typedVal := val.(type) {
			case string:
				return strings.TrimSpace(typedVal)
			case json.Number:
				return typedVal.String()
			case float64:
				return fmt.Sprintf("%.0f", typedVal)
			case int64:
				return fmt.Sprintf("%d", typedVal)
			case fmt.Stringer:
				return strings.TrimSpace(typedVal.String())
			}
		}
	}
	return ""
}

func userIDFromJWT(jwtToken string) string {
	parts := strings.Split(jwtToken, ".")
	if len(parts) < 2 {
		return "0"
	}

	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		segment := parts[1]
		if l := len(segment) % 4; l > 0 {
			segment += strings.Repeat("=", 4-l)
		}
		decoded, err = base64.URLEncoding.DecodeString(segment)
	}
	if err != nil {
		return "0"
	}

	var payload struct {
		Data struct {
			ID json.Number `json:"id"`
		} `json:"data"`
		UserID json.Number `json:"user_id"`
		UID    json.Number `json:"uid"`
		Sub    json.Number `json:"sub"`
	}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		// Fallback if the fields are raw strings
		var stringPayload struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
			UserID string `json:"user_id"`
			UID    string `json:"uid"`
			Sub    string `json:"sub"`
		}
		if err := json.Unmarshal(decoded, &stringPayload); err == nil {
			for _, candidate := range []string{stringPayload.Data.ID, stringPayload.UserID, stringPayload.UID, stringPayload.Sub} {
				if strings.TrimSpace(candidate) != "" {
					return strings.TrimSpace(candidate)
				}
			}
		}
		return "0"
	}

	for _, candidate := range []json.Number{payload.Data.ID, payload.UserID, payload.UID, payload.Sub} {
		if strings.TrimSpace(candidate.String()) != "" {
			return strings.TrimSpace(candidate.String())
		}
	}
	return "0"
}
