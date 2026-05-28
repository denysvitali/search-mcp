package search

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

type stubProvider struct {
	name string
	// err, when set, is returned from Search.
	err error
	// lastReq captures the request the provider last received.
	lastReq *Request
}

func (s *stubProvider) Name() string {
	return s.name
}

func (s *stubProvider) Search(ctx context.Context, req Request) (Response, error) {
	s.lastReq = &req
	if s.err != nil {
		return Response{}, s.err
	}
	return Response{
		Query:    req.Query,
		Provider: s.name,
		Results: []Result{{
			Title:  "result",
			URL:    "https://example.com",
			Source: s.name,
		}},
	}, nil
}

func TestServiceSearchUsesRequestedProvider(t *testing.T) {
	service, err := NewService([]Provider{&stubProvider{name: "first"}, &stubProvider{name: "second"}}, 100, 1, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	resp, err := service.Search(context.Background(), Request{Query: "test", Provider: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "second" {
		t.Fatalf("provider = %q, want second", resp.Provider)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(resp.Results))
	}
}

func TestServiceRejectsUnknownProvider(t *testing.T) {
	service, err := NewService([]Provider{&stubProvider{name: "first"}}, 100, 1, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Search(context.Background(), Request{Query: "test", Provider: "missing"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestServicePropagatesProviderError(t *testing.T) {
	wantErr := errors.New("provider boom")
	service, err := NewService([]Provider{&stubProvider{name: "first", err: wantErr}}, 100, 1, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Search(context.Background(), Request{Query: "test"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestServiceDefaultsCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   int
	}{
		{"zero", 0},
		{"negative", -5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &stubProvider{name: "first"}
			service, err := NewService([]Provider{provider}, 100, 1, logrus.New())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Search(context.Background(), Request{Query: "test", Count: tc.in}); err != nil {
				t.Fatal(err)
			}
			if provider.lastReq == nil {
				t.Fatal("provider was not called")
			}
			if provider.lastReq.Count != 10 {
				t.Fatalf("count = %d, want default 10", provider.lastReq.Count)
			}
		})
	}
}

func TestServiceCountPreservedWhenPositive(t *testing.T) {
	provider := &stubProvider{name: "first"}
	service, err := NewService([]Provider{provider}, 100, 1, logrus.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Search(context.Background(), Request{Query: "test", Count: 3}); err != nil {
		t.Fatal(err)
	}
	if provider.lastReq.Count != 3 {
		t.Fatalf("count = %d, want 3", provider.lastReq.Count)
	}
}

func TestServiceRateLimits(t *testing.T) {
	// rps=1, burst=1 means the second request must wait roughly 1s.
	provider := &stubProvider{name: "first"}
	service, err := NewService([]Provider{provider}, 1, 1, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Search(context.Background(), Request{Query: "first"}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if _, err := service.Search(context.Background(), Request{Query: "second"}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Fatalf("second request waited %v, expected to be throttled", elapsed)
	}
}

func TestServiceFallsBackOnRateLimited(t *testing.T) {
	for _, sentinel := range []error{ErrRateLimited, ErrBlocked} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			// "alpha" is the primary (first alphabetically) and fails; "beta" succeeds.
			primary := &stubProvider{name: "alpha", err: fmt.Errorf("wrapped: %w", sentinel)}
			secondary := &stubProvider{name: "beta"}
			service, err := NewService([]Provider{primary, secondary}, 100, 1, logrus.New())
			if err != nil {
				t.Fatal(err)
			}

			resp, err := service.Search(context.Background(), Request{Query: "test"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Provider != "beta" {
				t.Fatalf("provider = %q, want beta", resp.Provider)
			}
			if primary.lastReq == nil || secondary.lastReq == nil {
				t.Fatal("both providers should have been called")
			}
		})
	}
}

func TestServiceNoFallbackOnNonFallbackError(t *testing.T) {
	wantErr := errors.New("query rejected")
	primary := &stubProvider{name: "alpha", err: wantErr}
	secondary := &stubProvider{name: "beta"}
	service, err := NewService([]Provider{primary, secondary}, 100, 1, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Search(context.Background(), Request{Query: "test"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if secondary.lastReq != nil {
		t.Fatal("secondary should not have been called on a non-fallback error")
	}
}

func TestServiceNoFallbackOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	primary := &stubProvider{name: "alpha", err: fmt.Errorf("wrapped: %w", ErrRateLimited)}
	secondary := &stubProvider{name: "beta"}
	service, err := NewService([]Provider{primary, secondary}, 100, 1, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Search(ctx, Request{Query: "test"})
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
	if secondary.lastReq != nil {
		t.Fatal("secondary should not be tried when context is cancelled")
	}
}

func TestServiceAllProvidersFailReturnsSentinel(t *testing.T) {
	primary := &stubProvider{name: "alpha", err: fmt.Errorf("a: %w", ErrRateLimited)}
	secondary := &stubProvider{name: "beta", err: fmt.Errorf("b: %w", ErrBlocked)}
	service, err := NewService([]Provider{primary, secondary}, 100, 1, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Search(context.Background(), Request{Query: "test"})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	// The last error (beta's ErrBlocked) must remain inspectable through wrapping.
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want errors.Is ErrBlocked", err)
	}
	if primary.lastReq == nil || secondary.lastReq == nil {
		t.Fatal("all providers should have been attempted")
	}
}

func TestServiceFallbackDeterministicOrder(t *testing.T) {
	// Primary "mid" fails; remaining providers are tried in sorted order, so
	// "aaa" must be tried before "zzz". "aaa" succeeds.
	primary := &stubProvider{name: "mid", err: fmt.Errorf("w: %w", ErrRateLimited)}
	first := &stubProvider{name: "aaa"}
	last := &stubProvider{name: "zzz"}
	service, err := NewService([]Provider{primary, first, last}, 100, 1, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	resp, err := service.Search(context.Background(), Request{Query: "test", Provider: "mid"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Provider != "aaa" {
		t.Fatalf("provider = %q, want aaa (first in sorted fallback order)", resp.Provider)
	}
	if last.lastReq != nil {
		t.Fatal("zzz should not have been tried once aaa succeeded")
	}
}

func TestServiceRespectsContextCancellation(t *testing.T) {
	// rps=1, burst=1: the limiter forces the second call to block on Wait, where
	// the cancelled context must surface as an error.
	provider := &stubProvider{name: "first"}
	service, err := NewService([]Provider{provider}, 1, 1, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Search(context.Background(), Request{Query: "warmup"}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = service.Search(ctx, Request{Query: "cancelled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
