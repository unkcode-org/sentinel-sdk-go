# Design decisions

Sentinel SDK Go is a thin OpenTelemetry bootstrap and integration layer. It
owns the configuration, resource, global provider installation, and lifecycle
needed to export to Sentinel. OpenTelemetry owns tracing, metrics, logs,
propagation, batching, sampling, OTLP encoding and transport, retries, and
instrumentation semantics. The SDK deliberately does not add Sentinel-specific
tracer, meter, logger, context, OTLP client, queue, or sampling APIs.

`sentinel.New` creates the official OTLP/HTTP trace, metric, and log exporters.
It configures a BatchSpanProcessor, PeriodicReader, and Logs BatchProcessor,
then installs standard TracerProvider, MeterProvider, and LoggerProvider
instances that share one resource. All exporters use the same base endpoint and
the only Sentinel-specific transport configuration is the `Authorization:
Bearer` header. Official exporters derive `/v1/traces`, `/v1/metrics`, and
`/v1/logs`; no signal path is manually constructed.

The resource includes standard `service.name`, `service.version`, and
`deployment.environment.name` plus standard OTel SDK, host, and OS detector
attributes. Sentinel tenant, application, and environment identity attributes
are never emitted because Ingest derives and owns those identities from the
credential.

Trace sampling is only OpenTelemetry `ParentBased(TraceIDRatioBased(...))`.
The default ratio is 1.0, and upstream parent decisions remain authoritative.
TraceContext and Baggage are the global propagators.

HTTP integration delegates to official `otelhttp`. Chi middleware needs one
small adapter because Chi middleware starts before routing resolves the
endpoint. It uses Chi's route matching API against a separate route context to
set `Request.Pattern` before `otelhttp` starts the span. `otelhttp` then emits
its normal HTTP semantic attributes and status behavior using a stable route
template. The production API is one `router.Use(sentinelhttp.ChiMiddleware())`
registration; route-level `ChiHandler` remains only for compatibility.

GORM integration delegates to the maintained `gorm.io/plugin/opentelemetry`
plugin and enables `WithoutQueryVariables`. There are no custom callbacks.
Applications preserve correlation with normal `db.WithContext(ctx)` usage.

Logrus integration delegates to OpenTelemetry's maintained `otellogrus` bridge.
It translates standard Logrus levels, message, timestamp, and fields into the
standard Logs data model. A Logrus entry created with `WithContext(ctx)` carries
the active OTel span context in the native LogRecord TraceID and SpanID fields.
Only explicitly supplied Logrus fields are considered; unsupported values follow
the upstream bridge's safe string conversion and do not panic. The SDK performs
no generic secret redaction: applications must not log sensitive fields.

Provider ownership is process-wide. A mutex implements a single-active-SDK
policy, and globals are installed only after all resources, exporters, and
providers have been created successfully. Partial exporter initialization is
cleaned up before returning an error. `ForceFlush` applies to all three
providers. `Shutdown` attempts all providers even when an earlier one fails,
joins errors, is idempotent, releases the active SDK reservation, and never
replaces OTel globals with no-op providers. Nil lifecycle contexts use
`context.Background()`.

Shutdown is terminal for one Sentinel SDK instance. Applications should receive
SIGTERM/SIGINT, stop accepting HTTP requests, wait for in-flight work, finish
application shutdown work, then flush and shut down Sentinel telemetry before
exiting. Applications must not continue serving normal requests after telemetry
shutdown. This preserves the opportunity to export final traces, metrics, and
logs.

The SDK intentionally does not replace the application's global OpenTelemetry
ErrorHandler. Asynchronous exporter failures follow normal OTel reporting,
which avoids recursively exporting telemetry implementation failures through
Logrus.
