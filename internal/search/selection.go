package search

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

// MaxResultCount is shared by the service, CLI and MCP entry points.
const MaxResultCount = 50

// candidateCount retrieves enough candidates to discover cross-provider
// agreement even for count=1. Filtering/diversity gets a larger, bounded pool.
// The ordinary default remains one ten-result request per provider.
func candidateCount(req Request) int {
	count := max(req.Count, 10)
	if len(req.IncludeDomains)+len(req.ExcludeDomains) > 0 || req.MaxPerHost > 0 {
		count = max(count, min(req.Count*3, MaxResultCount))
	}
	return min(count, MaxResultCount)
}

func normalizeDomains(domains []string) ([]string, error) {
	var normalized []string
	for _, entry := range domains {
		for _, domain := range strings.Split(entry, ",") {
			domain = strings.ToLower(strings.TrimSpace(domain))
			if domain == "" {
				continue
			}
			domain = strings.TrimSuffix(domain, ".")
			ascii, err := idna.Lookup.ToASCII(domain)
			if err != nil || strings.ContainsAny(ascii, "/:@?#* ") || ascii == "" {
				return nil, fmt.Errorf("invalid search domain %q: use a hostname such as example.com", domain)
			}
			for _, label := range strings.Split(ascii, ".") {
				if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
					return nil, fmt.Errorf("invalid search domain %q", domain)
				}
			}
			normalized = append(normalized, ascii)
		}
	}
	return normalized, nil
}

func matchesDomain(host string, domains []string) bool {
	for _, domain := range domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// selectResults applies filters after fusion and before truncation. Providers
// and their caches hold unfiltered candidates so one request cannot poison
// another request's domain selection.
func selectResults(results []Result, req Request) []Result {
	selected := make([]Result, 0, min(len(results), req.Count))
	hostCounts := make(map[string]int)
	for _, result := range results {
		u, err := url.Parse(result.URL)
		if err != nil {
			continue
		}
		host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
		if net.ParseIP(host) == nil {
			host, err = idna.Lookup.ToASCII(host)
			if err != nil {
				continue
			}
		}
		if len(req.IncludeDomains) > 0 && !matchesDomain(host, req.IncludeDomains) {
			continue
		}
		if matchesDomain(host, req.ExcludeDomains) {
			continue
		}
		host = strings.TrimPrefix(host, "www.")
		if req.MaxPerHost > 0 && hostCounts[host] >= req.MaxPerHost {
			continue
		}
		hostCounts[host]++
		selected = append(selected, result)
		if len(selected) >= req.Count {
			break
		}
	}
	return selected
}
