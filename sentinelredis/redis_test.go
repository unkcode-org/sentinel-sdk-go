package sentinelredis

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestInstrumentCreatesChildSpanWithoutRedisKeysOrValues(t *testing.T) {
	recorder := installRecorder(t)
	client := redis.NewClient(&redis.Options{
		Protocol:        2,
		DisableIdentity: true,
		Dialer: func(_ context.Context, _, _ string) (net.Conn, error) {
			client, server := net.Pipe()
			go serveRedis(t, server)
			return client, nil
		},
	})
	t.Cleanup(func() { _ = client.Close() })

	if err := Instrument(client); err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}
	// Instrument is safe to call from a shared bootstrap path more than once.
	if err := Instrument(client); err != nil {
		t.Fatalf("second Instrument() error = %v", err)
	}

	ctx, parent := otel.Tracer("application").Start(context.Background(), "order.lookup")
	const key = "orders:customer-123:payment-token"
	if err := client.Get(ctx, key).Err(); err != redis.Nil {
		t.Fatalf("Get() error = %v, want redis.Nil", err)
	}
	parentContext := parent.SpanContext()
	parent.End()

	span := findClientSpan(recorder.Ended())
	if span == nil {
		t.Fatalf("no Redis CLIENT span in %#v", recorder.Ended())
	}
	if got, want := span.Parent().SpanID(), parentContext.SpanID(); got != want {
		t.Fatalf("Redis span parent = %s, want %s", got, want)
	}
	if got := span.Name(); got != "get" {
		t.Fatalf("Redis span name = %q, want operation only", got)
	}
	if values := strings.Join(attributeStrings(span.Attributes()), " "); strings.Contains(values, key) || strings.Contains(values, "payment-token") {
		t.Fatalf("Redis span captured key or value data: %s", values)
	}
	if attributeValue(span.Attributes(), "db.statement") != "" {
		t.Fatalf("Redis span unexpectedly has db.statement: %#v", span.Attributes())
	}
	if got := countClientSpans(recorder.Ended(), "get"); got != 1 {
		t.Fatalf("GET spans = %d, want one after idempotent instrumentation", got)
	}
}

func TestInstrumentRejectsNilClient(t *testing.T) {
	if err := Instrument(nil); err == nil || err.Error() != "sentinelredis: client is nil" {
		t.Fatalf("Instrument(nil) error = %v", err)
	}
	var client *redis.Client
	if err := Instrument(client); err == nil || err.Error() != "sentinelredis: client is nil" {
		t.Fatalf("Instrument(typed nil) error = %v", err)
	}
}

func installRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(t.Context())
	})
	return recorder
}

func findClientSpan(spans []sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	for _, span := range spans {
		if span.SpanKind() == trace.SpanKindClient && span.Name() == "get" {
			return span
		}
	}
	return nil
}

func countClientSpans(spans []sdktrace.ReadOnlySpan, name string) int {
	count := 0
	for _, span := range spans {
		if span.SpanKind() == trace.SpanKindClient && span.Name() == name {
			count++
		}
	}
	return count
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

func serveRedis(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		command, err := readRESPCommand(reader)
		if err != nil {
			if err != io.EOF {
				t.Errorf("read Redis command: %v", err)
			}
			return
		}
		switch strings.ToUpper(command[0]) {
		case "HELLO":
			_, _ = io.WriteString(conn, "-ERR unknown command 'HELLO'\r\n")
		case "GET":
			_, _ = io.WriteString(conn, "$-1\r\n")
		default:
			_, _ = io.WriteString(conn, "+OK\r\n")
		}
	}
}

func readRESPCommand(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("expected RESP array, got %q", line)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return nil, err
	}
	command := make([]string, n)
	for i := range command {
		line, err = reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(line, "$") {
			return nil, fmt.Errorf("expected RESP bulk string, got %q", line)
		}
		length, err := strconv.Atoi(strings.TrimSpace(line[1:]))
		if err != nil {
			return nil, err
		}
		value := make([]byte, length+2)
		if _, err := io.ReadFull(reader, value); err != nil {
			return nil, err
		}
		command[i] = string(value[:length])
	}
	return command, nil
}
