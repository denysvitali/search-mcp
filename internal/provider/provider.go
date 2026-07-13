// Package provider contains the shared provider registry and error sentinels.
// Concrete backends live in sibling subpackages and register themselves from
// init, so adding a backend does not require editing a central switch.
package provider

import (
	"fmt"
	"sort"
	"sync"

	"github.com/denysvitali/search-mcp/internal/search"
)

var (
	ErrRateLimited = search.ErrRateLimited
	ErrBlocked     = search.ErrBlocked
)

// Constructor builds one provider from its configured key and endpoint. A
// provider that does not need either argument simply ignores them.
type Constructor func(key, endpoint string) (search.Provider, error)

// Provider is the common search contract implemented by every backend.
type Provider = search.Provider

var registry = struct {
	sync.RWMutex
	constructors map[string]Constructor
}{constructors: make(map[string]Constructor)}

// Register adds a provider constructor. It is intended for init functions in
// dedicated provider subpackages and panics on duplicate names so a wiring
// mistake cannot silently select the wrong backend.
func Register(name string, constructor Constructor) {
	if name == "" || constructor == nil {
		panic("provider: invalid registration")
	}
	registry.Lock()
	defer registry.Unlock()
	if _, exists := registry.constructors[name]; exists {
		panic("provider: duplicate registration for " + name)
	}
	registry.constructors[name] = constructor
}

// Names returns all registered provider names in stable order.
func Names() []string {
	registry.RLock()
	defer registry.RUnlock()
	names := make([]string, 0, len(registry.constructors))
	for name := range registry.constructors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// New constructs a registered provider by name.
func New(name, key, endpoint string) (search.Provider, error) {
	registry.RLock()
	constructor := registry.constructors[name]
	registry.RUnlock()
	if constructor == nil {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	return constructor(key, endpoint)
}
