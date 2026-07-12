package reader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func TestSiteReaderPredicates(t *testing.T) {
	cases := []struct {
		url  string
		pred func(*url.URL) bool
		want bool
	}{
		{"https://en.wikipedia.org/wiki/Go_(programming_language)", isWikipediaArticleURL, true},
		{"https://de.wikipedia.org/wiki/Zürich", isWikipediaArticleURL, true},
		{"https://en.wikipedia.org/wiki/", isWikipediaArticleURL, false},
		{"https://wikipedia.org/about", isWikipediaArticleURL, false},
		{"https://github.com/owner/repo/blob/main/cmd/serve.go", isGitHubBlobURL, true},
		{"https://github.com/owner/repo/tree/main/cmd", isGitHubBlobURL, false},
		{"https://github.com/owner/repo", isGitHubBlobURL, false},
		{"https://lobste.rs/s/abc123/some_title", isLobstersStoryURL, true},
		{"https://lobste.rs/t/go", isLobstersStoryURL, false},
		{"https://gitlab.com/group/proj/-/issues/42", isGitLabIssuableURL, true},
		{"https://gitlab.com/group/sub/proj/-/merge_requests/7", isGitLabIssuableURL, true},
		{"https://gitlab.com/group/proj/-/pipelines/1", isGitLabIssuableURL, false},
		{"https://gitlab.com/group/proj", isGitLabIssuableURL, false},
		{"https://pkg.go.dev/golang.org/x/time/rate", isPkgGoDevURL, true},
		{"https://pkg.go.dev/", isPkgGoDevURL, false},
	}
	for _, tc := range cases {
		if got := tc.pred(mustParse(t, tc.url)); got != tc.want {
			t.Errorf("predicate(%s) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestFetchWikipediaContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("titles"); got != "Go_(programming_language)" {
			t.Errorf("titles = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"query":{"pages":[{"title":"Go (programming language)","extract":"Go is a statically typed language.","fullurl":"https://en.wikipedia.org/wiki/Go_(programming_language)"}]}}`))
	}))
	defer server.Close()
	wikipediaAPIBaseURL = server.URL
	t.Cleanup(func() { wikipediaAPIBaseURL = "" })

	got, err := Read(context.Background(), "https://en.wikipedia.org/wiki/Go_(programming_language)")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"# Go (programming language)", "statically typed", "wikipedia.org/wiki"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFetchWikipediaMissingArticle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"query":{"pages":[{"title":"Nope","missing":true}]}}`))
	}))
	defer server.Close()
	wikipediaAPIBaseURL = server.URL
	t.Cleanup(func() { wikipediaAPIBaseURL = "" })

	if _, err := Read(context.Background(), "https://en.wikipedia.org/wiki/Nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want not found", err)
	}
}

func TestFetchGitHubFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/owner/repo/main/cmd/serve.go" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("package cmd\n\nfunc main() {}\n"))
	}))
	defer server.Close()
	githubRawBaseURL = server.URL
	t.Cleanup(func() { githubRawBaseURL = "https://raw.githubusercontent.com" })

	got, err := Read(context.Background(), "https://github.com/owner/repo/blob/main/cmd/serve.go")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"# owner/repo: cmd/serve.go (at main)", "```go", "package cmd"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFetchGitHubMarkdownFileIsVerbatim(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("## Docs\n\nSome docs.\n"))
	}))
	defer server.Close()
	githubRawBaseURL = server.URL
	t.Cleanup(func() { githubRawBaseURL = "https://raw.githubusercontent.com" })

	got, err := Read(context.Background(), "https://github.com/owner/repo/blob/main/docs/README.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if strings.Contains(got, "```") {
		t.Errorf("markdown file should not be fenced:\n%s", got)
	}
	if !strings.Contains(got, "## Docs") {
		t.Errorf("markdown content missing:\n%s", got)
	}
}

func TestFetchLobstersStory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/s/abc123.json" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"title":"A story","url":"https://example.test/post","score":42,
			"created_at":"2026-07-01T00:00:00Z","submitter_user":"alice",
			"comment_count":2,"short_id_url":"https://lobste.rs/s/abc123",
			"comments":[
				{"comment":"<p>Top comment</p>","score":5,"indent_level":0,"commenting_user":"bob"},
				{"comment":"<p>Reply</p>","score":2,"indent_level":1,"commenting_user":{"username":"carol"}}
			]}`))
	}))
	defer server.Close()
	lobstersBaseURL = server.URL
	t.Cleanup(func() { lobstersBaseURL = "https://lobste.rs" })

	got, err := Read(context.Background(), "https://lobste.rs/s/abc123/a_story")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"# A story", "alice", "**bob** (score 5)", "Top comment", "> **carol**", "> Reply"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFetchGitLabIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/projects/group%2Fproj/issues/42":
			_, _ = w.Write([]byte(`{"title":"Broken thing","description":"It breaks.","state":"opened","created_at":"2026-07-01T00:00:00Z","web_url":"https://gitlab.com/group/proj/-/issues/42","author":{"username":"dave"},"user_notes_count":1}`))
		case "/projects/group%2Fproj/issues/42/notes":
			_, _ = w.Write([]byte(`[{"body":"Can reproduce.","system":false,"created_at":"2026-07-02T00:00:00Z","author":{"username":"erin"}},{"body":"changed the description","system":true,"author":{"username":"bot"}}]`))
		default:
			t.Errorf("unexpected path %q", r.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	gitlabAPIBaseURL = server.URL
	t.Cleanup(func() { gitlabAPIBaseURL = "https://gitlab.com/api/v4" })

	got, err := Read(context.Background(), "https://gitlab.com/group/proj/-/issues/42")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"# Issue #42: Broken thing", "dave", "It breaks.", "### erin", "Can reproduce."} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "changed the description") {
		t.Errorf("system note leaked:\n%s", got)
	}
}

func TestFetchPkgGoDevDocumentation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
<header>site chrome</header>
<div class="Documentation"><h2>Overview</h2><p>Package rate provides a rate limiter.</p></div>
<footer>footer chrome</footer>
</body></html>`))
	}))
	defer server.Close()
	pkgGoDevBaseURL = server.URL
	t.Cleanup(func() { pkgGoDevBaseURL = "https://pkg.go.dev" })

	got, err := Read(context.Background(), "https://pkg.go.dev/golang.org/x/time/rate")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"# golang.org/x/time/rate", "rate limiter"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "site chrome") {
		t.Errorf("chrome leaked:\n%s", got)
	}
}
