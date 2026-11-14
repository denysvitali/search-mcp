// Package reader fetches a URL and returns its content as Markdown, with
// site-specific shortcuts for GitHub and Reddit that pull from their JSON
// APIs instead of scraping rendered HTML.
package reader

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/PuerkitoBio/goquery"
	readability "github.com/go-shiori/go-readability"
)

const (
	defaultUserAgent     = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"
	defaultAccept        = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	defaultAcceptLang    = "en-US,en;q=0.9"
	defaultHTTPTimeout   = 30 * time.Second
	maxHTTPRedirectCount = 10

	// maxResponseBodyBytes caps how many bytes we read from any remote
	// response body, protecting against unbounded/malicious payloads.
	maxResponseBodyBytes = 10 << 20 // 10 MiB
	maxPDFPages          = 10000

	// maxErrorBodyBytes caps how many bytes of a non-OK response body we read
	// to include in an error message.
	maxErrorBodyBytes = 4096

	// maxConsecutiveBlankLines is the largest run of blank lines that
	// cleanMarkdown leaves in its output.
	maxConsecutiveBlankLines = 1
)

var supportedSchemes = []string{"http", "https"}

// allowPrivateHosts disables the SSRF guard in guardDialAddress. It is only
// ever flipped by the test suite so it can target httptest servers bound to
// loopback addresses; production code leaves it false.
var allowPrivateHosts = false

// Read fetches the URL and returns its content as Markdown. GitHub repo /
// issue / pull-request URLs and Reddit comment threads are routed through
// their respective JSON APIs; everything else is fetched as HTML and
// converted via html-to-markdown.
func Read(ctx context.Context, urlStr string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	parsedURL, err := validateURL(urlStr)
	if err != nil {
		return "", err
	}

	client := newHTTPClient()
	defer client.CloseIdleConnections()
	if isRedditThreadURL(parsedURL) {
		return fetchRedditContentAsMarkdown(ctx, client, parsedURL)
	}
	if isGitHubIssueOrPRURL(parsedURL) {
		return fetchGitHubContentAsMarkdown(ctx, client, parsedURL)
	}
	if isGitHubBlobURL(parsedURL) {
		return fetchGitHubFileAsMarkdown(ctx, client, parsedURL)
	}
	if isGitHubRepoURL(parsedURL) {
		return fetchGitHubRepoAsMarkdown(ctx, client, parsedURL)
	}
	if isWikipediaArticleURL(parsedURL) {
		return fetchWikipediaContentAsMarkdown(ctx, client, parsedURL)
	}
	if isLobstersStoryURL(parsedURL) {
		return fetchLobstersContentAsMarkdown(ctx, client, parsedURL)
	}
	if isGitLabIssuableURL(parsedURL) {
		return fetchGitLabContentAsMarkdown(ctx, client, parsedURL)
	}
	if isGerritChangeURL(parsedURL) {
		return fetchGerritChangeAsMarkdown(ctx, client, parsedURL)
	}
	if isGitilesURL(parsedURL) {
		// Fall through to the generic fetch if the Gitiles-specific routes
		// all fail, e.g. on a URL that merely looks like a Gitiles object.
		if markdown, err := fetchGitilesContentAsMarkdown(ctx, client, parsedURL); err == nil {
			return markdown, nil
		}
	}
	if isPkgGoDevURL(parsedURL) {
		return fetchPkgGoDevContentAsMarkdown(ctx, client, parsedURL)
	}
	if isYouTubeVideoURL(parsedURL) {
		return fetchYouTubeContentAsMarkdown(ctx, client, parsedURL)
	}
	if isHackerNewsItemURL(parsedURL) {
		return fetchHackerNewsContentAsMarkdown(ctx, client, parsedURL)
	}
	if isStackOverflowQuestionURL(parsedURL) {
		return fetchStackOverflowContentAsMarkdown(ctx, client, parsedURL)
	}
	if isArxivURL(parsedURL) {
		content, err := fetchArxivContentAsMarkdown(ctx, client, parsedURL)
		if err == nil {
			return content, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// The public abstract page remains useful when the Atom API is down.
	}
	return fetchWithWaybackFallback(ctx, client, parsedURL.String())
}

func validateURL(urlStr string) (*url.URL, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	parsedURL.Scheme = strings.ToLower(parsedURL.Scheme)
	if !slices.Contains(supportedSchemes, parsedURL.Scheme) {
		return nil, fmt.Errorf("unsupported URL scheme: %s (only http and https are supported)", parsedURL.Scheme)
	}
	if parsedURL.Hostname() == "" {
		return nil, fmt.Errorf("invalid URL: host is required")
	}
	if err := checkDomainPolicy(parsedURL); err != nil {
		return nil, err
	}
	return parsedURL, nil
}

func fetchGenericHTMLAsMarkdown(ctx context.Context, client *http.Client, urlStr string) (string, error) {
	if content, ok := webPageCache.getFresh(urlStr); ok {
		return content, nil
	}

	req, err := newRequest(ctx, urlStr, defaultAccept)
	if err != nil {
		return "", err
	}
	// Revalidate a stale cached copy instead of re-downloading it.
	etag, lastModified := webPageCache.validators(urlStr)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		if content, ok := webPageCache.refresh(urlStr); ok {
			return content, nil
		}
		return "", fmt.Errorf("server answered 304 but no cached copy exists")
	}
	if resp.StatusCode != http.StatusOK {
		return "", &httpStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}

	// cache stores the rendered content along with the response validators so
	// the next stale hit can revalidate with a conditional GET.
	cache := func(content string) string {
		webPageCache.store(urlStr, content, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"))
		return content
	}

	body, err := readResponseBody(resp.Body)
	if err != nil {
		return "", err
	}
	contentType := responseMediaType(resp, body)
	if contentType == "application/pdf" || bytes.HasPrefix(body, []byte("%PDF-")) {
		text, err := extractPDFText(body)
		if err != nil {
			return "", err
		}
		return cache(text), nil
	}
	if contentType != "text/html" && contentType != "application/xhtml+xml" {
		if isBinaryResponse(contentType, body) {
			return "", fmt.Errorf("refusing to return binary response (%s)", contentType)
		}
		if markdown, ok := renderFeed(contentType, body); ok {
			return cache(markdown), nil
		}
		if pretty, ok := prettyJSON(contentType, body); ok {
			return cache(pretty), nil
		}
		return cache(string(body)), nil
	}
	if challenge, detected, challengeErr := parseAnubisChallenge(body); detected {
		if challengeErr != nil {
			return "", challengeErr
		}
		resp.Body.Close()
		resp, err = passAnubisChallenge(ctx, client, urlStr, challenge)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", &httpStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
		}
		body, err = readResponseBody(resp.Body)
		if err != nil {
			return "", fmt.Errorf("read response after Anubis challenge: %w", err)
		}
		if _, detectedAgain, _ := parseAnubisChallenge(body); detectedAgain {
			return "", fmt.Errorf("anubis challenge persisted after successful proof submission")
		}
	}

	finalURL := responseURL(resp, urlStr)
	doc, err := prepareHTML(body, finalURL)
	if err != nil {
		return "", err
	}
	// Dates live in metadata and in <time>, not in the article text a
	// changelog question needs. Capture them before either renderer runs.
	facts := pageFacts(doc)
	normalized, err := doc.Html()
	if err != nil {
		return "", fmt.Errorf("failed to serialize HTML: %w", err)
	}
	// Resolve links before readability or fallback conversion, so both paths
	// navigate relative to the final redirect destination and HTML base.
	if markdown, ok := readableMarkdown([]byte(normalized), finalURL); ok {
		return cache(withPageFacts(markdown, facts)), nil
	}

	doc.Find("script, style, nav, footer, header, aside").Each(func(i int, s *goquery.Selection) {
		s.Remove()
	})

	html, err := doc.Html()
	if err != nil {
		return "", fmt.Errorf("failed to serialize HTML: %w", err)
	}

	markdown, err := convertHTMLToMarkdown(html)
	if err != nil {
		return "", err
	}

	return cache(withPageFacts(cleanMarkdown(markdown), facts)), nil
}

// maxPageFacts bounds the metadata block prepended to a page. A marketing
// page can carry dozens of <time> nodes; the first ones are the ones a
// "when did this ship?" question can use.
const maxPageFacts = 12

// pageFacts collects the dates a renderer would otherwise throw away: the
// page's own published/modified metadata, then visible <time> values. Text
// is taken only from attributes the page author set, never invented.
func pageFacts(doc *goquery.Document) []string {
	var facts []string
	add := func(label, value string) {
		value = strings.Join(strings.Fields(value), " ")
		if value == "" || len(facts) >= maxPageFacts {
			return
		}
		facts = append(facts, label+": "+value)
	}
	for _, meta := range []struct{ selector, label string }{
		{`meta[property="article:published_time"]`, "published"},
		{`meta[property="article:modified_time"]`, "modified"},
		{`meta[name="date"]`, "published"},
		{`meta[name="pubdate"]`, "published"},
		{`meta[name="dc.date"]`, "published"},
		{`meta[name="dc.date.issued"]`, "published"},
		{`meta[itemprop="datePublished"]`, "published"},
		{`meta[itemprop="dateModified"]`, "modified"},
	} {
		if content, ok := doc.Find(meta.selector).First().Attr("content"); ok {
			add(meta.label, content)
		}
	}
	doc.Find("time").Each(func(_ int, s *goquery.Selection) {
		when, _ := s.Attr("datetime")
		if when == "" {
			when = s.Text()
		}
		add("time", when)
	})
	return facts
}

// withPageFacts prepends a short block of the page's own dates and a heading
// outline. Both survive a truncated tool reply, so the caller can see when
// the page says something happened and which sections the cut-off tail holds.
func withPageFacts(markdown string, facts []string) string {
	outline := headingOutline(markdown)
	if len(facts) == 0 && outline == "" {
		return markdown
	}
	var b strings.Builder
	if len(facts) > 0 {
		b.WriteString("Page dates (from the page's own metadata, not verified):\n")
		for _, fact := range facts {
			b.WriteString("- ")
			b.WriteString(fact)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	if outline != "" {
		b.WriteString("Headings:\n")
		b.WriteString(outline)
		b.WriteString("\n\n")
	}
	b.WriteString(markdown)
	return b.String()
}

// maxOutlineEntries caps the heading index. It is an index of the page, not
// a second copy of it.
const maxOutlineEntries = 40

// headingOutline lists Markdown headings in document order so a caller that
// only sees the start of a long page still knows which sections exist.
func headingOutline(markdown string) string {
	var b strings.Builder
	count := 0
	for _, line := range strings.Split(markdown, "\n") {
		level, title, ok := markdownHeading(line)
		if !ok {
			continue
		}
		if count >= maxOutlineEntries {
			if count > 0 {
				b.WriteByte('\n')
			}
			b.WriteString("- …")
			break
		}
		if count > 0 {
			b.WriteByte('\n')
		}
		for range level - 1 {
			b.WriteString("  ")
		}
		b.WriteString("- ")
		b.WriteString(title)
		count++
	}
	return b.String()
}

// markdownHeading reports the level and text of an ATX heading. It ignores
// setext headings and heading-looking text inside fenced code, which the
// converter does not emit as a bare line starting with hashes.
func markdownHeading(line string) (int, string, bool) {
	if !strings.HasPrefix(line, "#") {
		return 0, "", false
	}
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level == len(line) || line[level] != ' ' {
		return 0, "", false
	}
	title := strings.TrimSpace(line[level+1:])
	title = strings.TrimRight(title, " #")
	title = strings.TrimSpace(title)
	if title == "" {
		return 0, "", false
	}
	return level, title, true
}

// minReadableTextLength is the smallest extracted-article text size that we
// trust; anything shorter suggests readability picked a fragment of the page
// (or the page is tiny), so we fall back to full-page conversion.
const minReadableTextLength = 500

// readableMarkdown attempts a boilerplate-stripping readability extraction of
// the page and converts the resulting article HTML to Markdown. It reports
// false whenever extraction looks unreliable so the caller can fall back.
func readableMarkdown(body []byte, urlStr string) (string, bool) {
	pageURL, err := url.Parse(urlStr)
	if err != nil {
		return "", false
	}
	article, err := readability.FromReader(bytes.NewReader(body), pageURL)
	if err != nil || strings.TrimSpace(article.Content) == "" {
		return "", false
	}
	if len(strings.TrimSpace(article.TextContent)) < minReadableTextLength {
		return "", false
	}
	markdown, err := convertHTMLToMarkdown(article.Content)
	if err != nil {
		return "", false
	}
	if article.Title != "" {
		markdown = "# " + article.Title + "\n\n" + markdown
	}
	return cleanMarkdown(markdown), true
}

func convertHTMLToMarkdown(html string) (string, error) {
	conv := converter.NewConverter(
		converter.WithPlugins(
			base.NewBasePlugin(),
			commonmark.NewCommonmarkPlugin(),
		),
	)
	markdown, err := conv.ConvertString(html)
	if err != nil {
		return "", fmt.Errorf("failed to convert to Markdown: %w", err)
	}
	return markdown, nil
}

func isBinaryResponse(contentType string, body []byte) bool {
	mediaType := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	lowerMediaType := strings.ToLower(mediaType)
	if strings.HasPrefix(lowerMediaType, "image/") ||
		strings.HasPrefix(lowerMediaType, "audio/") ||
		strings.HasPrefix(lowerMediaType, "video/") {
		return true
	}
	// application/octet-stream only means "untyped": servers often send text
	// files (ICS calendars, CSV, plain text) that way. Return those when the
	// body itself sniffs as text; real binaries stay refused.
	if lowerMediaType == "application/octet-stream" {
		return !strings.HasPrefix(http.DetectContentType(body), "text/") || bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body)
	}
	if strings.HasPrefix(lowerMediaType, "application/") && !isTextApplication(lowerMediaType) {
		return true
	}
	return bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body)
}

func isTextApplication(mediaType string) bool {
	return strings.HasSuffix(mediaType, "+json") ||
		strings.HasSuffix(mediaType, "+xml") ||
		strings.HasSuffix(mediaType, "+yaml") ||
		strings.Contains(mediaType, "json") ||
		strings.Contains(mediaType, "javascript") ||
		strings.Contains(mediaType, "xml") ||
		strings.Contains(mediaType, "yaml") ||
		strings.Contains(mediaType, "toml") ||
		strings.Contains(mediaType, "x-www-form-urlencoded") ||
		strings.Contains(mediaType, "graphql") ||
		strings.Contains(mediaType, "sql")
}

func pathSegments(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		segments = append(segments, part)
	}
	return segments
}

func cleanMarkdown(markdown string) string {
	lines := strings.Split(markdown, "\n")
	var cleaned []string

	emptyCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			emptyCount++
			if emptyCount <= maxConsecutiveBlankLines {
				cleaned = append(cleaned, "")
			}
		} else {
			emptyCount = 0
			cleaned = append(cleaned, trimmed)
		}
	}

	for len(cleaned) > 0 && cleaned[0] == "" {
		cleaned = cleaned[1:]
	}
	for len(cleaned) > 0 && cleaned[len(cleaned)-1] == "" {
		cleaned = cleaned[:len(cleaned)-1]
	}

	return strings.Join(cleaned, "\n")
}
