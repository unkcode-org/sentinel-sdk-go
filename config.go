package sentinel

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	defaultMetricInterval = 30 * time.Second
	defaultExportTimeout  = 10 * time.Second
)

// Config configures the OpenTelemetry SDK installed by Sentinel.
//
// Endpoint is the OTLP/HTTP base endpoint, for example
// https://ingest.sentinel.unkcode.com. The SDK appends the standard OTLP signal
// paths (/v1/traces and /v1/metrics).
type Config struct {
	Endpoint       string
	Token          string
	ServiceName    string
	ServiceVersion string
	Environment    string

	// Insecure permits an http:// endpoint. HTTPS is required otherwise.
	Insecure bool

	// TraceSampleRatio is the root trace sampling ratio. Nil uses the production
	// default of 1.0. A non-nil value of 0 drops new root traces.
	TraceSampleRatio *float64
	MetricInterval   time.Duration
	ExportTimeout    time.Duration
}

func normalizeConfig(c Config) (Config, error) {
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Token = strings.TrimSpace(c.Token)
	c.ServiceName = strings.TrimSpace(c.ServiceName)
	c.ServiceVersion = strings.TrimSpace(c.ServiceVersion)
	c.Environment = strings.TrimSpace(c.Environment)

	if c.Endpoint == "" {
		return Config{}, fmt.Errorf("sentinel: endpoint is required")
	}
	u, err := url.ParseRequestURI(c.Endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Config{}, fmt.Errorf("sentinel: endpoint must be an absolute HTTP(S) URL")
	}
	if u.Path != "" && u.Path != "/" {
		return Config{}, fmt.Errorf("sentinel: endpoint must be an OTLP base URL without a path")
	}
	switch u.Scheme {
	case "https":
		if c.Insecure {
			return Config{}, fmt.Errorf("sentinel: Insecure can only be used with an http endpoint")
		}
	case "http":
		if !c.Insecure {
			return Config{}, fmt.Errorf("sentinel: http endpoint requires Insecure to be true")
		}
	default:
		return Config{}, fmt.Errorf("sentinel: endpoint scheme must be https or http")
	}
	if c.Token == "" {
		return Config{}, fmt.Errorf("sentinel: ingestion token is required")
	}
	if c.ServiceName == "" {
		return Config{}, fmt.Errorf("sentinel: service name is required")
	}
	if c.Environment == "" {
		return Config{}, fmt.Errorf("sentinel: deployment environment is required")
	}
	if c.TraceSampleRatio == nil {
		defaultRatio := 1.0
		c.TraceSampleRatio = &defaultRatio
	}
	if *c.TraceSampleRatio < 0 || *c.TraceSampleRatio > 1 {
		return Config{}, fmt.Errorf("sentinel: trace sample ratio must be between 0 and 1")
	}
	if c.MetricInterval == 0 {
		c.MetricInterval = defaultMetricInterval
	}
	if c.MetricInterval <= 0 {
		return Config{}, fmt.Errorf("sentinel: metric interval must be positive")
	}
	if c.ExportTimeout == 0 {
		c.ExportTimeout = defaultExportTimeout
	}
	if c.ExportTimeout <= 0 {
		return Config{}, fmt.Errorf("sentinel: export timeout must be positive")
	}

	// URL parsing normalizes no semantic fields, but String removes accidental
	// whitespace while preserving the explicit protocol selected by the caller.
	c.Endpoint = strings.TrimRight(u.String(), "/")
	return c, nil
}
