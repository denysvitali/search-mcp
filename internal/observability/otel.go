package observability

import (
	"context"
	"errors"
	"io"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

type Config struct {
	Enabled     bool
	ServiceName string
	Exporter    string
	Writer      io.Writer
}

func Setup(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = "search-mcp"
	}
	if cfg.Writer == nil {
		cfg.Writer = io.Discard
	}
	if cfg.Exporter == "" {
		cfg.Exporter = "stdout"
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
	))
	if err != nil {
		return nil, err
	}

	traceExporter, err := newTraceExporter(ctx, cfg)
	if err != nil {
		return nil, err
	}
	tracerProvider := trace.NewTracerProvider(trace.WithBatcher(traceExporter), trace.WithResource(res))
	otel.SetTracerProvider(tracerProvider)

	metricExporter, err := newMetricExporter(ctx, cfg)
	if err != nil {
		return nil, err
	}
	meterProvider := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExporter, metric.WithInterval(30*time.Second))),
		metric.WithResource(res),
	)
	otel.SetMeterProvider(meterProvider)

	return func(ctx context.Context) error {
		return errors.Join(tracerProvider.Shutdown(ctx), meterProvider.Shutdown(ctx))
	}, nil
}

func newTraceExporter(ctx context.Context, cfg Config) (trace.SpanExporter, error) {
	if cfg.Exporter == "otlp" {
		return otlptracehttp.New(ctx)
	}
	return stdouttrace.New(stdouttrace.WithWriter(cfg.Writer))
}

func newMetricExporter(ctx context.Context, cfg Config) (metric.Exporter, error) {
	if cfg.Exporter == "otlp" {
		return otlpmetrichttp.New(ctx)
	}
	return stdoutmetric.New(stdoutmetric.WithWriter(cfg.Writer))
}
