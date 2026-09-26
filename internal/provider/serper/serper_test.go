package serper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

func TestGeneralWebSearch(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.Header.Get("X-API-KEY") != "free-key" {
			t.Error("incorrect method or auth")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["q"] != "Kyoto autumn" || body["num"] != float64(50) || body["gl"] != "jp" || body["hl"] != "en" || body["tbs"] != "qdr:y" {
			t.Errorf("bad payload %+v", body)
		}
		fmt.Fprint(w, `{"organic":[{"title":"Kyoto Travel Guide","link":"https://www.japan-guide.com/e/e2158.html","snippet":"When to visit Kyoto","date":"Sep 1, 2026"}],"answerBox":{"answer":"Ignore generated answers"},"peopleAlsoAsk":[{"title":"Not an organic result"}]}`)
	}))
	defer server.Close()
	p, err := New("free-key", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Search(context.Background(), search.Request{Query: "Kyoto autumn", Count: 50, Country: "JP", Language: "EN", Freshness: "py"})
	if err != nil || len(resp.Results) != 1 || resp.Results[0].Source != "serper" || resp.Results[0].Description != "When to visit Kyoto" || calls != 1 {
		t.Fatalf("%+v %v calls=%d", resp, err, calls)
	}
}

func TestFailuresAndEmptyResults(t *testing.T) {
	for _, tc := range []struct {
		status   int
		body     string
		sentinel error
		fails    bool
	}{
		{429, `{}`, search.ErrRateLimited, true}, {402, `{}`, search.ErrBlocked, true}, {403, `{}`, search.ErrBlocked, true},
		{200, `{"error":"failed"}`, nil, true}, {200, `{"organic":[]}`, nil, false}, {200, `<html>error</html>`, nil, true},
	} {
		t.Run(fmt.Sprint(tc.status, tc.body), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			p, err := New("key", server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Search(context.Background(), search.Request{Query: "q"})
			if (err != nil) != tc.fails || tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Fatalf("err %v", err)
			}
			if tc.status == 429 {
				var limited *search.RateLimitedError
				if !errors.As(err, &limited) || limited.RetryAfter != 3*time.Second {
					t.Fatal("lost Retry-After")
				}
			}
		})
	}
}

func TestValidationAndCancellation(t *testing.T) {
	if _, err := New(" ", ""); err == nil {
		t.Fatal("empty key accepted")
	}
	if _, err := New("key", "/relative"); err == nil {
		t.Fatal("relative endpoint accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected network request") }))
	defer server.Close()
	p, err := New("key", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Search(ctx, search.Request{Query: "q"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation %v", err)
	}
	if _, err := p.Search(context.Background(), search.Request{Query: "q", Freshness: "invalid"}); err == nil {
		t.Fatal("invalid freshness accepted")
	}
}
