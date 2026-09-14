# Sentinel SDK for Go

`sentinel-sdk-go` is a thin OpenTelemetry bootstrap and integration layer for
Sentinel. It configures the official OpenTelemetry Go SDK and OTLP/HTTP
exporters for traces, metrics, and logs. It does not implement a separate
tracing, metrics, logging, propagation, batching, sampling, retry, or OTLP
protocol.

After startup, application code continues to use standard APIs:

```go
tracer := otel.Tracer("orders")
meter := otel.Meter("orders")
logger.WithContext(ctx).Error("payment failed")
```

## Installation

```sh
go get github.com/unkcode-org/sentinel-sdk-go
```

## Configuration

```go
telemetry, err := sentinel.New(context.Background(), sentinel.Config{
	Endpoint:       "https://ingest.sentinel.unkcode.com",
	Token:          os.Getenv("SENTINEL_INGESTION_TOKEN"),
	ServiceName:    "elloco-backend",
	ServiceVersion: "<git-sha/version>",
	Environment:    "production",
})
if err != nil {
	return err
}
```

Or configure from the environment:

```go
telemetry, err := sentinel.NewFromEnv(context.Background())
if err != nil {
	return err
}
```

```ini
SENTINEL_ENDPOINT=https://ingest.sentinel.unkcode.com
SENTINEL_INGESTION_TOKEN=sip_xxx
OTEL_SERVICE_NAME=elloco-backend
OTEL_SERVICE_VERSION=<git-sha/version>
OTEL_DEPLOYMENT_ENVIRONMENT=production
```

| Variable | Required | Description |
| --- | --- | --- |
| `SENTINEL_ENDPOINT` | yes | OTLP/HTTP base URL; exporters derive `/v1/traces`, `/v1/metrics`, and `/v1/logs` |
| `SENTINEL_INGESTION_TOKEN` | yes | Ingestion credential, sent only as `Authorization: Bearer …` |
| `OTEL_SERVICE_NAME` | yes | OpenTelemetry `service.name` |
| `OTEL_SERVICE_VERSION` | no | OpenTelemetry `service.version` |
| `OTEL_DEPLOYMENT_ENVIRONMENT` | yes | OpenTelemetry `deployment.environment.name` |
| `SENTINEL_TRACE_SAMPLE_RATIO` | no | Root sampling ratio (`0` through `1`); default `1` |
| `SENTINEL_METRIC_INTERVAL` | no | Periodic metric export interval; default `30s` |
| `SENTINEL_EXPORT_TIMEOUT` | no | Per-export timeout; default `10s` |
| `SENTINEL_INSECURE` | no | `true` only for an explicit local `http://` collector |

HTTPS is the default and TLS verification is never disabled. HTTP requires both
an `http://` endpoint and explicit `Insecure: true` (or `SENTINEL_INSECURE=true`).
Custom ports and IPv6 host:ports are supported.

## Centralized Chi, GORM, and Logrus integration

The production integration is centralized. Do not wrap every endpoint,
repository, or use case.

```go
logger := logrus.New()
if err := sentinellogrus.Instrument(logger); err != nil {
	return err
}

db, err := gorm.Open(...)
if err != nil {
	return err
}
if err := sentinelgorm.Instrument(db); err != nil {
	return err
}

router := chi.NewRouter()
router.Use(sentinelhttp.ChiMiddleware())
router.Get("/orders/{orderID}", getOrder)
```

`ChiMiddleware` is the recommended Chi API. It resolves the Chi route template
before delegating all HTTP behavior to official `otelhttp`. Thus
`GET /orders/abc/items/456` has span name
`GET /orders/{orderID}/items/{itemID}` and `http.route`
`/orders/{orderID}/items/{itemID}`; raw IDs do not become span names. It does
not capture request/response bodies, headers, cookies, or query values.
`ChiHandler` remains only for existing route-level integrations and is
deprecated for new applications.

`sentinelgorm.Instrument` installs the maintained GORM OpenTelemetry plugin
once and uses `WithoutQueryVariables`; it never installs custom callbacks or
captures SQL bind values. Propagate normal request context:

```text
Handler → UseCase → Repository → db.WithContext(ctx)
```

## Logs and trace/log correlation

`sentinellogrus` adds OpenTelemetry's maintained `otellogrus` hook. Existing
Logrus calls and outputs remain in place. Standard Logrus levels map to native
OpenTelemetry log severities. Structured fields become OTel attributes. Values
without a safe native OTel representation are stringified by the upstream
bridge; they do not panic the application.

```go
logger.WithContext(ctx).
	WithFields(logrus.Fields{
		"order_id":         order.ID,
		"payment_provider": "mercadopago",
	}).
	Error("payment failed")
```

When `ctx` has an active OTel span, the log record carries its native TraceID
and SpanID. It does not create duplicate `trace_id` or `span_id` attributes.

## Manual telemetry and sampling

There is no Sentinel tracer, meter, or logger API:

```go
tracer := otel.Tracer("orders")
ctx, span := tracer.Start(ctx, "order.checkout")
defer span.End()

meter := otel.Meter("orders")
counter, err := meter.Int64Counter("orders.processed")
if err != nil {
	return err
}
counter.Add(ctx, 1)
```

Traces use `ParentBased(TraceIDRatioBased(...))`: upstream sampling decisions
are preserved and new root traces default to 100%. Set
`SENTINEL_TRACE_SAMPLE_RATIO` or `Config.TraceSampleRatio` from `0` through `1`
to change new-root sampling.

## Lifecycle and graceful shutdown

The SDK owns one process-wide TracerProvider, MeterProvider, and LoggerProvider
with the same resource. It installs TraceContext+Baggage propagation. Only one
Sentinel SDK instance may be active. `ForceFlush` flushes all signals;
`Shutdown` flushes and stops all providers, is idempotent, and is terminal for
that SDK instance. It does not reset OTel globals to no-op.

Use this production shutdown order:

1. Receive `SIGTERM` or `SIGINT`.
2. Stop accepting new HTTP requests.
3. Wait for in-flight requests.
4. Finish application shutdown work.
5. Call `telemetry.Shutdown` with a bounded context.
6. Exit.

Do not continue serving normal requests after telemetry shutdown; this keeps
final traces, metrics, and logs exportable.

## Elloco-style startup

```go
func main() {
	ctx := context.Background()
	telemetry, err := sentinel.NewFromEnv(ctx)
	if err != nil {
		log.Fatal(err)
	}

	logger := logrus.New()
	if err := sentinellogrus.Instrument(logger); err != nil {
		log.Fatal(err)
	}
	db, err := gorm.Open(...)
	if err != nil {
		log.Fatal(err)
	}
	if err := sentinelgorm.Instrument(db); err != nil {
		log.Fatal(err)
	}

	router := chi.NewRouter()
	router.Use(sentinelhttp.ChiMiddleware())
	// Register normal application routes.
	_ = &http.Server{Handler: router}
	_ = telemetry // shut down server first, then telemetry on termination.
}
```

## Security, privacy, and troubleshooting

- The ingestion token is used only as an OTLP Authorization header and is not
  included in SDK validation or lifecycle errors.
- The resource has standard `service.name`, `service.version`, and
  `deployment.environment.name`, never Sentinel tenant/application/environment
  identity attributes.
- The SDK never automatically collects HTTP bodies, headers, cookies,
  environment variables, credentials, or GORM bind values.
- Only fields explicitly supplied to Logrus are translated. Do not log
  credentials, body contents, or other sensitive data; v0.1 does not apply
  generic redaction.
- The SDK leaves the application's global OpenTelemetry ErrorHandler unchanged.
  Exporter failures use normal OTel error handling, avoiding a Sentinel logging
  loop.

If no data arrives, verify endpoint, token, service metadata, outbound
networking, and graceful shutdown. `ErrAlreadyInitialized` means Sentinel was
initialized twice; initialize once at application bootstrap. v0.1 targets Go
1.25+ and the mutually compatible pinned OpenTelemetry Go modules. The official
OpenTelemetry Logs API is separately versioned upstream; this module pins a
compatible official Logs SDK/exporter release for each SDK release.
