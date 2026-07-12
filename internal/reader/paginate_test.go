package reader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPaginateContent(t *testing.T) {
	content := strings.Repeat("a", 100)

	t.Run("no limit returns everything", func(t *testing.T) {
		if got := paginateContent(content, 0, 0); got != content {
			t.Errorf("got %d chars, want full content", len(got))
		}
	})

	t.Run("window with truncation marker", func(t *testing.T) {
		got := paginateContent(content, 10, 20)
		if !strings.HasPrefix(got, strings.Repeat("a", 20)) {
			t.Errorf("window prefix wrong: %q", got)
		}
		if !strings.Contains(got, "start_index=30") {
			t.Errorf("missing continuation marker: %q", got)
		}
		if !strings.Contains(got, "characters 10-30 of 100") {
			t.Errorf("missing range info: %q", got)
		}
	})

	t.Run("final chunk has no marker", func(t *testing.T) {
		got := paginateContent(content, 90, 20)
		if got != strings.Repeat("a", 10) {
			t.Errorf("got %q, want 10 chars without marker", got)
		}
	})

	t.Run("start beyond end", func(t *testing.T) {
		got := paginateContent(content, 200, 10)
		if !strings.Contains(got, "beyond the end") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("multibyte runes are not split", func(t *testing.T) {
		got := paginateContent("héllo wörld", 1, 4)
		if !strings.HasPrefix(got, "éllo") {
			t.Errorf("got %q, want prefix éllo", got)
		}
	})
}

func TestGrepContent(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line number %d", i))
	}
	lines[9] = "the NEEDLE is here"
	lines[19] = "another needle appears"
	content := strings.Join(lines, "\n")

	t.Run("case-insensitive matches with line numbers and context", func(t *testing.T) {
		got := grepContent(content, "needle", 1, 10)
		for _, want := range []string{"2 match(es)", "10: the NEEDLE is here", "9: line number 9", "11: line number 11", "20: another needle appears", "--"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
	})

	t.Run("max matches caps output", func(t *testing.T) {
		got := grepContent(content, "line", 0, 3)
		if !strings.Contains(got, "3 match(es)") {
			t.Errorf("got:\n%s", got)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		got := grepContent(content, "zebra", 0, 0)
		if !strings.Contains(got, "no matches") {
			t.Errorf("got %q", got)
		}
	})
}

func TestReadWithOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><p>alpha beta gamma delta epsilon</p></body></html>"))
	}))
	t.Cleanup(server.Close)

	t.Run("pagination", func(t *testing.T) {
		got, err := ReadWithOptions(context.Background(), server.URL, ReadOptions{MaxLength: 5})
		if err != nil {
			t.Fatalf("ReadWithOptions: %v", err)
		}
		if !strings.HasPrefix(got, "alpha") || !strings.Contains(got, "start_index=5") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("query", func(t *testing.T) {
		got, err := ReadWithOptions(context.Background(), server.URL, ReadOptions{Query: "GAMMA"})
		if err != nil {
			t.Fatalf("ReadWithOptions: %v", err)
		}
		if !strings.Contains(got, "1 match(es)") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("invalid options", func(t *testing.T) {
		if _, err := ReadWithOptions(context.Background(), server.URL, ReadOptions{StartIndex: -1}); err == nil {
			t.Error("expected error for negative start_index")
		}
		if _, err := ReadWithOptions(context.Background(), server.URL, ReadOptions{ContextLines: 99}); err == nil {
			t.Error("expected error for oversized context")
		}
	})
}
