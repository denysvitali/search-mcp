package cmd

import (
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
