package reader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWaybackFallbackOnGonePage(t *testing.T) {
	mux := http.NewServeMux()
	var origin *httptest.Server
	mux.HandleFunc("/dead", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><p>archived content lives on</p></body></html>"))
	})
	mux.HandleFunc("/wayback/available", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("url"); !strings.Contains(got, "/dead") {
			t.Errorf("availability url = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"archived_snapshots":{"closest":{"available":true,"url":"%s/snapshot","timestamp":"20260101000000"}}}`, origin.URL)
	})
	origin = httptest.NewServer(mux)
	defer origin.Close()

	waybackAvailabilityBaseURL = origin.URL + "/wayback/available"
	t.Cleanup(func() { waybackAvailabilityBaseURL = "http://127.0.0.1:1/wayback/available" })

	got, err := Read(context.Background(), origin.URL+"/dead")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []string{"Wayback Machine snapshot", "20260101000000", "archived content lives on"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestWaybackFallbackSkippedWhenNoSnapshot(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/dead", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/wayback/available", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"archived_snapshots":{}}`))
	})
	origin := httptest.NewServer(mux)
	defer origin.Close()

	waybackAvailabilityBaseURL = origin.URL + "/wayback/available"
	t.Cleanup(func() { waybackAvailabilityBaseURL = "http://127.0.0.1:1/wayback/available" })

	_, err := Read(context.Background(), origin.URL+"/dead")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("err = %v, want original HTTP 404", err)
	}
}

func TestWaybackNotTriedForClientErrors(t *testing.T) {
	if isWaybackWorthy(&httpStatusError{StatusCode: http.StatusForbidden, Status: "403 Forbidden"}) {
		t.Error("403 should not trigger wayback")
	}
	if !isWaybackWorthy(&httpStatusError{StatusCode: http.StatusNotFound, Status: "404 Not Found"}) {
		t.Error("404 should trigger wayback")
	}
	if !isWaybackWorthy(&httpStatusError{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway"}) {
		t.Error("502 should trigger wayback")
	}
	if isWaybackWorthy(fmt.Errorf("plain error")) {
		t.Error("non-status error should not trigger wayback")
	}
}
