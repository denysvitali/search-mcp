package reader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// maxGerritMessages caps how many change messages (review comments posted on
// the change) are rendered.
const maxGerritMessages = 20

// maxGerritFiles caps how many changed files are listed before truncating.
const maxGerritFiles = 100

// gerritPseudoFiles are magic file paths in the Gerrit files map that do not
// correspond to real repository files and are hidden in the web UI too.
var gerritPseudoFiles = map[string]bool{
	"/COMMIT_MSG":     true,
	"/PATCHSET_LEVEL": true,
	"/MERGE_LIST":     true,
}

type gerritAccount struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

func (a gerritAccount) display() string {
	switch {
	case a.DisplayName != "":
		return a.DisplayName
	case a.Name != "":
		return a.Name
	case a.Email != "":
		return a.Email
	default:
		return "unknown"
	}
}

type gerritApproval struct {
	Value       *int   `json:"value"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

func (a gerritApproval) display() string {
	return gerritAccount{Name: a.Name, DisplayName: a.DisplayName, Email: a.Email}.display()
}

type gerritLabel struct {
	All []gerritApproval `json:"all"`
}

type gerritRevision struct {
	Number int `json:"_number"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
}

type gerritMessage struct {
	Message        string        `json:"message"`
	Author         gerritAccount `json:"author"`
	Date           string        `json:"date"`
	RevisionNumber int           `json:"_revision_number"`
}

type gerritChange struct {
	Number                 int                       `json:"_number"`
	ChangeID               string                    `json:"change_id"`
	Project                string                    `json:"project"`
	Branch                 string                    `json:"branch"`
	Subject                string                    `json:"subject"`
	Status                 string                    `json:"status"`
	Created                string                    `json:"created"`
	Updated                string                    `json:"updated"`
	Insertions             int                       `json:"insertions"`
	Deletions              int                       `json:"deletions"`
	TotalCommentCount      int                       `json:"total_comment_count"`
	UnresolvedCommentCount int                       `json:"unresolved_comment_count"`
	Owner                  gerritAccount             `json:"owner"`
	Labels                 map[string]gerritLabel    `json:"labels"`
	Messages               []gerritMessage           `json:"messages"`
	CurrentRevision        string                    `json:"current_revision"`
	Revisions              map[string]gerritRevision `json:"revisions"`
}

type gerritFileInfo struct {
	Status        string `json:"status"`
	LinesInserted int    `json:"lines_inserted"`
	LinesDeleted  int    `json:"lines_deleted"`
	Binary        bool   `json:"binary"`
}

// parseGerritChangeURL extracts (project, changeID, patchset) from a Gerrit
// change URL of the shape /c/<project>/+/<change-id>[/<patchset>], e.g.
// https://chromium-review.googlesource.com/c/chromium/src/+/8124214 or the
// project-less https://gerrit-review.googlesource.com/c/+/361223. The
// change-id is either a change number or an I-prefixed Change-Id. Patchset is
// 0 when the URL does not name one. ok is false for any other URL shape.
//
// The host is not restricted: any Gerrit instance exposes this URL shape and
// the matching REST API on the same origin.
func parseGerritChangeURL(parsedURL *url.URL) (project, changeID string, patchset int, ok bool) {
	segments := pathSegments(parsedURL.Path)
	if len(segments) < 3 || segments[0] != "c" {
		return "", "", 0, false
	}
	plus := -1
	for i, segment := range segments[1:] {
		if segment == "+" {
			plus = i + 1
			break
		}
	}
	if plus <= 0 || plus > len(segments)-2 {
		return "", "", 0, false
	}
	changeID = segments[plus+1]
	if !isGerritChangeID(changeID) {
		return "", "", 0, false
	}
	project = strings.Join(segments[1:plus], "/")

	// Trailing segments after the change id are a patch set and/or a file
	// path within the change (e.g. /c/proj/+/123/5/src/foo.cc); the reader
	// renders the whole change, picking the patch set when one is named.
	if len(segments) > plus+2 {
		if ps, err := strconv.Atoi(segments[plus+2]); err == nil && ps > 0 {
			patchset = ps
		}
	}
	return project, changeID, patchset, true
}

// isGerritChangeID reports whether s is a Gerrit change number (digits) or a
// Change-Id (an "I" followed by 8-40 hex characters).
func isGerritChangeID(s string) bool {
	if s == "" {
		return false
	}
	if strings.Trim(s, "0123456789") == "" {
		return true
	}
	if len(s) < 9 || len(s) > 41 || s[0] != 'I' {
		return false
	}
	return strings.Trim(s[1:], "0123456789abcdefABCDEF") == ""
}

func isGerritChangeURL(parsedURL *url.URL) bool {
	_, _, _, ok := parseGerritChangeURL(parsedURL)
	return ok
}

// fetchGerritChangeAsMarkdown renders a Gerrit change through the host's REST
// API. The rendered PolyGerrit page is a JavaScript SPA, so a plain HTML fetch
// yields no content; the API is public for readable changes and needs no
// authentication.
func fetchGerritChangeAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	project, changeID, patchset, ok := parseGerritChangeURL(parsedURL)
	if !ok {
		return "", fmt.Errorf("unsupported gerrit URL: %s", parsedURL)
	}

	// Qualify with the project when we know it (chromium/src~8124214); the
	// bare id still works on hosts where numbers are globally unique.
	changeRef := changeID
	if project != "" {
		changeRef = project + "~" + changeID
	}
	const options = "?o=CURRENT_REVISION&o=CURRENT_COMMIT&o=DETAILED_LABELS&o=DETAILED_ACCOUNTS&o=MESSAGES"
	base := fmt.Sprintf("%s://%s/changes/%s", parsedURL.Scheme, parsedURL.Host, url.PathEscape(changeRef))

	var change gerritChange
	err := fetchGerritJSON(ctx, client, base+options, &change)
	if err != nil && project != "" {
		// The project parsed from the URL may not match the change's actual
		// project; retry with the bare change id before giving up.
		bare := fmt.Sprintf("%s://%s/changes/%s", parsedURL.Scheme, parsedURL.Host, url.PathEscape(changeID))
		if errRetry := fetchGerritJSON(ctx, client, bare+options, &change); errRetry == nil {
			base, err = bare, nil
		}
	}
	if err != nil {
		return "", err
	}

	files := map[string]gerritFileInfo{}
	revisionRef := "current"
	if patchset > 0 {
		revisionRef = strconv.Itoa(patchset)
	}
	if err := fetchGerritJSON(ctx, client, base+"/revisions/"+revisionRef+"/files", &files); err != nil {
		// File lists are best-effort; the change itself is still useful.
		files = nil
	}

	return renderGerritMarkdown(parsedURL, change, patchset, files), nil
}

// gerritXSSIPrefix is the magic prefix Gerrit prepends to every JSON response
// to prevent XSSI; it must be stripped before decoding.
const gerritXSSIPrefix = ")]}'"

func fetchGerritJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	req, err := newRequest(ctx, endpoint, "application/json")
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("gerrit request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(limitedBody(resp.Body))
	if err != nil {
		return fmt.Errorf("failed to read gerrit response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gerrit request failed: HTTP %d: %s", resp.StatusCode, readErrorBody(bytes.NewReader(body)))
	}
	body = bytes.TrimPrefix(body, []byte(gerritXSSIPrefix))
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("failed to decode gerrit response: %w", err)
	}
	return nil
}

func gerritStatusText(status string) string {
	switch status {
	case "NEW":
		return "Open"
	case "MERGED":
		return "Merged"
	case "ABANDONED":
		return "Abandoned"
	default:
		return status
	}
}

// gerritCommitMessage picks the commit message for the requested patch set,
// falling back to the current revision.
func gerritCommitMessage(change gerritChange, patchset int) string {
	if patchset > 0 {
		for _, revision := range change.Revisions {
			if revision.Number == patchset {
				return revision.Commit.Message
			}
		}
	}
	if revision, ok := change.Revisions[change.CurrentRevision]; ok {
		return revision.Commit.Message
	}
	return ""
}

func renderGerritMarkdown(pageURL *url.URL, change gerritChange, patchset int, files map[string]gerritFileInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", strings.TrimSpace(change.Subject))
	fmt.Fprintf(&b, "- Change: %d", change.Number)
	if change.ChangeID != "" {
		fmt.Fprintf(&b, " (%s)", change.ChangeID)
	}
	b.WriteString("\n")
	if change.Project != "" {
		fmt.Fprintf(&b, "- Project: %s\n", change.Project)
	}
	if change.Branch != "" {
		fmt.Fprintf(&b, "- Branch: %s\n", change.Branch)
	}
	fmt.Fprintf(&b, "- Status: %s\n", gerritStatusText(change.Status))
	fmt.Fprintf(&b, "- Owner: %s\n", change.Owner.display())
	if change.Created != "" {
		fmt.Fprintf(&b, "- Created: %s\n", change.Created)
	}
	if change.Updated != "" {
		fmt.Fprintf(&b, "- Updated: %s\n", change.Updated)
	}
	fmt.Fprintf(&b, "- Size: +%d/-%d\n", change.Insertions, change.Deletions)
	if change.TotalCommentCount > 0 || change.UnresolvedCommentCount > 0 {
		fmt.Fprintf(&b, "- Comments: %d total, %d unresolved\n", change.TotalCommentCount, change.UnresolvedCommentCount)
	}
	if patchset > 0 {
		fmt.Fprintf(&b, "- Patch set: %d\n", patchset)
	}
	fmt.Fprintf(&b, "- Link: %s\n", pageURL)

	if labels := renderGerritLabels(change.Labels); labels != "" {
		b.WriteString("\n## Review status\n\n")
		b.WriteString(labels)
	}

	if message := gerritCommitMessage(change, patchset); strings.TrimSpace(message) != "" {
		b.WriteString("\n## Commit message\n\n")
		b.WriteString(strings.TrimSpace(message))
		b.WriteString("\n")
	}

	if fileList := renderGerritFiles(files); fileList != "" {
		b.WriteString("\n## Changed files\n\n")
		b.WriteString(fileList)
	}

	b.WriteString("\n## Messages\n\n")
	rendered := 0
	for _, message := range change.Messages {
		if strings.TrimSpace(message.Message) == "" {
			continue
		}
		if rendered >= maxGerritMessages {
			fmt.Fprintf(&b, "_... %d more messages omitted._\n", len(change.Messages)-rendered)
			break
		}
		header := message.Author.display()
		if message.Date != "" {
			header += " (" + message.Date
			if message.RevisionNumber > 0 {
				fmt.Fprintf(&b, "### %s, patch set %d)\n\n", header, message.RevisionNumber)
			} else {
				fmt.Fprintf(&b, "### %s)\n\n", header)
			}
		} else {
			fmt.Fprintf(&b, "### %s\n\n", header)
		}
		b.WriteString(strings.TrimSpace(message.Message))
		b.WriteString("\n\n")
		rendered++
	}
	if rendered == 0 {
		b.WriteString("_No messages available._\n")
	}

	return cleanMarkdown(b.String())
}

func renderGerritLabels(labels map[string]gerritLabel) string {
	if len(labels) == 0 {
		return ""
	}
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		// Zero-valued votes are Gerrit's "no score" and pure noise in a
		// readable summary; keep only the opinions that actually voted.
		votes := make([]gerritApproval, 0)
		for _, approval := range labels[name].All {
			if approval.Value != nil && *approval.Value != 0 {
				votes = append(votes, approval)
			}
		}
		sort.SliceStable(votes, func(i, j int) bool { return *votes[i].Value > *votes[j].Value })

		fmt.Fprintf(&b, "- %s: ", name)
		if len(votes) == 0 {
			b.WriteString("no votes")
		} else {
			parts := make([]string, 0, len(votes))
			for _, vote := range votes {
				parts = append(parts, fmt.Sprintf("%+d %s", *vote.Value, vote.display()))
			}
			b.WriteString(strings.Join(parts, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func renderGerritFiles(files map[string]gerritFileInfo) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		if gerritPseudoFiles[path] {
			continue
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return ""
	}
	sort.Strings(paths)

	var b strings.Builder
	for i, path := range paths {
		if i >= maxGerritFiles {
			fmt.Fprintf(&b, "- _... %d more files omitted._\n", len(paths)-maxGerritFiles)
			break
		}
		info := files[path]
		status := info.Status
		if status == "" {
			status = "M"
		}
		if info.Binary {
			fmt.Fprintf(&b, "- %s %s (binary)\n", status, path)
		} else {
			fmt.Fprintf(&b, "- %s %s (+%d/-%d)\n", status, path, info.LinesInserted, info.LinesDeleted)
		}
	}
	return b.String()
}
