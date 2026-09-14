// Package sentinel bootstraps the official OpenTelemetry Go SDK for Sentinel.
//
// It configures standard trace, metric, and log providers to export with
// OTLP/HTTP. Applications continue to use the normal OpenTelemetry APIs; this
// package intentionally does not define Sentinel-specific telemetry APIs.
package sentinel

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	logglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ErrAlreadyInitialized is returned when another active Sentinel SDK instance
// already owns the process-wide OpenTelemetry providers.
var ErrAlreadyInitialized = errors.New("sentinel: SDK is already initialized")

var processState struct {
	sync.Mutex
	active *SDK
}

// SDK owns the OpenTelemetry providers installed by New.
type SDK struct {
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
	loggerProvider *sdklog.LoggerProvider

	shutdownOnce sync.Once
	shutdownErr  error
}

// New validates c, creates OpenTelemetry trace, metric, and log providers, and
// installs them globally with the W3C TraceContext+Baggage propagator. All
// three providers share the same Resource. Only one Sentinel SDK can be active
// in a process at a time.
func New(ctx context.Context, c Config) (*SDK, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c, err := normalizeConfig(c)
	if err != nil {
		return nil, err
	}

	processState.Lock()
	defer processState.Unlock()
	if processState.active != nil {
		return nil, ErrAlreadyInitialized
	}

	res, err := newResource(ctx, c)
	if err != nil {
		return nil, err
	}
	endpoint, _ := url.Parse(c.Endpoint) // normalizeConfig has already validated it.
	headers := map[string]string{"Authorization": "Bearer " + c.Token}

	traceOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint.Host),
		otlptracehttp.WithHeaders(headers),
		otlptracehttp.WithTimeout(c.ExportTimeout),
	}
	metricOpts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(endpoint.Host),
		otlpmetrichttp.WithHeaders(headers),
		otlpmetrichttp.WithTimeout(c.ExportTimeout),
	}
	logOpts := []otlploghttp.Option{
		otlploghttp.WithEndpoint(endpoint.Host),
		otlploghttp.WithHeaders(headers),
		otlploghttp.WithTimeout(c.ExportTimeout),
	}
	if endpoint.Scheme == "http" {
		traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
		metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
		logOpts = append(logOpts, otlploghttp.WithInsecure())
	}

	traceExporter, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return nil, fmt.Errorf("sentinel: create trace exporter: %w", err)
	}
	metricExporter, err := otlpmetrichttp.New(ctx, metricOpts...)
	if err != nil {
		_ = traceExporter.Shutdown(ctx)
		return nil, fmt.Errorf("sentinel: create metric exporter: %w", err)
	}
	logExporter, err := otlploghttp.New(ctx, logOpts...)
	if err != nil {
		// Exporters can own HTTP transports. Clean up every successful partial
		// initialization before returning, while leaving globals untouched.
		_ = traceExporter.Shutdown(ctx)
		_ = metricExporter.Shutdown(ctx)
		return nil, fmt.Errorf("sentinel: create log exporter: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(*c.TraceSampleRatio))),
		sdktrace.WithBatcher(traceExporter),
	)
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(c.MetricInterval))),
	)
	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
	)

	s := &SDK{
		tracerProvider: tracerProvider,
		meterProvider:  meterProvider,
		loggerProvider: loggerProvider,
	}
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	logglobal.SetLoggerProvider(loggerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	processState.active = s
	return s, nil
}

// TracerProvider returns the installed OpenTelemetry tracer provider.
func (s *SDK) TracerProvider() *sdktrace.TracerProvider { return s.tracerProvider }

// MeterProvider returns the installed OpenTelemetry meter provider.
func (s *SDK) MeterProvider() *sdkmetric.MeterProvider { return s.meterProvider }

// LoggerProvider returns the installed OpenTelemetry logger provider.
func (s *SDK) LoggerProvider() *sdklog.LoggerProvider { return s.loggerProvider }

// ForceFlush asks OpenTelemetry to immediately export pending spans, metrics,
// and logs. It is useful before a controlled short-lived process exit. A nil
// context is treated as context.Background.
func (s *SDK) ForceFlush(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return safeJoin(
		s.tracerProvider.ForceFlush(ctx),
		s.meterProvider.ForceFlush(ctx),
		s.loggerProvider.ForceFlush(ctx),
	)
}

// Shutdown terminally flushes and stops the trace, metric, and log providers.
// It is safe to call more than once; later calls return the result of the first
// call. A nil context is treated as context.Background. It never resets
// OpenTelemetry globals, because other instrumentation may still hold their
// references.
//
// Applications must not continue serving normal requests after Shutdown. During
// graceful termination, stop accepting new requests, wait for in-flight work,
// finish application shutdown work, then call Shutdown before exiting.
func (s *SDK) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.shutdownOnce.Do(func() {
		// Always attempt every provider: a failed exporter must not prevent the
		// remaining signals from flushing and releasing their resources.
		s.shutdownErr = safeJoin(
			s.tracerProvider.Shutdown(ctx),
			s.meterProvider.Shutdown(ctx),
			s.loggerProvider.Shutdown(ctx),
		)
		processState.Lock()
		if processState.active == s {
			processState.active = nil
		}
		processState.Unlock()
	})
	return s.shutdownErr
}

// safeJoin combines lifecycle errors. Exporters are configured with the token
// only as an HTTP header, and lifecycle errors are never constructed from that
// header or Config.
func safeJoin(errs ...error) error {
	filtered := make([]error, 0, len(errs))
	for _, err := range errs {
		if err == nil {
			continue
		}
		filtered = append(filtered, err)
	}
	return errors.Join(filtered...)
}
