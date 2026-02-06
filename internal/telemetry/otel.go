package telemetry

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

var (
	enabled atomic.Bool

	initOnce     sync.Once
	initErr      error
	shutdownFunc func(context.Context) error = func(context.Context) error { return nil }
)

func init() {
	enabled.Store(true)
}

// Init configures OpenTelemetry SDK exactly once.
//
// - Default: enabled
// - Disable: OTEL_SDK_DISABLED=true
// - Export endpoint: OTEL_EXPORTER_OTLP_ENDPOINT (defaults to 127.0.0.1:4318)
func Init(serviceName string) (func(context.Context) error, error) {
	initOnce.Do(func() {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")), "true") {
			enabled.Store(false)
			shutdownFunc = func(context.Context) error { return nil }
			return
		}

		attrs := []attribute.KeyValue{
			semconv.ServiceNameKey.String(serviceName),
		}
		if version := strings.TrimSpace(os.Getenv("APP_VERSION")); version != "" {
			attrs = append(attrs, semconv.ServiceVersionKey.String(version))
		}

		r, err := resource.New(
			context.Background(),
			resource.WithAttributes(attrs...),
		)
		if err != nil {
			initErr = err
			return
		}

		exporter, err := newOTLPHTTPExporter()
		if err != nil {
			initErr = err
			return
		}

		tp := sdktrace.NewTracerProvider(
			sdktrace.WithResource(r),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
			sdktrace.WithBatcher(
				exporter,
				sdktrace.WithBatchTimeout(5*time.Second),
				sdktrace.WithMaxExportBatchSize(512),
			),
		)

		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))

		shutdownFunc = tp.Shutdown
	})

	return shutdownFunc, initErr
}

func newOTLPHTTPExporter() (*otlptrace.Exporter, error) {
	raw := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if raw == "" {
		raw = "127.0.0.1:4318"
	}

	endpoint, urlPath, insecure := normalizeOTLPEndpoint(raw)
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint),
	}
	if insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	if urlPath != "" && urlPath != "/" {
		opts = append(opts, otlptracehttp.WithURLPath(urlPath))
	}

	return otlptracehttp.New(context.Background(), opts...)
}

func normalizeOTLPEndpoint(raw string) (endpoint string, urlPath string, insecure bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "127.0.0.1:4318", "", true
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		u, err := url.Parse(raw)
		if err == nil {
			insecure = u.Scheme == "http"
			if strings.TrimSpace(u.Host) != "" {
				endpoint = u.Host
			}
			urlPath = u.EscapedPath()
		} else {
			log.Warnf("failed to parse OTLP endpoint URL %q, treating as host:port: %v", raw, err)
		}
	}
	if endpoint == "" {
		endpoint = raw
		insecure = true
	}
	return endpoint, urlPath, insecure
}

func Enabled() bool {
	return enabled.Load()
}

func GinMiddleware(serviceName string) gin.HandlerFunc {
	if !Enabled() {
		return func(c *gin.Context) { c.Next() }
	}
	return otelgin.Middleware(serviceName)
}

func WrapTransport(rt http.RoundTripper) http.RoundTripper {
	if !Enabled() {
		return rt
	}
	if rt == nil {
		rt = http.DefaultTransport
	}
	return otelhttp.NewTransport(rt)
}
