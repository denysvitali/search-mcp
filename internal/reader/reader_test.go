package reader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestReadValidatesScheme(t *testing.T) {
	if _, err := Read(context.Background(), "ftp://example.com/foo"); err == nil {
		t.Fatal("expected error for unsupported scheme")
	} else if !strings.Contains(err.Error(), "unsupported URL scheme") {
		t.Fatalf("error = %q, want unsupported scheme mention", err.Error())
	}
}

func TestReadGenericHTMLToMarkdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>
<header>HEADER NOISE</header>
<nav>NAV NOISE</nav>
<main>
  <h1>Hello</h1>
  <p>This is <strong>important</strong> text.</p>
  <a href="https://example.com/x">link</a>
</main>
<footer>FOOTER NOISE</footer>
</body></html>`))
	}))
	defer server.Close()

	out, err := Read(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "# Hello") {
		t.Errorf("missing H1: %q", out)
	}
	if !strings.Contains(out, "**important**") {
		t.Errorf("missing strong: %q", out)
	}
	if !strings.Contains(out, "https://example.com/x") {
		t.Errorf("missing link: %q", out)
	}
	for _, noise := range []string{"HEADER NOISE", "NAV NOISE", "FOOTER NOISE"} {
		if strings.Contains(out, noise) {
			t.Errorf("kept noise %q: %q", noise, out)
		}
	}
}

func TestReadNonHTMLReturnsRawBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("plain body"))
	}))
	defer server.Close()

	out, err := Read(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out != "plain body" {
		t.Errorf("out = %q, want plain body", out)
	}
}

func TestReadPropagatesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := Read(context.Background(), server.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error = %q, want HTTP 500 mention", err.Error())
	}
}

func TestIsRedditThreadURL(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://www.reddit.com/r/golang/comments/abc123/title/", true},
		{"https://reddit.com/r/golang/comments/abc123/", true},
		{"https://www.reddit.com/r/golang/", false},
		{"https://example.com/r/golang/comments/abc123/", false},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		if got := isRedditThreadURL(u); got != tc.want {
			t.Errorf("isRedditThreadURL(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestParseGitHubIssueOrPRURL(t *testing.T) {
	cases := []struct {
		raw      string
		wantOwn  string
		wantRepo string
		wantNum  int
		wantKind gitHubThreadKind
		wantOK   bool
	}{
		{"https://github.com/golang/go/issues/123", "golang", "go", 123, gitHubThreadIssue, true},
		{"https://github.com/golang/go/pull/456", "golang", "go", 456, gitHubThreadPullRequest, true},
		{"https://github.com/golang/go", "", "", 0, "", false},
		{"https://github.com/golang/go/discussions/789", "", "", 0, "", false},
		{"https://gitlab.com/foo/bar/issues/1", "", "", 0, "", false},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		owner, repo, num, kind, ok := parseGitHubIssueOrPRURL(u)
		if ok != tc.wantOK {
			t.Errorf("%q ok = %v, want %v", tc.raw, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if owner != tc.wantOwn || repo != tc.wantRepo || num != tc.wantNum || kind != tc.wantKind {
			t.Errorf("%q = (%q,%q,%d,%q), want (%q,%q,%d,%q)",
				tc.raw, owner, repo, num, kind, tc.wantOwn, tc.wantRepo, tc.wantNum, tc.wantKind)
		}
	}
}

func TestIsGitHubRepoURL(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://github.com/golang/go", true},
		{"https://github.com/golang/go/", true},
		{"https://github.com/golang/go/issues/1", false},
		{"https://github.com/golang", false},
		{"https://gitlab.com/foo/bar", false},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		if got := isGitHubRepoURL(u); got != tc.want {
			t.Errorf("isGitHubRepoURL(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
