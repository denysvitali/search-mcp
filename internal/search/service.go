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
	// providers maps a provider name to its implementation.
	providers map[string]Provider
	// limiters holds one rate limiter per provider name.
	limiters map[string]*rate.Limiter
	// logger is used for diagnostics and non-fatal warnings.
	logger logrus.FieldLogger
	// tracer creates spans for search operations.
	tracer trace.Tracer
	// requests counts search requests, labelled by provider and status. May be nil if instrument creation failed.
	requests metric.Int64Counter
	// latency records search request durations in milliseconds. May be nil if instrument creation failed.
	latency metric.Float64Histogram
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
	requests, err := meter.Int64Counter("search_requests_total")
	if err != nil {
		logger.WithError(err).Warn("failed to create search_requests_total counter; request metrics disabled")
		requests = nil
	}
	latency, err := meter.Float64Histogram("search_request_duration_ms")
	if err != nil {
		logger.WithError(err).Warn("failed to create search_request_duration_ms histogram; latency metrics disabled")
		latency = nil
	}

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
		names := s.ProviderNames()
		if len(names) == 0 {
			return Response{}, errors.New("no providers configured")
		}
		req.Provider = names[0]
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
		s.countRequest(ctx, req.Provider, "rate_limited")
		return Response{}, err
	}

	resp, err := provider.Search(ctx, req)
	status := "ok"
	if err != nil {
		status = "error"
		span.RecordError(err)
	}

	s.countRequest(ctx, req.Provider, status)
	if s.latency != nil {
		s.latency.Record(ctx, float64(time.Since(start).Milliseconds()), metric.WithAttributes(attrs...))
	}
	return resp, err
}

// countRequest increments the request counter if it is available.
func (s *Service) countRequest(ctx context.Context, provider, status string) {
	if s.requests == nil {
		return
	}
	s.requests.Add(ctx, 1, metric.WithAttributes(
		attribute.String("search.provider", provider),
		attribute.String("search.status", status),
	))
}
