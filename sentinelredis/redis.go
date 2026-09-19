// Package sentinelredis provides conservative Redis instrumentation through
// go-redis's maintained OpenTelemetry plugin.
package sentinelredis

import (
	"fmt"
	"sync"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
)

var instrumentedClients = struct {
	sync.Mutex
	clients map[redis.UniversalClient]struct{}
}{
	clients: make(map[redis.UniversalClient]struct{}),
}

// Instrument enables tracing and metrics for a go-redis client using the
// process-wide OpenTelemetry providers installed by sentinel.
//
// Redis commands receive their context from normal go-redis operations, such
// as client.Get(ctx, key). Traces include the Redis operation, duration, and
// errors. It deliberately does not record raw command statements or caller
// locations: Redis keys, values, and filesystem paths therefore do not become
// span attributes. Metrics are the maintained redisotel pool metrics, using
// semantic-convention-compliant cumulative instruments.
//
// Calling Instrument more than once with the same client is idempotent. Hooks
// added directly with redisotel cannot be inspected by go-redis, so callers
// must not combine that instrumentation with this function on one client.
func Instrument(client redis.UniversalClient) error {
	if err := validateClient(client); err != nil {
		return err
	}

	instrumentedClients.Lock()
	defer instrumentedClients.Unlock()
	if _, ok := instrumentedClients.clients[client]; ok {
		return nil
	}

	if err := redisotel.InstrumentTracing(
		client,
		redisotel.WithDBStatement(false),
		redisotel.WithCallerEnabled(false),
	); err != nil {
		return fmt.Errorf("sentinelredis: instrument tracing: %w", err)
	}
	if err := redisotel.InstrumentMetrics(
		client,
		redisotel.WithSemConvCompliantMetrics(true),
	); err != nil {
		// go-redis hooks cannot be removed. Keep the client marked to prevent a
		// later call from installing a duplicate tracing hook.
		instrumentedClients.clients[client] = struct{}{}
		return fmt.Errorf("sentinelredis: instrument metrics: %w", err)
	}

	instrumentedClients.clients[client] = struct{}{}
	return nil
}

func validateClient(client redis.UniversalClient) error {
	switch typed := client.(type) {
	case nil:
		return fmt.Errorf("sentinelredis: client is nil")
	case *redis.Client:
		if typed == nil {
			return fmt.Errorf("sentinelredis: client is nil")
		}
	case *redis.ClusterClient:
		if typed == nil {
			return fmt.Errorf("sentinelredis: client is nil")
		}
	case *redis.Ring:
		if typed == nil {
			return fmt.Errorf("sentinelredis: client is nil")
		}
	default:
		return fmt.Errorf("sentinelredis: %T not supported", client)
	}
	return nil
}
