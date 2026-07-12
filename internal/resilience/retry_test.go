package resilience

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

// noSleep is an instant, ctx-aware sleep hook for deterministic tests.
func noSleep(ctx context.Context, _ time.Duration) error {
	return ctx.Err()
}

func newTestRetry(inner search.Provider, maxAttempts int) *RetryProvider {
	r := NewRetryProvider(inner, RetryOptions{
		MaxAttempts: maxAttempts,
		BaseDelay:   time.Millisecond,
		sleep:       noSleep,
		jitter:      func(d time.Duration) time.Duration { return d },
	})
	return r
}

func TestRetry_StopsOnBlocked(t *testing.T) {
	blocked := fmt.Errorf("ddg: %w", search.ErrBlocked)
	stub := &stubProvider{errs: []error{blocked}}
	r := newTestRetry(stub, 3)

	_, err := r.Search(context.Background(), search.Request{Query: "x"})
	if !errors.Is(err, search.ErrBlocked) {
		t.Fatalf("want ErrBlocked, got %v", err)
	}
	if got := stub.callCount(); got != 1 {
		t.Fatalf("blocked must not retry: want 1 call, got %d", got)
	}
}

func TestRetry_RetriesRateLimitedUpToMax(t *testing.T) {
	rl := fmt.Errorf("ddg: %w", search.ErrRateLimited)
	stub := &stubProvider{errs: []error{rl}} // always rate limited
	r := newTestRetry(stub, 3)

	_, err := r.Search(context.Background(), search.Request{Query: "x"})
	if !errors.Is(err, search.ErrRateLimited) {
		t.Fatalf("want last error ErrRateLimited, got %v", err)
	}
	if got := stub.callCount(); got != 3 {
		t.Fatalf("want 3 attempts, got %d", got)
	}
}

func TestRetry_SucceedsAfterTransient(t *testing.T) {
	rl := fmt.Errorf("ddg: %w", search.ErrRateLimited)
	want := search.Response{Provider: "stub", Query: "x"}
	stub := &stubProvider{
		resp: want,
		errs: []error{rl, rl, nil}, // fail twice then succeed
	}
	r := newTestRetry(stub, 5)

	got, err := r.Search(context.Background(), search.Request{Query: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Provider != want.Provider {
		t.Fatalf("want %+v, got %+v", want, got)
	}
	if c := stub.callCount(); c != 3 {
		t.Fatalf("want 3 calls, got %d", c)
	}
}

func TestRetry_GenericErrorIsRetried(t *testing.T) {
	generic := errors.New("connection reset by peer")
	stub := &stubProvider{errs: []error{generic}}
	r := newTestRetry(stub, 3)

	_, err := r.Search(context.Background(), search.Request{Query: "x"})
	if err == nil {
		t.Fatal("want error")
	}
	if c := stub.callCount(); c != 3 {
		t.Fatalf("generic errors should retry: want 3 calls, got %d", c)
	}
}

func TestRetry_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	stub := &stubProvider{errs: []error{errors.New("boom")}}
	r := newTestRetry(stub, 3)

	_, err := r.Search(ctx, search.Request{Query: "x"})
	if err == nil {
		t.Fatal("want error")
	}
	if c := stub.callCount(); c != 0 {
		t.Fatalf("cancelled ctx should not call inner: got %d calls", c)
	}
}

func TestRetry_StopsWhenCtxExpiresDuringBackoff(t *testing.T) {
	rl := fmt.Errorf("ddg: %w", search.ErrRateLimited)
	stub := &stubProvider{errs: []error{rl}}
	// sleep hook simulates ctx expiring during backoff.
	r := NewRetryProvider(stub, RetryOptions{
		MaxAttempts: 5,
		BaseDelay:   time.Millisecond,
		sleep: func(_ context.Context, _ time.Duration) error {
			return context.DeadlineExceeded
		},
	})

	_, err := r.Search(context.Background(), search.Request{Query: "x"})
	if !errors.Is(err, search.ErrRateLimited) {
		t.Fatalf("want last provider error surfaced, got %v", err)
	}
	if c := stub.callCount(); c != 1 {
		t.Fatalf("want 1 call before backoff abort, got %d", c)
	}
}

func TestRetry_MaxAttemptsOneNoRetry(t *testing.T) {
	rl := fmt.Errorf("ddg: %w", search.ErrRateLimited)
	stub := &stubProvider{errs: []error{rl}}
	r := newTestRetry(stub, 0) // clamped to 1

	_, _ = r.Search(context.Background(), search.Request{Query: "x"})
	if c := stub.callCount(); c != 1 {
		t.Fatalf("want exactly 1 call, got %d", c)
	}
}

func TestRetry_NameDelegates(t *testing.T) {
	stub := &stubProvider{name: "ddg"}
	r := newTestRetry(stub, 3)
	if r.Name() != "ddg" {
		t.Fatalf("want ddg, got %q", r.Name())
	}
}

func TestRetry_DefaultBackoffWithinRange(t *testing.T) {
	// Sanity-check the full-jitter range without injecting a hook.
	r := NewRetryProvider(&stubProvider{}, RetryOptions{MaxAttempts: 4, BaseDelay: 100 * time.Millisecond})
	for attempt := 0; attempt < 4; attempt++ {
		base := time.Duration(100*(1<<attempt)) * time.Millisecond
		d := r.backoff(attempt)
		if d < base/2 || d > base {
			t.Fatalf("attempt %d: backoff %v out of [%v, %v]", attempt, d, base/2, base)
		}
	}
}

func TestRetry_HonorsRetryAfterHint(t *testing.T) {
	rateLimited := fmt.Errorf("mojeek: %w", &search.RateLimitedError{RetryAfter: 7 * time.Second})
	stub := &stubProvider{errs: []error{rateLimited, nil}}

	var slept []time.Duration
	r := NewRetryProvider(stub, RetryOptions{
		MaxAttempts: 3,
		BaseDelay:   time.Millisecond,
		sleep: func(ctx context.Context, d time.Duration) error {
			slept = append(slept, d)
			return ctx.Err()
		},
		jitter: func(d time.Duration) time.Duration { return d },
	})

	if _, err := r.Search(context.Background(), search.Request{Query: "x"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(slept) != 1 || slept[0] != 7*time.Second {
		t.Fatalf("slept = %v, want [7s] from Retry-After hint", slept)
	}
}

func TestRetry_CapsHostileRetryAfter(t *testing.T) {
	rateLimited := fmt.Errorf("mojeek: %w", &search.RateLimitedError{RetryAfter: time.Hour})
	stub := &stubProvider{errs: []error{rateLimited, nil}}

	var slept []time.Duration
	r := NewRetryProvider(stub, RetryOptions{
		MaxAttempts: 2,
		BaseDelay:   time.Millisecond,
		sleep: func(ctx context.Context, d time.Duration) error {
			slept = append(slept, d)
			return ctx.Err()
		},
		jitter: func(d time.Duration) time.Duration { return d },
	})

	if _, err := r.Search(context.Background(), search.Request{Query: "x"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(slept) != 1 || slept[0] != maxRetryAfterWait {
		t.Fatalf("slept = %v, want capped %s", slept, maxRetryAfterWait)
	}
}
