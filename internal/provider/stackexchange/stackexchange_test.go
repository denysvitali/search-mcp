package stackexchange

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/denysvitali/search-mcp/internal/search"
)

func TestSearchMetadataAndBackoff(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if r.URL.Path != "/search/advanced" || q.Get("q") != "Go context" || q.Get("sort") != "relevance" || q.Get("fromdate") == "" || q.Get("key") != "" {
			t.Errorf("bad request %s", r.URL)
		}
		fmt.Fprint(w, `{"items":[{"title":"Go &amp; contexts","link":"https://stackoverflow.com/questions/123","accepted_answer_id":456,"answer_count":2,"score":10,"creation_date":1704067200,"body":"<p>First paragraph.</p><p>Use <code>context.Context</code>.</p>"}],"backoff":30,"quota_remaining":5}`)
	}))
	defer server.Close()
	p := NewStackExchange(server.URL + "/search/advanced")
	resp, err := p.Search(context.Background(), search.Request{Query: "Go context", Freshness: "py"})
	if err != nil || len(resp.Results) != 1 {
		t.Fatalf("%+v %v", resp, err)
	}
	hit := resp.Results[0]
	if hit.Title != "Go & contexts" || hit.Published != "2024-01-01" || !strings.Contains(hit.Description, "accepted answer") || strings.Contains(hit.Description, "<p>") {
		t.Fatalf("bad metadata %+v", hit)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Search(context.Background(), search.Request{Query: "other"})
			if !errors.Is(err, search.ErrRateLimited) {
				t.Errorf("ignored backoff: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("made %d requests during cooldown", calls.Load())
	}
}

func TestThrottleEnvelopeAndQuota(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body       string
		firstFails bool
	}{
		{400, `{"error_id":502,"error_message":"throttle_violation","backoff":60}`, true},
		{200, `{"items":[],"quota_remaining":0}`, false},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			p := NewStackExchange(server.URL)
			_, err := p.Search(context.Background(), search.Request{Query: "q"})
			if (err != nil) != tc.firstFails {
				t.Fatalf("first response: %v", err)
			}
			_, err = p.Search(context.Background(), search.Request{Query: "other"})
			var limited *search.RateLimitedError
			if !errors.As(err, &limited) || limited.RetryAfter <= 0 || calls.Load() != 1 {
				t.Fatalf("cooldown not honored: %v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestDefaultEndpointAndInvalidEnvelope(t *testing.T) {
	if got := NewStackExchange("").api.Endpoint; got != defaultEndpoint {
		t.Fatalf("duplicated endpoint: %s", got)
	}
	for _, body := range []string{`{}`, `{"items":null}`, `{"error_id":400,"error_message":"bad query"}`} {
		if _, err := decodeQuestions([]byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
