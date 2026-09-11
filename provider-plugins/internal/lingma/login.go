package lingma

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	lingmaBridgeStartTimeout = 20 * time.Second
	lingmaOAuthFlowTTL       = 10 * time.Minute
)

type authLoginStartRPCRequest struct {
	pluginapi.AuthLoginStartRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type authLoginPollRPCRequest struct {
	pluginapi.AuthLoginPollRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type lingmaOAuthSession struct {
	state      string
	workDir    string
	binaryPath string
	socketPort int
	httpPort   int
	cmd        *exec.Cmd
	done       chan struct{}
	conn       *websocket.Conn
	expiresAt  time.Time
	mu         sync.Mutex
	closed     bool
}

func (s *lingmaOAuthSession) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.conn != nil {
		_ = s.conn.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		requestLingmaProcessStop(s.cmd)
		if s.done != nil {
			select {
			case <-s.done:
			case <-time.After(3 * time.Second):
				_ = s.cmd.Process.Kill()
			}
		}
	}
	if s.workDir != "" {
		_ = os.RemoveAll(s.workDir)
	}
}

func (p *Plugin) startLogin(raw []byte) ([]byte, error) {
	var req authLoginStartRPCRequest
	if errUnmarshal := unmarshalRequest(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Lingma login start request: %w", errUnmarshal)
	}

	state, errState := newLingmaOAuthState()
	if errState != nil {
		return nil, errState
	}

	binaryPath, errBin := EnsureLingmaServiceBinary()
	if errBin != nil {
		return nil, fmt.Errorf("ensure Lingma service binary: %w", errBin)
	}

	workDir, errDir := os.MkdirTemp("", "cpa-lingma-oauth-")
	if errDir != nil {
		return nil, fmt.Errorf("create Lingma temp work dir: %w", errDir)
	}

	socketPort, errPort1 := reserveLoopbackPort()
	if errPort1 != nil {
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("reserve Lingma socket port: %w", errPort1)
	}

	httpPort, errPort2 := reserveLoopbackPort()
	if errPort2 != nil {
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("reserve Lingma callback port: %w", errPort2)
	}
	for httpPort == socketPort {
		httpPort, _ = reserveLoopbackPort()
	}

	session := &lingmaOAuthSession{
		state:      state,
		workDir:    workDir,
		binaryPath: binaryPath,
		socketPort: socketPort,
		httpPort:   httpPort,
		expiresAt:  time.Now().Add(lingmaOAuthFlowTTL).UTC(),
		done:       make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), lingmaBridgeStartTimeout)
	defer cancel()

	if errStart := p.startLingmaBridge(ctx, session); errStart != nil {
		session.stop()
		return nil, fmt.Errorf("start Lingma service bridge: %w", errStart)
	}

	loginURL, errURL := p.generateLingmaLoginURL(ctx, session)
	if errURL != nil {
		session.stop()
		return nil, fmt.Errorf("generate Lingma login URL: %w", errURL)
	}

	p.oauthMu.Lock()
	if p.oauthSessions == nil {
		p.oauthSessions = make(map[string]*lingmaOAuthSession)
	}
	p.oauthSessions[state] = session
	p.oauthMu.Unlock()

	return pluginruntime.OK(pluginapi.AuthLoginStartResponse{
		Provider:  ProviderID,
		URL:       loginURL,
		State:     state,
		ExpiresAt: session.expiresAt,
		Metadata: map[string]any{
			"state":      state,
			"http_port":  httpPort,
			"expires_at": session.expiresAt,
		},
	})
}

func (p *Plugin) pollLogin(raw []byte) ([]byte, error) {
	var req authLoginPollRPCRequest
	if errUnmarshal := unmarshalRequest(raw, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Lingma login poll request: %w", errUnmarshal)
	}

	state := strings.TrimSpace(req.State)
	if state == "" {
		return nil, fmt.Errorf("invalid Lingma login state")
	}

	p.oauthMu.Lock()
	session, exists := p.oauthSessions[state]
	p.oauthMu.Unlock()

	if !exists || session == nil {
		return pluginruntime.OK(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "Lingma login session not found",
		})
	}

	if time.Now().After(session.expiresAt) {
		p.removeOAuthSession(state)
		return pluginruntime.OK(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "Lingma login expired",
		})
	}

	cacheDir := filepath.Join(session.workDir, "cache")
	idFile := filepath.Join(cacheDir, "id")
	userFile := filepath.Join(cacheDir, "user")

	if _, errID := os.Stat(idFile); errID != nil {
		return pluginruntime.OK(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending})
	}
	if _, errUser := os.Stat(userFile); errUser != nil {
		return pluginruntime.OK(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending})
	}

	creds, errCreds := loadCredentialsFromDir(cacheDir)
	if errCreds != nil || creds == nil || creds.CosyKey == "" || creds.UID == "" {
		return pluginruntime.OK(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending})
	}

	host := hostRPC{call: p.hostCall, callbackID: req.HostCallbackID}
	config := p.configSnapshot()
	if errExchange := exchangeToken(host, creds, config.APIBaseURL); errExchange != nil {
		return nil, fmt.Errorf("exchange Lingma token: %w", errExchange)
	}
	if errValidate := validateCredentials(*creds); errValidate != nil {
		return nil, errValidate
	}

	// Login succeeded! Clean up process and work directory
	p.removeOAuthSession(state)

	label := strings.TrimSpace(creds.Name)
	if label == "" {
		label = creds.UID
	}
	fileName := normalizedFileName("", label)
	auth := pluginapi.AuthData{
		Provider:         ProviderID,
		ID:               stableAuthID(*creds),
		FileName:         fileName,
		Label:            label,
		StorageJSON:      marshalStorage(*creds),
		Metadata:         sanitizedMetadata(*creds, label),
		Attributes:       map[string]string{"account": label},
		NextRefreshAfter: nextRefreshTime(*creds, time.Now()),
	}

	return pluginruntime.OK(pluginapi.AuthLoginPollResponse{
		Status: pluginapi.AuthLoginStatusSuccess,
		Auth:   auth,
	})
}

func (p *Plugin) removeOAuthSession(state string) {
	p.oauthMu.Lock()
	defer p.oauthMu.Unlock()
	if session, ok := p.oauthSessions[state]; ok && session != nil {
		session.stop()
		delete(p.oauthSessions, state)
	}
}

func (p *Plugin) startLingmaBridge(ctx context.Context, s *lingmaOAuthSession) error {
	s.cmd = exec.Command(
		s.binaryPath,
		"start",
		"--transportType=websocket",
		fmt.Sprintf("--workDir=%s", s.workDir),
		fmt.Sprintf("--socketPort=%d", s.socketPort),
		fmt.Sprintf("--httpPort=%d", s.httpPort),
	)
	configureLingmaCommand(s.cmd)
	s.cmd.Stdout = io.Discard
	s.cmd.Stderr = io.Discard

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("start Lingma process: %w", err)
	}

	go func() {
		_ = s.cmd.Wait()
		close(s.done)
	}()

	wsURL := fmt.Sprintf("ws://127.0.0.1:%d", s.socketPort)
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}

	for {
		conn, _, err := dialer.DialContext(ctx, wsURL, nil)
		if err == nil {
			s.conn = conn
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return fmt.Errorf("Lingma service process exited unexpectedly")
		case <-time.After(150 * time.Millisecond):
		}
	}

	// Send LSP initialize
	if err := writeLSPRPC(s.conn, 1, "initialize", map[string]any{
		"processId":    nil,
		"clientInfo":   map[string]string{"name": "cpa-lingma-plugin", "version": "1.0"},
		"rootUri":      "file:///tmp/cpa-lingma-login",
		"capabilities": map[string]any{},
		"workspaceFolders": []map[string]string{
			{"uri": "file:///tmp/cpa-lingma-login", "name": "cpa-lingma-login"},
		},
	}); err != nil {
		return fmt.Errorf("write initialize RPC: %w", err)
	}

	if err := readLSPRPCResponse(ctx, s.conn, 1, nil); err != nil {
		return fmt.Errorf("read initialize response: %w", err)
	}

	// Send initialized notification
	return writeLSPNotification(s.conn, "initialized", map[string]any{})
}

func (p *Plugin) generateLingmaLoginURL(ctx context.Context, s *lingmaOAuthSession) (string, error) {
	if err := writeLSPRPC(s.conn, 2, "login/generateUrl", map[string]string{
		"loginDedicatedType": "",
	}); err != nil {
		return "", fmt.Errorf("write login/generateUrl: %w", err)
	}

	var result struct {
		LoginURL  string `json:"loginUrl"`
		Success   *bool  `json:"success"`
		ErrorCode string `json:"errorCode"`
		ErrorMsg  string `json:"errorMsg"`
	}
	if err := readLSPRPCResponse(ctx, s.conn, 2, &result); err != nil {
		return "", fmt.Errorf("read login/generateUrl response: %w", err)
	}
	if result.Success != nil && !*result.Success {
		if result.ErrorCode != "" {
			return "", fmt.Errorf("Lingma login URL generation failed (code %s)", result.ErrorCode)
		}
		return "", fmt.Errorf("Lingma login URL generation failed")
	}

	loginURL := strings.TrimSpace(result.LoginURL)
	parsed, err := url.Parse(loginURL)
	if err != nil || parsed.Scheme != "https" || !strings.Contains(parsed.Hostname(), "aliyun.com") {
		return "", fmt.Errorf("Lingma returned invalid login URL: %s", loginURL)
	}
	return loginURL, nil
}

func writeLSPRPC(conn *websocket.Conn, id int, method string, params any) error {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(payload), payload)
	return conn.WriteMessage(websocket.TextMessage, []byte(frame))
}

func writeLSPNotification(conn *websocket.Conn, method string, params any) error {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(payload), payload)
	return conn.WriteMessage(websocket.TextMessage, []byte(frame))
}

func readLSPRPCResponse(ctx context.Context, conn *websocket.Conn, expectedID int, result any) error {
	readDeadline := time.Now().Add(lingmaBridgeStartTimeout)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(readDeadline) {
		readDeadline = deadline
	}
	if err := conn.SetReadDeadline(readDeadline); err != nil {
		return err
	}
	defer conn.SetReadDeadline(time.Time{})

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		payload := raw
		if sep := strings.Index(string(raw), "\r\n\r\n"); sep >= 0 {
			payload = raw[sep+4:]
		}

		var resp struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &resp) != nil || resp.ID != expectedID {
			continue
		}
		if resp.Error != nil {
			return fmt.Errorf("RPC error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		if result != nil && len(resp.Result) > 0 && string(resp.Result) != "null" {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("decode RPC result: %w", err)
			}
		}
		return nil
	}
}

func newLingmaOAuthState() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate login state: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func reserveLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
