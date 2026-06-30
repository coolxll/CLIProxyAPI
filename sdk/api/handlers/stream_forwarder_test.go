package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

func TestForwardStreamStoresTerminalErrorInGinContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	handler := &BaseAPIHandler{}
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{
		StatusCode: http.StatusInternalServerError,
		Error:      errors.New("stream error: stream ID 17; INTERNAL_ERROR; received from peer"),
	}
	close(errs)
	close(data)

	var canceled error
	handler.ForwardStream(c, c.Writer, func(err error) {
		canceled = err
	}, data, errs, StreamForwardOptions{
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {},
	})

	if canceled == nil || canceled.Error() != "stream error: stream ID 17; INTERNAL_ERROR; received from peer" {
		t.Fatalf("cancel error = %v", canceled)
	}
	value, exists := c.Get("API_RESPONSE_ERROR")
	if !exists {
		t.Fatal("expected API_RESPONSE_ERROR in gin context")
	}
	apiErrors, ok := value.([]*interfaces.ErrorMessage)
	if !ok {
		t.Fatalf("API_RESPONSE_ERROR type = %T", value)
	}
	if len(apiErrors) != 1 {
		t.Fatalf("api response errors = %d, want 1", len(apiErrors))
	}
	if got := apiErrors[0].Error.Error(); got != "stream error: stream ID 17; INTERNAL_ERROR; received from peer" {
		t.Fatalf("api response error = %q", got)
	}
}
