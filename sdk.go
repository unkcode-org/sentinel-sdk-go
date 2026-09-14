// Package sentinel configures the official OpenTelemetry Go SDK to export
// traces and metrics to Sentinel using OTLP/HTTP.
package sentinel

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
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

	shutdownOnce sync.Once
	shutdownErr  error
}

// New validates c, installs OpenTelemetry's global tracer provider, meter
// provider, and W3C TraceContext+Baggage propagator, and returns their owner.
// Only one Sentinel SDK can be active in a process at a time.
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
	if endpoint.Scheme == "http" {
		traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
		metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
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

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(*c.TraceSampleRatio))),
		sdktrace.WithBatcher(traceExporter),
	)
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(c.MetricInterval))),
	)

	s := &SDK{tracerProvider: tracerProvider, meterProvider: meterProvider}
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
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

// ForceFlush asks OpenTelemetry to immediately export pending spans and metric
// data. It is useful before a controlled short-lived process exit.
func (s *SDK) ForceFlush(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return errors.Join(s.tracerProvider.ForceFlush(ctx), s.meterProvider.ForceFlush(ctx))
}

// Shutdown flushes and stops both providers. It is safe to call more than once;
// later calls return the result of the first call. It never resets OpenTelemetry
// globals, because other instrumentation may still hold their references.
func (s *SDK) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.shutdownOnce.Do(func() {
		s.shutdownErr = errors.Join(s.tracerProvider.Shutdown(ctx), s.meterProvider.Shutdown(ctx))
		processState.Lock()
		if processState.active == s {
			processState.active = nil
		}
		processState.Unlock()
	})
	return s.shutdownErr
}
