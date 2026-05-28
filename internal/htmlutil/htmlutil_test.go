package htmlutil

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func parse(t *testing.T, s string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

func TestCollapseWhitespace(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"   ":               "",
		"a":                 "a",
		"  a  b  ":          "a b",
		"a\t\nb\r  c":       "a b c",
		"already clean str": "already clean str",
	}
	for in, want := range cases {
		if got := CollapseWhitespace(in); got != want {
			t.Errorf("CollapseWhitespace(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindElementHasClassAttrText(t *testing.T) {
	doc := parse(t, `<div><p class="a b">hello <span>world</span></p><a href="/x" class="link">link</a></div>`)

	p := FindElement(doc, func(n *html.Node) bool {
		return n.Data == "p" && HasClass(n, "b")
	})
	if p == nil {
		t.Fatal("expected to find p.b")
	}
	if got := TextContent(p); got != "hello world" {
		t.Errorf("TextContent = %q, want %q", got, "hello world")
	}
	if !HasClass(p, "a") || HasClass(p, "c") {
		t.Error("HasClass mismatch")
	}

	a := FindElement(doc, func(n *html.Node) bool { return n.Data == "a" })
	if a == nil {
		t.Fatal("expected to find a")
	}
	if got := Attr(a, "href"); got != "/x" {
		t.Errorf("Attr href = %q, want /x", got)
	}
	if got := Attr(a, "missing"); got != "" {
		t.Errorf("Attr missing = %q, want empty", got)
	}
}

func TestFindElementNilRoot(t *testing.T) {
	if FindElement(nil, func(*html.Node) bool { return true }) != nil {
		t.Error("FindElement(nil) should be nil")
	}
}
