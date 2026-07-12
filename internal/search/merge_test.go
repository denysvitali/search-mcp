package search

import (
	"context"
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
)

type fanoutStub struct {
	name    string
	results []Result
	err     error
}

func (p *fanoutStub) Name() string { return p.name }
func (p *fanoutStub) Search(_ context.Context, req Request) (Response, error) {
	if p.err != nil {
		return Response{}, p.err
	}
	return Response{Query: req.Query, Provider: p.name, Results: p.results}, nil
}

func newAllService(t *testing.T, providers ...Provider) *Service {
	t.Helper()
	svc, err := NewService(providers, 100, 100, logrus.New())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestSearchAllMergesWithRRF(t *testing.T) {
	a := &fanoutStub{name: "alpha", results: []Result{
		{Title: "Shared", URL: "https://shared.test/page", Source: "alpha"},
		{Title: "Alpha only", URL: "https://alpha.test/a", Source: "alpha"},
	}}
	b := &fanoutStub{name: "beta", results: []Result{
		{Title: "Beta only", URL: "https://beta.test/b", Source: "beta"},
		{Title: "Shared trailing slash", URL: "http://shared.test/page/", Source: "beta"},
	}}
	svc := newAllService(t, a, b)

	resp, err := svc.Search(context.Background(), Request{Query: "q", Provider: AllProviders, Count: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.Provider != AllProviders {
		t.Errorf("provider = %q, want all", resp.Provider)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results = %d, want 3 (shared deduped): %+v", len(resp.Results), resp.Results)
	}
	// The shared URL appears in both rankings, so RRF must put it first.
	first := resp.Results[0]
	if first.URL != "https://shared.test/page" {
		t.Errorf("first result = %+v, want shared URL on top", first)
	}
	if first.Source != "alpha,beta" {
		t.Errorf("first source = %q, want alpha,beta", first.Source)
	}
}

func TestSearchAllToleratesPartialFailure(t *testing.T) {
	ok := &fanoutStub{name: "alpha", results: []Result{{Title: "A", URL: "https://a.test", Source: "alpha"}}}
	bad := &fanoutStub{name: "beta", err: errors.New("boom")}
	svc := newAllService(t, ok, bad)

	resp, err := svc.Search(context.Background(), Request{Query: "q", Provider: AllProviders})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Title != "A" {
		t.Errorf("results = %+v", resp.Results)
	}
}

func TestSearchAllFailsWhenAllFail(t *testing.T) {
	bad1 := &fanoutStub{name: "alpha", err: errors.New("boom-a")}
	bad2 := &fanoutStub{name: "beta", err: errors.New("boom-b")}
	svc := newAllService(t, bad1, bad2)

	if _, err := svc.Search(context.Background(), Request{Query: "q", Provider: AllProviders}); err == nil {
		t.Fatal("expected error when every provider fails")
	}
}

func TestSearchAllRespectsCount(t *testing.T) {
	a := &fanoutStub{name: "alpha", results: []Result{
		{URL: "https://a.test/1", Source: "alpha"},
		{URL: "https://a.test/2", Source: "alpha"},
		{URL: "https://a.test/3", Source: "alpha"},
	}}
	svc := newAllService(t, a)

	resp, err := svc.Search(context.Background(), Request{Query: "q", Provider: AllProviders, Count: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Errorf("results = %d, want 2", len(resp.Results))
	}
}

func TestNormalizeResultURL(t *testing.T) {
	cases := map[string]string{
		"https://Example.test/Page/":       "https://example.test/Page",
		"http://example.test/Page":         "https://example.test/Page",
		"https://example.test/Page#anchor": "https://example.test/Page",
	}
	for in, want := range cases {
		if got := normalizeResultURL(in); got != want {
			t.Errorf("normalizeResultURL(%q) = %q, want %q", in, got, want)
		}
	}
}
