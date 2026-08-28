package lingma

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type statusError struct {
	code int
	msg  string
}

func (e statusError) Error() string   { return e.msg }
func (e statusError) StatusCode() int { return e.code }

func newStatusError(code int, message string) error {
	if code <= 0 {
		code = http.StatusBadGateway
	}
	return statusError{code: code, msg: strings.TrimSpace(message)}
}

func safeUpstreamMessage(raw []byte) string {
	const maxLength = 4096
	message := strings.TrimSpace(string(raw))
	if len(message) > maxLength {
		message = message[:maxLength] + "…"
	}
	if message == "" {
		return http.StatusText(http.StatusBadGateway)
	}
	return message
}

func unmarshalRequest(raw []byte, target any) error {
	if len(raw) == 0 {
		return fmt.Errorf("request is empty")
	}
	return json.Unmarshal(raw, target)
}

func pluginOK(value any) ([]byte, error) {
	return pluginruntime.OK(value)
}

func applyCustomHeaders(headers http.Header, attributes map[string]string) {
	if headers == nil {
		return
	}
	for key, value := range attributes {
		if !strings.HasPrefix(key, "header:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(key, "header:"))
		value = strings.TrimSpace(value)
		if name != "" && value != "" {
			headers.Set(name, value)
		}
	}
}

func usageDetailCopy(in *pluginapi.UsageDetail) *pluginapi.UsageDetail {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
