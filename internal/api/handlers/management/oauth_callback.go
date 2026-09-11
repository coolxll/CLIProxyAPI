package management

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type oauthCallbackRequest struct {
	Provider    string `json:"provider"`
	RedirectURL string `json:"redirect_url"`
	Code        string `json:"code"`
	State       string `json:"state"`
	Error       string `json:"error"`
}

func (h *Handler) PostOAuthCallback(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "handler not initialized"})
		return
	}

	var req oauthCallbackRequest
	if errBindJSON := c.ShouldBindJSON(&req); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid body"})
		return
	}
	h.handleOAuthCallback(c, req)
}

func (h *Handler) GetOAuthCallback(c *gin.Context) {
	req := oauthCallbackRequest{
		Provider: strings.TrimSpace(c.Query("provider")),
		Code:     strings.TrimSpace(c.Query("code")),
		State:    strings.TrimSpace(c.Query("state")),
		Error:    firstNonEmpty(c.Query("error"), c.Query("error_description")),
	}
	h.handleOAuthCallback(c, req)
}

func (h *Handler) handleOAuthCallback(c *gin.Context, req oauthCallbackRequest) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "handler not initialized"})
		return
	}

	state := strings.TrimSpace(req.State)
	code := strings.TrimSpace(req.Code)
	errMsg := strings.TrimSpace(req.Error)

	var parsedURL *url.URL
	if rawRedirect := strings.TrimSpace(req.RedirectURL); rawRedirect != "" {
		u, errParse := url.Parse(rawRedirect)
		if errParse != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid redirect_url"})
			return
		}
		parsedURL = u
		q := u.Query()
		if state == "" {
			state = strings.TrimSpace(q.Get("state"))
		}
		if code == "" {
			code = strings.TrimSpace(q.Get("code"))
		}
		if errMsg == "" {
			errMsg = strings.TrimSpace(q.Get("error"))
			if errMsg == "" {
				errMsg = strings.TrimSpace(q.Get("error_description"))
			}
		}
		if u.Fragment != "" {
			if fragQuery, errFrag := url.ParseQuery(u.Fragment); errFrag == nil {
				if state == "" {
					state = strings.TrimSpace(fragQuery.Get("state"))
				}
				if code == "" {
					code = strings.TrimSpace(fragQuery.Get("code"))
				}
				if errMsg == "" {
					errMsg = firstNonEmpty(fragQuery.Get("error"), fragQuery.Get("error_description"))
				}
			}
			if code == "" {
				code = strings.TrimSpace(u.Fragment)
			}
		}
		if code == "" {
			if q.Get("refreshToken") != "" || q.Get("refresh_token") != "" || q.Get("token") != "" || q.Get("auth") != "" {
				code = u.RawQuery
			}
		}
	}

	if state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "state is required"})
		return
	}
	if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}
	sessionProvider, sessionStatus, isPlugin, metadata, completed, ok := GetOAuthSessionDetails(state)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "unknown or expired state"})
		return
	}
	if completed {
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": "oauth flow is already completed"})
		return
	}
	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		provider = sessionProvider
	}
	var canonicalProvider string
	var errNormalize error
	if isPlugin {
		canonicalProvider, errNormalize = NormalizePluginOAuthCallbackProvider(provider)
	} else {
		canonicalProvider, errNormalize = NormalizeOAuthCallbackProvider(provider)
	}
	if errNormalize != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "unsupported provider"})
		return
	}
	if sessionStatus != "" {
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": sessionStatus})
		return
	}
	if !strings.EqualFold(sessionProvider, canonicalProvider) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "provider does not match state"})
		return
	}

	if isPlugin {
		var httpPort int
		if metadata != nil {
			switch v := metadata["http_port"].(type) {
			case int:
				httpPort = v
			case int64:
				httpPort = int(v)
			case float64:
				httpPort = int(v)
			}
		}
		if httpPort > 0 {
			if errMsg != "" {
				SetOAuthSessionError(state, errMsg)
				c.JSON(http.StatusOK, gin.H{"status": "error", "error": errMsg})
				return
			}
			if errForward := forwardPluginCallback(httpPort, parsedURL, strings.TrimSpace(req.RedirectURL)); errForward != nil {
				log.WithError(errForward).WithField("provider", canonicalProvider).Error("failed to forward plugin callback")
				c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to forward callback to plugin"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
			return
		}
	}

	if code == "" && errMsg == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "code or error is required"})
		return
	}

	if _, errWrite := WriteOAuthCallbackFileForPendingSession(h.cfg.AuthDir, canonicalProvider, state, code, errMsg); errWrite != nil {
		if errors.Is(errWrite, errOAuthSessionNotPending) {
			_, status, okSession := GetOAuthSession(state)
			if okSession && status != "" {
				c.JSON(http.StatusConflict, gin.H{"status": "error", "error": status})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"status": "error", "error": "oauth flow is not pending"})
			return
		}
		log.WithError(errWrite).Error("failed to persist oauth callback")
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "failed to persist oauth callback"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

var orgIDRegex = regexp.MustCompile(`"orgId"\s*:\s*"([a-zA-Z0-9_-]+)"`)

func forwardPluginCallback(httpPort int, parsedURL *url.URL, rawRedirect string) error {
	path := "/auth/callback"
	rawQuery := ""
	if parsedURL != nil {
		if parsedURL.Path != "" {
			path = parsedURL.Path
		}
		rawQuery = parsedURL.RawQuery
	} else if rawRedirect != "" {
		if u, err := url.Parse(rawRedirect); err == nil {
			if u.Path != "" {
				path = u.Path
			}
			rawQuery = u.RawQuery
		}
	}

	targetURL := fmt.Sprintf("http://127.0.0.1:%d%s", httpPort, path)
	if rawQuery != "" {
		targetURL += "?" + rawQuery
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return fmt.Errorf("create callback request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("do callback request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	// If the page prompts for account/organization selection (is_select_account = true),
	// automatically select the default organization to complete authentication.
	if strings.Contains(bodyStr, "is_select_account = true") {
		if matches := orgIDRegex.FindStringSubmatch(bodyStr); len(matches) > 1 && matches[1] != "" {
			orgID := matches[1]
			followURL := fmt.Sprintf("http://127.0.0.1:%d/auth/loginWithOrganization?organizationId=%s", httpPort, url.QueryEscape(orgID))
			if rawQuery != "" {
				followURL += "&" + rawQuery
			}
			followReq, errFollow := http.NewRequest(http.MethodGet, followURL, nil)
			if errFollow == nil {
				followResp, errFollowDo := client.Do(followReq)
				if errFollowDo == nil {
					_ = followResp.Body.Close()
				}
			}
		}
	}

	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
