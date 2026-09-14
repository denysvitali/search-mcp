package common

import (
	"net/url"
	"strings"
	"testing"
)

func TestURLWithQueryMergesEndpointOptions(t *testing.T) {
	target, err := URLWithQuery("https://search.example.test/results?fixed=1&q=old", url.Values{
		"q":     {"new query"},
		"count": {"10"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("q"); got != "new query" {
		t.Errorf("q = %q, want new query", got)
	}
	if got := parsed.Query().Get("fixed"); got != "1" {
		t.Errorf("fixed = %q, want 1", got)
	}
	if got := parsed.Query().Get("count"); got != "10" {
		t.Errorf("count = %q, want 10", got)
	}
	if strings.Count(target, "?") != 1 {
		t.Errorf("target = %q, want one query separator", target)
	}
}

func TestURLWithQueryRejectsNonHTTPEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "/relative", "ftp://search.example.test/results"} {
		if _, err := URLWithQuery(endpoint, nil); err == nil {
			t.Errorf("URLWithQuery(%q) accepted invalid endpoint", endpoint)
		}
	}
}
