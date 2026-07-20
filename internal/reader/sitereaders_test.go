package reader

import (
	"context"
	"encoding/base64"
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
		{"https://chromium-review.googlesource.com/c/chromium/src/+/8124214", isGerritChangeURL, true},
		{"https://gerrit-review.googlesource.com/c/+/361223", isGerritChangeURL, true},
		{"https://chromium-review.googlesource.com/c/chromium/src/+/8124214/2", isGerritChangeURL, true},
		{"https://chromium-review.googlesource.com/c/chromium/src/+/8124214/2/base/foo.cc", isGerritChangeURL, true},
		{"https://chromium-review.googlesource.com/c/chromium/src/+/I02dcdd3479cf6c739650da39bd8c3432261178bd", isGerritChangeURL, true},
		{"https://chromium-review.googlesource.com/c/chromium/src", isGerritChangeURL, false},
		{"https://chromium-review.googlesource.com/c/proj/+/notachange", isGerritChangeURL, false},
		{"https://chromium-review.googlesource.com/dashboard/self", isGerritChangeURL, false},
		{"https://chromium.googlesource.com/chromium/src/+/refs/tags/140.0.7339.207/base/logging.h", isGitilesURL, true},
		{"https://chromium.googlesource.com/chromium/src/+/refs/heads/main/", isGitilesURL, true},
		{"https://chromium.googlesource.com/chromium/src/+/refs/heads/main", isGitilesURL, true},
		{"https://chromium.googlesource.com/chromium/src", isGitilesURL, false},
		{"https://chromium.googlesource.com/", isGitilesURL, false},
		{"https://chromium-review.googlesource.com/c/chromium/src/+/8124214", isGitilesURL, false},
		{"https://pkg.go.dev/golang.org/x/time/rate", isPkgGoDevURL, true},
		{"https://pkg.go.dev/", isPkgGoDevURL, false},
		{"https://www.youtube.com/watch?v=abc123", isYouTubeVideoURL, true},
		{"https://youtu.be/abc123", isYouTubeVideoURL, true},
		{"https://www.youtube.com/shorts/abc123", isYouTubeVideoURL, true},
		{"https://www.youtube.com/channel/example", isYouTubeVideoURL, false},
	}
	for _, tc := range cases {
		if got := tc.pred(mustParse(t, tc.url)); got != tc.want {
			t.Errorf("predicate(%s) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestFetchYouTubeTranscript(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch":
			if got := r.URL.Query().Get("v"); got != "abc123" {
				t.Errorf("video ID = %q", got)
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<script>var ytInitialPlayerResponse = {"videoDetails":{"title":"Go talk","author":"Gopher","lengthSeconds":"125"},"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":[{"baseUrl":"` + serverURL + `/api/timedtext?lang=en","languageCode":"en","name":{"simpleText":"English"}}]}}};</script>`))
		case "/api/timedtext":
			if got := r.URL.Query().Get("fmt"); got != "json3" {
				t.Errorf("format = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"events":[{"tStartMs":0,"segs":[{"utf8":"Hello world"}]},{"tStartMs":65000,"segs":[{"utf8":"Second line"}]}]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	youTubeWatchBaseURL = server.URL + "/watch"
	t.Cleanup(func() { youTubeWatchBaseURL = "https://www.youtube.com/watch" })

	got, err := Read(context.Background(), "https://youtu.be/abc123")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"# Go talk", "Gopher", "02:05", "## Transcript (English)", "[00:00] Hello world", "[01:05] Second line"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFetchYouTubeWithoutCaptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<script>ytInitialPlayerResponse = {"videoDetails":{"title":"Silent video"}};</script>`))
	}))
	defer server.Close()
	youTubeWatchBaseURL = server.URL + "/watch"
	t.Cleanup(func() { youTubeWatchBaseURL = "https://www.youtube.com/watch" })

	got, err := Read(context.Background(), "https://www.youtube.com/watch?v=abc123")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(got, "No public transcript") {
		t.Errorf("missing no-transcript message:\n%s", got)
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

func TestFetchGerritChange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/changes/test%2Fproj~123":
			_, _ = w.Write([]byte(")]}'\n" + `{"_number":123,"change_id":"I02dcdd3479cf6c739650da39bd8c3432261178bd","project":"test/proj","branch":"refs/heads/main","subject":"Make it work","status":"NEW","created":"2026-07-01 10:00:00.000000000","updated":"2026-07-01 11:00:00.000000000","insertions":10,"deletions":5,"total_comment_count":1,"unresolved_comment_count":0,"owner":{"name":"alice@example.com","display_name":"Alice Liddell"},"labels":{"Code-Review":{"all":[{"value":2,"name":"bob"},{"value":0,"name":"carol"},{"name":"dave"}]}},"messages":[{"message":"Patch Set 1: LGTM","author":{"name":"bob"},"date":"2026-07-01 10:30:00.000000000","_revision_number":1}],"current_revision":"abc123","revisions":{"abc123":{"_number":1,"commit":{"message":"Make it work\n\nBug: 42\nChange-Id: I02dcdd3479cf6c739650da39bd8c3432261178bd"}}}}`))
		case "/changes/test%2Fproj~123/revisions/current/files":
			_, _ = w.Write([]byte(")]}'\n" + `{"/COMMIT_MSG":{"status":"A","lines_inserted":10},"src/main.go":{"status":"M","lines_inserted":10,"lines_deleted":5},"docs/pic.png":{"status":"A","binary":true}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	got, err := Read(context.Background(), server.URL+"/c/test/proj/+/123")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{
		"# Make it work",
		"- Change: 123 (I02dcdd3479cf6c739650da39bd8c3432261178bd)",
		"- Project: test/proj",
		"- Status: Open",
		"- Owner: Alice Liddell",
		"- Size: +10/-5",
		"Code-Review: +2 bob",
		"## Commit message",
		"Bug: 42",
		"- M src/main.go (+10/-5)",
		"- A docs/pic.png (binary)",
		"### bob (2026-07-01 10:30:00.000000000, patch set 1)",
		"Patch Set 1: LGTM",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"/COMMIT_MSG", "+0 carol", "no votes"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unwanted %q in:\n%s", unwanted, got)
		}
	}
}

func TestFetchGerritChangeBareIDFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/changes/wrong%2Fproj~123":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("Not found"))
		case "/changes/123":
			_, _ = w.Write([]byte(")]}'\n" + `{"_number":123,"project":"right/proj","subject":"Found via bare id","status":"MERGED","owner":{"name":"alice"}}`))
		case "/changes/123/revisions/current/files":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected path %q", r.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	got, err := Read(context.Background(), server.URL+"/c/wrong/proj/+/123")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"# Found via bare id", "- Status: Merged"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFetchGitilesFile(t *testing.T) {
	source := "package main\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/proj/+/refs/heads/main/src/hello.go" {
			t.Errorf("unexpected path %q", r.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("format") {
		case "TEXT":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(source))))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	got, err := Read(context.Background(), server.URL+"/proj/+/refs/heads/main/src/hello.go")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"# hello.go", "```go", "\tfmt.Println(\"hi\")"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFetchGitilesDirectoryJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/proj/+/refs/heads/main/tools" {
			t.Errorf("unexpected path %q", r.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("format") {
		case "JSON":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(")]}'\n" + `{"id":"abc","entries":[{"name":"gen.py","type":"blob"},{"name":"build","type":"tree"}]}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	got, err := Read(context.Background(), server.URL+"/proj/+/refs/heads/main/tools")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"# Directory listing", "- build/", "- gen.py"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFetchGitilesTreeListingViaText(t *testing.T) {
	listing := "100644 blob 275db31a8e5b4b0d3255bfef95601890afd80709\tREADME.md\n" +
		"040000 tree 6c5bb3c994d18b3fab340412530fcb61f864e75d\tsrc\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "TEXT" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(listing))))
	}))
	defer server.Close()

	got, err := Read(context.Background(), server.URL+"/proj/+/refs/heads/main/vendor")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"# Directory listing", "- src/", "- README.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFetchGitilesRenderedSourceFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("format") {
		case "TEXT", "JSON":
			w.WriteHeader(http.StatusBadRequest)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><body><div class="FileContents">` +
				`<div class="u-pre FileContents-line"><a class="u-lineNum">1</a><span class="FileContents-lineContents">key = value</span></div>` +
				`<div class="u-pre FileContents-line"><a class="u-lineNum">2</a><span class="FileContents-lineContents">  indented = true</span></div>` +
				`</div></body></html>`))
		}
	}))
	defer server.Close()

	got, err := Read(context.Background(), server.URL+"/proj/+/refs/heads/main/.config")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"# .config", "key = value", "indented = true"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
