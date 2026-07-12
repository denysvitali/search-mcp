package reader

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
)

// maxFeedItems caps how many feed entries are rendered so a huge feed cannot
// flood the caller.
const maxFeedItems = 50

// feedSniffBytes is how far into the body we look for an rss/feed root
// element before attempting a full XML parse.
const feedSniffBytes = 1024

type rssFeed struct {
	Channel struct {
		Title       string    `xml:"title"`
		Link        string    `xml:"link"`
		Description string    `xml:"description"`
		Items       []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
}

type atomFeed struct {
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title     string     `xml:"title"`
	Links     []atomLink `xml:"link"`
	Published string     `xml:"published"`
	Updated   string     `xml:"updated"`
	Summary   string     `xml:"summary"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

// looksLikeFeed cheaply detects RSS/Atom bodies so we only pay for XML
// parsing when the page plausibly is a feed.
func looksLikeFeed(contentType string, body []byte) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if strings.Contains(mediaType, "rss") || strings.Contains(mediaType, "atom") {
		return true
	}
	if !strings.Contains(mediaType, "xml") && mediaType != "" && mediaType != "text/plain" {
		return false
	}
	head := body
	if len(head) > feedSniffBytes {
		head = head[:feedSniffBytes]
	}
	return bytes.Contains(head, []byte("<rss")) || bytes.Contains(head, []byte("<feed"))
}

// renderFeed converts an RSS or Atom body into a Markdown digest. It reports
// false when the body is not a parseable feed.
func renderFeed(contentType string, body []byte) (string, bool) {
	if !looksLikeFeed(contentType, body) {
		return "", false
	}
	if md, ok := renderRSS(body); ok {
		return md, true
	}
	return renderAtom(body)
}

func renderRSS(body []byte) (string, bool) {
	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil || (feed.Channel.Title == "" && len(feed.Channel.Items) == 0) {
		return "", false
	}
	var b strings.Builder
	writeFeedHeader(&b, feed.Channel.Title, feed.Channel.Description, len(feed.Channel.Items))
	for i, item := range feed.Channel.Items {
		if i >= maxFeedItems {
			break
		}
		writeFeedItem(&b, item.Title, item.Link, item.PubDate, item.Description)
	}
	return cleanMarkdown(b.String()), true
}

func renderAtom(body []byte) (string, bool) {
	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil || (feed.Title == "" && len(feed.Entries) == 0) {
		return "", false
	}
	var b strings.Builder
	writeFeedHeader(&b, feed.Title, "", len(feed.Entries))
	for i, entry := range feed.Entries {
		if i >= maxFeedItems {
			break
		}
		date := entry.Published
		if date == "" {
			date = entry.Updated
		}
		writeFeedItem(&b, entry.Title, atomEntryLink(entry), date, entry.Summary)
	}
	return cleanMarkdown(b.String()), true
}

func atomEntryLink(entry atomEntry) string {
	for _, link := range entry.Links {
		if link.Rel == "" || link.Rel == "alternate" {
			return link.Href
		}
	}
	if len(entry.Links) > 0 {
		return entry.Links[0].Href
	}
	return ""
}

func writeFeedHeader(b *strings.Builder, title, description string, total int) {
	if title != "" {
		fmt.Fprintf(b, "# %s\n\n", strings.TrimSpace(title))
	}
	if description != "" {
		fmt.Fprintf(b, "%s\n\n", strings.TrimSpace(description))
	}
	if total > maxFeedItems {
		fmt.Fprintf(b, "Showing the first %d of %d entries.\n\n", maxFeedItems, total)
	}
}

func writeFeedItem(b *strings.Builder, title, link, date, description string) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "(untitled)"
	}
	if link != "" {
		fmt.Fprintf(b, "## [%s](%s)\n", title, strings.TrimSpace(link))
	} else {
		fmt.Fprintf(b, "## %s\n", title)
	}
	if date != "" {
		fmt.Fprintf(b, "*%s*\n", strings.TrimSpace(date))
	}
	if desc := feedDescriptionMarkdown(description); desc != "" {
		fmt.Fprintf(b, "\n%s\n", desc)
	}
	b.WriteString("\n")
}

// feedDescriptionMarkdown converts a (frequently HTML) feed description to
// Markdown, falling back to the raw text when conversion fails.
func feedDescriptionMarkdown(description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return ""
	}
	if markdown, err := convertHTMLToMarkdown(description); err == nil {
		return strings.TrimSpace(markdown)
	}
	return description
}

// prettyJSON re-indents JSON bodies so they are readable without tooling. It
// reports false for non-JSON content types or unparseable bodies.
func prettyJSON(contentType string, body []byte) (string, bool) {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if !strings.Contains(mediaType, "json") {
		return "", false
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(body), "", "  "); err != nil {
		return "", false
	}
	return buf.String(), true
}
