package lingma

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	lingmaencoding "github.com/router-for-me/CLIProxyAPI/v7/sdk/encoding/lingma"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

const (
	defaultAPIBaseURL = "https://lingma-api.tongyi.aliyun.com"
	signatureSecret   = "d2FyLCB3YXIgbmV2ZXIgY2hhbmdlcw=="
	serverPubKeyPEM   = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`
)

var serverPublicKey = mustParseServerPublicKey()

func mustParseServerPublicKey() *rsa.PublicKey {
	block, _ := pem.Decode([]byte(serverPubKeyPEM))
	if block == nil {
		panic("Lingma server public key is invalid")
	}
	parsed, errParse := x509.ParsePKIXPublicKey(block.Bytes)
	if errParse != nil {
		panic("parse Lingma server public key: " + errParse.Error())
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		panic("Lingma server public key is not RSA")
	}
	return key
}

func credentialsFromStorage(raw []byte) (credentials, error) {
	var creds credentials
	if len(raw) == 0 {
		return creds, fmt.Errorf("Lingma credential storage is empty")
	}
	if errUnmarshal := json.Unmarshal(raw, &creds); errUnmarshal != nil {
		return creds, fmt.Errorf("decode Lingma credential storage: %w", errUnmarshal)
	}
	if errValidate := validateCredentials(creds); errValidate != nil {
		return creds, errValidate
	}
	return creds, nil
}

func exchangeToken(host hostRPC, creds *credentials, apiBaseURL string) error {
	if creds == nil {
		return fmt.Errorf("Lingma credentials are nil")
	}
	temporaryKey := make([]byte, 16)
	if _, errRead := rand.Read(temporaryKey); errRead != nil {
		return fmt.Errorf("generate Lingma temporary key: %w", errRead)
	}
	encrypted, errEncrypt := rsa.EncryptPKCS1v15(rand.Reader, serverPublicKey, temporaryKey)
	if errEncrypt != nil {
		return fmt.Errorf("encrypt Lingma temporary key: %w", errEncrypt)
	}
	creds.CosyKey = base64.StdEncoding.EncodeToString(encrypted)

	grantPayload := map[string]any{
		"userId":             creds.UID,
		"personalToken":      "",
		"securityOauthToken": creds.SecurityOAuthToken,
		"refreshToken":       "",
		"needRefresh":        false,
		"authInfo": map[string]string{
			"key": creds.CosyKey,
		},
	}
	grantBody, errGrant := callV3API(host, http.MethodPost, "/algo/api/v3/user/grantAuthInfos", grantPayload, *creds, apiBaseURL)
	if errGrant != nil {
		return fmt.Errorf("grant Lingma auth infos: %w", errGrant)
	}
	var organizations []struct {
		OrganizationID string `json:"orgId"`
	}
	if errDecode := json.Unmarshal(grantBody, &organizations); errDecode == nil && len(organizations) > 0 {
		creds.OrganizationID = organizations[0].OrganizationID
	}

	statusPayload := map[string]any{
		"userId":             creds.UID,
		"personalToken":      "",
		"securityOauthToken": creds.SecurityOAuthToken,
		"refreshToken":       "",
		"needRefresh":        true,
		"authInfo":           map[string]any{},
	}
	statusBody, errStatus := callV3API(host, http.MethodPost, "/algo/api/v3/user/status", statusPayload, *creds, apiBaseURL)
	if errStatus != nil {
		return fmt.Errorf("fetch Lingma user status: %w", errStatus)
	}
	status := gjson.ParseBytes(statusBody)
	if value := status.Get("id"); value.Exists() {
		creds.UID = value.String()
	}
	if value := status.Get("name"); value.Exists() {
		creds.Name = value.String()
	}
	if value := status.Get("userType"); value.Exists() {
		creds.UserType = value.String()
	}
	if value := status.Get("encryptUserInfo"); value.Exists() {
		creds.EncryptUserInfo = value.String()
	}
	if value := status.Get("expireTime"); value.Exists() {
		creds.ExpireTime = value.Int()
	} else if value := status.Get("expire_time"); value.Exists() {
		creds.ExpireTime = value.Int()
	}

	if errActivate := activateCosyKey(host, *creds, apiBaseURL); errActivate != nil {
		return fmt.Errorf("activate Lingma key: %w", errActivate)
	}
	return nil
}

func activateCosyKey(host hostRPC, creds credentials, apiBaseURL string) error {
	modelURL := strings.TrimRight(apiBaseURL, "/") + "/algo/api/v2/model/list"
	headers, errHeaders := buildHeaders(creds, "", modelURL, time.Now())
	if errHeaders != nil {
		return errHeaders
	}
	resp, errDo := host.do(pluginapi.HTTPRequest{
		Method:    http.MethodGet,
		URL:       modelURL,
		Headers:   headers,
		Transport: pluginapi.HTTPTransportOptions{ForceHTTP11: true},
	})
	if errDo != nil {
		return errDo
	}
	if resp.StatusCode != http.StatusOK {
		return newStatusError(resp.StatusCode, "Lingma activation API error: "+safeUpstreamMessage(resp.Body))
	}
	return nil
}

func callV3API(host hostRPC, method, path string, payload map[string]any, creds credentials, apiBaseURL string) ([]byte, error) {
	innerJSON, errInner := json.Marshal(payload)
	if errInner != nil {
		return nil, fmt.Errorf("encode Lingma V3 payload: %w", errInner)
	}
	wrapperJSON, errWrapper := json.Marshal(map[string]string{
		"payload":       string(innerJSON),
		"encodeVersion": "1",
	})
	if errWrapper != nil {
		return nil, fmt.Errorf("encode Lingma V3 wrapper: %w", errWrapper)
	}
	encodedBody := lingmaencoding.Encode(wrapperJSON)
	requestURL := strings.TrimRight(apiBaseURL, "/") + path + "?Encode=1"
	date := time.Now().UTC().Format(http.TimeFormat)
	signature := fmt.Sprintf("%x", md5.Sum([]byte("cosy&"+signatureSecret+"&"+date)))
	headers := http.Header{
		"Content-Type":   []string{"application/json"},
		"Appcode":        []string{"cosy"},
		"Date":           []string{date},
		"Signature":      []string{signature},
		"Cosy-Machineid": []string{creds.MachineID},
		"Login-Version":  []string{"v2"},
		"Cosy-Version":   []string{"0.11.0"},
	}
	resp, errDo := host.do(pluginapi.HTTPRequest{
		Method:    method,
		URL:       requestURL,
		Headers:   headers,
		Body:      []byte(encodedBody),
		Transport: pluginapi.HTTPTransportOptions{ForceHTTP11: true},
	})
	if errDo != nil {
		return nil, errDo
	}
	if resp.StatusCode != http.StatusOK {
		return nil, newStatusError(resp.StatusCode, "Lingma V3 API error: "+safeUpstreamMessage(resp.Body))
	}
	return resp.Body, nil
}

func buildHeaders(creds credentials, encodedBody, fullURL string, now time.Time) (http.Header, error) {
	if strings.TrimSpace(creds.CosyKey) == "" || strings.TrimSpace(creds.UID) == "" {
		return nil, fmt.Errorf("Lingma credentials cannot sign requests")
	}
	cosyDate := fmt.Sprintf("%d", now.Unix())
	payloadJSON, errPayload := json.Marshal(map[string]string{
		"cosyVersion": "0.11.0",
		"ideVersion":  "",
		"info":        creds.EncryptUserInfo,
		"requestId":   newUUID(),
		"version":     "v1",
	})
	if errPayload != nil {
		return nil, fmt.Errorf("encode Lingma signature payload: %w", errPayload)
	}
	payloadBase64 := base64.StdEncoding.EncodeToString(payloadJSON)
	pathWithoutAlgo := extractPathWithoutAlgo(fullURL)
	signatureInput := payloadBase64 + "\n" + creds.CosyKey + "\n" + cosyDate + "\n" + encodedBody + "\n" + pathWithoutAlgo
	signature := fmt.Sprintf("%x", md5.Sum([]byte(signatureInput)))
	return http.Header{
		"Content-Type":         []string{"application/json"},
		"Accept":               []string{"text/event-stream"},
		"Accept-Encoding":      []string{"identity"},
		"Cache-Control":        []string{"no-cache"},
		"Login-Version":        []string{"v2"},
		"Authorization":        []string{"Bearer COSY." + payloadBase64 + "." + signature},
		"Cosy-Date":            []string{cosyDate},
		"Cosy-Key":             []string{creds.CosyKey},
		"Cosy-Version":         []string{"0.11.0"},
		"Cosy-Clienttype":      []string{"0"},
		"Cosy-Machineid":       []string{creds.MachineID},
		"Cosy-User":            []string{creds.UID},
		"Cosy-Organization-Id": []string{creds.OrganizationID},
		"User-Agent":           []string{"Go-http-client/1.1"},
	}, nil
}

func extractPathWithoutAlgo(rawURL string) string {
	parsed, errParse := url.Parse(rawURL)
	if errParse != nil {
		return rawURL
	}
	return strings.TrimPrefix(parsed.Path, "/algo")
}

func newUUID() string {
	value := make([]byte, 16)
	if _, errRead := rand.Read(value); errRead != nil {
		panic("generate Lingma UUID: " + errRead.Error())
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:])
}

func credentialExpiry(creds credentials, now time.Time) time.Time {
	if creds.ExpireTime <= 0 {
		return now.Add(24 * time.Hour)
	}
	if creds.ExpireTime > 32503680000 {
		return time.UnixMilli(creds.ExpireTime)
	}
	return time.Unix(creds.ExpireTime, 0)
}

func nextRefreshTime(creds credentials, now time.Time) time.Time {
	next := credentialExpiry(creds, now).Add(-12 * time.Hour)
	if !next.After(now) {
		return now.Add(5 * time.Minute)
	}
	return next
}
