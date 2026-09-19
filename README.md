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

## Full backend setup

The production integration is centralized: bootstrap Sentinel once, instrument
infrastructure once, and pass normal `context.Context` values through the
application. Consumer projects do not configure `otelhttp`, `otelgrpc`,
Redis OTel plugins, GORM OTel plugins, or OTLP exporters directly.

```go
telemetry, err := sentinel.NewFromEnv(ctx)
if err != nil {
	return err
}

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

redisClient := redis.NewClient(&redis.Options{Addr: "redis:6379"})
if err := sentinelredis.Instrument(redisClient); err != nil {
	return err
}

router := chi.NewRouter()
router.Use(sentinelhttp.ChiMiddleware())
router.Get("/orders/{orderID}", getOrder)

externalHTTP := sentinelhttp.NewClient(nil)
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

`sentinelredis.Instrument` installs the maintained go-redis `redisotel`
tracing and metrics hooks. Call it once for each `*redis.Client`, cluster, or
ring client. Redis operation spans use the context supplied to calls such as
`redisClient.Get(ctx, key)`. Sentinel disables upstream raw command statements
and caller locations, so keys and values are not attached to spans. Its pool
metrics use bounded connection-pool attributes. Calling Sentinel's function
again with the same client is idempotent; do not also install `redisotel`
directly on that client, because go-redis does not expose installed hooks for
cross-package duplicate detection.

For external HTTP calls, create a client once at startup and pass the incoming
request context through the adapter or service. `NewClient` wraps the official
OpenTelemetry `otelhttp` transport: it creates a CLIENT span, propagates W3C
trace context downstream, and records method, peer/server information, status,
and errors. CLIENT spans are named `HTTP <METHOD>` so paths, IDs, and query
values never affect span names. The integration does not capture request or
response bodies, headers, cookies, URL paths, query values, or credentials.

```go
type MapsAdapter struct {
	client *http.Client
}

// After sentinel.New at startup: one shared client for all outbound adapters.
mapsAdapter := &MapsAdapter{client: sentinelhttp.NewClient(nil)}

func (a *MapsAdapter) Geocode(ctx context.Context, place string) error {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"https://maps.example/geocode?address="+url.QueryEscape(place),
		nil,
	)
	if err != nil {
		return err
	}

	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return nil
}

// Incoming HTTP request context -> adapter/service -> outbound request context
// produces a child HTTP CLIENT span automatically.
func getOrder(adapter *MapsAdapter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = adapter.Geocode(r.Context(), "customer supplied address")
	}
}
```

Use `sentinelhttp.NewTransport(http.DefaultTransport)` when a library or
existing `http.Client` needs only a `RoundTripper`. If the supplied transport
is already the official OpenTelemetry `otelhttp.Transport`, Sentinel returns it
unchanged to avoid redundant nested CLIENT spans.

### gRPC clients and servers

For outbound gRPC, `sentinelgrpc.NewClient` is a small convenience around the
modern lazy `grpc.NewClient`: it adds the official OTel stats handler and passes
every supplied gRPC option through unchanged.

```go
conn, err := sentinelgrpc.NewClient(
	"orders.internal:443",
	grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
)
if err != nil {
	return err
}
defer conn.Close()

ordersClient := ordersv1.NewOrdersClient(conn)
// The ctx passed to ordersClient methods is propagated automatically.
```

For composition with an existing gRPC connection factory, use
`sentinelgrpc.ClientOption()` directly:

```go
conn, err := grpc.NewClient(
	"orders.internal:443",
	sentinelgrpc.ClientOption(),
	grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
)
```

For every inbound gRPC server, add one composable server option:

```go
grpcServer := grpc.NewServer(
	sentinelgrpc.ServerOption(),
)
ordersv1.RegisterOrdersServer(grpcServer, ordersServer)
```

These handlers use Sentinel's global TraceContext+Baggage propagator to create
client and server RPC spans, preserving distributed parentage. The maintained
OTel handler records the full RPC service/method, status, errors, duration, and
standard supported RPC metrics. Sentinel does not enable message events and
does not capture protobuf request or response bodies, gRPC metadata,
`Authorization` values, tokens, or certificates.

After this setup, add manual telemetry only where a business operation adds
meaningful visibility; use standard `otel.Tracer(...)` and `otel.Meter(...)`
there as usual.

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
	redisClient := redis.NewClient(&redis.Options{Addr: "redis:6379"})
	if err := sentinelredis.Instrument(redisClient); err != nil {
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
  environment variables, credentials, GORM bind values, Redis command
  statements/keys/values, gRPC protobuf bodies, or gRPC metadata.
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
