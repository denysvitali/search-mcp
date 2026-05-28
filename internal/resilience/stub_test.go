package resilience

import (
	"context"
	"sync"

	"github.com/denysvitali/search-mcp/internal/search"
)

// stubProvider is a configurable search.Provider for tests. It records call
// counts and returns scripted results/errors.
type stubProvider struct {
	mu sync.Mutex

	name  string
	calls int

	// resp is returned on a successful call.
	resp search.Response

	// errs, if non-empty, is consumed one error per call (nil means success).
	// Once exhausted, the final element is repeated. A single nil/empty errs
	// means always succeed.
	errs []error

	// fn, if set, fully overrides behaviour and receives the (1-based) call
	// number.
	fn func(call int, ctx context.Context, req search.Request) (search.Response, error)
}

func (s *stubProvider) Name() string {
	if s.name == "" {
		return "stub"
	}
	return s.name
}

func (s *stubProvider) Search(ctx context.Context, req search.Request) (search.Response, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	fn := s.fn
	errs := s.errs
	resp := s.resp
	s.mu.Unlock()

	if fn != nil {
		return fn(call, ctx, req)
	}

	var err error
	if len(errs) > 0 {
		idx := call - 1
		if idx >= len(errs) {
			idx = len(errs) - 1
		}
		err = errs[idx]
	}
	if err != nil {
		return search.Response{}, err
	}
	return resp, nil
}

func (s *stubProvider) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
