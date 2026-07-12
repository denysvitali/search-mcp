package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/denysvitali/search-mcp/internal/search"
)

func TestLLMNativeProviders(t *testing.T) {
	tests := []struct {
		name, auth, response string
		new                  func(string) *apiProvider
	}{
		{"kagi", "Bot key", `{"data":[{"t":"Kagi result","url":"https://kagi.test","snippet":"snippet","published":"2026-01-01"}]}`, func(endpoint string) *apiProvider { return &NewKagi("key", endpoint).apiProvider }},
		{"exa", "key", `{"results":[{"title":"Exa result","url":"https://exa.test","highlights":["highlight"],"publishedDate":"2026-01-02"}]}`, func(endpoint string) *apiProvider { return &NewExa("key", endpoint).apiProvider }},
		{"tavily", "Bearer key", `{"results":[{"title":"Tavily result","url":"https://tavily.test","content":"content","published_date":"2026-01-03"}]}`, func(endpoint string) *apiProvider { return &NewTavily("key", endpoint).apiProvider }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.name == "kagi" {
					if r.Method != http.MethodGet || r.URL.Query().Get("q") != "go" {
						t.Errorf("unexpected Kagi request %s %s", r.Method, r.URL)
					}
					if r.Header.Get("Authorization") != tc.auth {
						t.Errorf("authorization = %q", r.Header.Get("Authorization"))
					}
				} else {
					if r.Method != http.MethodPost {
						t.Errorf("method = %s", r.Method)
					}
					if tc.name == "exa" && r.Header.Get("X-Api-Key") != tc.auth {
						t.Errorf("x-api-key = %q", r.Header.Get("X-Api-Key"))
					}
					if tc.name == "tavily" && r.Header.Get("Authorization") != tc.auth {
						t.Errorf("authorization = %q", r.Header.Get("Authorization"))
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			p := tc.new(server.URL)
			var resp search.Response
			var err error
			switch tc.name {
			case "kagi":
				resp, err = (&Kagi{*p}).Search(context.Background(), search.Request{Query: "go", Count: 2})
			case "exa":
				resp, err = (&Exa{*p}).Search(context.Background(), search.Request{Query: "go", Count: 2})
			case "tavily":
				resp, err = (&Tavily{*p}).Search(context.Background(), search.Request{Query: "go", Count: 2})
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Results) != 1 || resp.Results[0].Source != tc.name || !strings.Contains(resp.Results[0].Title, "result") {
				t.Fatalf("unexpected response: %#v", resp)
			}
		})
	}
}

func TestLLMNativeProvidersRejectEmptyKey(t *testing.T) {
	for _, newProvider := range []func(string) error{
		func(key string) error { _, err := NewKagiChecked(key); return err },
		func(key string) error { _, err := NewExaChecked(key); return err },
		func(key string) error { _, err := NewTavilyChecked(key); return err },
	} {
		if newProvider(" ") == nil {
			t.Error("expected empty key error")
		}
	}
}
