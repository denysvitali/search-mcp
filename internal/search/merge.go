package search

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// AllProviders is the reserved provider name that fans a query out to every
// configured provider concurrently and merges the results.
const AllProviders = "all"

// rrfK is the reciprocal-rank-fusion constant; 60 is the standard value from
// the original RRF paper and keeps single-provider top hits from dominating.
const rrfK = 60

// searchAll queries every provider in parallel and merges their result lists
// with reciprocal rank fusion, deduplicating by normalized URL. Providers
// that fail are skipped; the call only errors when every provider fails.
func (s *Service) searchAll(ctx context.Context, span trace.Span, req Request) (Response, error) {
	names := s.ProviderNames()
	span.SetAttributes(attribute.Bool("search.fanout", true))

	type outcome struct {
		name string
		resp Response
		err  error
	}
	outcomes := make([]outcome, len(names))

	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			attempt := req
			attempt.Provider = name
			resp, err := s.searchOne(ctx, span, attempt)
			outcomes[i] = outcome{name: name, resp: resp, err: err}
		}(i, name)
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return Response{}, err
	}

	var responses []Response
	var errs []error
	for _, o := range outcomes {
		if o.err != nil {
			s.logger.WithError(o.err).WithField("provider", o.name).Warn("provider failed during fan-out")
			errs = append(errs, o.err)
			continue
		}
		responses = append(responses, o.resp)
	}
	if len(responses) == 0 {
		return Response{}, errors.Join(errs...)
	}

	merged := fuseResults(responses, req.Count)
	return Response{Query: req.Query, Provider: AllProviders, Results: merged}, nil
}

// fuseResults merges per-provider rankings with reciprocal rank fusion:
// score(doc) = Σ 1/(rrfK + rank). The first occurrence of a URL supplies the
// displayed title/description; Source accumulates every provider that
// returned it.
func fuseResults(responses []Response, count int) []Result {
	type fused struct {
		result  Result
		score   float64
		sources []string
	}
	byURL := make(map[string]*fused)
	var order []string

	for _, resp := range responses {
		for rank, result := range resp.Results {
			key := normalizeResultURL(result.URL)
			entry, ok := byURL[key]
			if !ok {
				entry = &fused{result: result}
				byURL[key] = entry
				order = append(order, key)
			}
			entry.score += 1.0 / float64(rrfK+rank+1)
			if !slices.Contains(entry.sources, result.Source) {
				entry.sources = append(entry.sources, result.Source)
			}
		}
	}

	sort.SliceStable(order, func(i, j int) bool {
		return byURL[order[i]].score > byURL[order[j]].score
	})

	results := make([]Result, 0, len(order))
	for _, key := range order {
		if count > 0 && len(results) >= count {
			break
		}
		entry := byURL[key]
		entry.result.Source = strings.Join(entry.sources, ",")
		results = append(results, entry.result)
	}
	return results
}

// normalizeResultURL folds trivial URL variations (scheme, host case,
// trailing slash, fragment) so the same document from two providers
// deduplicates.
func normalizeResultURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.ToLower(raw)
	}
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}
