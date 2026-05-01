package search

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus"
)

type stubProvider struct {
	name string
}

func (s stubProvider) Name() string {
	return s.name
}

func (s stubProvider) Search(ctx context.Context, req Request) (Response, error) {
	return Response{
		Query:    req.Query,
		Provider: s.name,
		Results: []Result{{
			Title:  "result",
			URL:    "https://example.com",
			Source: s.name,
		}},
	}, nil
}

func TestServiceSearchUsesRequestedProvider(t *testing.T) {
	service, err := NewService([]Provider{stubProvider{name: "first"}, stubProvider{name: "second"}}, 100, 1, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	resp, err := service.Search(context.Background(), Request{Query: "test", Provider: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "second" {
		t.Fatalf("provider = %q, want second", resp.Provider)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(resp.Results))
	}
}

func TestServiceRejectsUnknownProvider(t *testing.T) {
	service, err := NewService([]Provider{stubProvider{name: "first"}}, 100, 1, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Search(context.Background(), Request{Query: "test", Provider: "missing"})
	if err == nil {
		t.Fatal("expected error")
	}
}
