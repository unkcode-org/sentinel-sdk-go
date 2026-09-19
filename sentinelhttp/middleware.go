// Package sentinelhttp provides thin net/http, HTTP client, and Chi
// integrations over the official OpenTelemetry otelhttp instrumentation.
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
// once with r.Use(sentinelhttp.ChiMiddleware()). It resolves Chi's matched
// route template before otelhttp starts the span so official otelhttp can
// record http.route. After Chi dispatches the route, otelhttp formats the span
// name again using Chi's authoritative Request.Pattern. Route templates—not
// concrete parameter values—are used throughout.
//
// It does not collect request or response bodies, headers, cookies, or query
// values. Those are not enabled by any option in this package.
func ChiMiddleware(opts ...Option) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		instrumented := otelhttp.NewMiddleware("http.server", withSafeSpanNaming("http.server", opts)...)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Chi middleware runs before the router dispatches the endpoint. Use
			// Find's resolved template from an isolated context so otelhttp can
			// add http.route when the span starts. Do not read the scratch
			// context's evolving RoutePattern: Chi documents that it is only
			// authoritative after downstream dispatch returns.
			if routeContext := chi.RouteContext(r.Context()); routeContext != nil && routeContext.Routes != nil {
				matched := chi.NewRouteContext()
				if pattern := routeContext.Routes.Find(matched, r.Method, r.URL.Path); pattern != "" {
					r.Pattern = pattern
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
