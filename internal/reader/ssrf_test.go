package reader

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestMain enables allowPrivateHosts for the whole reader test suite so the
// httptest servers (bound to loopback) are reachable. Tests that exercise the
// SSRF guard itself flip it back off locally.
func TestMain(m *testing.M) {
	allowPrivateHosts = true
	os.Exit(m.Run())
}

func TestReadBlocksLoopbackWhenGuardEnabled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>secret</body></html>"))
	}))
	defer ts.Close()

	allowPrivateHosts = false
	defer func() { allowPrivateHosts = true }()

	_, err := Read(context.Background(), ts.URL)
	if err == nil {
		t.Fatal("expected SSRF guard to block loopback address")
	}
	if !strings.Contains(err.Error(), "non-public address") {
		t.Fatalf("error = %q, want non-public address mention", err.Error())
	}
}

func TestIsDisallowedIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"10.0.0.5", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true}, // cloud metadata
		{"0.0.0.0", true},
		{"fc00::1", true},
		{"fe80::1", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false}, // example.com
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("could not parse test ip %q", tc.ip)
		}
		if got := isDisallowedIP(ip); got != tc.want {
			t.Errorf("isDisallowedIP(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestGuardDialAddressRejectsBadInput(t *testing.T) {
	allowPrivateHosts = false
	defer func() { allowPrivateHosts = true }()

	if err := guardDialAddress("tcp", "not-an-address", nil); err == nil {
		t.Error("expected error for malformed dial address")
	}
	if err := guardDialAddress("tcp", "example.com:443", nil); err == nil {
		t.Error("expected error for unresolved host (non-IP)")
	}
}
