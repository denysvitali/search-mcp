package reader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// gitlabAPIBaseURL is the GitLab REST API root. It is a var so tests can
// point it at an httptest server.
var gitlabAPIBaseURL = "https://gitlab.com/api/v4"

// maxGitLabNotes caps how many discussion notes are rendered.
const maxGitLabNotes = 30

type gitlabIssuable struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	CreatedAt   string `json:"created_at"`
	WebURL      string `json:"web_url"`
	Author      struct {
		Username string `json:"username"`
	} `json:"author"`
	UserNotesCount int `json:"user_notes_count"`
}

type gitlabNote struct {
	Body      string `json:"body"`
	System    bool   `json:"system"`
	CreatedAt string `json:"created_at"`
	Author    struct {
		Username string `json:"username"`
	} `json:"author"`
}

// parseGitLabIssuableURL extracts (project, kind, iid) from
// gitlab.com/{group...}/{project}/-/issues/{iid} or /-/merge_requests/{iid}.
// kind is "issues" or "merge_requests"; ok is false for any other URL.
func parseGitLabIssuableURL(parsedURL *url.URL) (project, kind string, iid int, ok bool) {
	host := strings.ToLower(parsedURL.Hostname())
	if host != "gitlab.com" && host != "www.gitlab.com" {
		return "", "", 0, false
	}
	segments := pathSegments(parsedURL.Path)
	for i, segment := range segments {
		if segment != "-" {
			continue
		}
		if i == 0 || len(segments) < i+3 {
			return "", "", 0, false
		}
		kind = segments[i+1]
		if kind != "issues" && kind != "merge_requests" {
			return "", "", 0, false
		}
		iid, err := strconv.Atoi(segments[i+2])
		if err != nil || iid <= 0 {
			return "", "", 0, false
		}
		return strings.Join(segments[:i], "/"), kind, iid, true
	}
	return "", "", 0, false
}

func isGitLabIssuableURL(parsedURL *url.URL) bool {
	_, _, _, ok := parseGitLabIssuableURL(parsedURL)
	return ok
}

func fetchGitLabContentAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	project, kind, iid, ok := parseGitLabIssuableURL(parsedURL)
	if !ok {
		return "", fmt.Errorf("unsupported gitlab URL: %s", parsedURL)
	}

	base := fmt.Sprintf("%s/projects/%s/%s/%d",
		strings.TrimRight(gitlabAPIBaseURL, "/"), url.PathEscape(project), kind, iid)

	var issuable gitlabIssuable
	if err := fetchGitLabJSON(ctx, client, base, &issuable); err != nil {
		return "", err
	}
	var notes []gitlabNote
	if err := fetchGitLabJSON(ctx, client, base+"/notes?sort=asc&per_page=100", &notes); err != nil {
		// Notes are best-effort; the issuable body alone is still useful.
		notes = nil
	}
	return renderGitLabMarkdown(project, kind, iid, issuable, notes), nil
}

func fetchGitLabJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	req, err := newRequest(ctx, endpoint, "application/json")
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("gitlab request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gitlab request failed: HTTP %d: %s", resp.StatusCode, readErrorBody(resp.Body))
	}
	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(target); err != nil {
		return fmt.Errorf("failed to decode gitlab response: %w", err)
	}
	return nil
}

func renderGitLabMarkdown(project, kind string, iid int, issuable gitlabIssuable, notes []gitlabNote) string {
	ref := "#"
	label := "Issue"
	if kind == "merge_requests" {
		ref = "!"
		label = "Merge request"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s %s%d: %s\n\n", label, ref, iid, strings.TrimSpace(issuable.Title))
	fmt.Fprintf(&b, "- Project: %s\n", project)
	fmt.Fprintf(&b, "- Author: %s\n", issuable.Author.Username)
	fmt.Fprintf(&b, "- State: %s\n", issuable.State)
	if issuable.CreatedAt != "" {
		fmt.Fprintf(&b, "- Created: %s\n", issuable.CreatedAt)
	}
	if issuable.WebURL != "" {
		fmt.Fprintf(&b, "- Link: %s\n", issuable.WebURL)
	}
	b.WriteString("\n## Description\n\n")
	if strings.TrimSpace(issuable.Description) == "" {
		b.WriteString("_No description provided._\n")
	} else {
		b.WriteString(strings.TrimSpace(issuable.Description))
		b.WriteString("\n")
	}

	b.WriteString("\n## Comments\n\n")
	rendered := 0
	for _, note := range notes {
		if note.System {
			continue
		}
		if rendered >= maxGitLabNotes {
			fmt.Fprintf(&b, "_... more comments omitted (%d total)._\n", issuable.UserNotesCount)
			break
		}
		fmt.Fprintf(&b, "### %s (%s)\n\n%s\n\n", note.Author.Username, note.CreatedAt, strings.TrimSpace(note.Body))
		rendered++
	}
	if rendered == 0 {
		b.WriteString("_No comments available._\n")
	}
	return cleanMarkdown(b.String())
}
