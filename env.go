package sentinel

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// NewFromEnv initializes Sentinel from environment variables.
//
// Required variables are SENTINEL_ENDPOINT, SENTINEL_INGESTION_TOKEN,
// OTEL_SERVICE_NAME, and OTEL_DEPLOYMENT_ENVIRONMENT. OTEL_SERVICE_VERSION,
// SENTINEL_TRACE_SAMPLE_RATIO, SENTINEL_METRIC_INTERVAL,
// SENTINEL_EXPORT_TIMEOUT, and SENTINEL_INSECURE are optional.
func NewFromEnv(ctx context.Context) (*SDK, error) {
	c, err := configFromEnv(os.Getenv)
	if err != nil {
		return nil, err
	}
	return New(ctx, c)
}

func configFromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		Endpoint:       getenv("SENTINEL_ENDPOINT"),
		Token:          getenv("SENTINEL_INGESTION_TOKEN"),
		ServiceName:    getenv("OTEL_SERVICE_NAME"),
		ServiceVersion: getenv("OTEL_SERVICE_VERSION"),
		Environment:    getenv("OTEL_DEPLOYMENT_ENVIRONMENT"),
	}

	if raw := strings.TrimSpace(getenv("SENTINEL_TRACE_SAMPLE_RATIO")); raw != "" {
		ratio, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return Config{}, fmt.Errorf("sentinel: parse SENTINEL_TRACE_SAMPLE_RATIO: %w", err)
		}
		c.TraceSampleRatio = &ratio
	}
	if raw := strings.TrimSpace(getenv("SENTINEL_METRIC_INTERVAL")); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("sentinel: parse SENTINEL_METRIC_INTERVAL: %w", err)
		}
		c.MetricInterval = interval
	}
	if raw := strings.TrimSpace(getenv("SENTINEL_EXPORT_TIMEOUT")); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("sentinel: parse SENTINEL_EXPORT_TIMEOUT: %w", err)
		}
		c.ExportTimeout = timeout
	}
	if raw := strings.TrimSpace(getenv("SENTINEL_INSECURE")); raw != "" {
		insecure, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("sentinel: parse SENTINEL_INSECURE: %w", err)
		}
		c.Insecure = insecure
	}

	return c, nil
}
