package reader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// setGitHubAPIBaseURL points gitHubAPIBaseURL at ts for the duration of a test
// and restores it afterwards.
func setGitHubAPIBaseURL(t *testing.T, base string) {
	t.Helper()
	prev := gitHubAPIBaseURL
	gitHubAPIBaseURL = base
	t.Cleanup(func() { gitHubAPIBaseURL = prev })
}

func setRedditAPITarget(t *testing.T, scheme, host string) {
	t.Helper()
	prevScheme, prevHost := redditAPIScheme, redditAPIHost
	redditAPIScheme, redditAPIHost = scheme, host
	t.Cleanup(func() {
		redditAPIScheme, redditAPIHost = prevScheme, prevHost
	})
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// --- cleanMarkdown -----------------------------------------------------------

func TestCleanMarkdownCollapsesBlankLines(t *testing.T) {
	in := "a\n\n\n\n\nb\n\n\nc"
	out := cleanMarkdown(in)
	if strings.Contains(out, "\n\n\n") {
		t.Fatalf("found 3+ consecutive newlines in %q", out)
	}
	want := "a\n\nb\n\nc"
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
}

func TestCleanMarkdownTrimsEdges(t *testing.T) {
	if got := cleanMarkdown("\n\n  hello  \n\n"); got != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

// --- reddit timestamp --------------------------------------------------------

func TestRedditUnixTimePrecision(t *testing.T) {
	// 1700000000.5 -> exact whole seconds plus 500ms, no float drift.
	got := redditUnixTime(1700000000.5)
	if got.Unix() != 1700000000 {
		t.Errorf("Unix() = %d, want 1700000000", got.Unix())
	}
	if got.Nanosecond() < 499_000_000 || got.Nanosecond() > 501_000_000 {
		t.Errorf("Nanosecond() = %d, want ~5e8", got.Nanosecond())
	}
}

func TestRedditUnixTimeZero(t *testing.T) {
	if !redditUnixTime(0).IsZero() {
		t.Error("expected zero time for 0 seconds")
	}
}

// --- GitHub helpers ----------------------------------------------------------

func TestParseGitHubNextLink(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"", ""},
		{`<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/x?page=5>; rel="last"`, "https://api.github.com/x?page=2"},
		{`<https://api.github.com/x?page=1>; rel="prev"`, ""},
	}
	for _, tc := range cases {
		if got := parseGitHubNextLink(tc.header); got != tc.want {
			t.Errorf("parseGitHubNextLink(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestWithPerPage(t *testing.T) {
	got := withPerPage("https://api.github.com/repos/o/r/issues/1/comments", 100)
	u := mustParseURL(t, got)
	if u.Query().Get("per_page") != "100" {
		t.Errorf("per_page = %q, want 100", u.Query().Get("per_page"))
	}
}

// --- GitHub repo rendering ---------------------------------------------------

func TestFetchGitHubRepoAsMarkdown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/octocat/hello/readme", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("# Hello README\n\nbody"))
	})
	mux.HandleFunc("/repos/octocat/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"full_name":"octocat/hello",
			"description":"a test repo",
			"html_url":"https://github.com/octocat/hello",
			"stargazers_count":42,
			"forks_count":7,
			"open_issues_count":3,
			"language":"Go",
			"default_branch":"main",
			"topics":["cli","tool"],
			"license":{"spdx_id":"MIT"}
		}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	setGitHubAPIBaseURL(t, ts.URL)

	out, err := fetchGitHubRepoAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://github.com/octocat/hello"))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, want := range []string{"# octocat/hello", "a test repo", "Stars: 42", "Forks: 7", "Primary language: Go", "License: MIT", "Topics: cli, tool", "# Hello README"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestFetchGitHubRepoNoReadme(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/readme", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"full_name":"o/r"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	setGitHubAPIBaseURL(t, ts.URL)

	out, err := fetchGitHubRepoAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://github.com/o/r"))
	if err != nil {
		t.Fatalf("404 README should not error: %v", err)
	}
	if !strings.Contains(out, "_No README available._") {
		t.Errorf("expected no-README marker, got:\n%s", out)
	}
}

func TestFetchGitHubRepoReadmeServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/readme", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"full_name":"o/r"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	setGitHubAPIBaseURL(t, ts.URL)

	_, err := fetchGitHubRepoAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://github.com/o/r"))
	if err == nil {
		t.Fatal("expected error for 500 README")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %q, want HTTP 500", err.Error())
	}
}

// --- GitHub issue rendering --------------------------------------------------

func TestFetchGitHubIssue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues/5", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"title":"A bug","state":"open","body":"it broke",
			"html_url":"https://github.com/o/r/issues/5",
			"user":{"login":"alice"},
			"labels":[{"name":"bug"},{"name":""}],
			"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-02T00:00:00Z"
		}`))
	})
	mux.HandleFunc("/repos/o/r/issues/5/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"body":"a comment","user":{"login":"bob"}}]`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	setGitHubAPIBaseURL(t, ts.URL)

	out, err := fetchGitHubContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://github.com/o/r/issues/5"))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, want := range []string{"o/r #5: A bug", "Type: Issue", "State: open", "Author: @alice", "Labels: bug", "it broke", "Comments (1)", "by @bob", "a comment"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// --- GitHub PR rendering + pagination ---------------------------------------

func TestFetchGitHubPullRequestPaginatesComments(t *testing.T) {
	// Build per_page-sized first page so pagination follows to page 2.
	var firstPage strings.Builder
	firstPage.WriteString("[")
	for i := 0; i < gitHubPerPage; i++ {
		if i > 0 {
			firstPage.WriteString(",")
		}
		fmt.Fprintf(&firstPage, `{"body":"c%d","user":{"login":"u%d"}}`, i, i)
	}
	firstPage.WriteString("]")

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues/9", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"title":"feat","state":"open","body":"do thing","user":{"login":"alice"}}`))
	})
	mux.HandleFunc("/repos/o/r/issues/9/comments", func(w http.ResponseWriter, r *http.Request) {
		base := strings.TrimSuffix(gitHubAPIBaseURL, "/")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"body":"last","user":{"login":"zoe"}}]`))
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues/9/comments?per_page=%d&page=2>; rel="next"`, base, gitHubPerPage))
		_, _ = w.Write([]byte(firstPage.String()))
	})
	mux.HandleFunc("/repos/o/r/pulls/9", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"draft":false,"merged":true,"commits":3,"additions":10,"deletions":2,"changed_files":4,"base":{"ref":"main"},"head":{"ref":"feature"}}`))
	})
	mux.HandleFunc("/repos/o/r/pulls/9/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"body":"review note","user":{"login":"rev"},"path":"main.go","position":12}]`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	setGitHubAPIBaseURL(t, ts.URL)

	out, err := fetchGitHubContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://github.com/o/r/pull/9"))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	wantCount := gitHubPerPage + 1
	if !strings.Contains(out, fmt.Sprintf("Comments (%d)", wantCount)) {
		t.Errorf("expected %d comments (paginated), got:\n%s", wantCount, out)
	}
	for _, want := range []string{"Type: Pull Request", "Merged: true", "Base branch: main", "Head branch: feature", "Review Comments (1)", "review note", "File: `main.go`", "by @zoe"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// --- GitHub error cases ------------------------------------------------------

func TestFetchGitHubJSON404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer ts.Close()
	setGitHubAPIBaseURL(t, ts.URL)

	var target map[string]any
	err := fetchGitHubJSON(context.Background(), ts.Client(), ts.URL+"/x", &target)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") || !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("err = %v, want HTTP 404 Not Found", err)
	}
}

func TestFetchGitHubJSON500(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()
	setGitHubAPIBaseURL(t, ts.URL)

	var target map[string]any
	err := fetchGitHubJSON(context.Background(), ts.Client(), ts.URL+"/x", &target)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v, want HTTP 500", err)
	}
}

func TestFetchGitHubJSONRateLimit429(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()
	setGitHubAPIBaseURL(t, ts.URL)

	var target map[string]any
	err := fetchGitHubJSON(context.Background(), ts.Client(), ts.URL+"/x", &target)
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("err = %v, want rate limit error", err)
	}
}

func TestFetchGitHubJSONRateLimit403(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()
	setGitHubAPIBaseURL(t, ts.URL)

	var target map[string]any
	err := fetchGitHubJSON(context.Background(), ts.Client(), ts.URL+"/x", &target)
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("err = %v, want rate limit error", err)
	}
}

func TestFetchGitHubJSONMalformed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer ts.Close()
	setGitHubAPIBaseURL(t, ts.URL)

	var target map[string]any
	err := fetchGitHubJSON(context.Background(), ts.Client(), ts.URL+"/x", &target)
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("err = %v, want decode error", err)
	}
}

// --- Reddit thread parsing ---------------------------------------------------

const redditSampleJSON = `[
  {"kind":"Listing","data":{"children":[
    {"kind":"t3","data":{
      "id":"abc","subreddit":"golang","title":"Why Go?","author":"op",
      "score":100,"num_comments":2,"created_utc":1700000000,
      "permalink":"/r/golang/comments/abc/why_go/","selftext":"because it is nice"
    }}
  ]}},
  {"kind":"Listing","data":{"children":[
    {"kind":"t1","data":{"id":"c1","author":"bob","score":10,"body":"top comment","created_utc":1700000001,
      "replies":{"kind":"Listing","data":{"children":[
        {"kind":"t1","data":{"id":"r1","author":"carol","score":2,"body":"a reply","created_utc":1700000002}}
      ]}}}},
    {"kind":"t1","data":{"id":"c2","author":"dave","score":5,"body":"second comment","created_utc":1700000003,"replies":""}}
  ]}}
]`

func TestFetchRedditThread(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ".json") {
			t.Errorf("expected .json suffix, got %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(redditSampleJSON))
	}))
	defer ts.Close()
	tsURL := mustParseURL(t, ts.URL)
	setRedditAPITarget(t, tsURL.Scheme, tsURL.Host)

	out, err := fetchRedditContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://www.reddit.com/r/golang/comments/abc/why_go/"))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, want := range []string{"# Why Go?", "Subreddit: r/golang", "Author: u/op", "because it is nice", "by u/bob", "top comment", "Replies", "u/carol", "a reply", "by u/dave"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRedditMalformedRepliesSkipped(t *testing.T) {
	// "replies" is a bogus number rather than a listing object: the subtree
	// must be skipped, not crash the whole render.
	const payload = `[
	  {"kind":"Listing","data":{"children":[
	    {"kind":"t3","data":{"id":"x","title":"T","author":"op","selftext":"body","created_utc":1700000000}}
	  ]}},
	  {"kind":"Listing","data":{"children":[
	    {"kind":"t1","data":{"id":"c1","author":"bob","body":"hi","created_utc":1700000001,"replies":12345}}
	  ]}}
	]`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer ts.Close()
	tsURL := mustParseURL(t, ts.URL)
	setRedditAPITarget(t, tsURL.Scheme, tsURL.Host)

	out, err := fetchRedditContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://www.reddit.com/r/x/comments/x/t/"))
	if err != nil {
		t.Fatalf("malformed replies should not error: %v", err)
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("expected comment body, got:\n%s", out)
	}
	if strings.Contains(out, "Replies") {
		t.Errorf("malformed replies should yield no replies section:\n%s", out)
	}
}

func TestRedditTopCommentLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString(`[{"kind":"Listing","data":{"children":[{"kind":"t3","data":{"id":"x","title":"T","author":"op","created_utc":1700000000}}]}},`)
	b.WriteString(`{"kind":"Listing","data":{"children":[`)
	total := redditTopCommentLimit + 5
	for i := 0; i < total; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"kind":"t1","data":{"id":"c%d","author":"u%d","body":"body%d","created_utc":1700000001}}`, i, i, i)
	}
	b.WriteString(`]}}]`)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(b.String()))
	}))
	defer ts.Close()
	tsURL := mustParseURL(t, ts.URL)
	setRedditAPITarget(t, tsURL.Scheme, tsURL.Host)

	out, err := fetchRedditContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://www.reddit.com/r/x/comments/x/t/"))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(out, fmt.Sprintf("%d more top-level comments omitted", total-redditTopCommentLimit)) {
		t.Errorf("expected omitted-count marker, got:\n%s", out)
	}
	if strings.Contains(out, fmt.Sprintf("Comment %d by", redditTopCommentLimit+1)) {
		t.Errorf("rendered more than the cap:\n%s", out)
	}
}

func TestFetchRedditThreadHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`blocked`))
	}))
	defer ts.Close()
	tsURL := mustParseURL(t, ts.URL)
	setRedditAPITarget(t, tsURL.Scheme, tsURL.Host)

	_, err := fetchRedditContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://www.reddit.com/r/x/comments/x/t/"))
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("err = %v, want HTTP 403", err)
	}
}

func TestFetchRedditThreadMalformedTopLevel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not an array`))
	}))
	defer ts.Close()
	tsURL := mustParseURL(t, ts.URL)
	setRedditAPITarget(t, tsURL.Scheme, tsURL.Host)

	_, err := fetchRedditContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://www.reddit.com/r/x/comments/x/t/"))
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("err = %v, want decode error", err)
	}
}

// --- Generic HTML: redirects, non-HTML, size cap ----------------------------

func TestReadRedirectLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always redirect to itself -> exceeds maxHTTPRedirectCount.
		http.Redirect(w, r, r.URL.String()+"x", http.StatusFound)
	}))
	defer ts.Close()

	_, err := Read(context.Background(), ts.URL)
	if err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("err = %v, want too many redirects", err)
	}
}

func TestReadSizeCap(t *testing.T) {
	// Serve a non-HTML body larger than the cap; output must be truncated.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		chunk := strings.Repeat("a", 1<<20)
		for i := 0; i < 12; i++ { // 12 MiB, over the 10 MiB cap
			_, _ = w.Write([]byte(chunk))
		}
	}))
	defer ts.Close()

	out, err := Read(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(out) != maxResponseBodyBytes {
		t.Fatalf("len(out) = %d, want cap %d", len(out), maxResponseBodyBytes)
	}
}

func TestReadHTMLSizeCapDoesNotError(t *testing.T) {
	// An oversized HTML body should still parse (truncated) without error.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><h1>Title</h1><p>"))
		_, _ = w.Write([]byte(strings.Repeat("x", 11<<20)))
		_, _ = w.Write([]byte("</p></body></html>"))
	}))
	defer ts.Close()

	out, err := Read(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "# Title") {
		t.Errorf("missing title in truncated HTML output")
	}
}

func TestNewHTTPClientTimeout(t *testing.T) {
	c := newHTTPClient()
	if c.Timeout != defaultHTTPTimeout {
		t.Errorf("timeout = %v, want %v", c.Timeout, defaultHTTPTimeout)
	}
}
