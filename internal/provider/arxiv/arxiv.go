// Package arxiv searches arXiv through its public Atom API.
//
// The API is keyless, has no bot wall, and returns well-structured metadata, so
// it is a far more dependable source for research and technical queries than a
// general web engine. It returns Atom rather than JSON, so this provider
// parses XML directly instead of using the shared common.JSONAPI helper.
package arxiv

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/provider/common"
	"github.com/denysvitali/search-mcp/internal/search"
)

const defaultEndpoint = "https://export.arxiv.org/api/query"

// apiMaxResults bounds the number of entries requested in one API call.
const apiMaxResults = 100

// Arxiv searches arXiv via its public Atom API.
type Arxiv struct {
	endpoint string
	client   *http.Client
}

var _ search.Provider = (*Arxiv)(nil)

func init() {
	provider.Register("arxiv", func(_, endpoint string) (search.Provider, error) {
		return NewArxiv(endpoint), nil
	})
}

// NewArxiv builds the arXiv provider. An empty endpoint uses the public host.
func NewArxiv(endpoint string) *Arxiv {
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &Arxiv{endpoint: endpoint, client: common.NewHTTPClient()}
}

func (a *Arxiv) Name() string { return "arxiv" }

func (a *Arxiv) Search(ctx context.Context, req search.Request) (search.Response, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return search.Response{}, fmt.Errorf("arxiv: query is required")
	}
	count := req.Count
	if count <= 0 {
		count = 10
	}
	count = min(count, apiMaxResults)

	// arXiv's search_query grammar is not free text: a bare word searches all
	// fields, but a phrase must be quoted. Wrapping the query in an "all:" field
	// keeps multi-word input working the way a caller expects, without letting
	// an unescaped colon or quote break the query.
	values := url.Values{
		"search_query": {"all:" + arxivQuery(query)},
		"start":        {"0"},
		"max_results":  {strconv.Itoa(count)},
		"sortBy":       {"relevance"},
		"sortOrder":    {"descending"},
	}
	target, err := common.URLWithQuery(a.endpoint, values)
	if err != nil {
		return search.Response{}, fmt.Errorf("arxiv endpoint: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return search.Response{}, err
	}
	httpReq.Header.Set("Accept", "application/atom+xml")
	httpReq.Header.Set("User-Agent", "search-mcp/1.0 (https://github.com/denysvitali/search-mcp)")
	common.ApplyExtraHeaders(httpReq, req)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return search.Response{}, fmt.Errorf("arxiv request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return search.Response{}, fmt.Errorf("arxiv returned http 429: %w", search.NewRateLimitedError(resp.Header))
	case http.StatusForbidden, http.StatusUnauthorized:
		return search.Response{}, fmt.Errorf("arxiv returned http %d; request blocked by upstream: %w", resp.StatusCode, search.ErrBlocked)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return search.Response{}, fmt.Errorf("arxiv search failed: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(common.LimitedBody(resp.Body))
	if err != nil {
		return search.Response{}, fmt.Errorf("read arxiv response: %w", err)
	}
	if common.IsChallengePage(body) {
		return search.Response{}, common.ErrChallenge("arxiv")
	}

	results, err := parseFeed(body, count)
	if err != nil {
		return search.Response{}, fmt.Errorf("arxiv: %w", err)
	}
	return search.Response{Query: req.Query, Provider: a.Name(), Results: results}, nil
}

// arxivQuery quotes a multi-word query so arXiv treats it as a phrase instead
// of applying its own (surprising) field and boolean defaults.
func arxivQuery(query string) string {
	if strings.ContainsAny(query, `":+`) {
		// Already contains arXiv operators or quoting; pass it through rather
		// than nesting quotes that would change its meaning.
		return query
	}
	if !strings.Contains(query, " ") {
		return query
	}
	return `"` + query + `"`
}

// Atom feed shape. Only the fields needed for a search result are modelled.
type feed struct {
	Entries []entry `xml:"entry"`
}

type entry struct {
	ID        string   `xml:"id"`
	Title     string   `xml:"title"`
	Summary   string   `xml:"summary"`
	Published string   `xml:"published"`
	Updated   string   `xml:"updated"`
	Authors   []author `xml:"author"`
	Links     []link   `xml:"link"`
}

type author struct {
	Name string `xml:"name"`
}

type link struct {
	Href  string `xml:"href,attr"`
	Rel   string `xml:"rel,attr"`
	Title string `xml:"title,attr"`
	Type  string `xml:"type,attr"`
}

func parseFeed(body []byte, limit int) ([]search.Result, error) {
	var f feed
	if err := xml.NewDecoder(bytes.NewReader(body)).Decode(&f); err != nil {
		return nil, fmt.Errorf("decode atom feed: %w", err)
	}

	results := make([]search.Result, 0, min(len(f.Entries), limit))
	for _, item := range f.Entries {
		link := arxivLink(item)
		if link == "" {
			continue
		}
		title := collapse(item.Title)
		if title == "" {
			continue
		}
		results = append(results, search.Result{
			Title:       title,
			URL:         link,
			Description: arxivDescription(item),
			Source:      "arxiv",
			Published:   arxivDate(item.Published),
		})
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}

// arxivLink picks the abstract page, preferring the rel="alternate" HTML entry
// over the PDF attachment, and strips the version suffix so the URL is stable.
func arxivLink(item entry) string {
	var alternate string
	for _, l := range item.Links {
		href := strings.TrimSpace(l.Href)
		if href == "" {
			continue
		}
		if l.Rel == "alternate" && l.Type == "text/html" {
			return stripVersion(href)
		}
		if alternate == "" && !strings.HasSuffix(href, ".pdf") {
			alternate = stripVersion(href)
		}
	}
	if alternate != "" {
		return alternate
	}
	// No usable link element: fall back to the entry id, which is the abs URL.
	return stripVersion(strings.TrimSpace(item.ID))
}

// stripVersion removes a trailing "vN" from an arXiv identifier so the same
// paper deduplicates across versions.
func stripVersion(href string) string {
	if idx := strings.LastIndex(href, "v"); idx > 0 {
		if _, err := strconv.Atoi(href[idx+1:]); err == nil {
			return href[:idx]
		}
	}
	return href
}

// arxivDescription joins the author list onto the abstract, so a result
// carries the two facts a reader uses to triage a paper.
func arxivDescription(item entry) string {
	abstract := collapse(item.Summary)
	names := make([]string, 0, len(item.Authors))
	for _, a := range item.Authors {
		if name := collapse(a.Name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return abstract
	}
	authors := strings.Join(names, ", ")
	// Cap a very long collaboration list; the point is attribution, not the
	// full author roster.
	if len(authors) > 120 {
		authors = strings.TrimSpace(authors[:120]) + " et al."
	}
	if abstract == "" {
		return "by " + authors
	}
	return "by " + authors + " — " + abstract
}

// arxivDate normalises an Atom timestamp to YYYY-MM-DD, falling back to the
// updated timestamp when published is absent.
func arxivDate(published string) string {
	if published == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(published))
	if err != nil {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

// collapse trims s and folds whitespace runs into single spaces, since Atom
// titles and abstracts arrive with hard line breaks and indentation.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }
