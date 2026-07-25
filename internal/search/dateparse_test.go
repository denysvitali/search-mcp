package search

import "testing"

func TestSplitPublished(t *testing.T) {
	for _, tc := range []struct {
		name          string
		in            string
		wantPublished string
		wantRest      string
	}{
		// Real prefixes captured from live Yahoo and DuckDuckGo SERPs.
		{
			name:          "yahoo middle-endian with interpunct",
			in:            "Jul 16, 2025 · Learn how to use operators, software extensions to Kubernetes.",
			wantPublished: "2025-07-16",
			wantRest:      "Learn how to use operators, software extensions to Kubernetes.",
		},
		{
			name:          "yahoo older date",
			in:            "Nov 20, 2021 · If you have a situation where you have multiple long-running operations.",
			wantPublished: "2021-11-20",
			wantRest:      "If you have a situation where you have multiple long-running operations.",
		},
		{
			name:          "day-first with dash",
			in:            "16 Jul 2025 - A deep dive into the operator pattern.",
			wantPublished: "2025-07-16",
			wantRest:      "A deep dive into the operator pattern.",
		},
		{
			name:          "iso date",
			in:            "2026-02-20 · A guide to distributed tracing.",
			wantPublished: "2026-02-20",
			wantRest:      "A guide to distributed tracing.",
		},
		{
			name:          "full month name",
			in:            "January 2, 2026 · New year notes.",
			wantPublished: "2026-01-02",
			wantRest:      "New year notes.",
		},

		// Snippets that merely contain a separator must be left intact.
		{
			name:     "no date prefix",
			in:       "Package context defines the Context type - it carries deadlines.",
			wantRest: "Package context defines the Context type - it carries deadlines.",
		},
		{
			name:     "separator but unparseable prefix",
			in:       "Introduction · Concurrency is a fundamental aspect of Go.",
			wantRest: "Introduction · Concurrency is a fundamental aspect of Go.",
		},
		{
			name:     "long prefix is not a date",
			in:       "The complete guide to everything - and more besides.",
			wantRest: "The complete guide to everything - and more besides.",
		},
		{
			name:     "empty",
			in:       "",
			wantRest: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			published, rest := SplitPublished(tc.in)
			if published != tc.wantPublished {
				t.Errorf("published = %q, want %q", published, tc.wantPublished)
			}
			if rest != tc.wantRest {
				t.Errorf("rest = %q, want %q", rest, tc.wantRest)
			}
		})
	}
}
