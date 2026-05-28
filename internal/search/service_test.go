package search

import (
	"context"
	"errors"
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
