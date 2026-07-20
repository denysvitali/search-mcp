package reader

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

const (
	// maxGitilesFileSize caps how many decoded bytes of a gitiles blob we
	// render, protecting against huge generated files.
	maxGitilesFileSize = 1 << 20 // 1 MiB

	// maxGitilesDirEntries caps how many directory entries are listed.
	maxGitilesDirEntries = 200
)

// isGitilesURL matches Gitiles object URLs of the shape
// /<project>/+/<ref>/<path>, e.g.
// https://chromium.googlesource.com/chromium/src/+/refs/tags/140.0.7339.207/base/logging.h.
// The host is not restricted — any Gitiles instance uses this URL shape. URLs
// under /c/ (the Gerrit web UI) are excluded; they are handled by the Gerrit
// reader.
func isGitilesURL(parsedURL *url.URL) bool {
	segments := pathSegments(parsedURL.Path)
	if len(segments) < 3 || segments[0] == "c" {
		return false
	}
	for i := 1; i < len(segments)-1; i++ {
		if segments[i] == "+" {
			return true
		}
	}
	return false
}

// fetchGitilesContentAsMarkdown renders a Gitiles object URL. Gitiles pages
// are HTML but bury file contents in line-numbered tables; the instance's
// machine-readable endpoints (?format=TEXT for blobs, ?format=JSON for trees)
// give clean content when enabled. Instances that disable them fall back to
// scraping the rendered page.
func fetchGitilesContentAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	if markdown, err := fetchGitilesBlob(ctx, client, parsedURL); err == nil {
		return markdown, nil
	}
	if markdown, err := fetchGitilesDirectory(ctx, client, parsedURL); err == nil {
		return markdown, nil
	}
	return fetchGitilesRenderedPage(ctx, client, parsedURL)
}

// gitilesFetch GETs the object URL with the given machine-readable format
// ("" for the rendered HTML page) and returns the raw response body.
func gitilesFetch(ctx context.Context, client *http.Client, parsedURL *url.URL, format string) ([]byte, error) {
	endpoint := *parsedURL
	query := endpoint.Query()
	if format == "" {
		query.Del("format")
	} else {
		query.Set("format", format)
	}
	endpoint.RawQuery = query.Encode()

	accept := "*/*"
	if format == "JSON" {
		accept = "application/json"
	}
	req, err := newRequest(ctx, endpoint.String(), accept)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitiles request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(limitedBody(resp.Body))
	if err != nil {
		return nil, fmt.Errorf("failed to read gitiles response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitiles request failed: HTTP %d: %s", resp.StatusCode, readErrorBody(bytes.NewReader(body)))
	}
	return body, nil
}

// fetchGitilesBlob fetches a file through ?format=TEXT, which returns the
// blob base64-encoded.
func fetchGitilesBlob(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	body, err := gitilesFetch(ctx, client, parsedURL, "TEXT")
	if err != nil {
		return "", err
	}
	// Go's base64 decoder tolerates the line breaks gitiles inserts; invalid
	// input means the response was not a base64 blob and we fall through.
	decoded, err := io.ReadAll(io.LimitReader(
		base64.NewDecoder(base64.StdEncoding, bytes.NewReader(body)), maxGitilesFileSize+1))
	if err != nil {
		return "", fmt.Errorf("failed to decode gitiles blob: %w", err)
	}
	truncated := len(decoded) > maxGitilesFileSize
	if truncated {
		decoded = decoded[:maxGitilesFileSize]
	}
	// format=TEXT on a tree returns an ls-tree style listing instead of file
	// content; render it as a directory.
	if entries, ok := parseGitilesTreeListing(decoded); ok {
		return renderGitilesDirectory(parsedURL, entries), nil
	}
	fileName := gitilesFileName(parsedURL)
	if isBinaryResponse("", decoded) {
		return fmt.Sprintf("# %s\n\n_Binary file — original at %s_\n", fileName, parsedURL), nil
	}
	return renderGitilesFile(parsedURL, fileName, string(decoded), truncated), nil
}

// renderGitilesFile renders file content as Markdown. Prose-like extensions
// are emitted verbatim (like the GitHub blob reader); everything else goes in
// a fenced code block. cleanMarkdown is deliberately not applied: it would
// strip the code's indentation.
func renderGitilesFile(parsedURL *url.URL, fileName, content string, truncated bool) string {
	ext := strings.ToLower(path.Ext(fileName))
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n- Link: %s\n\n", fileName, parsedURL)
	switch ext {
	case ".md", ".markdown", ".txt", ".rst":
		b.WriteString(strings.TrimSpace(content))
	default:
		fmt.Fprintf(&b, "```%s\n%s\n```", codeFenceLanguages[ext], strings.TrimRight(content, "\n"))
	}
	if truncated {
		fmt.Fprintf(&b, "\n\n_... truncated at %d KiB._", maxGitilesFileSize/1024)
	}
	b.WriteString("\n")
	return b.String()
}

type gitilesEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// parseGitilesTreeListing parses gitiles' ls-tree style directory output
// ("<mode> <type> <sha>\t<name>" per line), which ?format=TEXT returns for
// trees. It reports false unless every line matches, so real file content is
// never mistaken for a listing.
func parseGitilesTreeListing(content []byte) ([]gitilesEntry, bool) {
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	entries := make([]gitilesEntry, 0, len(lines))
	for _, line := range lines {
		meta, name, found := strings.Cut(line, "\t")
		if !found || name == "" {
			return nil, false
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return nil, false
		}
		if _, err := strconv.ParseUint(fields[0], 8, 32); err != nil {
			return nil, false
		}
		if fields[1] != "blob" && fields[1] != "tree" && fields[1] != "commit" {
			return nil, false
		}
		if len(fields[2]) != 40 || strings.Trim(fields[2], "0123456789abcdef") != "" {
			return nil, false
		}
		entries = append(entries, gitilesEntry{Name: name, Type: fields[1]})
	}
	if len(entries) == 0 {
		return nil, false
	}
	return entries, true
}

type gitilesDirectory struct {
	Entries []gitilesEntry `json:"entries"`
}

// fetchGitilesDirectory lists a tree through ?format=JSON.
func fetchGitilesDirectory(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	body, err := gitilesFetch(ctx, client, parsedURL, "JSON")
	if err != nil {
		return "", err
	}
	body = bytes.TrimPrefix(body, []byte(gerritXSSIPrefix))
	var dir gitilesDirectory
	if err := json.Unmarshal(body, &dir); err != nil {
		return "", fmt.Errorf("failed to decode gitiles directory: %w", err)
	}
	if dir.Entries == nil {
		return "", fmt.Errorf("gitiles JSON response is not a directory listing")
	}
	return renderGitilesDirectory(parsedURL, dir.Entries), nil
}

func renderGitilesDirectory(parsedURL *url.URL, entries []gitilesEntry) string {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	var b strings.Builder
	fmt.Fprintf(&b, "# Directory listing\n\n- Link: %s\n\n", parsedURL)
	if len(entries) == 0 {
		b.WriteString("_Empty directory._\n")
		return b.String()
	}
	for i, entry := range entries {
		if i >= maxGitilesDirEntries {
			fmt.Fprintf(&b, "- _... %d more entries omitted._\n", len(entries)-maxGitilesDirEntries)
			break
		}
		if entry.Type == "tree" {
			fmt.Fprintf(&b, "- %s/\n", entry.Name)
		} else {
			fmt.Fprintf(&b, "- %s\n", entry.Name)
		}
	}
	return b.String()
}

// fetchGitilesRenderedPage scrapes the server-rendered HTML page: Markdown
// documents (rendered under .doc), source files (line cells under
// .FileContents), and anything else via readability / full-page conversion.
func fetchGitilesRenderedPage(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	body, err := gitilesFetch(ctx, client, parsedURL, "")
	if err != nil {
		return "", err
	}
	if markdown, ok := gitilesSourceLines(parsedURL, body); ok {
		return markdown, nil
	}
	if markdown, ok := gitilesMarkdownDoc(parsedURL, body); ok {
		return markdown, nil
	}
	if markdown, ok := readableMarkdown(body, parsedURL.String()); ok {
		return markdown, nil
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to parse gitiles HTML: %w", err)
	}
	doc.Find("script, style, nav, footer, header, aside").Each(func(_ int, s *goquery.Selection) {
		s.Remove()
	})
	html, err := doc.Html()
	if err != nil {
		return "", fmt.Errorf("failed to serialize gitiles HTML: %w", err)
	}
	markdown, err := convertHTMLToMarkdown(html)
	if err != nil {
		return "", err
	}
	return cleanMarkdown(markdown), nil
}

// gitilesSourceLines reconstructs a source file from gitiles' line cells, for
// instances that disable ?format=TEXT.
func gitilesSourceLines(parsedURL *url.URL, body []byte) (string, bool) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	lines := doc.Find(".FileContents-lineContents")
	if lines.Length() == 0 {
		return "", false
	}
	var content strings.Builder
	lines.Each(func(_ int, s *goquery.Selection) {
		content.WriteString(s.Text())
		content.WriteByte('\n')
	})
	return renderGitilesFile(parsedURL, gitilesFileName(parsedURL), content.String(), false), true
}

// gitilesMarkdownDoc converts a gitiles-rendered Markdown document (.doc) back
// to Markdown.
func gitilesMarkdownDoc(parsedURL *url.URL, body []byte) (string, bool) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	inner, err := doc.Find(".doc").First().Html()
	if err != nil || strings.TrimSpace(inner) == "" {
		return "", false
	}
	markdown, err := convertHTMLToMarkdown(inner)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("# %s\n\n- Link: %s\n\n%s\n", gitilesFileName(parsedURL), parsedURL, strings.TrimSpace(markdown)), true
}

// gitilesFileName derives a display name from the URL's last path segment.
func gitilesFileName(parsedURL *url.URL) string {
	if name := path.Base(parsedURL.Path); name != "." && name != "/" {
		return name
	}
	return parsedURL.Host
}
