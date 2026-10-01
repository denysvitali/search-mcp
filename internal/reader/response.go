package reader

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// readResponseBody rejects incomplete documents instead of caching truncated
// HTML or handing a truncated file to the PDF parser.
func readResponseBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxResponseBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	if len(body) > maxResponseBodyBytes {
		return nil, fmt.Errorf("response exceeds %d-byte size limit", maxResponseBodyBytes)
	}
	return body, nil
}

func responseMediaType(resp *http.Response, body []byte) string {
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType == "" {
		mediaType, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	return strings.ToLower(mediaType)
}

func responseURL(resp *http.Response, fallback string) string {
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String()
	}
	return fallback
}

// prepareHTML detects known interstitials and makes navigation independent of
// whether readability or the full-page renderer is used.
func prepareHTML(body []byte, pageURL string) (*goquery.Document, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}
	title := strings.ToLower(strings.TrimSpace(doc.Find("title").First().Text()))
	switch title {
	case "just a moment...", "attention required! | cloudflare", "access denied", "robot check", "captcha", "verify you are human":
		return nil, fmt.Errorf("page served an access or anti-bot challenge (%s)", title)
	}
	if doc.Find("#challenge-form, #cf-challenge-running, #cf-browser-verification, form[action*='/sorry/']").Length() > 0 {
		return nil, fmt.Errorf("page served an anti-bot challenge instead of content")
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil, fmt.Errorf("invalid page URL: %w", err)
	}
	if href, ok := doc.Find("base[href]").First().Attr("href"); ok {
		if candidate, err := base.Parse(strings.TrimSpace(href)); err == nil && (candidate.Scheme == "http" || candidate.Scheme == "https") {
			base = candidate
		}
	}
	for _, attribute := range []string{"href", "src"} {
		doc.Find("[" + attribute + "]").Each(func(_ int, s *goquery.Selection) {
			if s.Is("base") {
				s.SetAttr(attribute, base.String())
				return
			}
			raw, _ := s.Attr(attribute)
			if attribute == "href" && strings.HasPrefix(strings.TrimSpace(raw), "#") {
				s.SetAttr("data-reader-fragment", "true")
			}
			if resolved, err := base.Parse(strings.TrimSpace(raw)); err == nil {
				s.SetAttr(attribute, resolved.String())
			}
		})
	}
	return doc, nil
}

func recoverPDF(text *string, err *error) {
	if r := recover(); r != nil {
		*text = ""
		*err = fmt.Errorf("failed to extract PDF: %v", r)
	}
}
