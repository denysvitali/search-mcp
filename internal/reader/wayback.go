package reader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// waybackAvailabilityBaseURL is the Wayback Machine availability API. It is a
// var so tests can point it at an httptest server.
var waybackAvailabilityBaseURL = "https://archive.org/wayback/available"

// httpStatusError reports a non-OK HTTP response so callers can decide
// whether an archived copy is worth trying.
type httpStatusError struct {
	StatusCode int
	Status     string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Status)
}

type waybackAvailabilityResponse struct {
	ArchivedSnapshots struct {
		Closest struct {
			Available bool   `json:"available"`
			URL       string `json:"url"`
			Timestamp string `json:"timestamp"`
		} `json:"closest"`
	} `json:"archived_snapshots"`
}

// isWaybackWorthy reports whether the fetch failure suggests the page is gone
// from the live web but may still exist in the archive.
func isWaybackWorthy(err error) bool {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	switch statusErr.StatusCode {
	case http.StatusNotFound, http.StatusGone, http.StatusUnavailableForLegalReasons:
		return true
	}
	return statusErr.StatusCode >= 500
}

// fetchWithWaybackFallback fetches the page normally and, when the live site
// answers with a gone/erroring status, falls back to the most recent Wayback
// Machine snapshot, clearly marking the result as archived.
func fetchWithWaybackFallback(ctx context.Context, client *http.Client, urlStr string) (string, error) {
	content, err := fetchGenericHTMLAsMarkdown(ctx, client, urlStr)
	if err == nil || !isWaybackWorthy(err) {
		return content, err
	}
	if parsed, perr := url.Parse(urlStr); perr == nil && strings.HasSuffix(strings.ToLower(parsed.Hostname()), "archive.org") {
		return "", err
	}

	snapshotURL, timestamp, ok := waybackSnapshot(ctx, client, urlStr)
	if !ok {
		return "", err
	}
	archived, archiveErr := fetchGenericHTMLAsMarkdown(ctx, client, snapshotURL)
	if archiveErr != nil {
		return "", err
	}
	note := fmt.Sprintf("> Live fetch failed (%v). Showing Wayback Machine snapshot %s from %s.\n\n", err, snapshotURL, timestamp)
	return note + archived, nil
}

// waybackSnapshot asks the availability API for the closest snapshot of url.
func waybackSnapshot(ctx context.Context, client *http.Client, urlStr string) (snapshotURL, timestamp string, ok bool) {
	endpoint := waybackAvailabilityBaseURL + "?url=" + url.QueryEscape(urlStr)
	req, err := newRequest(ctx, endpoint, "application/json")
	if err != nil {
		return "", "", false
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", false
	}
	var decoded waybackAvailabilityResponse
	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(&decoded); err != nil {
		return "", "", false
	}
	closest := decoded.ArchivedSnapshots.Closest
	if !closest.Available || closest.URL == "" {
		return "", "", false
	}
	// The API frequently hands back http:// archive links; upgrade them.
	snapshotURL = strings.Replace(closest.URL, "http://web.archive.org/", "https://web.archive.org/", 1)
	return snapshotURL, closest.Timestamp, true
}
