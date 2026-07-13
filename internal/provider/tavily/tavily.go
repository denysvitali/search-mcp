package tavily

import (
	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/provider/api"
	"github.com/denysvitali/search-mcp/internal/search"
)

type Tavily = api.Tavily

var _ provider.Provider = (*Tavily)(nil)

func New(key string, endpoint ...string) *Tavily { return api.NewTavily(key, endpoint...) }
func NewChecked(key string, endpoint ...string) (*Tavily, error) {
	return api.NewTavilyChecked(key, endpoint...)
}

func init() {
	provider.Register("tavily", func(key, endpoint string) (search.Provider, error) {
		return NewChecked(key, endpoint)
	})
}
