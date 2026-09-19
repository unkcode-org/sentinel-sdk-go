package sentinelhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
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

func TestChiMiddlewareGofipNestedRouteRegression(t *testing.T) {
	recorder := installRecorder(t)
	router := chi.NewRouter()
	router.Use(ChiMiddleware())
	router.Route("/api/v2", func(r chi.Router) {
		r.With(func(next http.Handler) http.Handler { return next }).Route("/emisores", func(r chi.Router) {
			r.Post("/{id-emisor}/arca/credenciales/{credencial-id}/verify", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
		})
	})

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodPost,
		"http://example.test/api/v2/emisores/01a0ba70-6412-7e07-a493-2e224433c418/arca/credenciales/f921-45c6-9f6c-b00b5e595b94/verify",
		nil,
	))

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if got := span.Name(); got != "POST /api/v2/emisores/{id-emisor}/arca/credenciales/{credencial-id}/verify" {
		t.Fatalf("span name = %q; raw path and partial pre-match route must not be used", got)
	}
	for _, attr := range span.Attributes() {
		if string(attr.Key) == "http.route" {
			if got := attr.Value.AsString(); got != "/api/v2/emisores/{id-emisor}/arca/credenciales/{credencial-id}/verify" {
				t.Fatalf("http.route = %q", got)
			}
			return
		}
	}
	t.Fatalf("http.route route template attribute was not recorded: %#v", span.Attributes())
}

func TestChiMiddlewareRouteTemplateMatrix(t *testing.T) {
	tests := []struct {
		name   string
		build  func(chi.Router)
		method string
		target string
		want   string
		route  string
	}{
		{
			name:   "simple route",
			build:  func(r chi.Router) { r.Get("/health", okHandler) },
			method: http.MethodGet,
			target: "http://example.test/health",
			want:   "GET /health",
			route:  "/health",
		},
		{
			name:   "path parameter excludes query",
			build:  func(r chi.Router) { r.Get("/users/{user-id}", okHandler) },
			method: http.MethodGet,
			target: "http://example.test/users/123?page=2",
			want:   "GET /users/{user-id}",
			route:  "/users/{user-id}",
		},
		{
			name: "router group",
			build: func(r chi.Router) {
				r.Group(func(r chi.Router) { r.Get("/groups/{group-id}", okHandler) })
			},
			method: http.MethodGet,
			target: "http://example.test/groups/abc",
			want:   "GET /groups/{group-id}",
			route:  "/groups/{group-id}",
		},
		{
			name: "mounted router",
			build: func(r chi.Router) {
				subrouter := chi.NewRouter()
				subrouter.Get("/things/{thing-id}", okHandler)
				r.Mount("/mount", subrouter)
			},
			method: http.MethodGet,
			target: "http://example.test/mount/things/abc",
			want:   "GET /mount/things/{thing-id}",
			route:  "/mount/things/{thing-id}",
		},
		{
			name:   "unmatched route uses bounded fallback",
			build:  func(r chi.Router) { r.Get("/configured", okHandler) },
			method: http.MethodGet,
			target: "http://example.test/not-found/user-controlled-value",
			want:   "http.server",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := installRecorder(t)
			router := chi.NewRouter()
			router.Use(ChiMiddleware())
			test.build(router)
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(test.method, test.target, nil))

			span := onlySpan(t, recorder)
			if got := span.Name(); got != test.want {
				t.Fatalf("span name = %q, want %q", got, test.want)
			}
			if test.route != "" {
				if got := spanAttribute(span, "http.route"); got != test.route {
					t.Fatalf("http.route = %q, want %q", got, test.route)
				}
			} else if got := spanAttribute(span, "http.route"); got != "" {
				t.Fatalf("unmatched span http.route = %q", got)
			}
		})
	}
}

func TestChiMiddlewareNestedSiblingsHaveDistinctTemplates(t *testing.T) {
	recorder := installRecorder(t)
	router := chi.NewRouter()
	router.Use(ChiMiddleware())
	router.Route("/api", func(r chi.Router) {
		r.Route("/v2", func(r chi.Router) {
			r.With(dummyAuth).Route("/emisores", func(r chi.Router) {
				for _, action := range []string{"verify", "validar", "activar"} {
					r.Post("/{id-emisor}/arca/credenciales/{credencial-id}/"+action, okHandler)
				}
			})
		})
	})

	for _, action := range []string{"verify", "validar", "activar"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(
			http.MethodPost,
			"http://example.test/api/v2/emisores/01abc/arca/credenciales/02def/"+action,
			nil,
		))
	}

	spans := recorder.Ended()
	if len(spans) != 3 {
		t.Fatalf("ended spans = %d, want 3", len(spans))
	}
	for index, action := range []string{"verify", "validar", "activar"} {
		wantRoute := "/api/v2/emisores/{id-emisor}/arca/credenciales/{credencial-id}/" + action
		if got := spans[index].Name(); got != "POST "+wantRoute {
			t.Fatalf("span %d name = %q, want %q", index, got, "POST "+wantRoute)
		}
		if got := spanAttribute(spans[index], "http.route"); got != wantRoute {
			t.Fatalf("span %d http.route = %q, want %q", index, got, wantRoute)
		}
	}
}

func TestChiMiddlewarePathParametersHaveBoundedCardinality(t *testing.T) {
	recorder := installRecorder(t)
	router := chi.NewRouter()
	router.Use(ChiMiddleware())
	router.Get("/users/{user-id}", okHandler)

	for _, id := range []string{"111", "222"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(
			http.MethodGet,
			"http://example.test/users/"+id,
			nil,
		))
	}

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(spans))
	}
	for index, span := range spans {
		if got := span.Name(); got != "GET /users/{user-id}" {
			t.Fatalf("span %d name = %q; concrete parameter value must not be used", index, got)
		}
		if got := spanAttribute(span, "http.route"); got != "/users/{user-id}" {
			t.Fatalf("span %d http.route = %q", index, got)
		}
	}
}

func TestChiMiddlewareUsesRouteTemplateForMetrics(t *testing.T) {
	recorder := installRecorder(t)
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })

	router := chi.NewRouter()
	router.Use(ChiMiddleware(otelhttp.WithMeterProvider(provider)))
	router.Route("/api/v2", func(r chi.Router) {
		r.With(dummyAuth).Route("/emisores", func(r chi.Router) {
			r.Post("/{id-emisor}/arca/credenciales/{credencial-id}/verify", okHandler)
		})
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodPost,
		"http://example.test/api/v2/emisores/01a0ba70-6412-7e07-a493-2e224433c418/arca/credenciales/f921-45c6-9f6c-b00b5e595b94/verify",
		nil,
	))
	if got := onlySpan(t, recorder).Name(); got != "POST /api/v2/emisores/{id-emisor}/arca/credenciales/{credencial-id}/verify" {
		t.Fatalf("span name = %q", got)
	}

	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	wantRoute := "/api/v2/emisores/{id-emisor}/arca/credenciales/{credencial-id}/verify"
	routes := metricRoutes(metrics)
	if len(routes) == 0 {
		t.Fatal("no HTTP server metric route attributes recorded")
	}
	for _, got := range routes {
		if got != wantRoute {
			t.Fatalf("metric http.route = %q, want %q", got, wantRoute)
		}
	}
}

func okHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func dummyAuth(next http.Handler) http.Handler { return next }

func onlySpan(t *testing.T, recorder *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	return spans[0]
}

func spanAttribute(span sdktrace.ReadOnlySpan, key string) string {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value.AsString()
		}
	}
	return ""
}

func metricRoutes(metrics metricdata.ResourceMetrics) []string {
	var routes []string
	for _, scope := range metrics.ScopeMetrics {
		for _, metric := range scope.Metrics {
			switch data := metric.Data.(type) {
			case metricdata.Histogram[int64]:
				for _, point := range data.DataPoints {
					if route, ok := point.Attributes.Value(attribute.Key("http.route")); ok {
						routes = append(routes, route.AsString())
					}
				}
			case metricdata.Histogram[float64]:
				for _, point := range data.DataPoints {
					if route, ok := point.Attributes.Value(attribute.Key("http.route")); ok {
						routes = append(routes, route.AsString())
					}
				}
			}
		}
	}
	return routes
}
