package reader

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// arxivAPIBaseURL is the arXiv Atom query API root. It is a var (not a const)
// so tests can point it at an httptest server.
var arxivAPIBaseURL = "http://export.arxiv.org/api/query"

type arxivFeed struct {
	XMLName xml.Name     `xml:"feed"`
	Entries []arxivEntry `xml:"entry"`
}

type arxivEntry struct {
	ID        string         `xml:"id"`
	Title     string         `xml:"title"`
	Summary   string         `xml:"summary"`
	Published string         `xml:"published"`
	Updated   string         `xml:"updated"`
	Authors   []arxivAuthor  `xml:"author"`
	Links     []arxivLink    `xml:"link"`
	Category  []arxivCategry `xml:"category"`
	Primary   arxivCategry   `xml:"primary_category"`
	Comment   string         `xml:"comment"`
	DOI       string         `xml:"doi"`
}

type arxivAuthor struct {
	Name string `xml:"name"`
}

type arxivLink struct {
	Href  string `xml:"href,attr"`
	Rel   string `xml:"rel,attr"`
	Type  string `xml:"type,attr"`
	Title string `xml:"title,attr"`
}

type arxivCategry struct {
	Term string `xml:"term,attr"`
}

func isArxivURL(parsedURL *url.URL) bool {
	host := strings.ToLower(parsedURL.Hostname())
	if host != "arxiv.org" {
		return false
	}
	_, ok := parseArxivID(parsedURL)
	return ok
}

// parseArxivID extracts the arXiv identifier from an /abs/ID or /pdf/ID URL,
// normalizing a trailing ".pdf" away.
func parseArxivID(parsedURL *url.URL) (string, bool) {
	segments := pathSegments(parsedURL.Path)
	if len(segments) < 2 {
		return "", false
	}
	if segments[0] != "abs" && segments[0] != "pdf" {
		return "", false
	}
	id := strings.Join(segments[1:], "/")
	id = strings.TrimSuffix(id, ".pdf")
	if strings.TrimSpace(id) == "" {
		return "", false
	}
	return id, true
}

func fetchArxivContentAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	id, ok := parseArxivID(parsedURL)
	if !ok {
		return "", fmt.Errorf("unsupported arxiv url: %s", parsedURL.String())
	}

	endpoint, err := arxivQueryEndpoint(id)
	if err != nil {
		return "", err
	}

	req, err := newRequest(ctx, endpoint, "application/atom+xml")
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("arxiv request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("arxiv request failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var feed arxivFeed
	if err := xml.NewDecoder(limitedBody(resp.Body)).Decode(&feed); err != nil {
		return "", fmt.Errorf("failed to parse arxiv atom response: %w", err)
	}
	if len(feed.Entries) == 0 {
		return "", fmt.Errorf("arxiv paper %s not found", id)
	}

	return renderArxivMarkdown(id, &feed.Entries[0]), nil
}

func arxivQueryEndpoint(id string) (string, error) {
	parsed, err := url.Parse(arxivAPIBaseURL)
	if err != nil {
		return "", fmt.Errorf("invalid arxiv api base url: %w", err)
	}
	query := parsed.Query()
	query.Set("id_list", id)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func renderArxivMarkdown(id string, entry *arxivEntry) string {
	var builder strings.Builder

	title := arxivCleanText(entry.Title)
	if title == "" {
		title = fmt.Sprintf("arXiv:%s", id)
	}
	fmt.Fprintf(&builder, "# %s\n\n", title)

	if names := arxivAuthorNames(entry.Authors); len(names) > 0 {
		fmt.Fprintf(&builder, "- Authors: %s\n", strings.Join(names, ", "))
	}
	if term := strings.TrimSpace(entry.Primary.Term); term != "" {
		fmt.Fprintf(&builder, "- Primary category: %s\n", term)
	}
	if cats := arxivCategories(entry.Category); len(cats) > 0 {
		fmt.Fprintf(&builder, "- Categories: %s\n", strings.Join(cats, ", "))
	}
	if published := arxivFormatTime(entry.Published); published != "" {
		fmt.Fprintf(&builder, "- Published: %s\n", published)
	}
	if updated := arxivFormatTime(entry.Updated); updated != "" && updated != arxivFormatTime(entry.Published) {
		fmt.Fprintf(&builder, "- Updated: %s\n", updated)
	}
	if doi := strings.TrimSpace(entry.DOI); doi != "" {
		fmt.Fprintf(&builder, "- DOI: %s\n", doi)
	}
	fmt.Fprintf(&builder, "- Link: https://arxiv.org/abs/%s\n", id)
	if pdf := arxivPDFLink(entry.Links); pdf != "" {
		fmt.Fprintf(&builder, "- PDF: %s\n", pdf)
	}
	if comment := arxivCleanText(entry.Comment); comment != "" {
		fmt.Fprintf(&builder, "- Comment: %s\n", comment)
	}
	builder.WriteString("\n")

	builder.WriteString("## Abstract\n\n")
	abstract := arxivCleanText(entry.Summary)
	if abstract == "" {
		builder.WriteString("_No abstract available._\n")
	} else {
		builder.WriteString(abstract)
		builder.WriteString("\n")
	}

	return cleanMarkdown(builder.String())
}

func arxivAuthorNames(authors []arxivAuthor) []string {
	names := make([]string, 0, len(authors))
	for _, author := range authors {
		name := strings.TrimSpace(author.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func arxivCategories(categories []arxivCategry) []string {
	terms := make([]string, 0, len(categories))
	for _, category := range categories {
		term := strings.TrimSpace(category.Term)
		if term != "" {
			terms = append(terms, term)
		}
	}
	return terms
}

func arxivPDFLink(links []arxivLink) string {
	for _, link := range links {
		if link.Title == "pdf" || link.Type == "application/pdf" {
			return link.Href
		}
	}
	return ""
}

// arxivFormatTime normalizes an RFC3339 timestamp; if it cannot be parsed it
// returns the trimmed raw value.
func arxivFormatTime(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return trimmed
}

// arxivCleanText collapses the runs of whitespace and newlines arXiv inserts
// into title/summary fields into single spaces.
func arxivCleanText(raw string) string {
	return strings.Join(strings.Fields(raw), " ")
}
