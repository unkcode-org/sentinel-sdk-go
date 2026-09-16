package sentinelhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestNewClientCreatesChildClientSpanAndPropagatesContext(t *testing.T) {
	recorder := installRecorder(t)
	traceparent := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent <- r.Header.Get("traceparent")
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	ctx, parent := otel.Tracer("application").Start(
		context.Background(),
		"GET /orders/{orderID}",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		server.URL+"/geocode?access_token=secret-token&email=person@example.test",
		strings.NewReader("sensitive request body"),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Cookie", "session=secret-cookie")

	response, err := NewClient(nil).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	parent.End()

	spans := recorder.Ended()
	clientSpan := spanByKind(spans, trace.SpanKindClient)
	if clientSpan == nil {
		t.Fatalf("no CLIENT span in %#v", spans)
	}
	if got, want := clientSpan.Parent().SpanID(), parent.SpanContext().SpanID(); got != want {
		t.Fatalf("client parent span ID = %s, want %s", got, want)
	}
	if got, want := clientSpan.SpanContext().TraceID(), parent.SpanContext().TraceID(); got != want {
		t.Fatalf("client trace ID = %s, want %s", got, want)
	}
	if got := clientSpan.Name(); got != "HTTP POST" {
		t.Fatalf("client span name = %q, want stable method-only name", got)
	}
	if got := attributeValue(clientSpan.Attributes(), "http.request.method"); got != http.MethodPost {
		t.Fatalf("http.request.method = %q, want %q", got, http.MethodPost)
	}
	if got := attributeValue(clientSpan.Attributes(), "http.response.status_code"); got != "202" {
		t.Fatalf("http.response.status_code = %q, want 202", got)
	}
	if got := attributeValue(clientSpan.Attributes(), "server.address"); got == "" {
		t.Fatal("server.address was not recorded")
	}
	if values := strings.Join(attributeStrings(clientSpan.Attributes()), " "); strings.Contains(values, "secret-token") || strings.Contains(values, "person@example.test") || strings.Contains(values, "sensitive request body") || strings.Contains(values, "secret-cookie") {
		t.Fatalf("sensitive request data was recorded in span attributes: %s", values)
	}

	downstreamContext := propagation.TraceContext{}.Extract(context.Background(), propagation.HeaderCarrier(http.Header{"Traceparent": []string{<-traceparent}}))
	downstreamParent := trace.SpanContextFromContext(downstreamContext)
	if got, want := downstreamParent.TraceID(), clientSpan.SpanContext().TraceID(); got != want {
		t.Fatalf("propagated trace ID = %s, want %s", got, want)
	}
	if got, want := downstreamParent.SpanID(), clientSpan.SpanContext().SpanID(); got != want {
		t.Fatalf("propagated parent span ID = %s, want %s", got, want)
	}
}

func TestNewClientRecordsTransportError(t *testing.T) {
	recorder := installRecorder(t)
	transportFailure := errors.New("dial external service: unavailable; token=secret-token")
	client := NewClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportFailure
	}))

	ctx, parent := otel.Tracer("application").Start(context.Background(), "order.checkout")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://payments.example/charge", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(request)
	if !errors.Is(err, transportFailure) {
		t.Fatalf("request error = %v, want %v", err, transportFailure)
	}
	parent.End()

	clientSpan := spanByKind(recorder.Ended(), trace.SpanKindClient)
	if clientSpan == nil {
		t.Fatal("no CLIENT span recorded")
	}
	if got := clientSpan.Status().Code; got != codes.Error {
		t.Fatalf("client span status = %s, want Error", got)
	}
	if description := clientSpan.Status().Description; strings.Contains(description, "secret-token") {
		t.Fatalf("sensitive transport error detail was recorded in span status: %s", description)
	}
	if got := attributeValue(clientSpan.Attributes(), "error.type"); got == "" {
		t.Fatal("transport error type was not recorded")
	}
	if values := strings.Join(attributeStrings(clientSpan.Attributes()), " "); strings.Contains(values, "secret-token") {
		t.Fatalf("sensitive transport error detail was recorded in span attributes: %s", values)
	}
	if got, want := clientSpan.Parent().SpanID(), parent.SpanContext().SpanID(); got != want {
		t.Fatalf("client parent span ID = %s, want %s", got, want)
	}
}

func TestNewClientCreatesSiblingSpansForSequentialRequests(t *testing.T) {
	recorder := installRecorder(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	ctx, parent := otel.Tracer("application").Start(context.Background(), "order.checkout")
	client := NewClient(nil)
	for _, path := range []string{"/first", "/second"} {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	parent.End()

	var clients []sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.SpanKind() == trace.SpanKindClient {
			clients = append(clients, span)
		}
	}
	if len(clients) != 2 {
		t.Fatalf("CLIENT spans = %d, want 2", len(clients))
	}
	for _, clientSpan := range clients {
		if got, want := clientSpan.Parent().SpanID(), parent.SpanContext().SpanID(); got != want {
			t.Fatalf("client parent span ID = %s, want sibling parent %s", got, want)
		}
	}
}

func TestNewTransportDoesNotWrapOTelTransportAgain(t *testing.T) {
	base := otelhttp.NewTransport(http.DefaultTransport)
	if got := NewTransport(base); got != base {
		t.Fatalf("NewTransport wrapped official otelhttp transport: got %T, want original", got)
	}

	sentinelTransport := NewTransport(http.DefaultTransport)
	if got := NewTransport(sentinelTransport); got != sentinelTransport {
		t.Fatalf("NewTransport wrapped Sentinel transport again: got %T, want original", got)
	}
}

func TestNewClientWorksWithNoopProvider(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(trace.NewNoopTracerProvider())
	t.Cleanup(func() { otel.SetTracerProvider(previousProvider) })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := NewClient(nil).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func spanByKind(spans []sdktrace.ReadOnlySpan, kind trace.SpanKind) sdktrace.ReadOnlySpan {
	for _, span := range spans {
		if span.SpanKind() == kind {
			return span
		}
	}
	return nil
}

func attributeValue(attributes []attribute.KeyValue, key string) string {
	for _, attribute := range attributes {
		if string(attribute.Key) == key {
			return attribute.Value.Emit()
		}
	}
	return ""
}

func attributeStrings(attributes []attribute.KeyValue) []string {
	values := make([]string, 0, len(attributes))
	for _, attribute := range attributes {
		values = append(values, string(attribute.Key)+"="+attribute.Value.Emit())
	}
	return values
}
