package common

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// HTMLSnippet extracts bounded plain text from API-provided HTML excerpts.
func HTMLSnippet(raw string) string {
	z := html.NewTokenizer(strings.NewReader(raw))
	var b strings.Builder
	skip := false
	for {
		switch z.Next() {
		case html.ErrorToken:
			text := []rune(strings.Join(strings.Fields(b.String()), " "))
			if len(text) > 800 {
				return string(text[:800]) + "…"
			}
			return string(text)
		case html.TextToken:
			if !skip {
				b.Write(z.Text())
			}
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			token := z.Token()
			tag := token.Data
			if tag == "script" || tag == "style" {
				skip = token.Type != html.EndTagToken
			}
			switch tag {
			case "p", "div", "br", "li", "pre", "blockquote", "tr", "td", "h1", "h2", "h3", "jats:p":
				b.WriteByte(' ')
			}
		}
	}
}

// FreshnessSince maps the shared relative freshness vocabulary to a cutoff.
// A zero time means no publication-date filter.
func FreshnessSince(value string, now time.Time) (time.Time, error) {
	var days int
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return time.Time{}, nil
	case "hour":
		return now.Add(-time.Hour), nil
	case "pd", "day":
		days = 1
	case "pw", "week":
		days = 7
	case "pm", "month":
		days = 30
	case "py", "year":
		days = 365
	default:
		return time.Time{}, fmt.Errorf("unsupported freshness %q; use pd, pw, pm, py, hour, day, week, month, or year", value)
	}
	return now.AddDate(0, 0, -days), nil
}
