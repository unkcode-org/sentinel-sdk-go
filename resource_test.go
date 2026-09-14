package sentinel

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

func TestResourceIncludesServiceMetadata(t *testing.T) {
	c := validConfig()
	c.ServiceVersion = "2026.09.14"
	res, err := newResource(context.Background(), c)
	if err != nil {
		t.Fatalf("newResource() error = %v", err)
	}
	attrs := res.Set()
	for _, want := range []struct {
		key   attribute.Key
		value string
	}{
		{semconv.ServiceNameKey, c.ServiceName},
		{semconv.ServiceVersionKey, c.ServiceVersion},
		{semconv.DeploymentEnvironmentNameKey, c.Environment},
	} {
		value, ok := attrs.Value(want.key)
		if !ok || value.AsString() != want.value {
			t.Errorf("resource %s = %q, want %q", want.key, value.AsString(), want.value)
		}
	}
}
