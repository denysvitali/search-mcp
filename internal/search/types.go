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
}

type Result struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	Published   string `json:"published,omitempty"`
}

type Response struct {
	Query    string   `json:"query"`
	Provider string   `json:"provider"`
	Results  []Result `json:"results"`
}

type Provider interface {
	Name() string
	Search(ctx context.Context, req Request) (Response, error)
}
