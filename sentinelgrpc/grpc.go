// Package sentinelgrpc provides gRPC client and server options backed by the
// official OpenTelemetry gRPC stats handlers.
package sentinelgrpc

import (
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
)

// ClientOption returns a composable gRPC client option that creates standard
// OpenTelemetry CLIENT spans, propagates the configured W3C context, records
// RPC status and duration, and emits supported standard RPC metrics.
//
// It uses the process-wide OpenTelemetry providers configured by sentinel.
// The upstream handler does not capture protobuf request or response bodies,
// and Sentinel does not enable message events or metadata attributes.
func ClientOption() grpc.DialOption {
	return grpc.WithStatsHandler(otelgrpc.NewClientHandler())
}

// ServerOption returns a composable gRPC server option that extracts the
// configured W3C context and creates standard OpenTelemetry SERVER spans and
// metrics. RPC service, method, status, errors, and duration are recorded by
// the official handler without recording protobuf bodies or metadata.
func ServerOption() grpc.ServerOption {
	return grpc.StatsHandler(otelgrpc.NewServerHandler())
}

// NewClient creates a modern lazy gRPC client connection instrumented with
// ClientOption. All standard grpc.DialOption values remain available and are
// passed directly to grpc.NewClient.
func NewClient(target string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	options := make([]grpc.DialOption, 0, len(opts)+1)
	options = append(options, ClientOption())
	options = append(options, opts...)
	return grpc.NewClient(target, options...)
}
