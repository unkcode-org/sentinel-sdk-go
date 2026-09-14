// Package sentinellogrus provides a thin bridge from Logrus to OpenTelemetry
// Logs. It delegates conversion to OpenTelemetry's maintained otellogrus
// bridge; it does not know about OTLP transport, Sentinel credentials, or
// batching.
package sentinellogrus

import (
	"fmt"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/bridges/otellogrus"
)

const instrumentationScope = "github.com/unkcode-org/sentinel-sdk-go/sentinellogrus"

// Instrument adds OpenTelemetry's maintained Logrus hook to logger.
//
// The hook uses the global OpenTelemetry LoggerProvider installed by sentinel.
// Existing Logrus output and hooks remain unchanged. A Logrus entry created
// with logger.WithContext(ctx) preserves the active span's native TraceID and
// SpanID in the resulting OpenTelemetry LogRecord.
//
// Logrus fields are converted by the upstream bridge to OpenTelemetry log
// attributes. Values without a supported OTel representation are converted to
// a string by that bridge; logging never panics solely because a field value is
// unsupported. Applications remain responsible for not explicitly logging
// sensitive values.
func Instrument(logger *logrus.Logger) error {
	if logger == nil {
		return fmt.Errorf("sentinellogrus: logger is nil")
	}
	logger.AddHook(otellogrus.NewHook(instrumentationScope))
	return nil
}
