package observability

import (
	"context"
	"errors"
	"fmt"
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

// defaultMetricInterval is the periodic metric export interval used when none
// is configured.
const defaultMetricInterval = 30 * time.Second

type Config struct {
	Enabled     bool
	ServiceName string
	// Exporter selects the exporter backend: "stdout" or "otlp".
	Exporter string
	// Endpoint, when set, overrides the OTLP exporter endpoint (otherwise the
	// standard OTEL_EXPORTER_OTLP_ENDPOINT env var / default is used). Ignored
	// for the stdout exporter.
	Endpoint string
	// MetricInterval overrides the periodic metric export interval. Defaults to
	// 30s when zero.
	MetricInterval time.Duration
	Writer         io.Writer
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
	if cfg.MetricInterval <= 0 {
		cfg.MetricInterval = defaultMetricInterval
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
		metric.WithReader(metric.NewPeriodicReader(metricExporter, metric.WithInterval(cfg.MetricInterval))),
		metric.WithResource(res),
	)
	otel.SetMeterProvider(meterProvider)

	return func(ctx context.Context) error {
		return errors.Join(tracerProvider.Shutdown(ctx), meterProvider.Shutdown(ctx))
	}, nil
}

func newTraceExporter(ctx context.Context, cfg Config) (trace.SpanExporter, error) {
	switch cfg.Exporter {
	case "otlp":
		opts := []otlptracehttp.Option{}
		if cfg.Endpoint != "" {
			opts = append(opts, otlptracehttp.WithEndpointURL(cfg.Endpoint))
		}
		return otlptracehttp.New(ctx, opts...)
	case "stdout":
		return stdouttrace.New(stdouttrace.WithWriter(cfg.Writer))
	default:
		return nil, fmt.Errorf("unknown otel exporter %q (want \"stdout\" or \"otlp\")", cfg.Exporter)
	}
}

func newMetricExporter(ctx context.Context, cfg Config) (metric.Exporter, error) {
	switch cfg.Exporter {
	case "otlp":
		opts := []otlpmetrichttp.Option{}
		if cfg.Endpoint != "" {
			opts = append(opts, otlpmetrichttp.WithEndpointURL(cfg.Endpoint))
		}
		return otlpmetrichttp.New(ctx, opts...)
	case "stdout":
		return stdoutmetric.New(stdoutmetric.WithWriter(cfg.Writer))
	default:
		return nil, fmt.Errorf("unknown otel exporter %q (want \"stdout\" or \"otlp\")", cfg.Exporter)
	}
}
