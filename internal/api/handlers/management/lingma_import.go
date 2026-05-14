package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/lingma"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type ImportLingmaRequest struct {
	MachineID string `json:"machine_id" binding:"required"`
	UserB64   string `json:"user_b64" binding:"required"`
	Name      string `json:"name"` // Optional label
}

// ImportLingmaCredentials handles the manual import of Lingma credentials.
func (h *Handler) ImportLingmaCredentials(c *gin.Context) {
	var req ImportLingmaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body", "details": err.Error()})
		return
	}

	// 1. Parse and validate credentials
	creds, err := lingma.ParseCredentials(req.MachineID, req.UserB64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse credentials", "details": err.Error()})
		return
	}

	// 2. Verify the imported local Lingma credentials can sign V2 business APIs.
	// The OAuth exchange path is intentionally not used here; lingma-tap's known
	// working route imports the decrypted local cache key directly.
	if err := lingma.ValidateCredentials(creds); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify Lingma credentials", "details": err.Error()})
		return
	}

	// 3. Construct Auth metadata
	fileName := lingmaCredentialFileName(req.Name, creds.UID)
	metadata := buildLingmaCredentialMetadata(req.Name, creds)

	// 4. Save to auth file
	filePath := filepath.Join(h.cfg.AuthDir, fileName)
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encode auth data", "details": err.Error()})
		return
	}

	if err := os.WriteFile(filePath, data, 0600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save auth file", "details": err.Error()})
		return
	}

	// 5. Trigger AuthManager hot reload
	if h.authManager != nil {
		// Trigger reload for the new auth file
		authObj := &coreauth.Auth{
			ID:       fileName,
			Provider: "lingma",
			FileName: fileName,
			Metadata: metadata,
		}
		if _, err := h.authManager.Register(c.Request.Context(), authObj); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to register credentials", "details": err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Lingma credentials imported successfully",
		"uid":     creds.UID,
		"org_id":  creds.OrganizationID,
		"file":    fileName,
	})
}

func buildLingmaCredentialMetadata(name string, creds *lingma.Credentials) map[string]any {
	return map[string]any{
		"type":                 "lingma",
		"machine_id":           creds.MachineID,
		"uid":                  creds.UID,
		"organization_id":      creds.OrganizationID,
		"key":                  creds.CosyKey,
		"security_oauth_token": creds.SecurityOAuthToken,
		"encrypt_user_info":    creds.EncryptUserInfo,
		"user_type":            creds.UserType,
		"expire_time":          creds.ExpireTime,
		"name":                 name,
		"expires_at": func() time.Time {
			if creds.ExpireTime > 0 {
				if creds.ExpireTime > 32503680000 {
					return time.UnixMilli(creds.ExpireTime)
				}
				return time.Unix(creds.ExpireTime, 0)
			}
			return time.Now().Add(24 * time.Hour)
		}(),
	}
}

func lingmaCredentialFileName(name, uid string) string {
	uidPart := sanitizeLingmaFileComponent(uid)
	if uidPart == "" {
		uidPart = "unknown"
	}
	namePart := sanitizeLingmaFileComponent(name)
	if namePart == "" {
		return fmt.Sprintf("lingma-%s.json", uidPart)
	}
	return fmt.Sprintf("lingma-%s-%s.json", namePart, uidPart)
}

func sanitizeLingmaFileComponent(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, ".")
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, value)
	value = strings.Trim(value, "-_.")
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	return value
}
