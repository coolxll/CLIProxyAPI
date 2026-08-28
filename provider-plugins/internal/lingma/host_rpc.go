package lingma

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/pluginruntime"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type hostRPC struct {
	call       HostCall
	callbackID string
}

type hostHTTPRequest struct {
	HostCallbackID string                `json:"host_callback_id,omitempty"`
	Request        pluginapi.HTTPRequest `json:"request"`
}

type hostHTTPStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers,omitempty"`
	StreamID   string      `json:"stream_id,omitempty"`
}

type hostHTTPStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type hostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type hostHTTPStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
}

type hostStreamEmitRequest struct {
	StreamID string                 `json:"stream_id"`
	Payload  []byte                 `json:"payload,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Usage    *pluginapi.UsageDetail `json:"usage,omitempty"`
}

type hostStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

type hostLogRequest struct {
	HostCallbackID string         `json:"host_callback_id,omitempty"`
	Level          string         `json:"level,omitempty"`
	Message        string         `json:"message,omitempty"`
	Fields         map[string]any `json:"fields,omitempty"`
}

func (h hostRPC) do(req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	return hostCall[pluginapi.HTTPResponse](h, pluginabi.MethodHostHTTPDo, hostHTTPRequest{
		HostCallbackID: h.callbackID,
		Request:        req,
	})
}

func (h hostRPC) doStream(req pluginapi.HTTPRequest) (hostHTTPStreamResponse, error) {
	return hostCall[hostHTTPStreamResponse](h, pluginabi.MethodHostHTTPDoStream, hostHTTPRequest{
		HostCallbackID: h.callbackID,
		Request:        req,
	})
}

func (h hostRPC) readHTTPStream(streamID string) (hostHTTPStreamReadResponse, error) {
	return hostCall[hostHTTPStreamReadResponse](h, pluginabi.MethodHostHTTPStreamRead, hostHTTPStreamReadRequest{StreamID: streamID})
}

func (h hostRPC) closeHTTPStream(streamID string) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	_, _ = hostCall[struct{}](h, pluginabi.MethodHostHTTPStreamClose, hostHTTPStreamCloseRequest{StreamID: streamID})
}

func (h hostRPC) emit(streamID string, payload []byte, usage *pluginapi.UsageDetail) error {
	_, err := hostCall[struct{}](h, pluginabi.MethodHostStreamEmit, hostStreamEmitRequest{
		StreamID: streamID,
		Payload:  payload,
		Usage:    usage,
	})
	return err
}

func (h hostRPC) closeOutputStream(streamID string, err error) {
	message := ""
	if err != nil {
		message = err.Error()
	}
	_, _ = hostCall[struct{}](h, pluginabi.MethodHostStreamClose, hostStreamCloseRequest{StreamID: streamID, Error: message})
}

func (h hostRPC) log(level, message string, fields map[string]any) {
	_, _ = hostCall[struct{}](h, pluginabi.MethodHostLog, hostLogRequest{
		HostCallbackID: h.callbackID,
		Level:          level,
		Message:        message,
		Fields:         fields,
	})
}

func hostCall[T any](h hostRPC, method string, request any) (T, error) {
	var zero T
	if h.call == nil {
		return zero, fmt.Errorf("Lingma host callback is unavailable")
	}
	rawRequest, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		return zero, fmt.Errorf("encode Lingma host request %s: %w", method, errMarshal)
	}
	rawResponse, errCall := h.call(method, rawRequest)
	if errCall != nil {
		return zero, errCall
	}
	var envelope pluginruntime.Envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &envelope); errUnmarshal != nil {
		return zero, fmt.Errorf("decode Lingma host response %s: %w", method, errUnmarshal)
	}
	if !envelope.OK {
		if envelope.Error != nil && strings.TrimSpace(envelope.Error.Message) != "" {
			return zero, fmt.Errorf("%s", envelope.Error.Message)
		}
		return zero, fmt.Errorf("Lingma host request %s failed", method)
	}
	if len(envelope.Result) == 0 {
		return zero, nil
	}
	var result T
	if errDecode := json.Unmarshal(envelope.Result, &result); errDecode != nil {
		return zero, fmt.Errorf("decode Lingma host result %s: %w", method, errDecode)
	}
	return result, nil
}
