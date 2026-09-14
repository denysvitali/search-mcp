package reader

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestDomainPolicy(t *testing.T) {
	t.Cleanup(func() { SetDomainPolicy(nil, nil) })

	t.Run("blocklist refuses host and subdomains", func(t *testing.T) {
		SetDomainPolicy(nil, []string{"Tracker.example"})
		for _, u := range []string{"https://tracker.example/x", "https://cdn.tracker.example/y"} {
			if _, err := Read(context.Background(), u); err == nil || !strings.Contains(err.Error(), "blocklist") {
				t.Errorf("Read(%s) err = %v, want blocklist error", u, err)
			}
		}
		if _, err := validateURL("https://ok.example/x"); err != nil {
			t.Errorf("unblocked host refused: %v", err)
		}
	})

	t.Run("allowlist permits only listed hosts", func(t *testing.T) {
		SetDomainPolicy([]string{"*.example.com"}, nil)
		if _, err := validateURL("https://docs.example.com/page"); err != nil {
			t.Errorf("allowed host refused: %v", err)
		}
		if _, err := validateURL("https://example.com/page"); err != nil {
			t.Errorf("apex of allowed domain refused: %v", err)
		}
		if _, err := validateURL("https://other.test/page"); err == nil || !strings.Contains(err.Error(), "allowlist") {
			t.Errorf("unlisted host err = %v, want allowlist error", err)
		}
	})

	t.Run("block wins over allow", func(t *testing.T) {
		SetDomainPolicy([]string{"example.com"}, []string{"evil.example.com"})
		if _, err := validateURL("https://evil.example.com/x"); err == nil {
			t.Error("blocked subdomain must be refused despite allowlist")
		}
	})

	t.Run("empty policy allows everything", func(t *testing.T) {
		SetDomainPolicy(nil, nil)
		if _, err := validateURL("https://anything.test/x"); err != nil {
			t.Errorf("empty policy refused: %v", err)
		}
		if parsed, err := validateURL("HTTPS://anything.test/x"); err != nil || parsed.Scheme != "https" {
			t.Errorf("uppercase scheme validation = %v, %v; want normalized https", parsed, err)
		}
	})
}

func TestRedirectHonorsDomainPolicy(t *testing.T) {
	t.Cleanup(func() { SetDomainPolicy(nil, nil) })
	SetDomainPolicy([]string{"allowed.example"}, []string{"blocked.example"})

	client := newHTTPClient()
	redirect, err := http.NewRequest(http.MethodGet, "https://blocked.example/redirected", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(redirect, nil); err == nil || !strings.Contains(err.Error(), "blocklist") {
		t.Fatalf("redirect error = %v, want blocklist rejection", err)
	}

	if _, err := validateURL("https://blocked.example./resource"); err == nil || !strings.Contains(err.Error(), "blocklist") {
		t.Fatalf("trailing-dot blocked host error = %v, want blocklist rejection", err)
	}
}
