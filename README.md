# Sentinel SDK for Go

`sentinel-sdk-go` is the official Go bootstrap SDK for exporting application traces and metrics to [Sentinel](https://ingest.sentinel.unkcode.com). It configures the official OpenTelemetry Go SDK with Sentinel's OTLP/HTTP endpoint and bearer authentication.

It is deliberately not a tracing, metrics, propagation, batching, or OTLP implementation. After initialization, use normal OpenTelemetry APIs such as `otel.Tracer(...)` and `otel.Meter(...)`.

Logs are out of scope for v0.1. Future log support will use OpenTelemetry Logs, not a Sentinel-specific logging protocol.

## Installation

```sh
go get github.com/unkcode-org/sentinel-sdk-go
```

## Explicit configuration

```go
ctx := context.Background()

obs, err := sentinel.New(ctx, sentinel.Config{
	Endpoint:       "https://ingest.sentinel.unkcode.com",
	Token:          os.Getenv("SENTINEL_INGESTION_TOKEN"),
	ServiceName:    "elloco-backend",
	ServiceVersion: "1.0.0",
	Environment:    "production",
})
if err != nil {
	return err
}
defer func() {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = obs.Shutdown(shutdownCtx)
}()
```

The SDK installs the process-global OpenTelemetry tracer provider, meter provider, and W3C TraceContext+Baggage propagator. Only one Sentinel SDK instance may be active at a time. `Shutdown` flushes providers, is safe to call repeatedly, and allows a later initialization; it deliberately does not reset OTel globals to nil.

## Environment configuration

`NewFromEnv` follows the same initialization path as `New`:

```go
obs, err := sentinel.NewFromEnv(context.Background())
```

| Variable | Required | Description |
| --- | --- | --- |
| `SENTINEL_ENDPOINT` | yes | OTLP/HTTP base URL, normally `https://ingest.sentinel.unkcode.com` |
| `SENTINEL_INGESTION_TOKEN` | yes | Sentinel ingestion credential, for example `sip_xxx` |
| `OTEL_SERVICE_NAME` | yes | OpenTelemetry `service.name` |
| `OTEL_SERVICE_VERSION` | no | OpenTelemetry `service.version` |
| `OTEL_DEPLOYMENT_ENVIRONMENT` | yes | OpenTelemetry `deployment.environment.name` |
| `SENTINEL_TRACE_SAMPLE_RATIO` | no | Root trace sampling ratio from `0` through `1`; default `1` |
| `SENTINEL_METRIC_INTERVAL` | no | Go duration for periodic metric export; default `30s` |
| `SENTINEL_EXPORT_TIMEOUT` | no | Go duration for each OTLP export; default `10s` |
| `SENTINEL_INSECURE` | no | Set `true` only for a local `http://` collector |

The endpoint is a base endpoint: exporters send standard OTLP requests to `/v1/traces` and `/v1/metrics`.

## HTTP instrumentation

Use the convenience package as a thin wrapper around the maintained `otelhttp` instrumentation:

```go
router := http.NewServeMux()
router.Handle("/health", sentinelhttp.Middleware("http.server")(http.HandlerFunc(health)))
```

For Chi, route-level wrapping is recommended. Chi has then resolved the route template, so `otelhttp` records `http.route` as `/users/{id}` rather than a raw ID path:

```go
r := chi.NewRouter()
r.Get("/users/{id}", sentinelhttp.ChiHandler(
	http.HandlerFunc(getUser), "http.server",
).ServeHTTP)
```

`sentinelhttp.ChiMiddleware("http.server")` is also available for router-wide spans; it ensures template-based span names. Route-level `ChiHandler` is preferable when you need the standard `http.route` attribute.

## Manual traces and metrics

There is no Sentinel tracer or meter abstraction:

```go
tracer := otel.Tracer("my-service/orders")
ctx, span := tracer.Start(ctx, "order.checkout")
defer span.End()

counter, err := otel.Meter("my-service/orders").Int64Counter("orders.checkout")
if err != nil {
	return err
}
counter.Add(ctx, 1)
```

The trace sampler is OpenTelemetry's `ParentBased(TraceIDRatioBased(...))`. The default samples all new root traces; parent sampling decisions are preserved. To disable root sampling intentionally, use a non-nil zero value:

```go
ratio := 0.25
cfg.TraceSampleRatio = &ratio
```

## GORM

`sentinelgorm` wraps the maintained [GORM OpenTelemetry plugin](https://gorm.io/plugin/opentelemetry) and does not implement GORM callbacks itself:

```go
if err := sentinelgorm.Instrument(db); err != nil {
	return err
}

// Keep the active span by passing request context to GORM.
err := db.WithContext(ctx).Find(&orders).Error
```

The helper enables the plugin's `WithoutQueryVariables` option, so bound SQL values are not captured. It may emit standard GORM DB stats metrics provided by that plugin.

## Elloco-style startup

```go
package main

func main() {
	ctx := context.Background()

	obs, err := sentinel.NewFromEnv(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := obs.Shutdown(ctx); err != nil {
			log.Printf("telemetry shutdown: %v", err)
		}
	}()

	// initialize DB; optionally call sentinelgorm.Instrument(db)
	// initialize a Chi router and add sentinelhttp instrumentation
	// start the HTTP server
}
```

For Docker or Dokploy:

```env
SENTINEL_ENDPOINT=https://ingest.sentinel.unkcode.com
SENTINEL_INGESTION_TOKEN=sip_xxx
OTEL_SERVICE_NAME=elloco-backend
OTEL_SERVICE_VERSION=<build version/git sha>
OTEL_DEPLOYMENT_ENVIRONMENT=production
```

Applications can inject a build version with their normal build process, for example `-ldflags "-X main.version=$GIT_SHA"`; this module does not depend on Git.

## Security and data safety

- The ingestion token is trimmed but never logged or included in SDK errors.
- TLS verification is always enabled. Plain HTTP requires both an `http://` URL and `Insecure: true` (or `SENTINEL_INSECURE=true`).
- The SDK only adds standard service and low-risk host/OS/SDK resource metadata. It never sends Sentinel-reserved identity attributes such as `sentinel.tenant.id`.
- It does not collect HTTP bodies, response bodies, Authorization headers, cookies, or application logs.
- `sentinelgorm` excludes SQL bind values by default. Review any application-added span attributes and instrumentation options for your own privacy obligations.
- Avoid raw URL paths as span names. Use route templates, particularly for parameterized HTTP routes.

## Troubleshooting

- **`endpoint is required` or metadata errors:** check the required environment variables above. Whitespace is safely trimmed.
- **`http endpoint requires Insecure`:** use HTTPS in production. Set the explicit insecure flag only for a local development collector.
- **`SDK is already initialized`:** initialize Sentinel once near application startup and share standard global OTel APIs afterwards.
- **No data in Sentinel:** ensure outbound access to the endpoint, a valid ingestion token, and a bounded shutdown call during graceful application termination. Runtime transport failures are handled by the official OTel exporters and do not panic request paths.

## Compatibility

v0.1 targets Go 1.25+ and OpenTelemetry Go `v1.46.x`, using OTLP/HTTP. This SDK follows Go module semantic versioning. Minor releases may add non-breaking convenience helpers; breaking public API changes wait for a new major version.
