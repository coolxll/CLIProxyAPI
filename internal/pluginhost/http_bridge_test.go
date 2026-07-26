package pluginhost

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHostHTTPDoStreamCancellationClosesBlockedResponseBody(t *testing.T) {
	body := &cancelBlockingBody{closed: make(chan struct{})}
	transport := cancelRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
			Request:    req,
		}, nil
	})
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), "cliproxy.roundtripper", transport))
	host := New()
	stream, errStream := host.newHTTPClient(nil).DoStream(ctx, pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    "https://example.test/stream",
	})
	if errStream != nil {
		t.Fatalf("open stream: %v", errStream)
	}
	cancel()

	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("blocked response body was not closed after cancellation")
	}
	select {
	case _, ok := <-stream.Chunks:
		if ok {
			t.Fatal("stream channel remained open after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("stream channel did not close after cancellation")
	}
}

type cancelRoundTripper func(*http.Request) (*http.Response, error)

func (fn cancelRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type cancelBlockingBody struct {
	closed chan struct{}
	once   sync.Once
}

func (b *cancelBlockingBody) Read(_ []byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}

func (b *cancelBlockingBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}
