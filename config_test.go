package sentinel

import (
	"strings"
	"testing"
	"time"
)

func ratio(v float64) *float64 { return &v }

func validConfig() Config {
	return Config{
		Endpoint:       "https://ingest.sentinel.unkcode.com",
		Token:          "sip_test_token",
		ServiceName:    "checkout",
		ServiceVersion: "1.2.3",
		Environment:    "test",
	}
}

func TestNormalizeConfig(t *testing.T) {
	c := validConfig()
	c.Endpoint = " https://ingest.sentinel.unkcode.com/ "
	c.Token = " sip_test_token "
	c.ServiceName = " checkout "
	c.Environment = " test "
	got, err := normalizeConfig(c)
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	if got.Endpoint != "https://ingest.sentinel.unkcode.com" || got.Token != "sip_test_token" || got.ServiceName != "checkout" || got.Environment != "test" {
		t.Fatalf("unexpected normalized config: %#v", got)
	}
	if got.TraceSampleRatio == nil || *got.TraceSampleRatio != 1 {
		t.Fatalf("default trace sample ratio = %v, want 1", got.TraceSampleRatio)
	}
	if got.MetricInterval != defaultMetricInterval || got.ExportTimeout != defaultExportTimeout {
		t.Fatalf("unexpected defaults: metric interval %s, export timeout %s", got.MetricInterval, got.ExportTimeout)
	}
}

func TestNormalizeConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
	}{
		{"missing endpoint", func(c *Config) { c.Endpoint = "" }},
		{"invalid endpoint", func(c *Config) { c.Endpoint = "not a URL" }},
		{"endpoint path", func(c *Config) { c.Endpoint = "https://example.test/other" }},
		{"http not explicitly insecure", func(c *Config) { c.Endpoint = "http://example.test" }},
		{"insecure HTTPS endpoint", func(c *Config) { c.Insecure = true }},
		{"missing token", func(c *Config) { c.Token = "" }},
		{"missing service name", func(c *Config) { c.ServiceName = "  " }},
		{"missing environment", func(c *Config) { c.Environment = "  " }},
		{"sampling too high", func(c *Config) { c.TraceSampleRatio = ratio(1.01) }},
		{"sampling negative", func(c *Config) { c.TraceSampleRatio = ratio(-0.01) }},
		{"negative metric interval", func(c *Config) { c.MetricInterval = -time.Second }},
		{"negative export timeout", func(c *Config) { c.ExportTimeout = -time.Second }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.edit(&c)
			if _, err := normalizeConfig(c); err == nil {
				t.Fatal("normalizeConfig() error = nil")
			}
		})
	}
}

func TestConfigErrorDoesNotExposeToken(t *testing.T) {
	c := validConfig()
	c.Endpoint = "%%%"
	c.Token = "sip_secret_that_must_not_appear"
	_, err := normalizeConfig(c)
	if err == nil {
		t.Fatal("normalizeConfig() error = nil")
	}
	assertNotContainsSecret(t, err, c.Token)
}

func TestNormalizeConfigAcceptsOTLPBaseEndpoints(t *testing.T) {
	for _, endpoint := range []struct {
		endpoint string
		insecure bool
	}{
		{"https://example.test", false},
		{"https://example.test:8443", false},
		{"http://127.0.0.1:4318", true},
		{"https://[2001:db8::1]:4318", false},
	} {
		c := validConfig()
		c.Endpoint = endpoint.endpoint
		c.Insecure = endpoint.insecure
		got, err := normalizeConfig(c)
		if err != nil {
			t.Fatalf("normalizeConfig(%q) error = %v", endpoint.endpoint, err)
		}
		if got.Endpoint != endpoint.endpoint {
			t.Fatalf("normalized endpoint = %q, want %q", got.Endpoint, endpoint.endpoint)
		}
	}
}

func assertNotContainsSecret(t *testing.T, err error, secret string) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposes secret: %v", err)
	}
}
