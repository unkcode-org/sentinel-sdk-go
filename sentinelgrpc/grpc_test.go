package sentinelgrpc

import (
	"context"
	"net"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

func TestClientAndServerOptionsPropagateTraceContext(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
		_ = provider.Shutdown(t.Context())
	})

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(ServerOption())
	grpc_health_v1.RegisterHealthServer(server, health.NewServer())
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, parent := otel.Tracer("application").Start(context.Background(), "order.checkout")
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer grpc-secret-token")
	_, err = grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Health.Check() error = %v", err)
	}
	parentContext := parent.SpanContext()
	parent.End()

	clientSpan, serverSpan := rpcSpans(recorder.Ended())
	if clientSpan == nil || serverSpan == nil {
		t.Fatalf("gRPC spans = %#v, want CLIENT and SERVER spans", recorder.Ended())
	}
	if got, want := clientSpan.Parent().SpanID(), parentContext.SpanID(); got != want {
		t.Fatalf("CLIENT parent = %s, want application parent %s", got, want)
	}
	if got, want := serverSpan.Parent().SpanID(), clientSpan.SpanContext().SpanID(); got != want {
		t.Fatalf("SERVER parent = %s, want propagated CLIENT span %s", got, want)
	}
	if got := attributeValue(clientSpan.Attributes(), "rpc.method"); got != "grpc.health.v1.Health/Check" {
		t.Fatalf("client rpc.method = %q, want full service/method", got)
	}
	if got := attributeValue(serverSpan.Attributes(), "rpc.response.status_code"); got == "" {
		t.Fatalf("server RPC status was not recorded: %#v", serverSpan.Attributes())
	}
	for _, span := range []sdktrace.ReadOnlySpan{clientSpan, serverSpan} {
		if values := strings.Join(attributeStrings(span.Attributes()), " "); strings.Contains(values, "grpc-secret-token") {
			t.Fatalf("gRPC span captured authorization metadata: %s", values)
		}
	}
}

func rpcSpans(spans []sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, sdktrace.ReadOnlySpan) {
	var client, server sdktrace.ReadOnlySpan
	for _, span := range spans {
		switch span.SpanKind() {
		case trace.SpanKindClient:
			client = span
		case trace.SpanKindServer:
			server = span
		}
	}
	return client, server
}

func attributeValue(attributes []attribute.KeyValue, key string) string {
	for _, attribute := range attributes {
		if string(attribute.Key) == key {
			return attribute.Value.Emit()
		}
	}
	return ""
}

func attributeStrings(attributes []attribute.KeyValue) []string {
	values := make([]string, 0, len(attributes))
	for _, attribute := range attributes {
		values = append(values, string(attribute.Key)+"="+attribute.Value.Emit())
	}
	return values
}
