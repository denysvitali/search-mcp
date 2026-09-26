// Package stackexchange searches Stack Overflow through its keyless public API.
package stackexchange

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

const defaultEndpoint = "https://api.stackexchange.com/2.3/search/advanced"

// maxPages is the API's per-request ceiling for pagesize.
const maxPages = 100

// StackExchange searches Stack Overflow via the Stack Exchange API.
type StackExchange struct {
	api         *common.JSONAPI
	gate        chan struct{}
	nextAllowed time.Time
}

var _ search.Provider = (*StackExchange)(nil)

func init() {
	provider.Register("stackexchange", func(_, endpoint string) (search.Provider, error) {
		return NewStackExchange(endpoint), nil
	})
}

// NewStackExchange accepts a complete endpoint URL, matching other providers.
func NewStackExchange(endpoint string) *StackExchange {
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	s := &StackExchange{gate: make(chan struct{}, 1)}
	s.api = &common.JSONAPI{
		Name:     "stackexchange",
		Endpoint: endpoint,
		Client:   common.NewHTTPClient(),
		BuildQuery: func(req search.Request, count int) map[string]string {
			params := map[string]string{
				"q":        req.Query,
				"site":     "stackoverflow",
				"order":    "desc",
				"sort":     "relevance",
				"pagesize": strconv.Itoa(min(count, maxPages)),
				"filter":   "withbody",
			}
			if since, _ := common.FreshnessSince(req.Freshness, time.Now()); !since.IsZero() {
				params["fromdate"] = strconv.FormatInt(since.Unix(), 10)
			}
			return params
		},
		Decode: s.decode,
	}
	return s
}

func (s *StackExchange) Name() string { return "stackexchange" }

func (s *StackExchange) Search(ctx context.Context, req search.Request) (search.Response, error) {
	if err := ctx.Err(); err != nil {
		return search.Response{}, err
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return search.Response{}, ctx.Err()
	}
	if delay := time.Until(s.nextAllowed); delay > 0 {
		return search.Response{}, &search.RateLimitedError{RetryAfter: delay}
	}
	return s.api.Search(ctx, req)
}

// apiQuestion is one question from the search/advanced response.
type apiQuestion struct {
	Title          string   `json:"title"`
	Link           string   `json:"link"`
	Score          int      `json:"score"`
	AnswerCount    int      `json:"answer_count"`
	IsAnswered     bool     `json:"is_answered"`
	AcceptedAnswer int      `json:"accepted_answer_id"`
	Tags           []string `json:"tags"`
	CreationDate   int64    `json:"creation_date"`
	LastActivity   int64    `json:"last_activity_date"`
	Body           string   `json:"body"`
	Excerpt        string   `json:"excerpt"`
}

func decodeQuestions(payload []byte) ([]search.Result, error) {
	var body struct {
		Items        []apiQuestion `json:"items"`
		HasMore      bool          `json:"has_more"`
		QuotaMax     int           `json:"quota_max"`
		ErrorID      int           `json:"error_id"`
		ErrorMessage string        `json:"error_message"`
		ErrorName    string        `json:"error_name"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	// The API reports quota exhaustion and bad parameters in a 200 body.
	if body.ErrorID != 0 {
		if body.ErrorID == 502 {
			return nil, fmt.Errorf("stackexchange throttle: %w", search.ErrRateLimited)
		}
		return nil, fmt.Errorf("api error %d: %s", body.ErrorID, collapse(body.ErrorMessage))
	}

	if body.Items == nil {
		return nil, fmt.Errorf("response missing items array")
	}
	results := make([]search.Result, 0, len(body.Items))
	for _, item := range body.Items {
		title := collapse(html.UnescapeString(item.Title))
		link := strings.TrimSpace(item.Link)
		if title == "" || link == "" {
			continue
		}
		results = append(results, search.Result{
			Title:       title,
			URL:         link,
			Description: questionDescription(item),
			Source:      "stackexchange",
			Published:   unixDate(item.CreationDate),
		})
	}
	return results, nil
}

// questionDescription prefers the API's purpose-built excerpt over the raw
// body, and appends the acceptance/score signals a reader needs to judge the
// answer's quality before clicking.
func questionDescription(item apiQuestion) string {
	var b strings.Builder
	b.WriteString(common.HTMLSnippet(item.Excerpt))
	if b.Len() == 0 {
		b.WriteString(common.HTMLSnippet(item.Body))
	}

	var meta []string
	switch {
	case item.AcceptedAnswer > 0:
		meta = append(meta, "accepted answer")
	case item.IsAnswered:
		meta = append(meta, "answered")
	}
	if item.Score != 0 {
		meta = append(meta, fmt.Sprintf("score %d", item.Score))
	}
	if item.AnswerCount > 0 {
		meta = append(meta, fmt.Sprintf("%d answers", item.AnswerCount))
	}
	if len(item.Tags) > 0 {
		meta = append(meta, strings.Join(item.Tags, " "))
	}
	if len(meta) > 0 {
		if b.Len() > 0 {
			b.WriteString(" — ")
		}
		b.WriteString(strings.Join(meta, " · "))
	}
	return collapse(b.String())
}

// unixDate converts a Unix timestamp to the YYYY-MM-DD form used across
// providers. A zero or negative timestamp yields no date.
func unixDate(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	return time.Unix(seconds, 0).UTC().Format("2006-01-02")
}

// collapse trims s and folds whitespace runs into single spaces.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// decode records the method-wide cooldown while Search holds the gate. A
// successful response can request backoff for the NEXT request.
func (s *StackExchange) decode(payload []byte) ([]search.Result, error) {
	var envelope struct {
		Backoff        int  `json:"backoff"`
		ErrorID        int  `json:"error_id"`
		QuotaRemaining *int `json:"quota_remaining"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, err
	}
	seconds := envelope.Backoff
	if envelope.ErrorID == 502 {
		seconds = max(seconds, 60)
	}
	if envelope.QuotaRemaining != nil && *envelope.QuotaRemaining == 0 {
		now := time.Now().UTC()
		midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
		seconds = max(seconds, int(time.Until(midnight).Seconds())+1)
	}
	if seconds > 0 {
		s.nextAllowed = time.Now().Add(time.Duration(min(seconds, 86400)) * time.Second)
	}
	if envelope.ErrorID == 502 {
		return nil, &search.RateLimitedError{RetryAfter: time.Until(s.nextAllowed)}
	}
	return decodeQuestions(payload)
}
