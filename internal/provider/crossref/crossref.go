// Package crossref searches scholarly metadata using Crossref's public API.
package crossref

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/provider/common"
	"github.com/denysvitali/search-mcp/internal/search"
)

const defaultEndpoint = "https://api.crossref.org/works"

// Crossref searches publication titles, authors and other bibliographic fields.
type Crossref struct{ api *common.JSONAPI }

func init() {
	provider.Register("crossref", func(_, endpoint string) (search.Provider, error) { return New(endpoint), nil })
}

// New constructs a keyless provider. An empty endpoint uses the public API.
func New(endpoint string) *Crossref {
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &Crossref{api: &common.JSONAPI{
		Name: "crossref", Endpoint: endpoint, Client: common.NewHTTPClient(),
		BuildQuery: func(req search.Request, count int) map[string]string {
			params := map[string]string{"query.bibliographic": req.Query, "rows": strconv.Itoa(min(count, 50)), "sort": "relevance"}
			// Repeated type filters are ORed by Crossref. Exclude separately
			// indexed figures/components that otherwise swamp paper searches.
			filters := []string{"type:journal-article", "type:proceedings-article", "type:posted-content"}
			if since, _ := common.FreshnessSince(req.Freshness, time.Now()); !since.IsZero() {
				filters = append(filters, "from-pub-date:"+since.UTC().Format("2006-01-02"))
			}
			params["filter"] = strings.Join(filters, ",")
			return params
		}, Decode: decode,
	}}
}

func (c *Crossref) Name() string { return "crossref" }
func (c *Crossref) Search(ctx context.Context, req search.Request) (search.Response, error) {
	return c.api.Search(ctx, req)
}

func decode(payload []byte) ([]search.Result, error) {
	var body struct {
		Status  string `json:"status"`
		Message struct {
			Items []struct {
				Title     []string `json:"title"`
				DOI       string   `json:"DOI"`
				Abstract  string   `json:"abstract"`
				Publisher string   `json:"publisher"`
				Container []string `json:"container-title"`
				Author    []struct {
					Given  string `json:"given"`
					Family string `json:"family"`
					Name   string `json:"name"`
				} `json:"author"`
				Published struct {
					Parts [][]int `json:"date-parts"`
				} `json:"published"`
			} `json:"items"`
		} `json:"message"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, fmt.Errorf("decode Crossref: %w", err)
	}
	if body.Status != "ok" || body.Message.Items == nil {
		return nil, fmt.Errorf("Crossref response missing successful items envelope")
	}
	results := make([]search.Result, 0, len(body.Message.Items))
	for _, item := range body.Message.Items {
		if len(item.Title) == 0 || strings.TrimSpace(item.DOI) == "" {
			continue
		}
		title := common.HTMLSnippet(item.Title[0])
		if title == "" {
			continue
		}
		link := (&url.URL{Scheme: "https", Host: "doi.org", Path: "/" + strings.TrimSpace(item.DOI)}).String()
		var meta []string
		for _, author := range item.Author[:min(3, len(item.Author))] {
			name := strings.TrimSpace(author.Given + " " + author.Family)
			if name == "" {
				name = author.Name
			}
			if name != "" {
				meta = append(meta, name)
			}
		}
		if len(item.Author) > 3 {
			meta = append(meta, "et al.")
		}
		meta = append(meta, item.Container...)
		if item.Publisher != "" {
			meta = append(meta, item.Publisher)
		}
		if item.Abstract != "" {
			meta = append(meta, common.HTMLSnippet(item.Abstract))
		}
		published := ""
		if len(item.Published.Parts) > 0 {
			published = publicationDate(item.Published.Parts[0])
		}
		results = append(results, search.Result{Title: title, URL: link, Description: strings.Join(meta, " · "), Source: "crossref", Published: published})
	}
	return results, nil
}

// Preserve the precision supplied by the publisher instead of inventing dates.
func publicationDate(parts []int) string {
	if len(parts) == 0 || parts[0] < 1 || parts[0] > 9999 {
		return ""
	}
	date := fmt.Sprintf("%04d", parts[0])
	if len(parts) < 2 || parts[1] < 1 || parts[1] > 12 {
		return date
	}
	date += fmt.Sprintf("-%02d", parts[1])
	if len(parts) < 3 || parts[2] < 1 || parts[2] > 31 {
		return date
	}
	return date + fmt.Sprintf("-%02d", parts[2])
}
