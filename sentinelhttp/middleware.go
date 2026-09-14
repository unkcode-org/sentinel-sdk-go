// Package sentinelhttp provides small convenience wrappers around the official
// OpenTelemetry net/http instrumentation.
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

// ChiMiddleware instruments a Chi router using official otelhttp behavior.
// It produces safe template-based span names. For an http.route attribute as
// well, prefer ChiHandler on each route: Chi sets Request.Pattern immediately
// before invoking a route handler, after router-wide middleware has begun.
// Use it with r.Use(sentinelhttp.ChiMiddleware("http.server")).
func ChiMiddleware(serviceOperation string, opts ...Option) func(http.Handler) http.Handler {
	opts = withSafeSpanNaming(serviceOperation, opts)
	return func(next http.Handler) http.Handler {
		instrumented := otelhttp.NewHandler(next, serviceOperation, opts...)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Pattern == "" {
				if routeContext := chi.RouteContext(r.Context()); routeContext != nil {
					if pattern := routeContext.RoutePattern(); pattern != "" {
						r.Pattern = pattern
					}
				}
			}
			instrumented.ServeHTTP(w, r)
		})
	}
}

// ChiHandler wraps one Chi route with official otelhttp instrumentation. It is
// the recommended Chi integration because Chi has populated Request.Pattern by
// the time route handlers run, so otelhttp emits the route template in standard
// HTTP semantic attributes. Register the result directly with r.Method, r.Get,
// and similar Chi route methods.
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
