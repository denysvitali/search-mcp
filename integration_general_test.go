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
)

// Test the out-of-box broad web path, without specialist indexes or API keys.
func TestIntegrationDefaultGeneralWebFallback(t *testing.T) {
	binary := buildBinary(t)
	topics := map[string]string{
		"Kyoto autumn travel":              "https://www.japan-guide.com/e/e3953.html",
		"coffee stain removal cotton":      "https://www.thespruce.com/remove-coffee-stains-from-clothing-1901014",
		"James Webb telescope discoveries": "https://science.nasa.gov/mission/webb/",
		"bicycle belt drive maintenance":   "https://www.gatescarbondrive.com/resources/manuals-and-tech",
	}
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<html><title>Captcha</title>verify you are human</html>`)
	}))
	defer blocked.Close()
	yahoo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("p")
		target, ok := topics[query]
		if !ok {
			t.Errorf("unexpected query %q", query)
			http.Error(w, "unexpected", 400)
			return
		}
		fmt.Fprintf(w, `<html><div id="web"><div class="algo-sr"><a href="%s"><h3 class="title">%s</h3></a><div class="compText">General web result</div></div></div></html>`, target, query)
	}))
	defer yahoo.Close()
	for query, target := range topics {
		t.Run(query, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "search", query, "--count", "1", "--json", "--log-level", "error")
			cmd.Env = append(os.Environ(), "SEARCH_MCP_PROVIDERS=", "SEARCH_MCP_PROVIDER=", "SEARCH_MCP_DUCKDUCKGO_ENDPOINT="+blocked.URL, "SEARCH_MCP_YAHOO_ENDPOINT="+yahoo.URL)
			for _, key := range []string{"BRAVE_API_KEY", "KAGI_API_KEY", "EXA_API_KEY", "TAVILY_API_KEY", "SERPER_API_KEY", "SEARXNG_URL"} {
				cmd.Env = append(cmd.Env, "SEARCH_MCP_"+key+"=")
			}
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("CLI failed %v\n%s", err, output)
			}
			var resp struct {
				Provider string `json:"provider"`
				Results  []struct {
					URL    string `json:"url"`
					Source string `json:"source"`
				} `json:"results"`
				Degraded []struct {
					Provider string `json:"provider"`
				} `json:"degraded"`
			}
			if err := json.Unmarshal(output, &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Provider != "all" || len(resp.Results) != 1 || resp.Results[0].URL != target || resp.Results[0].Source != "yahoo" || len(resp.Degraded) != 1 || resp.Degraded[0].Provider != "duckduckgo" {
				t.Fatalf("wrong general-web fallback: %+v", resp)
			}
		})
	}
}
