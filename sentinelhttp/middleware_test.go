package sentinelhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func installRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = provider.Shutdown(t.Context())
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})
	return recorder
}

func TestMiddlewareContinuesTraceAndRecordsStatus(t *testing.T) {
	recorder := installRecorder(t)
	handler := Middleware("http.server")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	request := httptest.NewRequest(http.MethodGet, "http://example.test/orders/42", nil)
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if span.Parent().SpanID().String() != "00f067aa0ba902b7" {
		t.Fatalf("parent span ID = %s", span.Parent().SpanID())
	}
	if span.Status().Code != codes.Error {
		t.Fatalf("span status = %s, want Error", span.Status().Code)
	}
	if span.Name() != "http.server" {
		t.Fatalf("span name = %q, want stable operation", span.Name())
	}
}

func TestChiMiddlewareUsesRouteTemplate(t *testing.T) {
	recorder := installRecorder(t)
	router := chi.NewRouter()
	router.Use(ChiMiddleware())
	router.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.test/users/123", nil))

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if got := spans[0].Name(); got != "GET /users/{id}" {
		t.Fatalf("span name = %q; raw path must not be used", got)
	}
	for _, attr := range spans[0].Attributes() {
		if string(attr.Key) == "http.route" && attr.Value.AsString() == "/users/{id}" {
			return
		}
	}
	t.Fatalf("http.route route template attribute was not recorded: %#v", spans[0].Attributes())
}

func TestChiMiddlewareUsesNestedRouteTemplatesAndContinuesTrace(t *testing.T) {
	recorder := installRecorder(t)
	router := chi.NewRouter()
	router.Use(ChiMiddleware())
	router.Get("/orders/{orderID}/items/{itemID}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	request := httptest.NewRequest(http.MethodGet, "http://example.test/orders/abc/items/456", nil)
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	router.ServeHTTP(httptest.NewRecorder(), request)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if got := span.Name(); got != "GET /orders/{orderID}/items/{itemID}" {
		t.Fatalf("span name = %q; raw IDs must not be used", got)
	}
	if got := span.Parent().SpanID().String(); got != "00f067aa0ba902b7" {
		t.Fatalf("parent span ID = %s", got)
	}
	for _, attr := range span.Attributes() {
		if string(attr.Key) == "http.route" {
			if got := attr.Value.AsString(); got != "/orders/{orderID}/items/{itemID}" {
				t.Fatalf("http.route = %q", got)
			}
			return
		}
	}
	t.Fatalf("http.route route template attribute was not recorded: %#v", span.Attributes())
}
