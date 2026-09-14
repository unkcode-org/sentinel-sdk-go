// Package sentinelgorm installs GORM's maintained OpenTelemetry tracing
// plugin with conservative data-safety defaults.
package sentinelgorm

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/plugin/opentelemetry/tracing"
)

// Instrument installs GORM's official OpenTelemetry plugin. Bound SQL values
// are intentionally excluded. Pass request contexts to GORM with db.WithContext
// to preserve trace context.
func Instrument(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("sentinelgorm: db is nil")
	}
	return db.Use(tracing.NewPlugin(tracing.WithoutQueryVariables()))
}
