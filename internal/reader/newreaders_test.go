package reader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- base URL overrides ------------------------------------------------------

func setHackerNewsAPIBaseURL(t *testing.T, base string) {
	t.Helper()
	prev := hackerNewsAPIBaseURL
	hackerNewsAPIBaseURL = base
	t.Cleanup(func() { hackerNewsAPIBaseURL = prev })
}

func setStackOverflowAPIBaseURL(t *testing.T, base string) {
	t.Helper()
	prev := stackOverflowAPIBaseURL
	stackOverflowAPIBaseURL = base
	t.Cleanup(func() { stackOverflowAPIBaseURL = prev })
}

func setArxivAPIBaseURL(t *testing.T, base string) {
	t.Helper()
	prev := arxivAPIBaseURL
	arxivAPIBaseURL = base
	t.Cleanup(func() { arxivAPIBaseURL = prev })
}

// --- URL detection -----------------------------------------------------------

func TestIsHackerNewsItemURL(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://news.ycombinator.com/item?id=12345", true},
		{"https://news.ycombinator.com/item?id=12345&foo=bar", true},
		{"https://news.ycombinator.com/item", false},
		{"https://news.ycombinator.com/news", false},
		{"https://example.com/item?id=1", false},
	}
	for _, tc := range cases {
		if got := isHackerNewsItemURL(mustParseURL(t, tc.raw)); got != tc.want {
			t.Errorf("isHackerNewsItemURL(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestIsStackOverflowQuestionURL(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://stackoverflow.com/questions/12345/how-to-go", true},
		{"https://stackoverflow.com/questions/12345", true},
		{"https://stackoverflow.com/questions/tagged/go", false},
		{"https://stackoverflow.com/users/1/foo", false},
		{"https://example.com/questions/1/x", false},
	}
	for _, tc := range cases {
		if got := isStackOverflowQuestionURL(mustParseURL(t, tc.raw)); got != tc.want {
			t.Errorf("isStackOverflowQuestionURL(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestIsArxivURL(t *testing.T) {
	cases := []struct {
		raw    string
		want   bool
		wantID string
	}{
		{"https://arxiv.org/abs/2103.00020", true, "2103.00020"},
		{"https://arxiv.org/pdf/2103.00020", true, "2103.00020"},
		{"https://arxiv.org/pdf/2103.00020.pdf", true, "2103.00020"},
		{"https://arxiv.org/abs/cs/0112017", true, "cs/0112017"},
		{"https://arxiv.org/list/cs.AI/recent", false, ""},
		{"https://example.com/abs/1", false, ""},
	}
	for _, tc := range cases {
		u := mustParseURL(t, tc.raw)
		if got := isArxivURL(u); got != tc.want {
			t.Errorf("isArxivURL(%q) = %v, want %v", tc.raw, got, tc.want)
		}
		if tc.want {
			if id, _ := parseArxivID(u); id != tc.wantID {
				t.Errorf("parseArxivID(%q) = %q, want %q", tc.raw, id, tc.wantID)
			}
		}
	}
}

// --- Hacker News -------------------------------------------------------------

func TestFetchHackerNewsContent(t *testing.T) {
	items := map[string]string{
		"100": `{"id":100,"type":"story","by":"alice","time":1700000000,"title":"Show HN: my thing","url":"https://example.com/thing","score":256,"text":"some <b>story</b> text","kids":[101,102]}`,
		"101": `{"id":101,"type":"comment","by":"bob","time":1700000001,"text":"great work","kids":[103]}`,
		"102": `{"id":102,"type":"comment","by":"carol","time":1700000002,"text":"second comment"}`,
		"103": `{"id":103,"type":"comment","by":"dave","time":1700000003,"text":"a reply"}`,
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/item/"), ".json")
		body, ok := items[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()
	setHackerNewsAPIBaseURL(t, ts.URL)

	out, err := fetchHackerNewsContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://news.ycombinator.com/item?id=100"))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, want := range []string{
		"# Show HN: my thing",
		"Author: alice",
		"Score: 256",
		"URL: https://example.com/thing",
		"some <b>story</b> text",
		"Comment 1 by bob",
		"great work",
		"Replies",
		"dave",
		"a reply",
		"Comment 2 by carol",
		"second comment",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestFetchHackerNewsTopCommentLimit(t *testing.T) {
	total := hackerNewsTopCommentLimit + 5
	kids := make([]string, 0, total)
	items := map[string]string{}
	for i := 0; i < total; i++ {
		id := 1000 + i
		kids = append(kids, fmt.Sprintf("%d", id))
		items[fmt.Sprintf("%d", id)] = fmt.Sprintf(`{"id":%d,"type":"comment","by":"u%d","time":1700000000,"text":"body%d"}`, id, i, i)
	}
	items["1"] = fmt.Sprintf(`{"id":1,"type":"story","by":"op","time":1700000000,"title":"T","score":1,"kids":[%s]}`, strings.Join(kids, ","))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/item/"), ".json")
		body, ok := items[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()
	setHackerNewsAPIBaseURL(t, ts.URL)

	out, err := fetchHackerNewsContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://news.ycombinator.com/item?id=1"))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(out, fmt.Sprintf("%d more top-level comments omitted", total-hackerNewsTopCommentLimit)) {
		t.Errorf("expected omitted-count marker, got:\n%s", out)
	}
	if strings.Contains(out, fmt.Sprintf("Comment %d by", hackerNewsTopCommentLimit+1)) {
		t.Errorf("rendered more than the cap:\n%s", out)
	}
}

func TestFetchHackerNewsHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer ts.Close()
	setHackerNewsAPIBaseURL(t, ts.URL)

	_, err := fetchHackerNewsContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://news.ycombinator.com/item?id=1"))
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v, want HTTP 500", err)
	}
}

// --- Stack Overflow ----------------------------------------------------------

func TestFetchStackOverflowContent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/questions/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"question_id":42,"title":"How to defer in Go?","body":"<p>I want to know about <code>defer</code>.</p>","score":33,"answer_count":2,"tags":["go","defer"],"link":"https://stackoverflow.com/questions/42","creation_date":1700000000,"owner":{"display_name":"asker"}}],"quota_remaining":100}`))
	})
	mux.HandleFunc("/questions/42/answers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"answer_id":1,"body":"<p>Use it like <strong>this</strong>.</p>","score":50,"is_accepted":false,"creation_date":1700000001,"owner":{"display_name":"high"}},
			{"answer_id":2,"body":"<p>The accepted way.</p>","score":10,"is_accepted":true,"creation_date":1700000002,"owner":{"display_name":"acc"}}
		]}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	setStackOverflowAPIBaseURL(t, ts.URL)

	out, err := fetchStackOverflowContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://stackoverflow.com/questions/42/how-to-defer"))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, want := range []string{
		"# How to defer in Go?",
		"Author: asker",
		"Score: 33",
		"Tags: go, defer",
		"`defer`",
		"## Answers (2)",
		"Answer 1 by acc",
		"(accepted)",
		"The accepted way.",
		"Answer 2 by high",
		"**this**",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// HTML must have been converted to Markdown, not left raw.
	if strings.Contains(out, "<strong>") || strings.Contains(out, "<code>") {
		t.Errorf("expected HTML to be converted to Markdown, got:\n%s", out)
	}
}

func TestFetchStackOverflowAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error_id":502,"error_name":"throttle_violation","error_message":"too many requests"}`))
	}))
	defer ts.Close()
	setStackOverflowAPIBaseURL(t, ts.URL)

	_, err := fetchStackOverflowContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://stackoverflow.com/questions/42/x"))
	if err == nil {
		t.Fatal("expected error for API error payload")
	}
	if !strings.Contains(err.Error(), "throttle_violation") || !strings.Contains(err.Error(), "api error 502") {
		t.Errorf("err = %q, want api error mention", err.Error())
	}
}

func TestFetchStackOverflowHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("nope"))
	}))
	defer ts.Close()
	setStackOverflowAPIBaseURL(t, ts.URL)

	_, err := fetchStackOverflowContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://stackoverflow.com/questions/42/x"))
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("err = %v, want HTTP 502", err)
	}
}

// --- arXiv -------------------------------------------------------------------

const arxivSampleAtom = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:arxiv="http://arxiv.org/schemas/atom">
  <entry>
    <id>http://arxiv.org/abs/2103.00020v1</id>
    <title>Learning Transferable Visual Models
    From Natural Language Supervision</title>
    <summary>  We present a simple approach
    to learning visual concepts.  </summary>
    <published>2021-02-26T18:54:14Z</published>
    <updated>2021-02-26T18:54:14Z</updated>
    <author><name>Alec Radford</name></author>
    <author><name>Jong Wook Kim</name></author>
    <arxiv:doi>10.1234/example</arxiv:doi>
    <arxiv:comment>30 pages</arxiv:comment>
    <link href="http://arxiv.org/abs/2103.00020v1" rel="alternate" type="text/html"/>
    <link title="pdf" href="http://arxiv.org/pdf/2103.00020v1" rel="related" type="application/pdf"/>
    <arxiv:primary_category term="cs.CV"/>
    <category term="cs.CV"/>
    <category term="cs.LG"/>
  </entry>
</feed>`

func TestFetchArxivContent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("id_list"); got != "2103.00020" {
			t.Errorf("id_list = %q, want 2103.00020", got)
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(arxivSampleAtom))
	}))
	defer ts.Close()
	setArxivAPIBaseURL(t, ts.URL)

	out, err := fetchArxivContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://arxiv.org/abs/2103.00020"))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, want := range []string{
		"# Learning Transferable Visual Models From Natural Language Supervision",
		"Authors: Alec Radford, Jong Wook Kim",
		"Primary category: cs.CV",
		"Categories: cs.CV, cs.LG",
		"Published: 2021-02-26T18:54:14Z",
		"DOI: 10.1234/example",
		"Comment: 30 pages",
		"Link: https://arxiv.org/abs/2103.00020",
		"PDF: http://arxiv.org/pdf/2103.00020v1",
		"## Abstract",
		"We present a simple approach to learning visual concepts.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestFetchArxivPDFURLNormalizes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(arxivSampleAtom))
	}))
	defer ts.Close()
	setArxivAPIBaseURL(t, ts.URL)

	// A /pdf/ID.pdf URL must be accepted and normalized to the abstract.
	out, err := fetchArxivContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://arxiv.org/pdf/2103.00020.pdf"))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(out, "Link: https://arxiv.org/abs/2103.00020") {
		t.Errorf("expected normalized abstract link, got:\n%s", out)
	}
}

func TestFetchArxivNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"></feed>`))
	}))
	defer ts.Close()
	setArxivAPIBaseURL(t, ts.URL)

	_, err := fetchArxivContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://arxiv.org/abs/9999.99999"))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestFetchArxivHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	setArxivAPIBaseURL(t, ts.URL)

	_, err := fetchArxivContentAsMarkdown(context.Background(), ts.Client(), mustParseURL(t, "https://arxiv.org/abs/1"))
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("err = %v, want HTTP 503", err)
	}
}

// --- Dispatch routing --------------------------------------------------------

func TestReadDispatchRoutesByHost(t *testing.T) {
	// Each backing server only serves its own reader's shape; routing to the
	// wrong reader would fail to produce the expected marker.
	hn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":7,"type":"story","by":"op","time":1700000000,"title":"HN Routed","score":1}`))
	}))
	defer hn.Close()
	setHackerNewsAPIBaseURL(t, hn.URL)

	so := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/answers") {
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"question_id":7,"title":"SO Routed","body":"<p>q</p>","score":1,"owner":{"display_name":"x"}}]}`))
	}))
	defer so.Close()
	setStackOverflowAPIBaseURL(t, so.URL)

	ax := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Replace(arxivSampleAtom, "Learning Transferable Visual Models\n    From Natural Language Supervision", "ARXIV Routed", 1)))
	}))
	defer ax.Close()
	setArxivAPIBaseURL(t, ax.URL)

	cases := []struct {
		raw  string
		want string
	}{
		{"https://news.ycombinator.com/item?id=7", "# HN Routed"},
		{"https://stackoverflow.com/questions/7/title", "# SO Routed"},
		{"https://arxiv.org/abs/2103.00020", "# ARXIV Routed"},
	}
	for _, tc := range cases {
		out, err := Read(context.Background(), tc.raw)
		if err != nil {
			t.Fatalf("Read(%q): %v", tc.raw, err)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("Read(%q) missing %q in:\n%s", tc.raw, tc.want, out)
		}
	}
}
