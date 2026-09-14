package sentinel

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

func newResource(ctx context.Context, c Config) (*resource.Resource, error) {
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithOS(),
		resource.WithHost(),
		resource.WithAttributes(
			semconv.ServiceName(c.ServiceName),
			semconv.ServiceVersion(c.ServiceVersion),
			semconv.DeploymentEnvironmentName(c.Environment),
		),
	)
	// Standard host/OS detection is best effort. Retain the useful partial
	// resource rather than making telemetry unavailable because, for example,
	// a hostname cannot be determined in a restricted runtime.
	if err != nil && !errors.Is(err, resource.ErrPartialResource) {
		return nil, fmt.Errorf("create resource: %w", err)
	}
	return res, nil
}
