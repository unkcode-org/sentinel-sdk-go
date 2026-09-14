// Package sentinelhttp provides thin net/http and Chi integration over the
// official OpenTelemetry otelhttp instrumentation.
package sentinelhttp

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Option is an official otelhttp option.
type Option = otelhttp.Option

// Middleware instruments a net/http handler using official otelhttp behavior.
// serviceOperation should be a stable operation name, not a raw URL path.
func Middleware(serviceOperation string, opts ...Option) func(http.Handler) http.Handler {
	opts = withSafeSpanNaming(serviceOperation, opts)
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, serviceOperation, opts...)
	}
}

// ChiMiddleware is the recommended centralized Chi integration. Register it
// once with r.Use(sentinelhttp.ChiMiddleware()). Chi resolves Request.Pattern
// before the routed handler returns, which lets official otelhttp record the
// route template in the span name and http.route attribute rather than a raw
// parameterized path.
//
// It does not collect request or response bodies, headers, cookies, or query
// values. Those are not enabled by any option in this package.
func ChiMiddleware(opts ...Option) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		instrumented := otelhttp.NewMiddleware("http.server", withSafeSpanNaming("http.server", opts)...)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Chi middleware runs before the router dispatches the endpoint, so
			// resolve its route into a separate context first. This is the one
			// Chi-specific step; otelhttp then owns all HTTP attributes, status
			// handling, propagation, metrics, and span lifecycle.
			if routeContext := chi.RouteContext(r.Context()); routeContext != nil && routeContext.Routes != nil {
				matched := chi.NewRouteContext()
				if routeContext.Routes.Match(matched, r.Method, r.URL.Path) {
					r.Pattern = matched.RoutePattern()
				}
			}
			instrumented.ServeHTTP(w, r)
		})
	}
}

// ChiHandler wraps one Chi route with official otelhttp instrumentation.
//
// Deprecated: prefer one router-wide ChiMiddleware registration. Keep this for
// existing applications that already wrap individual routes.
func ChiHandler(next http.Handler, serviceOperation string, opts ...Option) http.Handler {
	return otelhttp.NewHandler(next, serviceOperation, withSafeSpanNaming(serviceOperation, opts)...)
}

func withSafeSpanNaming(operation string, opts []Option) []Option {
	defaultOption := otelhttp.WithSpanNameFormatter(func(fallback string, r *http.Request) string {
		if r.Pattern != "" {
			return r.Method + " " + r.Pattern
		}
		return fallback
	})
	// Append caller options last so an application can deliberately use any
	// official otelhttp naming policy it needs.
	return append([]Option{defaultOption}, opts...)
}
