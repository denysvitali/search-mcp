package search

import "context"

type Request struct {
	Query        string
	Count        int
	Country      string
	Language     string
	SafeSearch   string
	Freshness    string
	Provider     string
	ExtraHeaders map[string]string
	// Result filters are applied locally, consistently across all providers.
	IncludeDomains []string
	ExcludeDomains []string
	MaxPerHost     int
}

type Result struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	Published   string `json:"published,omitempty"`
}

// ProviderFailure records a provider that could not be reached or refused to
// answer during a fan-out or fallback search. It is reported alongside the
// results that surviving providers returned.
type ProviderFailure struct {
	Provider string `json:"provider"`
	Error    string `json:"error"`
}

type Response struct {
	Query    string   `json:"query"`
	Provider string   `json:"provider"`
	Results  []Result `json:"results"`
	// Degraded lists the providers that failed during a fan-out or fallback. Empty
	// when every provider answered.
	Degraded []ProviderFailure `json:"degraded,omitempty"`
}

type Provider interface {
	Name() string
	Search(ctx context.Context, req Request) (Response, error)
}
