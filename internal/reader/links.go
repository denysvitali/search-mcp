package reader

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// maxExtractedLinks caps how many links ExtractLinks returns.
const maxExtractedLinks = 500

// ExtractLinks fetches the URL and returns a deduplicated Markdown list of
// the absolute links on the page with their anchor text, so a caller can
// navigate a site without re-reading full page content.
func ExtractLinks(ctx context.Context, urlStr string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	parsedURL, err := validateURL(urlStr)
	if err != nil {
		return "", err
	}

	client := newHTTPClient()
	defer client.CloseIdleConnections()
	req, err := newRequest(ctx, parsedURL.String(), defaultAccept)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &httpStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}
	body, err := readResponseBody(resp.Body)
	if err != nil {
		return "", err
	}
	contentType := responseMediaType(resp, body)
	if contentType != "text/html" && contentType != "application/xhtml+xml" {
		return "", fmt.Errorf("cannot extract links from %s content", contentType)
	}
	if challenge, detected, challengeErr := parseAnubisChallenge(body); detected {
		if challengeErr != nil {
			return "", challengeErr
		}
		resp.Body.Close()
		resp, err = passAnubisChallenge(ctx, client, parsedURL.String(), challenge)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", &httpStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
		}
		body, err = readResponseBody(resp.Body)
		if err != nil {
			return "", fmt.Errorf("read links page after Anubis challenge: %w", err)
		}
	}

	finalURL := responseURL(resp, parsedURL.String())
	doc, err := prepareHTML(body, finalURL)
	if err != nil {
		return "", err
	}
	base, err := url.Parse(finalURL)
	if err != nil {
		return "", err
	}

	seen := make(map[string]bool)
	total := 0
	var b strings.Builder
	fmt.Fprintf(&b, "# Links on %s\n\n", finalURL)
	doc.Find("a[href]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		href, _ := s.Attr("href")
		if original, ok := s.Attr("data-reader-fragment"); ok && original == "true" {
			return true
		}
		link, ok := resolveLink(base, href)
		if !ok || seen[link] {
			return true
		}
		seen[link] = true
		total++
		if total > maxExtractedLinks {
			return false
		}
		text := strings.Join(strings.Fields(s.Text()), " ")
		if text == "" {
			text = link
		}
		fmt.Fprintf(&b, "- [%s](%s)\n", text, link)
		return true
	})
	if total == 0 {
		b.WriteString("_No links found._\n")
	}
	if total > maxExtractedLinks {
		fmt.Fprintf(&b, "\n_Truncated at %d links._\n", maxExtractedLinks)
	}
	return cleanMarkdown(b.String()), nil
}

// resolveLink turns href into an absolute http(s) URL against base, dropping
// fragments, javascript:/mailto:/tel: pseudo-links, and unparseable values.
func resolveLink(base *url.URL, href string) (string, bool) {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return "", false
	}
	resolved, err := base.Parse(href)
	if err != nil {
		return "", false
	}
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", false
	}
	resolved.Fragment = ""
	return resolved.String(), true
}
