package search

import (
	"strings"
	"time"
)

// publishedLayouts are the date formats seen at the head of HTML providers'
// snippets, most specific first. Yahoo and DuckDuckGo both prefix a result's
// description with its publication date rather than exposing it as a field.
var publishedLayouts = []string{
	"Jan 2, 2006",
	"Jan 2 2006",
	"2 Jan 2006",
	"January 2, 2006",
	"2006-01-02",
	"02/01/2006",
}

// publishedSeparators are the glyphs a provider puts between the date prefix and
// the snippet body.
var publishedSeparators = []string{" · ", " — ", " – ", " - "}

// SplitPublished pulls a leading publication date off a result snippet.
//
// It returns the date normalised to YYYY-MM-DD plus the snippet with the prefix
// removed, or ("", description) when there is no date to extract. Without this
// the date stays buried in the description and every Result.Published from an
// HTML provider is empty, leaving a caller unable to tell a 2015 page from a
// current one.
func SplitPublished(description string) (published, rest string) {
	trimmed := strings.TrimSpace(description)
	if trimmed == "" {
		return "", description
	}

	for _, sep := range publishedSeparators {
		prefix, remainder, found := strings.Cut(trimmed, sep)
		if !found {
			continue
		}
		prefix = strings.TrimSpace(prefix)
		// A date prefix is short; anything longer is a sentence that happens to
		// contain the separator.
		if prefix == "" || len(prefix) > 20 {
			continue
		}
		for _, layout := range publishedLayouts {
			parsed, err := time.Parse(layout, prefix)
			if err != nil {
				continue
			}
			return parsed.Format("2006-01-02"), strings.TrimSpace(remainder)
		}
	}
	return "", description
}
