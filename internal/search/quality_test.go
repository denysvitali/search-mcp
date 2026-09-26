package search

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestFusionDoesNotRewardRepeatedURLs(t *testing.T) {
	responses := []Response{
		{Provider: "alpha", Results: []Result{
			{URL: "https://duplicate.test/", Description: "short"},
			{URL: "https://duplicate.test/?utm_source=x", Description: "a more useful description", Title: "Useful title", Published: "2026-09-01"},
			{URL: "https://duplicate.test/#a"},
			{URL: "https://consensus.test/"},
		}},
		{Provider: "beta", Results: []Result{{URL: "https://consensus.test/"}}},
	}
	got := fuseResults(responses, 10)
	if len(got) != 2 || got[0].URL != "https://consensus.test/" || got[0].Source != "alpha,beta" {
		t.Fatalf("duplicate provider votes outranked consensus: %+v", got)
	}
	if got[1].Title != "Useful title" || got[1].Description != "a more useful description" || got[1].Published != "2026-09-01" {
		t.Fatalf("metadata was not enriched: %+v", got[1])
	}
	if responses[0].Results[0].Title != "" {
		t.Fatal("fusion mutated provider/cache results")
	}
}

type countAwareProvider struct {
	name string
}

func (p countAwareProvider) Name() string { return p.name }
func (p countAwareProvider) Search(_ context.Context, req Request) (Response, error) {
	results := []Result{{URL: "https://" + p.name + ".test/"}, {URL: "https://shared.test/"}}
	return Response{Results: results[:min(req.Count, len(results))]}, nil
}

func TestSmallCountFindsAgreementBelowFirstRank(t *testing.T) {
	svc := newAllService(t, countAwareProvider{"alpha"}, countAwareProvider{"beta"})
	resp, err := svc.Search(context.Background(), Request{Query: "q", Count: 1})
	if err != nil || len(resp.Results) != 1 || resp.Results[0].URL != "https://shared.test/" {
		t.Fatalf("count=1 lost consensus candidate: %+v, %v", resp, err)
	}
}

func TestSelectionConsistentAcrossSearchModes(t *testing.T) {
	rows := []Result{
		{URL: "javascript:alert(1)"},
		{URL: "https://badexample.com/page"},
		{URL: "https://spam.example.com/page"},
		{URL: "https://docs.example.com/one"},
		{URL: "https://www.docs.example.com/two"},
		{URL: "https://api.example.com/three"},
	}
	svc := newAllService(t, &fanoutStub{name: "alpha", results: rows})
	for _, mode := range []string{"alpha", AllProviders} {
		resp, err := svc.Search(context.Background(), Request{
			Query: "q", Provider: mode, Count: 2, MaxPerHost: 1,
			IncludeDomains: []string{" EXAMPLE.com. "}, ExcludeDomains: []string{"spam.example.com"},
		})
		if err != nil || len(resp.Results) != 2 {
			t.Fatalf("mode %s: %+v, %v", mode, resp, err)
		}
		if resp.Results[0].URL != rows[3].URL || resp.Results[1].URL != rows[5].URL {
			t.Fatalf("mode %s filtered or truncated incorrectly: %+v", mode, resp.Results)
		}
		if resp.Results[0].Title != rows[3].URL || resp.Results[0].Source != "alpha" {
			t.Fatalf("mode %s did not repair metadata: %+v", mode, resp.Results[0])
		}
	}
	// An earlier filtered search must not modify the next search's candidates.
	unfiltered, err := svc.Search(context.Background(), Request{Query: "q", Provider: "alpha", Count: 50})
	if err != nil || len(unfiltered.Results) != 5 || rows[3].Title != "" {
		t.Fatalf("filter leaked into provider state: %+v, %v", unfiltered, err)
	}
}

func TestSearchValidationBeforeProviderCall(t *testing.T) {
	for _, req := range []Request{
		{Count: -1}, {MaxPerHost: -1},
		{IncludeDomains: []string{"https://example.com"}},
		{ExcludeDomains: []string{"*.example.com"}},
		{ExcludeDomains: []string{"example..com"}},
	} {
		p := &stubProvider{name: "alpha"}
		req.Query = "q"
		_, err := newAllService(t, p).Search(context.Background(), req)
		if err == nil || p.lastReq != nil {
			t.Fatalf("invalid request reached provider: %+v, %v", req, err)
		}
	}
}

func TestLocalFiltersStayOutsideProviderCache(t *testing.T) {
	p := &stubProvider{name: "alpha"}
	svc := newAllService(t, p)
	_, err := svc.Search(context.Background(), Request{Query: "q", Count: 20, IncludeDomains: []string{"example.com"}, MaxPerHost: 2})
	if err != nil {
		t.Fatal(err)
	}
	if p.lastReq.Count != MaxResultCount || len(p.lastReq.IncludeDomains) != 0 || p.lastReq.MaxPerHost != 0 {
		t.Fatalf("unexpected provider request: %+v", p.lastReq)
	}
}

func TestFallbackReportsFailuresAndFiltersBeforeSuccess(t *testing.T) {
	svc := newAllService(t,
		&fanoutStub{name: "alpha", err: errors.New("unavailable")},
		&fanoutStub{name: "beta", results: []Result{{URL: "https://excluded.test/"}}},
		&fanoutStub{name: "gamma", results: []Result{{URL: "https://wanted.test/"}}},
	)
	resp, err := svc.Search(context.Background(), Request{Query: "q", Provider: "alpha", IncludeDomains: []string{"wanted.test"}})
	if err != nil || resp.Provider != "gamma" || len(resp.Degraded) != 1 || resp.Degraded[0].Provider != "alpha" {
		t.Fatalf("unexpected fallback: %+v, %v", resp, err)
	}
}

func TestDomainNormalization(t *testing.T) {
	got, err := normalizeDomains([]string{" EXAMPLE.com., bücher.de "})
	want := []string{"example.com", "xn--bcher-kva.de"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("domains = %v, %v", got, err)
	}
}

func TestNormalizedURLPreservesDocumentIdentity(t *testing.T) {
	for _, raw := range []string{"https://[::1]/", "https://example.test/a%2Fb/", "https://example.test/a?article=2"} {
		key := NormalizeResultURL(raw)
		if again := NormalizeResultURL(key); key != again {
			t.Fatalf("not idempotent: %q => %q => %q", raw, key, again)
		}
	}
	if NormalizeResultURL("https://example.test/a%2Fb/") == NormalizeResultURL("https://example.test/a/b/") {
		t.Fatal("escaped slash merged distinct documents")
	}
	if NormalizeResultURL("https://example.test/a%2F") == NormalizeResultURL("https://example.test/a/") {
		t.Fatal("trailing encoded slash merged distinct documents")
	}
}

func TestServiceCapsResultsEvenWhenProviderIgnoresCount(t *testing.T) {
	rows := make([]Result, 0, 70)
	for i := range 70 {
		rows = append(rows, Result{URL: fmt.Sprintf("https://example.test/%d", i)})
	}
	for _, mode := range []string{"alpha", AllProviders} {
		resp, err := newAllService(t, &fanoutStub{name: "alpha", results: rows}).Search(context.Background(), Request{Query: "q", Count: 1000, Provider: mode})
		if err != nil || len(resp.Results) != MaxResultCount {
			t.Fatalf("mode %s: got %d results, %v", mode, len(resp.Results), err)
		}
	}
}
