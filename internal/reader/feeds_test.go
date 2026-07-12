package reader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
<title>Example Blog</title>
<link>https://example.test/</link>
<description>A feed of examples</description>
<item><title>First Post</title><link>https://example.test/first</link><pubDate>Mon, 06 Jul 2026 10:00:00 GMT</pubDate><description>&lt;p&gt;Hello &lt;b&gt;world&lt;/b&gt;&lt;/p&gt;</description></item>
<item><title>Second Post</title><link>https://example.test/second</link><description>Plain text summary</description></item>
</channel></rss>`

const sampleAtom = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
<title>Atom Feed</title>
<entry><title>Entry One</title><link rel="alternate" href="https://example.test/one"/><updated>2026-07-06T10:00:00Z</updated><summary>Summary one</summary></entry>
</feed>`

func TestRenderRSSFeed(t *testing.T) {
	got, ok := renderFeed("application/rss+xml", []byte(sampleRSS))
	if !ok {
		t.Fatal("renderFeed did not recognize RSS")
	}
	for _, want := range []string{"# Example Blog", "A feed of examples", "[First Post](https://example.test/first)", "Mon, 06 Jul 2026", "Hello **world**", "[Second Post](https://example.test/second)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderAtomFeed(t *testing.T) {
	got, ok := renderFeed("application/atom+xml", []byte(sampleAtom))
	if !ok {
		t.Fatal("renderFeed did not recognize Atom")
	}
	for _, want := range []string{"# Atom Feed", "[Entry One](https://example.test/one)", "2026-07-06", "Summary one"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderFeedRejectsNonFeeds(t *testing.T) {
	if _, ok := renderFeed("text/html", []byte("<html><body>nope</body></html>")); ok {
		t.Error("HTML misdetected as feed")
	}
	if _, ok := renderFeed("application/xml", []byte("<config><x>1</x></config>")); ok {
		t.Error("generic XML misdetected as feed")
	}
}

func TestPrettyJSON(t *testing.T) {
	got, ok := prettyJSON("application/json", []byte(`{"b":1,"a":[1,2]}`))
	if !ok {
		t.Fatal("prettyJSON did not recognize JSON")
	}
	if !strings.Contains(got, "\n  \"b\": 1") {
		t.Errorf("not indented:\n%s", got)
	}
	if _, ok := prettyJSON("application/json", []byte("not json")); ok {
		t.Error("invalid JSON should not pretty-print")
	}
	if _, ok := prettyJSON("text/plain", []byte(`{}`)); ok {
		t.Error("non-JSON content type should pass through")
	}
}

func TestReadRendersFeedAndJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(sampleRSS))
	})
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","items":[1,2,3]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	feed, err := Read(context.Background(), server.URL+"/feed")
	if err != nil {
		t.Fatalf("Read feed: %v", err)
	}
	if !strings.Contains(feed, "# Example Blog") {
		t.Errorf("feed digest missing:\n%s", feed)
	}

	api, err := Read(context.Background(), server.URL+"/api")
	if err != nil {
		t.Fatalf("Read json: %v", err)
	}
	if !strings.Contains(api, "\"status\": \"ok\"") {
		t.Errorf("json not pretty-printed:\n%s", api)
	}
}
