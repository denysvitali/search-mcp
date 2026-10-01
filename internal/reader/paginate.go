package reader

import (
	"context"
	"fmt"
	"strings"
)

const (
	// defaultQueryContextLines is how many lines surround each in-page query
	// match when the caller does not specify a context.
	defaultQueryContextLines = 2
	// maxQueryContextLines bounds surrounding lines returned for each match.
	maxQueryContextLines = 10
	// defaultMaxQueryMatches is how many matches an in-page query returns when
	// the caller does not specify a limit.
	defaultMaxQueryMatches = 20
	// maxQueryMatches bounds how many matches an in-page query may return.
	maxQueryMatches = 100
)

// ReadOptions shape the Markdown returned by ReadWithOptions without changing
// how the page is fetched.
type ReadOptions struct {
	// MaxLength truncates the content to at most this many characters
	// (runes). Zero means no limit.
	MaxLength int
	// StartIndex skips this many characters before applying MaxLength,
	// allowing chunked reads of long pages.
	StartIndex int
	// Query, when non-empty, switches to grep mode: only lines matching the
	// case-insensitive query are returned, each with ContextLines of
	// surrounding lines and a line-number prefix.
	Query string
	// ContextLines is the number of lines of context around each query match.
	ContextLines int
	// ContextLinesSet distinguishes an explicit zero from the default.
	ContextLinesSet bool
	// MaxMatches caps how many query matches are returned.
	MaxMatches int
}

// ReadWithOptions fetches the URL like Read and then applies pagination or an
// in-page query to the resulting Markdown.
func ReadWithOptions(ctx context.Context, urlStr string, opts ReadOptions) (string, error) {
	if err := opts.Validate(); err != nil {
		return "", err
	}

	content, err := Read(ctx, urlStr)
	if err != nil {
		return "", err
	}
	return opts.Apply(content)
}

// Validate checks options before performing any network request.
func (opts ReadOptions) Validate() error {
	if opts.MaxLength < 0 {
		return fmt.Errorf("max_length must not be negative")
	}
	if opts.StartIndex < 0 {
		return fmt.Errorf("start_index must not be negative")
	}
	if opts.ContextLines < 0 || opts.ContextLines > maxQueryContextLines {
		return fmt.Errorf("context must be between 0 and %d", maxQueryContextLines)
	}
	if opts.MaxMatches < 0 || opts.MaxMatches > maxQueryMatches {
		return fmt.Errorf("max_matches must be between 0 and %d", maxQueryMatches)
	}

	return nil
}

// Apply shapes already fetched content, including query and link results.
func (opts ReadOptions) Apply(content string) (string, error) {
	if err := opts.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(opts.Query) != "" {
		contextLines := opts.ContextLines
		if contextLines == 0 && !opts.ContextLinesSet {
			contextLines = defaultQueryContextLines
		}
		content = grepContent(content, strings.TrimSpace(opts.Query), contextLines, opts.MaxMatches)
	}
	return paginateContent(content, opts.StartIndex, opts.MaxLength), nil
}

// paginateContent returns the [start, start+maxLength) character window of
// content, with a trailing marker telling the caller how to fetch the next
// chunk when content was truncated.
func paginateContent(content string, start, maxLength int) string {
	runes := []rune(content)
	total := len(runes)
	if start >= total && start > 0 {
		return fmt.Sprintf("[no content: start_index=%d is beyond the end of the %d-character document]", start, total)
	}
	end := total
	if maxLength > 0 && maxLength < total-start {
		end = start + maxLength
	}
	window := string(runes[start:end])
	if end < total {
		window += fmt.Sprintf("\n\n[content truncated: showing characters %d-%d of %d; call again with start_index=%d to continue]", start, end, total, end)
	}
	return window
}

// grepContent returns the lines of content matching the case-insensitive
// query, each block prefixed with 1-based line numbers and padded with
// contextLines of surrounding lines. Overlapping blocks are merged.
func grepContent(content, query string, contextLines, maxMatches int) string {
	if maxMatches == 0 {
		maxMatches = defaultMaxQueryMatches
	}

	lines := strings.Split(content, "\n")
	lowerQuery := strings.ToLower(query)

	var matched []int
	truncated := false
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), lowerQuery) {
			if len(matched) >= maxMatches {
				truncated = true
				break
			}
			matched = append(matched, i)
		}
	}
	if len(matched) == 0 {
		return fmt.Sprintf("[no matches for %q in %d lines]", query, len(lines))
	}

	var output strings.Builder
	fmt.Fprintf(&output, "%d match(es) for %q:\n", len(matched), query)
	lastPrinted := -1
	for _, m := range matched {
		start := m - contextLines
		if start <= lastPrinted {
			start = lastPrinted + 1
		}
		if start < 0 {
			start = 0
		}
		end := m + contextLines
		if end >= len(lines) {
			end = len(lines) - 1
		}
		if lastPrinted >= 0 && start > lastPrinted+1 {
			output.WriteString("--\n")
		}
		for i := start; i <= end; i++ {
			fmt.Fprintf(&output, "%d: %s\n", i+1, lines[i])
		}
		lastPrinted = end
	}
	if truncated {
		fmt.Fprintf(&output, "[matches truncated at %d; increase max_matches or narrow query]\n", maxMatches)
	}
	return strings.TrimSpace(output.String())
}
