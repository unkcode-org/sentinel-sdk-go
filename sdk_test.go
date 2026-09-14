package sentinel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	collectormetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

type otlpRequest struct {
	path string
	auth string
	body []byte
}

func newOTLPReceiver(t *testing.T) (*httptest.Server, <-chan otlpRequest) {
	t.Helper()
	requests := make(chan otlpRequest, 20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		requests <- otlpRequest{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	return server, requests
}

func testSDKConfig(endpoint string) Config {
	c := validConfig()
	c.Endpoint = endpoint
	c.Insecure = true
	c.MetricInterval = time.Hour
	c.ExportTimeout = 2 * time.Second
	return c
}

func waitForRequest(t *testing.T, requests <-chan otlpRequest, path string) otlpRequest {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case request := <-requests:
			if request.path == path {
				return request
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", path)
		}
	}
}

func TestSDKExportsTracesMetricsAndResource(t *testing.T) {
	server, requests := newOTLPReceiver(t)
	defer server.Close()

	obs, err := New(context.Background(), testSDKConfig(server.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = obs.Shutdown(context.Background()) }()

	ctx, parent := otel.Tracer("test/traces").Start(context.Background(), "parent")
	_, child := otel.Tracer("test/traces").Start(ctx, "child")
	child.End()
	parent.End()

	counter, err := otel.Meter("test/metrics").Int64Counter("orders.processed")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v", err)
	}
	counter.Add(context.Background(), 1, metric.WithAttributes())
	if err := obs.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}

	traceRequest := waitForRequest(t, requests, "/v1/traces")
	metricRequest := waitForRequest(t, requests, "/v1/metrics")
	if traceRequest.auth != "Bearer sip_test_token" || metricRequest.auth != "Bearer sip_test_token" {
		t.Fatalf("authorization headers = %q and %q", traceRequest.auth, metricRequest.auth)
	}

	var traces collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(traceRequest.body, &traces); err != nil {
		t.Fatalf("decode traces: %v", err)
	}
	if len(traces.ResourceSpans) == 0 {
		t.Fatal("no resource spans exported")
	}
	if !resourceHasAttributes(traces.ResourceSpans[0].Resource.Attributes, map[string]string{
		"service.name":                "checkout",
		"service.version":             "1.2.3",
		"deployment.environment.name": "test",
	}) {
		t.Fatalf("expected service resource attributes, got %#v", traces.ResourceSpans[0].Resource.Attributes)
	}
	parentSpan, childSpan := traceSpans(&traces)
	if parentSpan == nil || childSpan == nil {
		t.Fatalf("expected parent and child spans, got %#v", traces.ResourceSpans)
	}
	if string(childSpan.ParentSpanId) != string(parentSpan.SpanId) {
		t.Fatalf("child parent span ID does not match parent")
	}

	var metrics collectormetric.ExportMetricsServiceRequest
	if err := proto.Unmarshal(metricRequest.body, &metrics); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if !hasMetric(&metrics, "orders.processed") {
		t.Fatal("orders.processed was not exported")
	}
}

func TestSDKSamplingAndPropagation(t *testing.T) {
	server, requests := newOTLPReceiver(t)
	defer server.Close()
	c := testSDKConfig(server.URL)
	c.TraceSampleRatio = ratio(0)
	obs, err := New(context.Background(), c)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = obs.Shutdown(context.Background()) }()

	ctx, span := otel.Tracer("test/sampling").Start(context.Background(), "not-sampled")
	span.End()
	if err := obs.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}
	if trace.SpanContextFromContext(ctx).IsSampled() {
		t.Fatal("root span is sampled with a zero sampling ratio")
	}

	carrier := propagation.MapCarrier{}
	remote := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1},
		SpanID:     trace.SpanID{2},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	otel.GetTextMapPropagator().Inject(trace.ContextWithRemoteSpanContext(context.Background(), remote), carrier)
	if !strings.HasPrefix(carrier.Get("traceparent"), "00-") || !strings.HasSuffix(carrier.Get("traceparent"), "-01") {
		t.Fatalf("traceparent = %q", carrier.Get("traceparent"))
	}

	select {
	case request := <-requests:
		if request.path == "/v1/traces" {
			t.Fatalf("received trace for unsampled span")
		}
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSDKShutdownAndDuplicateInitialization(t *testing.T) {
	server, requests := newOTLPReceiver(t)
	defer server.Close()
	obs, err := New(context.Background(), testSDKConfig(server.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, span := otel.Tracer("test/shutdown").Start(context.Background(), "flush-on-shutdown")
	span.End()
	if _, err := New(context.Background(), testSDKConfig(server.URL)); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second New() error = %v, want ErrAlreadyInitialized", err)
	}
	if err := obs.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := obs.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}
	_ = waitForRequest(t, requests, "/v1/traces")

	second, err := New(context.Background(), testSDKConfig(server.URL))
	if err != nil {
		t.Fatalf("New() after Shutdown error = %v", err)
	}
	if err := second.Shutdown(context.Background()); err != nil {
		t.Fatalf("second SDK Shutdown() error = %v", err)
	}
}

func resourceHasAttributes(attrs []*commonv1.KeyValue, want map[string]string) bool {
	got := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		got[attr.Key] = attr.Value.GetStringValue()
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}

func traceSpans(request *collectortrace.ExportTraceServiceRequest) (parent, child *tracev1.Span) {
	for _, resourceSpans := range request.ResourceSpans {
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			for _, span := range scopeSpans.Spans {
				switch span.Name {
				case "parent":
					parent = span
				case "child":
					child = span
				}
			}
		}
	}
	return parent, child
}

func hasMetric(request *collectormetric.ExportMetricsServiceRequest, name string) bool {
	for _, resourceMetrics := range request.ResourceMetrics {
		for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
			for _, metric := range scopeMetrics.Metrics {
				if metric.Name == name {
					return true
				}
			}
		}
	}
	return false
}
