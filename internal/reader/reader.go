// Package reader fetches a URL and returns its content as Markdown, with
// site-specific shortcuts for GitHub and Reddit that pull from their JSON
// APIs instead of scraping rendered HTML.
package reader

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/PuerkitoBio/goquery"
	"github.com/dslipak/pdf"
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
	parsedURL, err := validateURL(urlStr)
	if err != nil {
		return "", err
	}

	client := newHTTPClient()
	if isRedditThreadURL(parsedURL) {
		return fetchRedditContentAsMarkdown(ctx, client, parsedURL)
	}
	if isGitHubIssueOrPRURL(parsedURL) {
		return fetchGitHubContentAsMarkdown(ctx, client, parsedURL)
	}
	if isGitHubRepoURL(parsedURL) {
		return fetchGitHubRepoAsMarkdown(ctx, client, parsedURL)
	}
	if isHackerNewsItemURL(parsedURL) {
		return fetchHackerNewsContentAsMarkdown(ctx, client, parsedURL)
	}
	if isStackOverflowQuestionURL(parsedURL) {
		return fetchStackOverflowContentAsMarkdown(ctx, client, parsedURL)
	}
	if isArxivURL(parsedURL) {
		return fetchArxivContentAsMarkdown(ctx, client, parsedURL)
	}
	return fetchGenericHTMLAsMarkdown(ctx, client, parsedURL.String())
}

func validateURL(urlStr string) (*url.URL, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if !slices.Contains(supportedSchemes, parsedURL.Scheme) {
		return nil, fmt.Errorf("unsupported URL scheme: %s (only http and https are supported)", parsedURL.Scheme)
	}
	return parsedURL, nil
}

func newHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: defaultHTTPTimeout, Control: guardDialAddress}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	client := &http.Client{Timeout: defaultHTTPTimeout, Transport: transport}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxHTTPRedirectCount {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}
	return client
}

// guardDialAddress runs after DNS resolution, just before the socket connects,
// so it sees the concrete IP the request will hit — including addresses reached
// via redirects or DNS rebinding. It refuses any non-public destination so the
// reader (which fetches caller-supplied URLs) cannot be turned into an SSRF
// vector against loopback, private, or link-local services such as the cloud
// metadata endpoint (169.254.169.254).
func guardDialAddress(_, address string, _ syscall.RawConn) error {
	if allowPrivateHosts {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("could not parse dial ip %q", host)
	}
	if isDisallowedIP(ip) {
		return fmt.Errorf("refusing to connect to non-public address %s", ip)
	}
	return nil
}

// isDisallowedIP reports whether ip is a loopback, private, link-local,
// unspecified, or multicast address that the reader must never connect to.
func isDisallowedIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast()
}

// readErrorBody reads up to maxErrorBodyBytes from a non-OK response body and
// returns it trimmed, for embedding in error messages. A read failure yields
// an empty string rather than masking the original status error.
func readErrorBody(r io.Reader) string {
	body, _ := io.ReadAll(io.LimitReader(r, maxErrorBodyBytes))
	return strings.TrimSpace(string(body))
}

func newRequest(ctx context.Context, urlStr, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Accept-Language", defaultAcceptLang)
	if accept != "" {
		req.Header.Set("Accept", accept)
	} else {
		req.Header.Set("Accept", defaultAccept)
	}
	return req, nil
}

// limitedBody wraps a response body with an io.LimitReader capped at
// maxResponseBodyBytes so that downstream readers/parsers cannot be forced to
// consume an unbounded amount of memory.
func limitedBody(r io.Reader) io.Reader {
	return io.LimitReader(r, maxResponseBodyBytes)
}

func fetchGenericHTMLAsMarkdown(ctx context.Context, client *http.Client, urlStr string) (string, error) {
	req, err := newRequest(ctx, urlStr, defaultAccept)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	contentType := resp.Header.Get("Content-Type")
	if isPDFResponse(contentType, urlStr) {
		body, err := io.ReadAll(limitedBody(resp.Body))
		if err != nil {
			return "", fmt.Errorf("failed to read PDF response body: %w", err)
		}
		return extractPDFText(body)
	}
	if !strings.Contains(contentType, "text/html") && !strings.Contains(contentType, "application/xhtml") {
		body, err := io.ReadAll(limitedBody(resp.Body))
		if err != nil {
			return "", fmt.Errorf("failed to read response body: %w", err)
		}
		if isBinaryResponse(contentType, body) {
			return "", fmt.Errorf("refusing to return binary response (%s)", contentType)
		}
		return string(body), nil
	}

	doc, err := goquery.NewDocumentFromReader(limitedBody(resp.Body))
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML: %w", err)
	}
	doc.Find("script, style, nav, footer, header, aside").Each(func(i int, s *goquery.Selection) {
		s.Remove()
	})

	html, err := doc.Html()
	if err != nil {
		return "", fmt.Errorf("failed to serialize HTML: %w", err)
	}

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

	return cleanMarkdown(markdown), nil
}

func isPDFResponse(contentType, urlStr string) bool {
	mediaType := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	return strings.EqualFold(mediaType, "application/pdf") ||
		strings.HasSuffix(strings.ToLower(strings.SplitN(urlStr, "?", 2)[0]), ".pdf")
}

func extractPDFText(body []byte) (string, error) {
	reader, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", fmt.Errorf("failed to parse PDF: %w", err)
	}
	textReader, err := reader.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("failed to extract PDF text: %w", err)
	}
	text, err := io.ReadAll(textReader)
	if err != nil {
		return "", fmt.Errorf("failed to read extracted PDF text: %w", err)
	}
	return cleanMarkdown(string(text)), nil
}

func isBinaryResponse(contentType string, body []byte) bool {
	mediaType := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	lowerMediaType := strings.ToLower(mediaType)
	if strings.HasPrefix(lowerMediaType, "image/") ||
		strings.HasPrefix(lowerMediaType, "audio/") ||
		strings.HasPrefix(lowerMediaType, "video/") ||
		(lowerMediaType == "application/octet-stream") ||
		(strings.HasPrefix(lowerMediaType, "application/") && !isTextApplication(lowerMediaType)) {
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
