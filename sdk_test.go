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

	"github.com/sirupsen/logrus"
	"github.com/unkcode-org/sentinel-sdk-go/sentinellogrus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	collectorlog "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logv1 "go.opentelemetry.io/proto/otlp/logs/v1"
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

func TestSDKExportsLogrusLogsWithTraceCorrelation(t *testing.T) {
	server, requests := newOTLPReceiver(t)
	defer server.Close()

	obs, err := New(context.Background(), testSDKConfig(server.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = obs.Shutdown(context.Background()) }()

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	logger.SetLevel(logrus.DebugLevel)
	if err := sentinellogrus.Instrument(logger); err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}

	ctx, span := otel.Tracer("test/logs").Start(context.Background(), "checkout")
	logger.WithContext(ctx).WithFields(logrus.Fields{
		"order_id":         "order-123",
		"payment_provider": "mercadopago",
	}).Error("payment failed")
	spanContext := span.SpanContext()
	span.End()
	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")

	if err := obs.ForceFlush(nil); err != nil {
		t.Fatalf("ForceFlush(nil) error = %v", err)
	}

	request := waitForRequest(t, requests, "/v1/logs")
	if request.auth != "Bearer sip_test_token" {
		t.Fatalf("authorization header = %q", request.auth)
	}
	var exported collectorlog.ExportLogsServiceRequest
	if err := proto.Unmarshal(request.body, &exported); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	if len(exported.ResourceLogs) == 0 {
		t.Fatal("no resource logs exported")
	}
	if !resourceHasAttributes(exported.ResourceLogs[0].Resource.Attributes, map[string]string{
		"service.name":                "checkout",
		"service.version":             "1.2.3",
		"deployment.environment.name": "test",
	}) {
		t.Fatalf("expected service resource attributes, got %#v", exported.ResourceLogs[0].Resource.Attributes)
	}

	byMessage := logRecords(&exported)
	for message, severity := range map[string]logv1.SeverityNumber{
		"debug message":  logv1.SeverityNumber_SEVERITY_NUMBER_DEBUG,
		"info message":   logv1.SeverityNumber_SEVERITY_NUMBER_INFO,
		"warn message":   logv1.SeverityNumber_SEVERITY_NUMBER_WARN,
		"payment failed": logv1.SeverityNumber_SEVERITY_NUMBER_ERROR,
	} {
		record := byMessage[message]
		if record == nil {
			t.Fatalf("missing log record %q", message)
		}
		if record.SeverityNumber != severity {
			t.Errorf("severity for %q = %v, want %v", message, record.SeverityNumber, severity)
		}
	}
	correlated := byMessage["payment failed"]
	traceID := spanContext.TraceID()
	spanID := spanContext.SpanID()
	if got := string(correlated.TraceId); got != string(traceID[:]) {
		t.Fatalf("log TraceID = %x, want %s", correlated.TraceId, spanContext.TraceID())
	}
	if got := string(correlated.SpanId); got != string(spanID[:]) {
		t.Fatalf("log SpanID = %x, want %s", correlated.SpanId, spanContext.SpanID())
	}
	if !resourceHasAttributes(correlated.Attributes, map[string]string{
		"order_id":         "order-123",
		"payment_provider": "mercadopago",
	}) {
		t.Fatalf("structured fields missing: %#v", correlated.Attributes)
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
	_, duplicateErr := New(context.Background(), testSDKConfig(server.URL))
	assertNotContainsSecret(t, duplicateErr, "sip_test_token")
	if err := obs.Shutdown(nil); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := obs.Shutdown(nil); err != nil {
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

func TestSDKFailedInitializationDoesNotReserveState(t *testing.T) {
	invalid := validConfig()
	invalid.Endpoint = "%%%"
	invalid.Token = "sip_secret_that_must_not_appear"
	if _, err := New(context.Background(), invalid); err == nil {
		t.Fatal("New() invalid config error = nil")
	} else {
		assertNotContainsSecret(t, err, invalid.Token)
	}

	server, _ := newOTLPReceiver(t)
	defer server.Close()
	obs, err := New(context.Background(), testSDKConfig(server.URL))
	if err != nil {
		t.Fatalf("New() after failed initialization error = %v", err)
	}
	if err := obs.Shutdown(nil); err != nil {
		t.Fatalf("Shutdown(nil) error = %v", err)
	}
}

func TestSDKConcurrentInitializationAllowsOneActiveInstance(t *testing.T) {
	server, _ := newOTLPReceiver(t)
	defer server.Close()

	const attempts = 12
	start := make(chan struct{})
	results := make(chan *SDK, attempts)
	errs := make(chan error, attempts)
	for range attempts {
		go func() {
			<-start
			sdk, err := New(context.Background(), testSDKConfig(server.URL))
			if err != nil {
				errs <- err
				return
			}
			results <- sdk
		}()
	}
	close(start)

	var active *SDK
	for range attempts {
		select {
		case sdk := <-results:
			if active != nil {
				t.Fatal("more than one New() call succeeded")
			}
			active = sdk
		case err := <-errs:
			if !errors.Is(err, ErrAlreadyInitialized) {
				t.Fatalf("concurrent New() error = %v", err)
			}
		}
	}
	if active == nil {
		t.Fatal("no concurrent New() call succeeded")
	}
	if err := active.Shutdown(nil); err != nil {
		t.Fatalf("Shutdown(nil) error = %v", err)
	}
}

func TestSDKShutdownFlushesPendingLogs(t *testing.T) {
	server, requests := newOTLPReceiver(t)
	defer server.Close()
	obs, err := New(context.Background(), testSDKConfig(server.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	if err := sentinellogrus.Instrument(logger); err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}
	logger.WithField("order_id", "order-456").Error("flush me")
	if err := obs.Shutdown(nil); err != nil {
		t.Fatalf("Shutdown(nil) error = %v", err)
	}
	request := waitForRequest(t, requests, "/v1/logs")
	var exported collectorlog.ExportLogsServiceRequest
	if err := proto.Unmarshal(request.body, &exported); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	if logRecords(&exported)["flush me"] == nil {
		t.Fatal("Shutdown did not flush pending log")
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

func logRecords(request *collectorlog.ExportLogsServiceRequest) map[string]*logv1.LogRecord {
	records := make(map[string]*logv1.LogRecord)
	for _, resourceLogs := range request.ResourceLogs {
		for _, scopeLogs := range resourceLogs.ScopeLogs {
			for _, record := range scopeLogs.LogRecords {
				records[record.Body.GetStringValue()] = record
			}
		}
	}
	return records
}
