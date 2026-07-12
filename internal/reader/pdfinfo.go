package reader

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

const (
	// maxPDFOutlineDepth caps how deep the rendered table of contents nests.
	maxPDFOutlineDepth = 4
	// maxPDFOutlineEntries caps how many outline entries are rendered.
	maxPDFOutlineEntries = 150
)

// pdfSummary renders document metadata, page count, and the outline so a
// caller can decide which pages to request next. The underlying PDF library
// panics on some malformed documents, so everything is wrapped in a recover.
func pdfSummary(body []byte, urlStr string) (summary string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("failed to summarize PDF: %v", r)
		}
	}()

	reader, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", fmt.Errorf("failed to parse PDF: %w", err)
	}

	info := reader.Trailer().Key("Info")
	title := pdfInfoText(info, "Title")
	if title == "" {
		title = "(untitled)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# PDF: %s\n\n", title)
	fmt.Fprintf(&b, "- URL: %s\n", urlStr)
	fmt.Fprintf(&b, "- Pages: %d\n", reader.NumPage())
	for _, field := range []string{"Author", "Subject", "Producer", "CreationDate"} {
		if value := pdfInfoText(info, field); value != "" {
			fmt.Fprintf(&b, "- %s: %s\n", field, value)
		}
	}

	entries := 0
	var writeOutline func(outline pdf.Outline, depth int)
	writeOutline = func(outline pdf.Outline, depth int) {
		if depth > maxPDFOutlineDepth || entries >= maxPDFOutlineEntries {
			return
		}
		title := strings.TrimSpace(outline.Title)
		if title != "" {
			fmt.Fprintf(&b, "%s- %s\n", strings.Repeat("  ", depth-1), title)
			entries++
		}
		for _, child := range outline.Child {
			childDepth := depth
			if title != "" {
				childDepth++
			}
			writeOutline(child, childDepth)
		}
	}
	root := reader.Outline()
	if len(root.Child) > 0 {
		b.WriteString("\n## Outline\n\n")
		writeOutline(root, 1)
		if entries >= maxPDFOutlineEntries {
			fmt.Fprintf(&b, "\n_Outline truncated at %d entries._\n", maxPDFOutlineEntries)
		}
	}

	b.WriteString("\nCall read_pdf again with pages (e.g. \"1-3,17\") or query to read content.\n")
	return cleanMarkdown(b.String()), nil
}

// pdfInfoText safely extracts a text field from the PDF Info dictionary.
func pdfInfoText(info pdf.Value, key string) string {
	defer func() { _ = recover() }()
	if info.Kind() != pdf.Dict {
		return ""
	}
	return strings.TrimSpace(info.Key(key).Text())
}
