package exa

import (
	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/provider/api"
	"github.com/denysvitali/search-mcp/internal/search"
)

type Exa = api.Exa

var _ provider.Provider = (*Exa)(nil)

func New(key string, endpoint ...string) *Exa { return api.NewExa(key, endpoint...) }
func NewChecked(key string, endpoint ...string) (*Exa, error) {
	return api.NewExaChecked(key, endpoint...)
}

func init() {
	provider.Register("exa", func(key, endpoint string) (search.Provider, error) {
		return NewChecked(key, endpoint)
	})
}
