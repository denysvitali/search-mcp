package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestIntegrationQualityControls(t *testing.T) {
	binary := buildBinary(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("keyless search sent authorization")
		}
		fmt.Fprint(w, `{"hits":[
            {"title":"First","url":"https://example.test/one","story_text":"An excerpt","created_at":"2026-09-01T00:00:00Z"},
            {"title":"Second","url":"https://example.test/two"},
            {"title":"Third","url":"https://other.test/three"}
        ]}`)
	}))
	defer mock.Close()
	env := append(os.Environ(),
		"SEARCH_MCP_PROVIDERS=hackernews",
		"SEARCH_MCP_HACKERNEWS_ENDPOINT="+mock.URL,
		"SEARCH_MCP_PROVIDER=hackernews",
		"SEARCH_MCP_INCLUDE_DOMAINS=example.test",
		"SEARCH_MCP_EXCLUDE_DOMAINS=",
		"SEARCH_MCP_MAX_PER_HOST=0",
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, binary, "search", "q", "--max-per-host", "1", "--json")
	command.Env = env
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI: %v\n%s", err, output)
	}
	var cli searchResponse
	if err := json.Unmarshal(output, &cli); err != nil {
		t.Fatal(err)
	}
	if len(cli.Results) != 1 || cli.Results[0].URL != "https://example.test/one" {
		t.Fatalf("CLI filter: %+v", cli)
	}

	serverCmd := exec.Command(binary, "serve")
	serverCmd.Env = env
	client := mcp.NewClient(&mcp.Implementation{Name: "quality-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: serverCmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	// Omission inherits configured filters; explicit [] clears them.
	for _, tc := range []struct {
		name string
		args map[string]any
		want int
	}{
		{"inherits config", map[string]any{"query": "q", "max_per_host": 1}, 1},
		{"clears config", map[string]any{"query": "q", "include_domains": []string{}, "max_per_host": 1}, 2},
		{"excludes domain", map[string]any{"query": "q", "include_domains": []string{}, "exclude_domains": []string{"example.test"}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search", Arguments: tc.args})
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError {
				t.Fatalf("MCP search: %+v", result.Content)
			}
			var resp searchResponse
			if err := json.Unmarshal([]byte(toolStructured(t, result)), &resp); err != nil {
				t.Fatal(err)
			}
			if len(resp.Results) != tc.want {
				t.Fatalf("MCP results = %+v, want %d", resp, tc.want)
			}
		})
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_batch", Arguments: map[string]any{
		"queries": []string{"q", "q"}, "include_domains": []string{}, "exclude_domains": []string{"example.test"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("MCP batch: %+v", result.Content)
	}
	var batch struct {
		Items []struct {
			Response searchResponse `json:"response"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(toolStructured(t, result)), &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 2 {
		t.Fatalf("batch: %+v", batch)
	}
	for _, item := range batch.Items {
		if len(item.Response.Results) != 1 || item.Response.Results[0].URL != "https://other.test/three" {
			t.Fatalf("batch lost filter or query: %+v", item)
		}
	}
}
