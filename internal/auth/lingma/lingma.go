package lingma

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/lingma"
	"github.com/tidwall/gjson"
)

const (
	signatureSecret = "d2FyLCB3YXIgbmV2ZXIgY2hhbmdlcw=="
	apiBaseURL      = "https://lingma-api.tongyi.aliyun.com"
	serverPubKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`
)

var serverPubKey *rsa.PublicKey

func init() {
	block, _ := pem.Decode([]byte(serverPubKeyPEM))
	if block == nil {
		panic("failed to parse server public key PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic("failed to parse server public key: " + err.Error())
	}
	serverPubKey = pub.(*rsa.PublicKey)
}

// Credentials holds Lingma authentication state
type Credentials struct {
	MachineID          string `json:"machine_id"`
	UID                string `json:"uid"`
	OrganizationID     string `json:"organization_id"`
	CosyKey            string `json:"key"`
	EncryptUserInfo    string `json:"encrypt_user_info"`
	UserType           string `json:"user_type"`
	SecurityOAuthToken string `json:"security_oauth_token"`
	RefreshToken       string `json:"refresh_token"`
	ExpireTime         int64  `json:"expire_time"`
	Name               string `json:"name"`
}

// ParseCredentials decrypts the raw `id` and `user` file contents into Credentials.
func ParseCredentials(machineID, userB64 string) (*Credentials, error) {
	machineID = strings.TrimSpace(machineID)
	userB64 = strings.TrimSpace(userB64)

	userJSON, err := decryptUser(userB64, machineID)
	if err != nil {
		return nil, fmt.Errorf("decrypt user file: %w", err)
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
		return nil, fmt.Errorf("parse user json: %w", err)
	}

	return &Credentials{
		MachineID:          machineID,
		UID:                user.UID,
		OrganizationID:     user.OrganizationID,
		CosyKey:            user.Key,
		EncryptUserInfo:    user.EncryptUserInfo,
		UserType:           user.UserType,
		SecurityOAuthToken: user.SecurityOAuthToken,
		RefreshToken:       user.RefreshToken,
		ExpireTime:         user.ExpireTime,
		Name:               user.Name,
	}, nil
}

func decryptUser(b64, machineID string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	if len(machineID) < 16 {
		return nil, fmt.Errorf("machineId too short: %d chars", len(machineID))
	}
	key := []byte(machineID[:16])

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes new cipher: %w", err)
	}

	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext not block-aligned: len=%d", len(ciphertext))
	}

	mode := cipher.NewCBCDecrypter(block, key)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	// PKCS7 unpad
	if len(plaintext) == 0 {
		return nil, fmt.Errorf("empty plaintext")
	}
	padding := int(plaintext[len(plaintext)-1])
	if padding == 0 || padding > aes.BlockSize || padding > len(plaintext) {
		return nil, fmt.Errorf("invalid PKCS7 padding")
	}
	return plaintext[:len(plaintext)-padding], nil
}

// ExchangeToken performs the grantAuthInfos handshake to activate the CosyKey.
// If the current CosyKey is stale, it generates a fresh one first.
func ExchangeToken(creds *Credentials) error {
	// Generate fresh CosyKey (16-byte random, RSA encrypted)
	tempKey := make([]byte, 16)
	if _, err := rand.Read(tempKey); err != nil {
		return fmt.Errorf("generate temp key: %w", err)
	}

	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, serverPubKey, tempKey)
	if err != nil {
		return fmt.Errorf("rsa encrypt: %w", err)
	}
	creds.CosyKey = base64.StdEncoding.EncodeToString(encrypted)

	// Call grantAuthInfos
	payload := map[string]interface{}{
		"userId":             creds.UID,
		"personalToken":      "",
		"securityOauthToken": creds.SecurityOAuthToken,
		"refreshToken":       "",
		"needRefresh":        false,
		"authInfo": map[string]string{
			"key": creds.CosyKey,
		},
	}

	body, err := callV3API("POST", "/algo/api/v3/user/grantAuthInfos", payload, creds)
	if err != nil {
		return fmt.Errorf("grant auth infos: %w", err)
	}

	var resp []struct {
		OrgId string `json:"orgId"`
	}
	if err := json.Unmarshal(body, &resp); err == nil && len(resp) > 0 {
		creds.OrganizationID = resp[0].OrgId
	}

	// Fetch status
	statusPayload := map[string]interface{}{
		"userId":             creds.UID,
		"personalToken":      "",
		"securityOauthToken": creds.SecurityOAuthToken,
		"refreshToken":       "",
		"needRefresh":        true,
		"authInfo":           map[string]interface{}{},
	}
	statusBody, err := callV3API("POST", "/algo/api/v3/user/status", statusPayload, creds)
	if err != nil {
		return fmt.Errorf("fetch user status: %w", err)
	}
	
	// Parse statusBody to update credentials
	res := gjson.ParseBytes(statusBody)
	if id := res.Get("id"); id.Exists() {
		creds.UID = id.String()
	}
	if name := res.Get("name"); name.Exists() {
		creds.Name = name.String()
	}
	if userType := res.Get("userType"); userType.Exists() {
		creds.UserType = userType.String()
	}
	if info := res.Get("encryptUserInfo"); info.Exists() {
		creds.EncryptUserInfo = info.String()
	}
	// Try to extract expiration time if returned by API
	if exp := res.Get("expireTime"); exp.Exists() {
		creds.ExpireTime = exp.Int()
	} else if exp := res.Get("expire_time"); exp.Exists() {
		creds.ExpireTime = exp.Int()
	}

	// Activate via V2 call
	_ = activateCosyKey(creds)

	return nil
}

func activateCosyKey(creds *Credentials) error {
	url := apiBaseURL + "/algo/api/v2/model/list"
	headers, err := BuildHeaders(creds, "", url)
	if err != nil {
		return err
	}

	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("activation API error (%d): %s", resp.StatusCode, string(b))
	}
	return nil
}

func callV3API(method, path string, payload map[string]interface{}, creds *Credentials) ([]byte, error) {
	innerJSON, _ := json.Marshal(payload)
	
	wrapper := map[string]string{
		"payload":       string(innerJSON),
		"encodeVersion": "1",
	}
	wrapperJSON, _ := json.Marshal(wrapper)
	encodedBody := lingma.Encode(wrapperJSON)

	url := apiBaseURL + path + "?Encode=1"
	date := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	
	sigInput := "cosy&" + signatureSecret + "&" + date
	h := md5.Sum([]byte(sigInput))
	signature := fmt.Sprintf("%x", h)

	req, _ := http.NewRequest(method, url, strings.NewReader(encodedBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Appcode", "cosy")
	req.Header.Set("Date", date)
	req.Header.Set("Signature", signature)
	req.Header.Set("Cosy-Machineid", creds.MachineID)
	req.Header.Set("Login-Version", "v2")
	req.Header.Set("Cosy-Version", "0.11.0")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(b))
	}

	return io.ReadAll(resp.Body)
}

// BuildHeaders constructs the Bearer COSY signature and HTTP headers for V2 endpoints (like chat/completions).
func BuildHeaders(creds *Credentials, encodedBody, fullURL string) (map[string]string, error) {
	cosyDate := fmt.Sprintf("%d", time.Now().Unix())

	payload := map[string]string{
		"cosyVersion": "0.11.0",
		"ideVersion":  "",
		"info":        creds.EncryptUserInfo,
		"requestId":   newUUID(),
		"version":     "v1",
	}
	data, _ := json.Marshal(payload)
	payloadB64 := base64.StdEncoding.EncodeToString(data)

	pathWithoutAlgo := extractPathWithoutAlgo(fullURL)

	sigInput := payloadB64 + "\n" + creds.CosyKey + "\n" + cosyDate + "\n" + encodedBody + "\n" + pathWithoutAlgo
	sig := fmt.Sprintf("%x", md5.Sum([]byte(sigInput)))
	bearer := "Bearer COSY." + payloadB64 + "." + sig

	return map[string]string{
		"Content-Type":         "application/json",
		"Accept":               "text/event-stream",
		"Accept-Encoding":      "identity",
		"Cache-Control":        "no-cache",
		"Login-Version":        "v2",
		"Authorization":        bearer,
		"Cosy-Date":            cosyDate,
		"Cosy-Key":             creds.CosyKey,
		"Cosy-Version":         "0.11.0",
		"Cosy-Clienttype":      "0",
		"Cosy-Machineid":       creds.MachineID,
		"Cosy-User":            creds.UID,
		"Cosy-Organization-Id": creds.OrganizationID,
		"User-Agent":           "Go-http-client/1.1",
	}, nil
}

func extractPathWithoutAlgo(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	path := strings.TrimPrefix(u.Path, "/algo")
	return path
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
