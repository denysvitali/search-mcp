package resilience

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// resilienceMetrics lazily creates the decorator instruments. Instrument
// creation failures leave the fields nil and metrics are silently skipped,
// matching the search service's graceful degradation.
type resilienceMetrics struct {
	once sync.Once
	// cacheEvents counts result-cache lookups, labelled by provider and
	// event=hit|miss.
	cacheEvents   metric.Int64Counter
	cacheHitRatio metric.Float64ObservableGauge
	cacheStats    sync.Map
	// breakerTransitions counts circuit-breaker state changes, labelled by
	// provider and the target state.
	breakerTransitions metric.Int64Counter
	// breakerState reports the current circuit-breaker state for each provider:
	// closed=0, open=1, and half-open=2.
	breakerState metric.Int64ObservableGauge
	states       sync.Map
}

type cacheStats struct {
	mu           sync.Mutex
	hits, misses int64
}

var metrics resilienceMetrics

func (m *resilienceMetrics) init() {
	m.once.Do(func() {
		meter := otel.Meter("search-mcp/resilience")
		if counter, err := meter.Int64Counter("search_cache_events_total"); err == nil {
			m.cacheEvents = counter
		}
		if gauge, err := meter.Float64ObservableGauge("search_cache_hit_ratio"); err == nil {
			m.cacheHitRatio = gauge
			_, _ = meter.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
				m.cacheStats.Range(func(provider, value any) bool {
					stats := value.(*cacheStats)
					stats.mu.Lock()
					total := stats.hits + stats.misses
					ratio := 0.0
					if total > 0 {
						ratio = float64(stats.hits) / float64(total)
					}
					stats.mu.Unlock()
					observer.ObserveFloat64(gauge, ratio, metric.WithAttributes(attribute.String("search.provider", provider.(string))))
					return true
				})
				return nil
			}, gauge)
		}
		if counter, err := meter.Int64Counter("search_breaker_transitions_total"); err == nil {
			m.breakerTransitions = counter
		}
		if gauge, err := meter.Int64ObservableGauge("search_breaker_state"); err == nil {
			m.breakerState = gauge
			_, _ = meter.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
				m.states.Range(func(provider, state any) bool {
					observer.ObserveInt64(gauge, state.(int64), metric.WithAttributes(
						attribute.String("search.provider", provider.(string)),
					))
					return true
				})
				return nil
			}, gauge)
		}
	})
}

// recordCacheEvent counts a cache hit or miss for a provider.
func recordCacheEvent(ctx context.Context, provider, event string) {
	metrics.init()
	value, _ := metrics.cacheStats.LoadOrStore(provider, &cacheStats{})
	stats := value.(*cacheStats)
	stats.mu.Lock()
	if event == "hit" {
		stats.hits++
	} else {
		stats.misses++
	}
	stats.mu.Unlock()
	if metrics.cacheEvents == nil {
		return
	}
	metrics.cacheEvents.Add(ctx, 1, metric.WithAttributes(
		attribute.String("search.provider", provider),
		attribute.String("cache.event", event),
	))
}

// recordBreakerTransition counts a breaker state change for a provider.
func recordBreakerTransition(provider, toState string) {
	recordBreakerState(provider, toState)
	metrics.init()
	if metrics.breakerTransitions == nil {
		return
	}
	metrics.breakerTransitions.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("search.provider", provider),
		attribute.String("breaker.to", toState),
	))
}

// recordBreakerState updates the current state reported for a provider.
func recordBreakerState(provider, state string) {
	metrics.init()
	var value int64
	switch state {
	case "open":
		value = 1
	case "half-open":
		value = 2
	}
	metrics.states.Store(provider, value)
}
