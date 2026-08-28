package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/empty"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/lingma"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// LingmaAuthenticator implements the CLI proxy authentication interface for Lingma.
// Since Lingma uses manual credential import or explicit config keys,
// the Login method here primarily handles automated background token refresh.
type LingmaAuthenticator struct{}

// NewLingmaAuthenticator constructs a Lingma authenticator.
func NewLingmaAuthenticator() *LingmaAuthenticator {
	return &LingmaAuthenticator{}
}

func (a *LingmaAuthenticator) Provider() string {
	return "lingma"
}

func (a *LingmaAuthenticator) RefreshLead() *time.Duration {
	// Lingma tokens expire relatively quickly, refresh 12 hours before expiration
	return new(12 * time.Hour)
}

func (a *LingmaAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	// This authenticator does not support interactive CLI login because of the complex
	// manual OAuth extraction required. It relies on credentials imported via the Management API
	// or provided directly in config.yaml.
	// If Metadata is empty, it means an interactive login was requested.
	if len(opts.Metadata) == 0 {
		return nil, fmt.Errorf("interactive login for Lingma is not supported; please import credentials via the Management API or specify them in config.yaml")
	}

	// If we are here, we are likely being called for a background refresh.
	// Extract the existing credentials from the metadata.
	machineID, ok1 := opts.Metadata["machine_id"]
	uid, ok2 := opts.Metadata["uid"]
	orgID, ok3 := opts.Metadata["organization_id"]
	cosyKey, ok4 := opts.Metadata["key"]
	securityToken, ok5 := opts.Metadata["security_oauth_token"]
	encryptUserInfo, ok6 := opts.Metadata["encrypt_user_info"]

	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || machineID == "" || uid == "" {
		return nil, fmt.Errorf("missing required metadata for Lingma refresh")
	}

	creds := &lingma.Credentials{
		MachineID:          machineID,
		UID:                uid,
		OrganizationID:     orgID,
		CosyKey:            cosyKey,
		SecurityOAuthToken: securityToken,
		EncryptUserInfo:    encryptUserInfo,
	}

	// Perform the exchange handshake to get a fresh CosyKey and activate it
	if err := lingma.ExchangeToken(creds); err != nil {
		return nil, fmt.Errorf("failed to refresh Lingma token: %w", err)
	}

	fileName := fmt.Sprintf("lingma-%s.json", creds.UID)
	if name, ok := opts.Metadata["name"]; ok && name != "" {
		fileName = fmt.Sprintf("lingma-%s-%s.json", name, creds.UID)
	}

	// Persist the refreshed credentials back into metadata
	metadata := map[string]any{
		"machine_id":           creds.MachineID,
		"uid":                  creds.UID,
		"organization_id":      creds.OrganizationID,
		"key":                  creds.CosyKey, // refreshed
		"security_oauth_token": creds.SecurityOAuthToken,
		"encrypt_user_info":    creds.EncryptUserInfo,
		"user_type":            creds.UserType,
		"expire_time":          creds.ExpireTime,
	}

	// Calculate dynamic expiration if available from credentials
	expiresAt := time.Now().Add(24 * time.Hour)
	if creds.ExpireTime > 0 {
		// Detect if it's milliseconds (larger than year 3000 in seconds)
		if creds.ExpireTime > 32503680000 {
			expiresAt = time.UnixMilli(creds.ExpireTime)
		} else {
			expiresAt = time.Unix(creds.ExpireTime, 0)
		}
	}
	metadata["expires_at"] = expiresAt

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Metadata: metadata,
		Storage:  &empty.EmptyStorage{},
	}, nil
}
