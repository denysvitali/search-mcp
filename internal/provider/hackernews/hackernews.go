// Package hackernews searches stories using the keyless Algolia HN API.
package hackernews

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/provider/common"
	"github.com/denysvitali/search-mcp/internal/search"
)

const defaultEndpoint = "https://hn.algolia.com/api/v1/search"

// maxHits is Algolia's per-page ceiling. The API rejects larger values.
const maxHits = 100

// HN searches Hacker News via the Algolia HN Search API.
type HN struct {
	api *common.JSONAPI
}

var _ search.Provider = (*HN)(nil)

func init() {
	provider.Register("hackernews", func(_, endpoint string) (search.Provider, error) {
		return NewHN(endpoint), nil
	})
}

// NewHN builds the Hacker News provider. An empty endpoint uses Algolia's
// public host.
func NewHN(endpoint string) *HN {
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &HN{api: &common.JSONAPI{
		Name:     "hackernews",
		Endpoint: endpoint,
		Client:   common.NewHTTPClient(),
		BuildQuery: func(req search.Request, count int) map[string]string {
			params := map[string]string{
				"query":       req.Query,
				"hitsPerPage": strconv.Itoa(min(count, maxHits)),
				"tags":        "story",
			}
			if since, _ := common.FreshnessSince(req.Freshness, time.Now()); !since.IsZero() {
				params["numericFilters"] = "created_at_i>=" + strconv.FormatInt(since.Unix(), 10)
			}
			return params
		},
		Decode: decodeStories,
	}}
}

func (h *HN) Name() string { return "hackernews" }

func (h *HN) Search(ctx context.Context, req search.Request) (search.Response, error) {
	return h.api.Search(ctx, req)
}

// algoliaHit is one story from the HN search response. story_text holds the
// self-post body, which is empty for link posts.
type algoliaHit struct {
	ObjectID    string `json:"objectID"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	StoryText   string `json:"story_text"`
	Author      string `json:"author"`
	Points      int    `json:"points"`
	NumComments int    `json:"num_comments"`
	CreatedAt   string `json:"created_at"`
}

func decodeStories(payload []byte) ([]search.Result, error) {
	var body struct {
		Hits []algoliaHit `json:"hits"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if body.Hits == nil {
		return nil, fmt.Errorf("response missing hits array")
	}
	results := make([]search.Result, 0, len(body.Hits))
	for _, hit := range body.Hits {
		title := collapse(html.UnescapeString(hit.Title))
		if title == "" {
			continue
		}
		// A self-post with no external link still has a discussion page, which
		// is the useful target when the post *is* the content.
		link := strings.TrimSpace(hit.URL)
		if link == "" {
			if hit.ObjectID == "" {
				continue
			}
			link = "https://news.ycombinator.com/item?id=" + hit.ObjectID
		}

		results = append(results, search.Result{
			Title:       title,
			URL:         link,
			Description: hnDescription(hit),
			Source:      "hackernews",
			Published:   hnPublished(hit.CreatedAt),
		})
	}
	return results, nil
}

// hnDescription renders the discussion metadata the API returns alongside each
// story, so a caller can judge a hit's reception without fetching the thread.
func hnDescription(hit algoliaHit) string {
	var b strings.Builder
	if text := common.HTMLSnippet(hit.StoryText); text != "" {
		b.WriteString(text)
	}
	meta := []string{}
	if hit.Points > 0 {
		meta = append(meta, fmt.Sprintf("%d points", hit.Points))
	}
	if hit.NumComments > 0 {
		meta = append(meta, fmt.Sprintf("%d comments", hit.NumComments))
	}
	if hit.Author != "" {
		meta = append(meta, "by "+hit.Author)
	}
	if len(meta) > 0 {
		if b.Len() > 0 {
			b.WriteString(" — ")
		}
		b.WriteString(strings.Join(meta, " · "))
	}
	return collapse(b.String())
}

// hnPublished normalises Algolia's RFC3339 timestamp to the YYYY-MM-DD form
// every other provider uses, so freshness stays comparable after merging.
// Algolia also appends a numeric timezone offset ("Z" is not always present).
func hnPublished(created string) string {
	if created == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-07:00", "2006-01-02T15:04:05Z0700"} {
		if t, err := time.Parse(layout, created); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

// collapse trims s and folds every run of whitespace into a single space, so
// markup-laden self-post bodies stay on one line in a result snippet.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }
