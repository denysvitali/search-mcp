package cmd

import (
	"strings"
	"testing"

	"github.com/denysvitali/search-mcp/internal/search"
	"github.com/sirupsen/logrus"
)

func TestClampCount(t *testing.T) {
	cases := []struct {
		name    string
		in      float64
		want    int
		wantErr bool
	}{
		{"zero", 0, 0, false},
		{"normal", 10, 10, false},
		{"truncates fractional", 10.9, 10, false},
		{"at cap", maxResultCount, maxResultCount, false},
		{"over cap clamps", 10_000, maxResultCount, false},
		{"negative errors", -1, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := clampCount(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("clampCount(%v) expected error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("clampCount(%v) error = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("clampCount(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewLoggerInvalidLevelDefaultsToInfo(t *testing.T) {
	t.Setenv("SEARCH_MCP_LOG_LEVEL", "not-a-level")
	logger := newLogger().(*logrus.Logger)
	if logger.GetLevel() != logrus.InfoLevel {
		t.Errorf("level = %v, want info", logger.GetLevel())
	}
}

func TestNewSearchServiceDefaults(t *testing.T) {
	svc, err := newSearchService(logrus.New())
	if err != nil {
		t.Fatalf("newSearchService error = %v", err)
	}
	if svc == nil {
		t.Fatal("newSearchService returned nil service")
	}
}

func TestRenderResults(t *testing.T) {
	out := renderResults(search.Response{
		Provider: "duckduckgo",
		Query:    "golang",
		Results: []search.Result{
			{Title: "Go", URL: "https://go.dev", Description: "The Go language"},
		},
	})
	for _, want := range []string{"duckduckgo", "golang", "Go", "https://go.dev", "The Go language"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderResults output missing %q\n%s", want, out)
		}
	}
}

func TestRenderResultsEmpty(t *testing.T) {
	out := renderResults(search.Response{Provider: "mojeek", Query: "x"})
	if !strings.Contains(out, "No results.") {
		t.Errorf("expected empty-results message, got %q", out)
	}
}
