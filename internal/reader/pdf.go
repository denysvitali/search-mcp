// PDF download and text extraction.

package reader

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/ledongthuc/pdf"
)

func extractPDFText(body []byte) (text string, err error) {
	defer recoverPDF(&text, &err)
	reader, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", fmt.Errorf("failed to parse PDF: %w", err)
	}

	if _, err := parsePDFPageSpec("", reader.NumPage()); err != nil {
		return "", err
	}
	pages := extractPDFPages(reader)
	text = cleanMarkdown(strings.Join(pages, "\n\n"))
	if text == "" {
		return "[no extractable text; this PDF may require OCR]", nil
	}
	return text, nil
}

func extractPDFPages(reader *pdf.Reader) []string {
	pages := make([]string, 0, reader.NumPage())
	for page := 1; page <= reader.NumPage(); page++ {
		pages = append(pages, extractPDFPage(reader.Page(page)))
	}
	return pages
}

func extractPDFPage(page pdf.Page) string {
	var text strings.Builder
	for _, row := range orderPDFRows(pdfRows(page)) {
		line := joinPDFRow(row)
		if line == "" {
			continue
		}
		text.WriteString(line)
		text.WriteString("\n")
	}
	return strings.TrimSpace(text.String())
}

func formatPDFPages(pages []string, selected []int, query string, contextLines, maxResults int) string {
	lowerQuery := strings.ToLower(query)
	var output strings.Builder
	results := 0
	truncated := false
pagesLoop:
	for _, pageNumber := range selected {
		lines := strings.Split(pages[pageNumber-1], "\n")
		if query == "" {
			if results >= maxResults {
				truncated = true
				break pagesLoop
			}
			writePDFPage(&output, pageNumber, lines)
			results++
			continue
		}

		for lineNumber, line := range lines {
			if !strings.Contains(strings.ToLower(line), lowerQuery) {
				continue
			}
			if results >= maxResults {
				truncated = true
				break pagesLoop
			}
			start := max(0, lineNumber-contextLines)
			end := min(len(lines), lineNumber+contextLines+1)
			writePDFPageLines(&output, pageNumber, start+1, lines[start:end])
			results++
		}
	}
	if results == 0 && query != "" {
		return fmt.Sprintf("[no matches for %q in selected PDF pages]", query)
	}
	if truncated {
		fmt.Fprintf(&output, "\n\n[results truncated at %d; narrow pages/query or increase max_results]", maxResults)
	}
	return strings.TrimSpace(output.String())
}

func writePDFPage(output *strings.Builder, pageNumber int, lines []string) {
	writePDFPageLines(output, pageNumber, 1, lines)
}

func writePDFPageLines(output *strings.Builder, pageNumber, firstLine int, lines []string) {
	if output.Len() > 0 {
		output.WriteString("\n\n")
	}
	fmt.Fprintf(output, "## Page %d\n", pageNumber)
	if strings.TrimSpace(strings.Join(lines, "")) == "" {
		output.WriteString("[no extractable text; this page may require OCR]\n")
		return
	}
	for offset, line := range lines {
		fmt.Fprintf(output, "%d: %s\n", firstLine+offset, line)
	}
}

func parsePDFPageSpec(spec string, totalPages int) ([]int, error) {
	if totalPages < 0 || totalPages > maxPDFPages {
		return nil, fmt.Errorf("PDF page count %d exceeds supported limit of %d", totalPages, maxPDFPages)
	}
	if strings.TrimSpace(spec) == "" {
		pages := make([]int, totalPages)
		for i := range pages {
			pages[i] = i + 1
		}
		return pages, nil
	}

	seen := make(map[int]bool)
	var pages []int
	for _, part := range strings.Split(spec, ",") {
		bounds := strings.Split(strings.TrimSpace(part), "-")
		if len(bounds) > 2 || len(bounds) == 0 {
			return nil, fmt.Errorf("invalid page range %q", part)
		}
		start, err := strconv.Atoi(strings.TrimSpace(bounds[0]))
		if err != nil {
			return nil, fmt.Errorf("invalid page range %q", part)
		}
		end := start
		if len(bounds) == 2 {
			end, err = strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid page range %q", part)
			}
		}
		if start < 1 || end < start || end > totalPages {
			return nil, fmt.Errorf("page range %q is outside 1-%d", part, totalPages)
		}
		for page := start; page <= end; page++ {
			if !seen[page] {
				seen[page] = true
				pages = append(pages, page)
			}
		}
	}
	return pages, nil
}

// The page-aware extraction below uses the same positional rows as the
// generic PDF reader, but keeps page boundaries so callers can target pages
// or search with context.
func readPDFPages(body []byte, pageSpec, query string, contextLines, maxResults int) (text string, err error) {
	defer recoverPDF(&text, &err)
	reader, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return "", fmt.Errorf("failed to parse PDF: %w", err)
	}
	selected, err := parsePDFPageSpec(pageSpec, reader.NumPage())
	if err != nil {
		return "", err
	}
	query = strings.TrimSpace(query)
	pages := make([]string, reader.NumPage())
	for i, pageNumber := range selected {
		if query == "" && i >= maxResults {
			break
		}
		pages[pageNumber-1] = extractPDFPage(reader.Page(pageNumber))
	}
	return formatPDFPages(pages, selected, strings.TrimSpace(query), contextLines, maxResults), nil
}

func fetchPDF(ctx context.Context, client *http.Client, urlStr string) ([]byte, error) {
	req, err := newRequest(ctx, urlStr, "application/pdf")
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}
	body, err := readResponseBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read PDF response body: %w", err)
	}
	if !bytes.HasPrefix(bytes.TrimSpace(body), []byte("%PDF-")) {
		return nil, fmt.Errorf("response is not a PDF")
	}
	return body, nil
}

// ReadPDF fetches a PDF and returns selected pages, query matches, or — when
// neither pages nor query is given — a metadata/outline summary that helps
// the caller pick page ranges. pageSpec uses 1-based ranges such as "1-3,17".
func ReadPDF(ctx context.Context, urlStr, pageSpec, query string, contextLines, maxResults int) (string, error) {
	parsedURL, err := validateURL(urlStr)
	if err != nil {
		return "", err
	}
	if contextLines < 0 {
		return "", fmt.Errorf("context must not be negative")
	}
	if maxResults <= 0 {
		return "", fmt.Errorf("max_results must be positive")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	client := newHTTPClient()
	defer client.CloseIdleConnections()
	body, err := fetchPDF(ctx, client, parsedURL.String())
	if err != nil {
		return "", err
	}
	// Without a page selection or query, return metadata plus the outline so
	// the caller can target specific pages instead of pulling the whole
	// document.
	if strings.TrimSpace(pageSpec) == "" && strings.TrimSpace(query) == "" {
		return pdfSummary(body, parsedURL.String())
	}
	return readPDFPages(body, pageSpec, query, contextLines, maxResults)
}

func pdfRows(page pdf.Page) [][]pdf.Text {
	byY := make(map[int64][]pdf.Text)
	for _, text := range page.Content().Text {
		if text.S != "" {
			byY[int64(text.Y)] = append(byY[int64(text.Y)], text)
		}
	}
	rows := make([][]pdf.Text, 0, len(byY))
	for _, row := range byY {
		sort.SliceStable(row, func(i, j int) bool { return row[i].X < row[j].X })
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i][0].Y > rows[j][0].Y })
	return rows
}

// joinPDFRow keeps wide horizontal gaps as column separators. This does not
// attempt to infer a formal table schema, but preserves enough layout for
// tables and command/description pairs to remain readable as Markdown text.
func joinPDFRow(row []pdf.Text) string {
	var line strings.Builder
	var previous pdf.Text
	for _, text := range row {
		if text.S == "" {
			continue
		}
		if strings.TrimSpace(text.S) == "" {
			line.WriteString(text.S)
			previous = text
			continue
		}
		if line.Len() > 0 {
			gap := text.X - (previous.X + previous.W)
			if gap >= 18 {
				line.WriteString("  ")
			} else if gap >= 4 && !strings.HasPrefix(text.S, ".") && !strings.HasPrefix(text.S, ",") {
				line.WriteByte(' ')
			}
		}
		line.WriteString(text.S)
		previous = text
	}
	return strings.TrimSpace(line.String())
}
