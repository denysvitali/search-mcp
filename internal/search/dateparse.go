package search

import (
	"regexp"
	"strconv"
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
		if date, ok := relativeDate(prefix); ok {
			return date, strings.TrimSpace(remainder)
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

// now is the clock relative ages are resolved against; tests replace it.
var now = time.Now

// relativeAge matches the ages HTML engines print instead of a date, such as
// "15 hours ago", "a day ago" or "6 days ago".
var relativeAge = regexp.MustCompile(`^(\d{1,3}|an?) (minute|hour|day|week|month|year)s? ago$`)

// relativeDate resolves a relative age to a UTC calendar date. Months and
// years are approximate (30 and 365 days); a caller only needs to tell a
// fresh page from a stale one, and an exact date beats "N days ago".
func relativeDate(prefix string) (string, bool) {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "yesterday" {
		return now().UTC().AddDate(0, 0, -1).Format("2006-01-02"), true
	}
	m := relativeAge.FindStringSubmatch(prefix)
	if m == nil {
		return "", false
	}
	n := 1
	if m[1] != "a" && m[1] != "an" {
		n, _ = strconv.Atoi(m[1])
	}
	unit := map[string]time.Duration{
		"minute": time.Minute, "hour": time.Hour, "day": 24 * time.Hour,
		"week": 7 * 24 * time.Hour, "month": 30 * 24 * time.Hour, "year": 365 * 24 * time.Hour,
	}[m[2]]
	return now().UTC().Add(-time.Duration(n) * unit).Format("2006-01-02"), true
}
