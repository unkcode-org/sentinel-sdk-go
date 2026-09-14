# Design decisions

Sentinel SDK Go is a thin OpenTelemetry bootstrap layer. Sentinel needs a consistent endpoint, bearer header, service resource metadata, propagation setup, and lifecycle ownership; OpenTelemetry already provides tracing, metrics, batching, OTLP encoding, retry behavior, sampling, and instrumentation semantics. Reimplementing any of those would fragment the Go telemetry ecosystem and make applications less portable.

For that reason, the package does not wrap the normal `otel.Tracer` or `otel.Meter` APIs. `sentinel.New` installs standard global OTel providers and consumers continue to use the APIs they already know. OTLP/HTTP remains the wire protocol, with official OTel HTTP exporters sending to the normal `/v1/traces` and `/v1/metrics` paths.

Providers are process-global in OpenTelemetry. v0.1 rejects a second active Sentinel SDK with `ErrAlreadyInitialized`, rather than silently leaking providers and exporters. `Shutdown` is idempotent, flushes both providers, and releases Sentinel's active-instance reservation. It does not replace global providers with nil or no-op instances because unrelated instrumentation can retain global references.

The default root trace sampling ratio is 1.0. Sampling is implemented only with the official `ParentBased(TraceIDRatioBased(...))` sampler, which preserves upstream sampling decisions. Metrics use the official periodic reader and SDK defaults for aggregation and temporality.

The resource includes standard `service.name`, `service.version`, and `deployment.environment.name` attributes, plus standard OTel SDK, OS, and host detectors. It intentionally does not add Sentinel-reserved tenant, application, or environment identity attributes; Sentinel derives those from the ingestion credential.

HTTP helpers delegate to `otelhttp`. Chi route-level wrapping is offered because Chi only makes its resolved route pattern available at the point it invokes a route handler; this lets official `otelhttp` produce `http.route` from a template instead of a raw parameterized path.

GORM integration is included because `gorm.io/plugin/opentelemetry` is maintained by the GORM project and supports current GORM. The Sentinel wrapper only installs that plugin with query variables excluded. It does not implement, fork, or modify GORM callbacks.

Logs are deferred. A future implementation should use the OpenTelemetry Logs data model and exporters, preserving the same thin-bootstrap boundary. Compatibility is maintained by pinning mutually compatible stable OTel modules in each release and supporting the Go version declared in `go.mod`.
