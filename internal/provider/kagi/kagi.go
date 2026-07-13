package kagi

import (
	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/provider/api"
	"github.com/denysvitali/search-mcp/internal/search"
)

type Kagi = api.Kagi

var _ provider.Provider = (*Kagi)(nil)

func New(key string, endpoint ...string) *Kagi { return api.NewKagi(key, endpoint...) }
func NewChecked(key string, endpoint ...string) (*Kagi, error) {
	return api.NewKagiChecked(key, endpoint...)
}

func init() {
	provider.Register("kagi", func(key, endpoint string) (search.Provider, error) {
		return NewChecked(key, endpoint)
	})
}
