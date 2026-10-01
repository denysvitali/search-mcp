package reader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

func TestNavigationUsesRedirectAndBase(t *testing.T) {
	for _, base := range []string{"", `<base href="../assets/">`} {
		t.Run(base, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/docs/chapter/", http.StatusFound)
					return
				}
				w.Header().Set("Content-Type", "Text/HTML; charset=UTF-8")
				fmt.Fprintf(w, `<html><head>%s</head><body><p>Read <a href="next.html">next</a>.</p><a href="#top">local</a></body></html>`, base)
			}))
			defer server.Close()
			path := "/docs/chapter/next.html"
			if base != "" {
				path = "/docs/assets/next.html"
			}
			for _, read := range []func(context.Context, string) (string, error){Read, ExtractLinks} {
				got, err := read(context.Background(), server.URL+"/start")
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(got, server.URL+path) {
					t.Fatalf("wrong destination: %s", got)
				}
			}
		})
	}
}

func TestReadSniffsHTMLAndPDF(t *testing.T) {
	for _, tc := range []struct {
		name, contentType string
		body              []byte
		want              string
	}{
		{"missing HTML type", "", []byte(`<html><body><p>clean content</p></body></html>`), "clean content"},
		{"opaque PDF URL", "application/octet-stream", testPDF("actual paper text"), "actual paper text"},
		{"PDF signature without type", "", testPDF("signature detected"), "signature detected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header()["Content-Type"] = []string{tc.contentType}
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()
			got, err := Read(context.Background(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(got) != tc.want {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestReaderRejectsChallengeWithoutCaching(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/html")
		if calls <= 2 {
			fmt.Fprint(w, `<html><title>Just a moment...</title><form id="challenge-form">Checking your browser</form></html>`)
			return
		}
		fmt.Fprint(w, `<p>Actual article about captcha research and unusual traffic.</p>`)
	}))
	defer server.Close()
	for _, read := range []func(context.Context, string) (string, error){Read, ExtractLinks} {
		if _, err := read(context.Background(), server.URL); err == nil || !strings.Contains(err.Error(), "challenge") {
			t.Fatalf("challenge error = %v", err)
		}
	}
	got, err := Read(context.Background(), server.URL)
	if err != nil || !strings.Contains(got, "Actual article") {
		t.Fatalf("real content: %q, %v", got, err)
	}
}

func TestReadOptionsApplyToQueries(t *testing.T) {
	content := "before\nneedle one\nafter\nneedle two\nlast"
	got, err := (ReadOptions{Query: " needle ", ContextLinesSet: true, MaxMatches: 1}).Apply(content)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "before") || strings.Contains(got, "after") || !strings.Contains(got, "matches truncated") {
		t.Fatalf("zero context/limit: %q", got)
	}
	opts := ReadOptions{Query: "needle", MaxLength: 10}
	first, err := opts.Apply(content)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "start_index=10") {
		t.Fatalf("query ignored pagination: %q", first)
	}
	opts.StartIndex = 10
	second, err := opts.Apply(content)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := (ReadOptions{Query: "needle"}).Apply(content)
	if !strings.HasPrefix(second, string([]rune(full)[10:20])) {
		t.Fatalf("wrong continuation: %q", second)
	}
}

func TestPaginationLargeWindowDoesNotOverflow(t *testing.T) {
	got := paginateContent("hello", 1, int(^uint(0)>>1))
	if got != "ello" {
		t.Fatalf("got %q", got)
	}
}

func TestReadCanceledCacheHit(t *testing.T) {
	SetPageCacheTTL(time.Minute)
	t.Cleanup(func() { SetPageCacheTTL(0) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "cached content") }))
	defer server.Close()
	if _, err := Read(context.Background(), server.URL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Read(ctx, server.URL); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestPDFEmptyResultsAreExplicit(t *testing.T) {
	body := testPDF("some paper text")
	got, err := readPDFPages(body, "1", "absent", 0, 2)
	if err != nil || !strings.Contains(got, "no matches") {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = extractPDFText(testPDF(""))
	if err != nil || !strings.Contains(got, "OCR") {
		t.Fatalf("empty PDF: %q, %v", got, err)
	}
}

func TestPDFMalformedStreamReturnsError(t *testing.T) {
	// A well-formed xref with a content operator missing its operand triggers
	// the library's extraction panic, rather than failing header parsing.
	broken := bytes.Replace(testPDF("text"), []byte("(text) Tj"), []byte("       Tj"), 1)
	for _, parse := range []func([]byte) (string, error){extractPDFText, func(b []byte) (string, error) { return readPDFPages(b, "1", "", 0, 2) }} {
		if _, err := parse(broken); err == nil {
			t.Fatal("malformed content operator should return an error")
		}
	}
}

func TestPDFColumnsAndTables(t *testing.T) {
	rows := make([][]pdf.Text, 0, 11)
	rows = append(rows, []pdf.Text{{X: 220, Y: 800, W: 180, S: "A full-width heading"}})
	for i := range 10 {
		rows = append(rows, []pdf.Text{
			{X: 50, Y: float64(700 - i*12), W: 220, S: fmt.Sprintf("Left prose paragraph line %02d, with enough text.", i)},
			{X: 330, Y: float64(700 - i*12), W: 220, S: fmt.Sprintf("Right prose paragraph line %02d, with enough text.", i)},
		})
	}
	got := orderPDFRows(rows)
	if len(got) != 21 || !strings.HasPrefix(joinPDFRow(got[1]), "Left") || !strings.Contains(joinPDFRow(got[10]), "09") || !strings.HasPrefix(joinPDFRow(got[11]), "Right") {
		t.Fatalf("wrong column order: %+v", got)
	}
	table := make([][]pdf.Text, 10)
	for i := range table {
		table[i] = []pdf.Text{{X: 50, W: 40, S: "name"}, {X: 330, W: 40, S: "value"}}
	}
	if got := orderPDFRows(table); len(got) != len(table) {
		t.Fatal("table rows were split")
	}
}

func FuzzReadPagination(f *testing.F) {
	f.Add("héllo world", 1, 4)
	f.Add("hello", 1, int(^uint(0)>>1))
	f.Fuzz(func(t *testing.T, content string, start, length int) {
		if start < 0 || length < 0 || !utf8.ValidString(content) {
			return
		}
		got, err := (ReadOptions{StartIndex: start, MaxLength: length}).Apply(content)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(got) {
			t.Fatal("split UTF-8 character")
		}
	})
}

func FuzzPDFPageRanges(f *testing.F) {
	f.Add("1-3,2,5", 5)
	f.Add("9223372036854775807", 3)
	f.Fuzz(func(t *testing.T, spec string, total int) {
		if total < 0 || total > 500 {
			return
		}
		pages, err := parsePDFPageSpec(spec, total)
		if err != nil {
			return
		}
		seen := map[int]bool{}
		for _, page := range pages {
			if page < 1 || page > total || seen[page] {
				t.Fatalf("invalid selection: %v", pages)
			}
			seen[page] = true
		}
	})
}
