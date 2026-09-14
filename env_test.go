package sentinel

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{
		"SENTINEL_ENDPOINT":           " https://ingest.sentinel.unkcode.com ",
		"SENTINEL_INGESTION_TOKEN":    " sip_test ",
		"OTEL_SERVICE_NAME":           " orders ",
		"OTEL_SERVICE_VERSION":        " v1 ",
		"OTEL_DEPLOYMENT_ENVIRONMENT": " production ",
		"SENTINEL_TRACE_SAMPLE_RATIO": "0.25",
		"SENTINEL_METRIC_INTERVAL":    "45s",
		"SENTINEL_EXPORT_TIMEOUT":     "7s",
		"SENTINEL_INSECURE":           "false",
	}
	c, err := configFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("configFromEnv() error = %v", err)
	}
	c, err = normalizeConfig(c)
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	if *c.TraceSampleRatio != 0.25 || c.MetricInterval != 45*time.Second || c.ExportTimeout != 7*time.Second {
		t.Fatalf("unexpected optional env config: %#v", c)
	}
}

func TestConfigFromEnvInvalidValues(t *testing.T) {
	for _, key := range []string{"SENTINEL_TRACE_SAMPLE_RATIO", "SENTINEL_METRIC_INTERVAL", "SENTINEL_EXPORT_TIMEOUT", "SENTINEL_INSECURE"} {
		t.Run(key, func(t *testing.T) {
			_, err := configFromEnv(func(name string) string {
				if name == key {
					return "not-valid"
				}
				return ""
			})
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("configFromEnv() error = %v, want error naming %s", err, key)
			}
		})
	}
}

func TestNewFromEnvUsesNormalInitializationPath(t *testing.T) {
	server, _ := newOTLPReceiver(t)
	defer server.Close()
	t.Setenv("SENTINEL_ENDPOINT", server.URL)
	t.Setenv("SENTINEL_INGESTION_TOKEN", "sip_test_token")
	t.Setenv("OTEL_SERVICE_NAME", "from-env")
	t.Setenv("OTEL_SERVICE_VERSION", "test")
	t.Setenv("OTEL_DEPLOYMENT_ENVIRONMENT", "test")
	t.Setenv("SENTINEL_INSECURE", "true")

	obs, err := NewFromEnv(context.Background())
	if err != nil {
		t.Fatalf("NewFromEnv() error = %v", err)
	}
	if err := obs.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}
