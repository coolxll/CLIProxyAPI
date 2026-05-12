package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

	// 2. Perform OAuth token exchange/activation to verify they work
	if err := lingma.ExchangeToken(creds); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify or activate Lingma tokens", "details": err.Error()})
		return
	}

	// 3. Construct Auth metadata
	fileName := fmt.Sprintf("lingma-%s.json", creds.UID)
	if req.Name != "" {
		fileName = fmt.Sprintf("lingma-%s-%s.json", req.Name, creds.UID)
	}

	metadata := map[string]any{
		"machine_id":           creds.MachineID,
		"uid":                  creds.UID,
		"organization_id":      creds.OrganizationID,
		"key":                  creds.CosyKey,
		"security_oauth_token": creds.SecurityOAuthToken,
		"encrypt_user_info":    creds.EncryptUserInfo,
		"user_type":            creds.UserType,
		"expire_time":          creds.ExpireTime,
		"name":                 req.Name,
		"expires_at":           func() time.Time {
			if creds.ExpireTime > 0 {
				if creds.ExpireTime > 32503680000 {
					return time.UnixMilli(creds.ExpireTime)
				}
				return time.Unix(creds.ExpireTime, 0)
			}
			return time.Now().Add(24 * time.Hour)
		}(),
	}

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
		_, _ = h.authManager.Register(c.Request.Context(), authObj)
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "Lingma credentials imported successfully",
		"uid":      creds.UID,
		"org_id":   creds.OrganizationID,
		"file":     fileName,
	})
}
