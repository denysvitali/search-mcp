package resilience

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

// fakeClock is an injectable, advanceable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestBreaker_OpensAfterThreshold(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	boom := errors.New("boom")
	stub := &stubProvider{errs: []error{boom}}
	b := NewCircuitBreaker(stub, BreakerOptions{Threshold: 3, Cooldown: time.Minute, now: clk.now})

	// 3 failures to trip.
	for i := 0; i < 3; i++ {
		if _, err := b.Search(context.Background(), search.Request{}); err == nil {
			t.Fatalf("call %d: want error", i)
		}
	}
	if stub.callCount() != 3 {
		t.Fatalf("want 3 inner calls, got %d", stub.callCount())
	}

	// Now open: should fast-fail with ErrCircuitOpen, no inner call.
	_, err := b.Search(context.Background(), search.Request{})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("want ErrCircuitOpen, got %v", err)
	}
	if stub.callCount() != 3 {
		t.Fatalf("open breaker must not call inner: got %d", stub.callCount())
	}
}

func TestBreaker_HalfOpenRecoversOnSuccess(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	boom := errors.New("boom")
	// Fail twice (trips at threshold 2), then succeed forever.
	stub := &stubProvider{
		resp: search.Response{Provider: "stub"},
		errs: []error{boom, boom, nil},
	}
	b := NewCircuitBreaker(stub, BreakerOptions{Threshold: 2, Cooldown: 30 * time.Second, now: clk.now})

	for i := 0; i < 2; i++ {
		_, _ = b.Search(context.Background(), search.Request{})
	}
	// Open now.
	if _, err := b.Search(context.Background(), search.Request{}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("want open, got %v", err)
	}

	// Not enough cooldown yet.
	clk.advance(10 * time.Second)
	if _, err := b.Search(context.Background(), search.Request{}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("still within cooldown: want open, got %v", err)
	}

	// Past cooldown: half-open trial call succeeds -> closed.
	clk.advance(30 * time.Second)
	if _, err := b.Search(context.Background(), search.Request{}); err != nil {
		t.Fatalf("half-open trial should succeed: %v", err)
	}
	// Subsequent calls pass through (closed).
	if _, err := b.Search(context.Background(), search.Request{}); err != nil {
		t.Fatalf("closed breaker should pass through: %v", err)
	}
}

func TestBreaker_HalfOpenReopensOnFailure(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	boom := errors.New("boom")
	stub := &stubProvider{errs: []error{boom}} // always fails
	b := NewCircuitBreaker(stub, BreakerOptions{Threshold: 1, Cooldown: 10 * time.Second, now: clk.now})

	// One failure trips it.
	_, _ = b.Search(context.Background(), search.Request{})
	if _, err := b.Search(context.Background(), search.Request{}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("want open, got %v", err)
	}

	// Cooldown elapses -> half-open trial -> fails -> re-open.
	clk.advance(10 * time.Second)
	callsBefore := stub.callCount()
	if _, err := b.Search(context.Background(), search.Request{}); errors.Is(err, ErrCircuitOpen) {
		t.Fatal("half-open should allow the trial call through")
	}
	if stub.callCount() != callsBefore+1 {
		t.Fatalf("half-open should make exactly one inner call")
	}

	// Should be open again immediately (no cooldown advance).
	if _, err := b.Search(context.Background(), search.Request{}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("trial failure should re-open: got %v", err)
	}
}

func TestBreaker_SuccessResetsFailureCount(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	boom := errors.New("boom")
	stub := &stubProvider{
		fn: func(call int, _ context.Context, _ search.Request) (search.Response, error) {
			// fail, fail, succeed, fail, fail -> never 3 consecutive.
			switch call {
			case 3:
				return search.Response{}, nil
			default:
				return search.Response{}, boom
			}
		},
	}
	b := NewCircuitBreaker(stub, BreakerOptions{Threshold: 3, Cooldown: time.Minute, now: clk.now})

	for i := 0; i < 5; i++ {
		if _, err := b.Search(context.Background(), search.Request{}); errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("call %d unexpectedly open; success should reset counter", i)
		}
	}
}

func TestBreaker_NameDelegates(t *testing.T) {
	b := NewCircuitBreaker(&stubProvider{name: "mojeek"}, BreakerOptions{})
	if b.Name() != "mojeek" {
		t.Fatalf("want mojeek, got %q", b.Name())
	}
}
