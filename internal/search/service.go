package search

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"
)

type Service struct {
	providers map[string]Provider
	limiters  map[string]*rate.Limiter
	logger    logrus.FieldLogger
	tracer    trace.Tracer
	requests  metric.Int64Counter
	latency   metric.Float64Histogram
}

func NewService(providers []Provider, requestsPerSecond float64, burst int, logger logrus.FieldLogger) (*Service, error) {
	if len(providers) == 0 {
		return nil, errors.New("at least one provider is required")
	}
	if requestsPerSecond <= 0 {
		requestsPerSecond = 1
	}
	if burst <= 0 {
		burst = 1
	}

	meter := otel.Meter("search-mcp/search")
	requests, _ := meter.Int64Counter("search_requests_total")
	latency, _ := meter.Float64Histogram("search_request_duration_ms")

	s := &Service{
		providers: make(map[string]Provider, len(providers)),
		limiters:  make(map[string]*rate.Limiter, len(providers)),
		logger:    logger,
		tracer:    otel.Tracer("search-mcp/search"),
		requests:  requests,
		latency:   latency,
	}

	for _, provider := range providers {
		name := provider.Name()
		s.providers[name] = provider
		s.limiters[name] = rate.NewLimiter(rate.Limit(requestsPerSecond), burst)
	}

	return s, nil
}

func (s *Service) ProviderNames() []string {
	names := make([]string, 0, len(s.providers))
	for name := range s.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *Service) Search(ctx context.Context, req Request) (Response, error) {
	ctx, span := s.tracer.Start(ctx, "search")
	defer span.End()

	if req.Query == "" {
		return Response{}, errors.New("query is required")
	}
	if req.Count <= 0 {
		req.Count = 10
	}
	if req.Provider == "" {
		req.Provider = s.ProviderNames()[0]
	}

	provider, ok := s.providers[req.Provider]
	if !ok {
		return Response{}, fmt.Errorf("unknown provider %q", req.Provider)
	}

	attrs := []attribute.KeyValue{
		attribute.String("search.provider", req.Provider),
		attribute.Int("search.count", req.Count),
	}
	span.SetAttributes(attrs...)

	start := time.Now()
	if err := s.limiters[req.Provider].Wait(ctx); err != nil {
		s.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("search.provider", req.Provider), attribute.String("search.status", "rate_limited")))
		return Response{}, err
	}

	resp, err := provider.Search(ctx, req)
	status := "ok"
	if err != nil {
		status = "error"
		span.RecordError(err)
	}

	s.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("search.provider", req.Provider), attribute.String("search.status", status)))
	s.latency.Record(ctx, float64(time.Since(start).Milliseconds()), metric.WithAttributes(attrs...))
	return resp, err
}
