// Package htmlutil provides small helpers for traversing and extracting data
// from golang.org/x/net/html node trees, shared across HTML-scraping providers.
package htmlutil

import (
	"strings"

	"golang.org/x/net/html"
)

// FindElement returns the first descendant of root (excluding root itself) for
// which match returns true, searching depth-first. It returns nil if no node
// matches.
func FindElement(root *html.Node, match func(*html.Node) bool) *html.Node {
	if root == nil {
		return nil
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && match(c) {
			return c
		}
		if found := FindElement(c, match); found != nil {
			return found
		}
	}
	return nil
}

// TextContent returns the concatenated text of all text nodes under n.
func TextContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// HasClass reports whether n carries the given CSS class in its class attribute.
func HasClass(n *html.Node, class string) bool {
	for _, a := range n.Attr {
		if a.Key != "class" {
			continue
		}
		for _, c := range strings.Fields(a.Val) {
			if c == class {
				return true
			}
		}
	}
	return false
}

// Attr returns the value of the named attribute on n, or "" if absent.
func Attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// CollapseWhitespace trims s and collapses every run of whitespace into a
// single space.
func CollapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
