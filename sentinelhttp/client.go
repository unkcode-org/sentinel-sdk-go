package sentinelhttp

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewClient returns an HTTP client whose outbound requests are instrumented
// with the official OpenTelemetry otelhttp transport. Pass the context that
// should parent the client span to http.NewRequestWithContext.
//
// A nil base uses http.DefaultTransport. It does not capture request or
// response bodies, headers, cookies, URL paths, query values, or credentials.
func NewClient(base http.RoundTripper) *http.Client {
	return &http.Client{Transport: NewTransport(base)}
}

// NewTransport returns an outbound HTTP transport instrumented with the
// official OpenTelemetry otelhttp transport. It creates CLIENT spans, injects
// the configured W3C trace context, and records standard HTTP attributes.
//
// The returned spans have stable "HTTP <METHOD>" names. To keep telemetry
// free of request data, the URL presented to otelhttp contains only the scheme
// and server address; the underlying transport still receives the complete
// original URL. No request or response bodies, headers, cookies, URL paths,
// query values, or credentials are captured by this wrapper.
//
// If base is already an official otelhttp transport, or was returned by this
// function, it is returned unchanged so callers do not get nested CLIENT
// spans. A nil base uses http.DefaultTransport.
func NewTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if _, ok := base.(*otelhttp.Transport); ok {
		return base
	}
	if _, ok := base.(instrumentedTransport); ok {
		return base
	}

	return &clientTransport{
		transport: otelhttp.NewTransport(
			&restoreURLTransport{base: base},
			otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				return "HTTP " + r.Method
			}),
		),
	}
}

// instrumentedTransport identifies a transport created by this package.
// It intentionally remains private so NewTransport can be idempotent without
// adding a Sentinel-specific transport interface to the public API.
type instrumentedTransport interface {
	http.RoundTripper
	sentinelHTTPTransport()
}

type clientTransport struct {
	transport http.RoundTripper
}

func (*clientTransport) sentinelHTTPTransport() {}

func (t *clientTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r == nil || r.URL == nil {
		return t.transport.RoundTrip(r)
	}

	// otelhttp follows the current semantic conventions and records url.full.
	// Give it only a copy with the scheme and authority to prevent request paths,
	// query parameters, and URL credentials from becoming span attributes.
	originalURL := *r.URL
	ctx := context.WithValue(r.Context(), originalURLContextKey{}, &originalURL)
	safeRequest := r.Clone(ctx)
	safeRequest.URL = &url.URL{
		Scheme: originalURL.Scheme,
		Host:   originalURL.Host,
	}

	response, err := t.transport.RoundTrip(safeRequest)
	var transportErr *sanitizedTransportError
	if errors.As(err, &transportErr) {
		return response, transportErr.err
	}
	return response, err
}

type originalURLContextKey struct{}

// restoreURLTransport restores the original target after otelhttp has created
// its span and injected propagation headers. It keeps all instrumentation in
// the maintained otelhttp transport while ensuring the real request is intact.
type restoreURLTransport struct {
	base http.RoundTripper
}

func (t *restoreURLTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	originalURL, ok := r.Context().Value(originalURLContextKey{}).(*url.URL)
	if !ok || originalURL == nil {
		return t.base.RoundTrip(r)
	}

	restored := r.Clone(r.Context())
	urlCopy := *originalURL
	restored.URL = &urlCopy
	response, err := t.base.RoundTrip(restored)
	if err != nil {
		// otelhttp records the returned error as a span status description.
		// Keep that description free of target URLs and provider error details;
		// clientTransport returns the original error to the application below.
		return response, &sanitizedTransportError{err: err}
	}
	return response, nil
}

type sanitizedTransportError struct {
	err error
}

func (*sanitizedTransportError) Error() string { return "HTTP transport failure" }

func (e *sanitizedTransportError) Unwrap() error { return e.err }
