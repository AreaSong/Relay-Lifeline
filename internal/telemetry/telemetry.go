package telemetry

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	"github.com/areasong/relay-lifeline/internal/egress"
)

var ErrUnavailable = errors.New("telemetry unavailable")

type Options struct {
	Enabled        bool
	Protocol       string
	Endpoint       string
	Insecure       bool
	SampleRatio    float64
	ServiceName    string
	ServiceVersion string
	Environment    string
	InstanceID     string
	ExportTimeout  time.Duration
	MetricInterval time.Duration
	EgressPolicy   egress.Policy
	serverName     string
}

type Status struct {
	Enabled              bool      `json:"enabled"`
	Protocol             string    `json:"protocol,omitempty"`
	Healthy              bool      `json:"healthy"`
	TraceHealthy         bool      `json:"traceHealthy"`
	MetricHealthy        bool      `json:"metricHealthy"`
	TraceExportFailures  uint64    `json:"traceExportFailures"`
	MetricExportFailures uint64    `json:"metricExportFailures"`
	LastSuccessAt        time.Time `json:"lastSuccessAt,omitempty"`
	LastFailureAt        time.Time `json:"lastFailureAt,omitempty"`
}

type Runtime struct {
	traceProvider *sdktrace.TracerProvider
	meterProvider *sdkmetric.MeterProvider
	tracker       *statusTracker
	shutdownOnce  sync.Once
	shutdownErr   error
}

type statusTracker struct {
	mu     sync.Mutex
	status Status
}

var currentStatus atomic.Pointer[statusTracker]

func init() {
	tracker := &statusTracker{status: Status{Healthy: true, TraceHealthy: true, MetricHealthy: true}}
	currentStatus.Store(tracker)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
}

func Setup(ctx context.Context, options Options) (*Runtime, error) {
	tracker := &statusTracker{status: Status{
		Enabled: options.Enabled, Protocol: options.Protocol, Healthy: true, TraceHealthy: true, MetricHealthy: true,
	}}
	currentStatus.Store(tracker)
	runtime := &Runtime{tracker: tracker}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if !options.Enabled {
		return runtime, nil
	}
	if options.ExportTimeout <= 0 {
		options.ExportTimeout = 10 * time.Second
	}
	if options.MetricInterval <= 0 {
		options.MetricInterval = time.Minute
	}
	if options.ServiceName == "" {
		options.ServiceName = "relay-lifeline"
	}
	if options.InstanceID == "" {
		options.InstanceID = randomInstanceID()
	}
	var err error
	options, err = secureTelemetryEndpoint(ctx, options)
	if err != nil {
		tracker.markFailure("trace")
		tracker.markFailure("metric")
		return runtime, fmt.Errorf("secure telemetry endpoint: %w", err)
	}
	res, err := telemetryResource(options)
	if err != nil {
		tracker.markFailure("trace")
		tracker.markFailure("metric")
		return runtime, fmt.Errorf("build telemetry resource: %w", err)
	}
	spanExporter, err := newSpanExporter(ctx, options)
	if err != nil {
		tracker.markFailure("trace")
		return runtime, fmt.Errorf("create trace exporter: %w", err)
	}
	metricExporter, err := newMetricExporter(ctx, options)
	if err != nil {
		_ = spanExporter.Shutdown(ctx)
		tracker.markFailure("metric")
		return runtime, fmt.Errorf("create metric exporter: %w", err)
	}
	trackedSpans := &trackingSpanExporter{SpanExporter: spanExporter, tracker: tracker}
	trackedMetrics := &trackingMetricExporter{Exporter: metricExporter, tracker: tracker}
	runtime.traceProvider = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(trackedSpans), sdktrace.WithResource(res), sdktrace.WithSampler(sampler(options.SampleRatio)),
	)
	reader := sdkmetric.NewPeriodicReader(trackedMetrics, sdkmetric.WithInterval(options.MetricInterval), sdkmetric.WithTimeout(options.ExportTimeout))
	runtime.meterProvider = sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(reader))
	otel.SetTracerProvider(runtime.traceProvider)
	otel.SetMeterProvider(runtime.meterProvider)
	return runtime, nil
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.shutdownOnce.Do(func() {
		var metricErr, traceErr error
		if r.meterProvider != nil {
			metricErr = r.meterProvider.Shutdown(ctx)
		}
		if r.traceProvider != nil {
			traceErr = r.traceProvider.Shutdown(ctx)
		}
		r.shutdownErr = errors.Join(metricErr, traceErr)
	})
	return r.shutdownErr
}

func CurrentStatus() Status {
	tracker := currentStatus.Load()
	if tracker == nil {
		return Status{Healthy: true, TraceHealthy: true, MetricHealthy: true}
	}
	return tracker.snapshot()
}

func Tracer(name string) oteltrace.Tracer { return otel.Tracer(name) }

func Meter(name string) metric.Meter { return otel.Meter(name) }

func Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

func Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	otel.GetTextMapPropagator().Inject(ctx, carrier)
}

func telemetryResource(options Options) (*resource.Resource, error) {
	attributes := []attribute.KeyValue{
		semconv.ServiceNameKey.String(options.ServiceName),
		semconv.ServiceVersionKey.String(options.ServiceVersion),
		semconv.ServiceInstanceIDKey.String(options.InstanceID),
	}
	if options.Environment != "" {
		attributes = append(attributes, attribute.String("deployment.environment.name", options.Environment))
	}
	return resource.Merge(resource.Default(), resource.NewSchemaless(attributes...))
}

func sampler(ratio float64) sdktrace.Sampler {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

func newSpanExporter(ctx context.Context, options Options) (sdktrace.SpanExporter, error) {
	switch options.Protocol {
	case "stdout":
		return stdouttrace.New(stdouttrace.WithPrettyPrint())
	case "http/protobuf":
		exporterOptions := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(options.Endpoint), otlptracehttp.WithTimeout(options.ExportTimeout)}
		if options.serverName != "" {
			exporterOptions = append(exporterOptions, otlptracehttp.WithTLSClientConfig(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: options.serverName}))
		}
		if options.Insecure {
			exporterOptions = append(exporterOptions, otlptracehttp.WithInsecure())
		}
		return otlptracehttp.New(ctx, exporterOptions...)
	case "grpc":
		exporterOptions := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(options.Endpoint), otlptracegrpc.WithTimeout(options.ExportTimeout), otlptracegrpc.WithDialOption(grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
			return options.EgressPolicy.DialContext(ctx, "tcp", address)
		}))}
		if options.Insecure {
			exporterOptions = append(exporterOptions, otlptracegrpc.WithInsecure())
		}
		return otlptracegrpc.New(ctx, exporterOptions...)
	default:
		return nil, fmt.Errorf("%w: unsupported protocol %q", ErrUnavailable, options.Protocol)
	}
}

func newMetricExporter(ctx context.Context, options Options) (sdkmetric.Exporter, error) {
	switch options.Protocol {
	case "stdout":
		return stdoutmetric.New()
	case "http/protobuf":
		exporterOptions := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpointURL(options.Endpoint), otlpmetrichttp.WithTimeout(options.ExportTimeout)}
		if options.serverName != "" {
			exporterOptions = append(exporterOptions, otlpmetrichttp.WithTLSClientConfig(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: options.serverName}))
		}
		if options.Insecure {
			exporterOptions = append(exporterOptions, otlpmetrichttp.WithInsecure())
		}
		return otlpmetrichttp.New(ctx, exporterOptions...)
	case "grpc":
		exporterOptions := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(options.Endpoint), otlpmetricgrpc.WithTimeout(options.ExportTimeout), otlpmetricgrpc.WithDialOption(grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
			return options.EgressPolicy.DialContext(ctx, "tcp", address)
		}))}
		if options.Insecure {
			exporterOptions = append(exporterOptions, otlpmetricgrpc.WithInsecure())
		}
		return otlpmetricgrpc.New(ctx, exporterOptions...)
	default:
		return nil, fmt.Errorf("%w: unsupported protocol %q", ErrUnavailable, options.Protocol)
	}
}

func secureTelemetryEndpoint(ctx context.Context, options Options) (Options, error) {
	if !options.Enabled || options.Protocol == "stdout" {
		return options, nil
	}
	policy := options.EgressPolicy.Normalize()
	raw := options.Endpoint
	if options.Protocol == "grpc" {
		scheme := "https"
		if options.Insecure {
			scheme = "http"
		}
		raw = scheme + "://" + options.Endpoint
	}
	target, err := url.Parse(raw)
	if err != nil || target.Hostname() == "" || target.User != nil {
		return options, fmt.Errorf("%w: invalid endpoint", ErrUnavailable)
	}
	if err := policy.ValidateURL(raw); err != nil {
		return options, err
	}
	addresses, err := policy.ResolveAllowed(ctx, target.Hostname())
	if err != nil {
		return options, err
	}
	if options.Protocol == "http/protobuf" {
		port := target.Port()
		if port == "" {
			if strings.EqualFold(target.Scheme, "https") {
				port = "443"
			} else {
				port = "80"
			}
		}
		pinned := *target
		pinned.Host = net.JoinHostPort(addresses[0].String(), port)
		options.Endpoint = pinned.String()
		if strings.EqualFold(target.Scheme, "https") && net.ParseIP(target.Hostname()) == nil {
			options.serverName = target.Hostname()
		}
	}
	options.EgressPolicy = policy
	return options, nil
}

type trackingSpanExporter struct {
	sdktrace.SpanExporter
	tracker *statusTracker
}

func (e *trackingSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := e.SpanExporter.ExportSpans(ctx, spans)
	e.tracker.mark("trace", err)
	return err
}

type trackingMetricExporter struct {
	sdkmetric.Exporter
	tracker *statusTracker
}

func (e *trackingMetricExporter) Export(ctx context.Context, metrics *metricdata.ResourceMetrics) error {
	err := e.Exporter.Export(ctx, metrics)
	e.tracker.mark("metric", err)
	return err
}

func (t *statusTracker) mark(signal string, err error) {
	if err != nil {
		t.markFailure(signal)
		return
	}
	t.mu.Lock()
	now := time.Now().UTC()
	t.status.LastSuccessAt = now
	if signal == "trace" {
		t.status.TraceHealthy = true
	} else {
		t.status.MetricHealthy = true
	}
	t.status.Healthy = t.status.TraceHealthy && t.status.MetricHealthy
	t.mu.Unlock()
}

func (t *statusTracker) markFailure(signal string) {
	t.mu.Lock()
	t.status.LastFailureAt = time.Now().UTC()
	if signal == "trace" {
		t.status.TraceHealthy = false
		t.status.TraceExportFailures++
	} else {
		t.status.MetricHealthy = false
		t.status.MetricExportFailures++
	}
	t.status.Healthy = false
	t.mu.Unlock()
}

func (t *statusTracker) snapshot() Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

func randomInstanceID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("process-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}
