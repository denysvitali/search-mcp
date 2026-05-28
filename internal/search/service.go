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

	primary := req.Provider
	if _, ok := s.providers[primary]; !ok {
		return Response{}, fmt.Errorf("unknown provider %q", primary)
	}

	span.SetAttributes(
		attribute.String("search.provider.requested", primary),
		attribute.Int("search.count", req.Count),
	)

	// Build the deterministic attempt order: the primary provider first, then the
	// remaining providers sorted by name. Fan-out only kicks in when a provider
	// returns a fallback-worthy error (rate limited / blocked).
	order := []string{primary}
	for _, name := range s.ProviderNames() {
		if name != primary {
			order = append(order, name)
		}
	}

	var lastErr error
	for i, name := range order {
		// Respect context cancellation/deadlines before each attempt; do not fall
		// back when the caller has already given up.
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return Response{}, lastErr
			}
			return Response{}, err
		}

		attempt := req
		attempt.Provider = name
		resp, err := s.searchOne(ctx, span, attempt)
		if err == nil {
			if i > 0 {
				// We only reach here past the primary when a fallback succeeded.
				span.SetAttributes(
					attribute.Bool("search.fallback", true),
					attribute.String("search.fallback.served_by", name),
				)
				span.AddEvent("search.fallback", trace.WithAttributes(
					attribute.String("search.fallback.from", primary),
					attribute.String("search.fallback.to", name),
				))
			}
			resp.Provider = name
			return resp, nil
		}

		lastErr = err

		// Only fall back on transient/blocked failures. Context errors and any
		// other error return immediately.
		if ctx.Err() != nil {
			return Response{}, err
		}
		if !isFallbackWorthy(err) {
			return Response{}, err
		}

		s.logger.WithError(err).WithFields(logrus.Fields{
			"provider": name,
		}).Warn("provider failed with fallback-worthy error; trying next provider")
	}

	if lastErr == nil {
		lastErr = errors.New("no providers attempted")
	}
	return Response{}, fmt.Errorf("all providers failed: %w", lastErr)
}

// isFallbackWorthy reports whether err is a transient/blocked failure that
// should trigger trying the next provider.
func isFallbackWorthy(err error) bool {
	return errors.Is(err, ErrRateLimited) || errors.Is(err, ErrBlocked)
}

// searchOne applies the provider's rate limiter and performs a single provider
// search, recording per-attempt metrics and tracing. req.Provider selects the
// provider and must already be validated by the caller.
func (s *Service) searchOne(ctx context.Context, span trace.Span, req Request) (Response, error) {
	provider := s.providers[req.Provider]

	attrs := []attribute.KeyValue{
		attribute.String("search.provider", req.Provider),
		attribute.Int("search.count", req.Count),
	}

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
