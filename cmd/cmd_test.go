package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denysvitali/search-mcp/internal/search"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

func TestClampCount(t *testing.T) {
	cases := []struct {
		name    string
		in      int
		want    int
		wantErr bool
	}{
		{"zero", 0, 0, false},
		{"normal", 10, 10, false},
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

type batchStubProvider struct{}

func (batchStubProvider) Name() string { return "stub" }
func (batchStubProvider) Search(_ context.Context, req search.Request) (search.Response, error) {
	if strings.Contains(req.Query, "fail") {
		return search.Response{}, errors.New("stub failure")
	}
	return search.Response{
		Query:    req.Query,
		Provider: "stub",
		Results:  []search.Result{{Title: "hit for " + req.Query, URL: "https://stub.test/" + req.Query, Source: "stub"}},
	}, nil
}

func TestRunBatchSearch(t *testing.T) {
	svc, err := search.NewService([]search.Provider{batchStubProvider{}}, 100, 100, logrus.New())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	result := runBatchSearch(context.Background(), svc, []string{"alpha", "will-fail", "beta"}, search.Request{Provider: "stub", Count: 5})
	if len(result.Responses) != 2 {
		t.Fatalf("responses = %d, want 2: %+v", len(result.Responses), result)
	}
	if len(result.Errors) != 1 || result.Errors["will-fail"] == "" {
		t.Errorf("errors = %+v, want one entry for will-fail", result.Errors)
	}
	got := map[string]bool{}
	for _, resp := range result.Responses {
		got[resp.Query] = true
	}
	if !got["alpha"] || !got["beta"] {
		t.Errorf("missing responses: %+v", got)
	}
}

func TestCollectProviderStatus(t *testing.T) {
	svc, err := newSearchService(logrus.New())
	if err != nil {
		t.Fatalf("newSearchService: %v", err)
	}
	result := collectProviderStatus(svc)
	if len(result.Providers) < 2 {
		t.Fatalf("providers = %d, want at least duckduckgo and mojeek", len(result.Providers))
	}
	for _, p := range result.Providers {
		if p.Breaker != "closed" {
			t.Errorf("provider %s breaker = %q, want closed", p.Name, p.Breaker)
		}
		if p.RateLimitTokens <= 0 {
			t.Errorf("provider %s tokens = %v, want > 0", p.Name, p.RateLimitTokens)
		}
	}
}

// TestInitConfig_IgnoresCwdConfig verifies that a search-mcp.yaml in the
// current working directory is NOT picked up by initConfig. This prevents
// running search-mcp from inside another project (e.g. one with its own
// search-mcp.yaml) from silently shadowing the global config in
// ~/.config/search-mcp/search-mcp.yaml.
func TestInitConfig_IgnoresCwdConfig(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	// A foreign search-mcp.yaml in the current dir — must NOT be loaded.
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "search-mcp.yaml"), []byte("log_level: error\n"), 0o644); err != nil {
		t.Fatalf("write cwd config: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// The global config that must win.
	globalDir := filepath.Join(tmpHome, ".config", "search-mcp")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "search-mcp.yaml"), []byte("log_level: debug\n"), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	// Use a local viper so we don't pollute the package-global one.
	v := viper.New()
	if cfg := v.GetString("config"); cfg != "" {
		v.SetConfigFile(cfg)
	} else {
		v.SetConfigName("search-mcp")
		v.SetConfigType("yaml")
		v.AddConfigPath("$HOME/.config/search-mcp")
	}
	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("read config: %v", err)
	}
	if got := v.GetString("log_level"); got != "debug" {
		t.Errorf("log_level = %q, want %q (global config should win over cwd)", got, "debug")
	}
}
