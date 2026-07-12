package reader

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// domainPolicy restricts which hosts the reader may fetch. An empty policy
// allows everything (the SSRF guard still applies).
type domainPolicy struct {
	mu    sync.RWMutex
	allow []string
	block []string
}

var readerDomainPolicy = &domainPolicy{}

// SetDomainPolicy configures the reader's host filter. Entries match the
// exact host and all of its subdomains ("example.com" covers both
// example.com and docs.example.com). When allow is non-empty, only matching
// hosts may be fetched; block entries are always refused and win over allow.
func SetDomainPolicy(allow, block []string) {
	readerDomainPolicy.mu.Lock()
	defer readerDomainPolicy.mu.Unlock()
	readerDomainPolicy.allow = normalizeDomainList(allow)
	readerDomainPolicy.block = normalizeDomainList(block)
}

func normalizeDomainList(domains []string) []string {
	normalized := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(domain, "*.")))
		domain = strings.Trim(domain, ".")
		if domain != "" {
			normalized = append(normalized, domain)
		}
	}
	return normalized
}

// checkDomainPolicy reports whether the URL's host is fetchable under the
// configured policy.
func checkDomainPolicy(parsedURL *url.URL) error {
	host := strings.ToLower(parsedURL.Hostname())

	readerDomainPolicy.mu.RLock()
	defer readerDomainPolicy.mu.RUnlock()

	for _, blocked := range readerDomainPolicy.block {
		if hostMatchesDomain(host, blocked) {
			return fmt.Errorf("host %q is blocked by the domain blocklist", host)
		}
	}
	if len(readerDomainPolicy.allow) == 0 {
		return nil
	}
	for _, allowed := range readerDomainPolicy.allow {
		if hostMatchesDomain(host, allowed) {
			return nil
		}
	}
	return fmt.Errorf("host %q is not on the domain allowlist", host)
}

// hostMatchesDomain reports whether host equals domain or is a subdomain of it.
func hostMatchesDomain(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}
