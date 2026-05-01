package main_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

type searchResponse struct {
	Query    string `json:"query"`
	Provider string `json:"provider"`
	Results  []struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Description string `json:"description"`
		Source      string `json:"source"`
	} `json:"results"`
}

func TestIntegrationCLIWebSearch(t *testing.T) {
	binary := buildBinary(t)
	mockSearch := newDuckDuckGoMock(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "search", "integration query", "--provider", "duckduckgo", "--count", "2", "--json")
	cmd.Env = append(os.Environ(), "SEARCH_MCP_DUCKDUCKGO_ENDPOINT="+mockSearch.URL)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("search command failed: %v\n%s", err, output)
	}

	var resp searchResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	assertSearchResponse(t, resp)
}

func TestIntegrationMCPWebSearchTool(t *testing.T) {
	binary := buildBinary(t)
	mockSearch := newDuckDuckGoMock(t)

	mcpClient, err := client.NewStdioMCPClient(binary, []string{
		"SEARCH_MCP_DUCKDUCKGO_ENDPOINT=" + mockSearch.URL,
	}, "serve")
	if err != nil {
		t.Fatalf("start mcp client: %v", err)
	}
	defer mcpClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcp.Implementation{Name: "integration-test", Version: "1.0.0"}
	if _, err := mcpClient.Initialize(ctx, initRequest); err != nil {
		t.Fatalf("initialize mcp client: %v", err)
	}

	request := mcp.CallToolRequest{}
	request.Params.Name = "search"
	request.Params.Arguments = map[string]any{
		"query":    "integration query",
		"provider": "duckduckgo",
		"count":    float64(2),
	}

	result, err := mcpClient.CallTool(ctx, request)
	if err != nil {
		t.Fatalf("call search: %v", err)
	}
	if result.IsError {
		t.Fatalf("search returned error: %#v", result.Content)
	}

	text := toolText(t, result)
	var resp searchResponse
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("decode tool result: %v\n%s", err, text)
	}
	assertSearchResponse(t, resp)
}

func buildBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "search-mcp")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build binary: %v\n%s", err, output)
	}
	return binary
}

func newDuckDuckGoMock(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if got := r.PostForm.Get("q"); got != "integration query" {
			t.Errorf("query = %q, want integration query", got)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>
<div class="result results_links results_links_deep web-result">
  <div class="links_main links_deep result__body">
    <h2 class="result__title"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.test%2Fintegration&amp;rut=abc">Integration Result</a></h2>
    <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.test%2Fintegration">Result from the mock DuckDuckGo HTML endpoint.</a>
  </div>
</div>
<div class="result results_links results_links_deep web-result">
  <div class="links_main links_deep result__body">
    <h2 class="result__title"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.test%2Fsecond">Second Result</a></h2>
    <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.test%2Fsecond">Second snippet.</a>
  </div>
</div>
</body></html>`))
	}))
	t.Cleanup(server.Close)
	return server
}

func assertSearchResponse(t *testing.T, resp searchResponse) {
	t.Helper()

	if resp.Query != "integration query" {
		t.Fatalf("query = %q, want integration query", resp.Query)
	}
	if resp.Provider != "duckduckgo" {
		t.Fatalf("provider = %q, want duckduckgo", resp.Provider)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
	if resp.Results[0].Title != "Integration Result" {
		t.Fatalf("first title = %q, want Integration Result", resp.Results[0].Title)
	}
	if resp.Results[0].URL != "https://example.test/integration" {
		t.Fatalf("first URL = %q, want https://example.test/integration", resp.Results[0].URL)
	}
	if resp.Results[1].URL != "https://example.test/second" {
		t.Fatalf("second URL = %q, want https://example.test/second", resp.Results[1].URL)
	}
}

func toolText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	var b strings.Builder
	for _, content := range result.Content {
		text, ok := content.(mcp.TextContent)
		if !ok {
			continue
		}
		b.WriteString(text.Text)
	}
	if b.Len() == 0 {
		t.Fatalf("tool result had no text content: %#v", result.Content)
	}
	return b.String()
}
