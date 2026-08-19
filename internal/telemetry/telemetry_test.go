package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/areasong/relay-lifeline/internal/egress"
)

func TestTelemetryResourceContainsStableServiceIdentity(t *testing.T) {
	resource, err := telemetryResource(Options{ServiceName: "relay", ServiceVersion: "2.3.0", Environment: "test", InstanceID: "instance-1"})
	if err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[attribute.Key]string{
		semconv.ServiceNameKey: "relay", semconv.ServiceVersionKey: "2.3.0", semconv.ServiceInstanceIDKey: "instance-1", attribute.Key("deployment.environment.name"): "test",
	} {
		value, ok := resource.Set().Value(key)
		if !ok || value.AsString() != expected {
			t.Fatalf("resource 缺少 %s=%q: %v", key, expected, resource.Attributes())
		}
	}
}

func TestSecureTelemetryEndpointPinsHTTPAndRejectsImplicitPrivateTargets(t *testing.T) {
	options := Options{Enabled: true, Protocol: "http/protobuf", Endpoint: "http://127.0.0.1:4318", Insecure: true, EgressPolicy: egress.Policy{DenyPrivateNetworks: true, AllowedHosts: []string{"127.0.0.1"}}}
	secured, err := secureTelemetryEndpoint(context.Background(), options)
	if err != nil || secured.Endpoint != "http://127.0.0.1:4318" {
		t.Fatalf("显式私网 OTLP 目标未被固定: %+v %v", secured, err)
	}
	options.EgressPolicy.AllowedHosts = []string{"*.254"}
	options.Endpoint = "http://169.254.169.254:4318"
	if _, err := secureTelemetryEndpoint(context.Background(), options); !errors.Is(err, egress.ErrDenied) {
		t.Fatalf("通配符不应授权元数据地址: %v", err)
	}
}

func TestSamplerHonorsConfiguredRatio(t *testing.T) {
	parameters := sdktrace.SamplingParameters{}
	if sampler(0).ShouldSample(parameters).Decision != sdktrace.Drop {
		t.Fatal("采样率 0 应丢弃无父级根 Span")
	}
	if sampler(1).ShouldSample(parameters).Decision != sdktrace.RecordAndSample {
		t.Fatal("采样率 1 应记录无父级根 Span")
	}
}

func TestExporterFailuresDegradeAndRecoverPerSignal(t *testing.T) {
	tracker := &statusTracker{status: Status{Enabled: true, Healthy: true, TraceHealthy: true, MetricHealthy: true}}
	spanExporter := &trackingSpanExporter{SpanExporter: failingSpanExporter{}, tracker: tracker}
	if err := spanExporter.ExportSpans(context.Background(), nil); err == nil {
		t.Fatal("预期 trace exporter 失败")
	}
	status := tracker.snapshot()
	if status.Healthy || status.TraceHealthy || status.TraceExportFailures != 1 || !status.MetricHealthy {
		t.Fatalf("trace 降级状态异常: %+v", status)
	}
	tracker.mark("trace", nil)
	if status = tracker.snapshot(); !status.Healthy || !status.TraceHealthy || status.LastSuccessAt.IsZero() {
		t.Fatalf("成功导出后应恢复: %+v", status)
	}

	metricExporter := &trackingMetricExporter{Exporter: failingMetricExporter{}, tracker: tracker}
	if err := metricExporter.Export(context.Background(), &metricdata.ResourceMetrics{}); err == nil {
		t.Fatal("预期 metric exporter 失败")
	}
	if status = tracker.snapshot(); status.Healthy || status.MetricHealthy || status.MetricExportFailures != 1 {
		t.Fatalf("metric 降级状态异常: %+v", status)
	}
}

func TestSetupRejectsUnsupportedProtocolWithoutPanicking(t *testing.T) {
	runtime, err := Setup(context.Background(), Options{Enabled: true, Protocol: "invalid", ServiceName: "relay", ExportTimeout: time.Second, MetricInterval: time.Minute})
	if err == nil || runtime == nil || CurrentStatus().Healthy {
		t.Fatalf("不支持的 exporter 应降级返回 runtime: runtime=%v status=%+v err=%v", runtime, CurrentStatus(), err)
	}
	if shutdownErr := runtime.Shutdown(context.Background()); shutdownErr != nil {
		t.Fatal(shutdownErr)
	}
}

type failingSpanExporter struct{}

func (failingSpanExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("trace export failed")
}

func (failingSpanExporter) Shutdown(context.Context) error { return nil }

type failingMetricExporter struct{}

func (failingMetricExporter) Temporality(sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.CumulativeTemporality
}

func (failingMetricExporter) Aggregation(sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.AggregationDefault{}
}

func (failingMetricExporter) Export(context.Context, *metricdata.ResourceMetrics) error {
	return errors.New("metric export failed")
}

func (failingMetricExporter) ForceFlush(context.Context) error { return nil }
func (failingMetricExporter) Shutdown(context.Context) error   { return nil }
