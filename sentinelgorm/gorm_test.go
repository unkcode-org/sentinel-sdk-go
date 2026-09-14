package sentinelgorm

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestInstrumentExcludesBoundQueryValues(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open GORM: %v", err)
	}
	if err := Instrument(db); err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}
	const secret = "not-for-telemetry"
	var got string
	if err := db.WithContext(context.Background()).Raw("SELECT ?", secret).Scan(&got).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != secret {
		t.Fatalf("query result = %q", got)
	}

	spans := recorder.Ended()
	if len(spans) == 0 {
		t.Fatal("expected a GORM span")
	}
	for _, attr := range spans[0].Attributes() {
		if strings.Contains(attr.Value.AsString(), secret) {
			t.Fatalf("GORM span captured bound value in %s", attr.Key)
		}
	}
}

func TestInstrumentRejectsNilDB(t *testing.T) {
	if err := Instrument(nil); err == nil {
		t.Fatal("Instrument(nil) error = nil")
	}
}

func TestInstrumentPreservesContextTrace(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open GORM: %v", err)
	}
	if err := Instrument(db); err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}

	ctx, parent := otel.Tracer("test/gorm").Start(context.Background(), "request")
	var value int
	if err := db.WithContext(ctx).Raw("SELECT 1").Scan(&value).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	parentContext := parent.SpanContext()
	parent.End()

	for _, span := range recorder.Ended() {
		if span.Parent().SpanID() == parentContext.SpanID() {
			return
		}
	}
	t.Fatalf("no GORM query span recorded: %#v", recorder.Ended())
}
