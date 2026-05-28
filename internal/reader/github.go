package reader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// gitHubAPIBaseURL is the GitHub REST API root. It is a var (not a const) so
// tests can point it at an httptest server.
var gitHubAPIBaseURL = "https://api.github.com"

const (
	// gitHubAPIVersion is the value sent in the X-GitHub-Api-Version header.
	gitHubAPIVersion = "2022-11-28"

	// gitHubPerPage is the page size requested for paginated list endpoints
	// (GitHub's maximum is 100).
	gitHubPerPage = 100

	// gitHubMaxPages caps how many pages we follow for any paginated list,
	// bounding work for very large threads.
	gitHubMaxPages = 10
)

type gitHubThreadKind string

const (
	gitHubThreadIssue       gitHubThreadKind = "issue"
	gitHubThreadPullRequest gitHubThreadKind = "pull_request"
)

type gitHubThread struct {
	Owner          string
	Repo           string
	Number         int
	Kind           gitHubThreadKind
	Title          string
	State          string
	URL            string
	Author         string
	Body           string
	Labels         []string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ClosedAt       *time.Time
	Comments       []gitHubIssueComment
	ReviewComments []gitHubReviewComment
	PullRequest    *gitHubPullRequestDetails
}

type gitHubIssueComment struct {
	Author    string
	Body      string
	URL       string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type gitHubReviewComment struct {
	Author    string
	Body      string
	URL       string
	Path      string
	Position  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

type gitHubPullRequestDetails struct {
	Draft        bool
	Merged       bool
	Commits      int
	Additions    int
	Deletions    int
	ChangedFiles int
	BaseRef      string
	HeadRef      string
}

type gitHubUser struct {
	Login string `json:"login"`
}

type gitHubLabel struct {
	Name string `json:"name"`
}

type gitHubIssueResponse struct {
	Title     string        `json:"title"`
	State     string        `json:"state"`
	Body      string        `json:"body"`
	HTMLURL   string        `json:"html_url"`
	User      gitHubUser    `json:"user"`
	Labels    []gitHubLabel `json:"labels"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	ClosedAt  *time.Time    `json:"closed_at"`
}

type gitHubIssueCommentResponse struct {
	Body      string     `json:"body"`
	HTMLURL   string     `json:"html_url"`
	User      gitHubUser `json:"user"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type gitHubPullRequestResponse struct {
	Draft        bool      `json:"draft"`
	Merged       bool      `json:"merged"`
	Commits      int       `json:"commits"`
	Additions    int       `json:"additions"`
	Deletions    int       `json:"deletions"`
	ChangedFiles int       `json:"changed_files"`
	UpdatedAt    time.Time `json:"updated_at"`
	Base         struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

type gitHubReviewCommentResponse struct {
	Body      string     `json:"body"`
	HTMLURL   string     `json:"html_url"`
	Path      string     `json:"path"`
	Position  int        `json:"position"`
	User      gitHubUser `json:"user"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type gitHubAPIError struct {
	Message string `json:"message"`
}

type gitHubRepoResponse struct {
	FullName      string   `json:"full_name"`
	Description   string   `json:"description"`
	HTMLURL       string   `json:"html_url"`
	Homepage      string   `json:"homepage"`
	Stars         int      `json:"stargazers_count"`
	Forks         int      `json:"forks_count"`
	OpenIssues    int      `json:"open_issues_count"`
	Language      string   `json:"language"`
	Topics        []string `json:"topics"`
	Archived      bool     `json:"archived"`
	DefaultBranch string   `json:"default_branch"`
	License       *struct {
		SPDXID string `json:"spdx_id"`
	} `json:"license"`
}

func isGitHubIssueOrPRURL(parsedURL *url.URL) bool {
	_, _, _, _, ok := parseGitHubIssueOrPRURL(parsedURL)
	return ok
}

func isGitHubRepoURL(parsedURL *url.URL) bool {
	if strings.ToLower(parsedURL.Hostname()) != "github.com" {
		return false
	}
	segments := pathSegments(parsedURL.Path)
	return len(segments) == 2
}

func fetchGitHubRepoAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	segments := pathSegments(parsedURL.Path)
	owner, repo := segments[0], segments[1]

	var repoResp gitHubRepoResponse
	repoEndpoint := fmt.Sprintf("%s/repos/%s/%s", gitHubAPIBaseURL, owner, repo)
	if err := fetchGitHubJSON(ctx, client, repoEndpoint, &repoResp); err != nil {
		return "", err
	}

	readme, err := fetchGitHubReadme(ctx, client, owner, repo)
	if err != nil && !errors.Is(err, errGitHubReadmeNotFound) {
		// A missing README (404) is fine and yields empty content; any
		// other failure (rate limit, server error, ...) is real and
		// should be reported rather than silently dropped.
		return "", err
	}

	var b strings.Builder
	fullName := repoResp.FullName
	if fullName == "" {
		fullName = fmt.Sprintf("%s/%s", owner, repo)
	}
	fmt.Fprintf(&b, "# %s\n\n", fullName)
	if strings.TrimSpace(repoResp.Description) != "" {
		fmt.Fprintf(&b, "%s\n\n", repoResp.Description)
	}

	b.WriteString("## Repository Info\n\n")
	if repoResp.HTMLURL != "" {
		fmt.Fprintf(&b, "- Link: %s\n", repoResp.HTMLURL)
	}
	if repoResp.Homepage != "" {
		fmt.Fprintf(&b, "- Homepage: %s\n", repoResp.Homepage)
	}
	if repoResp.Language != "" {
		fmt.Fprintf(&b, "- Primary language: %s\n", repoResp.Language)
	}
	fmt.Fprintf(&b, "- Stars: %d\n", repoResp.Stars)
	fmt.Fprintf(&b, "- Forks: %d\n", repoResp.Forks)
	fmt.Fprintf(&b, "- Open issues: %d\n", repoResp.OpenIssues)
	if repoResp.DefaultBranch != "" {
		fmt.Fprintf(&b, "- Default branch: %s\n", repoResp.DefaultBranch)
	}
	if repoResp.License != nil && repoResp.License.SPDXID != "" && repoResp.License.SPDXID != "NOASSERTION" {
		fmt.Fprintf(&b, "- License: %s\n", repoResp.License.SPDXID)
	}
	if repoResp.Archived {
		b.WriteString("- Archived: true\n")
	}
	if len(repoResp.Topics) > 0 {
		fmt.Fprintf(&b, "- Topics: %s\n", strings.Join(repoResp.Topics, ", "))
	}
	b.WriteString("\n")

	b.WriteString("## README\n\n")
	if strings.TrimSpace(readme) == "" {
		b.WriteString("_No README available._\n")
	} else {
		b.WriteString(strings.TrimSpace(readme))
		b.WriteString("\n")
	}

	return b.String(), nil
}

// errGitHubReadmeNotFound signals that a repository simply has no README
// (HTTP 404), which is a normal condition rather than a fetch failure.
var errGitHubReadmeNotFound = errors.New("github readme not found")

func fetchGitHubReadme(ctx context.Context, client *http.Client, owner, repo string) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/readme", gitHubAPIBaseURL, owner, repo)
	req, err := newRequest(ctx, endpoint, "application/vnd.github.raw")
	if err != nil {
		return "", err
	}
	req.Header.Set("X-GitHub-Api-Version", gitHubAPIVersion)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("github readme request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", errGitHubReadmeNotFound
	}
	if err := checkGitHubRateLimit(resp); err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github readme request failed: HTTP %d: %s", resp.StatusCode, decodeGitHubAPIError(resp.Body))
	}

	body, err := io.ReadAll(limitedBody(resp.Body))
	if err != nil {
		return "", fmt.Errorf("failed to read readme body: %w", err)
	}
	return string(body), nil
}

func fetchGitHubContentAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	thread, err := fetchGitHubThread(ctx, client, parsedURL)
	if err != nil {
		return "", err
	}
	return renderGitHubThreadMarkdown(thread), nil
}

func fetchGitHubThread(ctx context.Context, client *http.Client, parsedURL *url.URL) (*gitHubThread, error) {
	owner, repo, number, kind, ok := parseGitHubIssueOrPRURL(parsedURL)
	if !ok {
		return nil, fmt.Errorf("unsupported GitHub issue or pull request URL: %s", parsedURL.String())
	}

	var issueResp gitHubIssueResponse
	issueEndpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d", gitHubAPIBaseURL, owner, repo, number)
	if err := fetchGitHubJSON(ctx, client, issueEndpoint, &issueResp); err != nil {
		return nil, err
	}

	issueCommentsEndpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", gitHubAPIBaseURL, owner, repo, number)
	issueCommentsResp, err := fetchGitHubPaginated[gitHubIssueCommentResponse](ctx, client, issueCommentsEndpoint)
	if err != nil {
		return nil, err
	}

	labels := make([]string, 0, len(issueResp.Labels))
	for _, label := range issueResp.Labels {
		if strings.TrimSpace(label.Name) == "" {
			continue
		}
		labels = append(labels, label.Name)
	}

	thread := &gitHubThread{
		Owner:     owner,
		Repo:      repo,
		Number:    number,
		Kind:      kind,
		Title:     issueResp.Title,
		State:     issueResp.State,
		URL:       issueResp.HTMLURL,
		Author:    defaultGitHubAuthor(issueResp.User.Login),
		Body:      strings.TrimSpace(issueResp.Body),
		Labels:    labels,
		CreatedAt: issueResp.CreatedAt,
		UpdatedAt: issueResp.UpdatedAt,
		ClosedAt:  issueResp.ClosedAt,
		Comments:  make([]gitHubIssueComment, 0, len(issueCommentsResp)),
	}

	for _, comment := range issueCommentsResp {
		thread.Comments = append(thread.Comments, gitHubIssueComment{
			Author:    defaultGitHubAuthor(comment.User.Login),
			Body:      strings.TrimSpace(comment.Body),
			URL:       comment.HTMLURL,
			CreatedAt: comment.CreatedAt,
			UpdatedAt: comment.UpdatedAt,
		})
	}

	if kind != gitHubThreadPullRequest {
		return thread, nil
	}

	var pullResp gitHubPullRequestResponse
	pullEndpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", gitHubAPIBaseURL, owner, repo, number)
	if err := fetchGitHubJSON(ctx, client, pullEndpoint, &pullResp); err != nil {
		return nil, err
	}

	reviewCommentsEndpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/comments", gitHubAPIBaseURL, owner, repo, number)
	reviewCommentsResp, err := fetchGitHubPaginated[gitHubReviewCommentResponse](ctx, client, reviewCommentsEndpoint)
	if err != nil {
		return nil, err
	}

	thread.PullRequest = &gitHubPullRequestDetails{
		Draft:        pullResp.Draft,
		Merged:       pullResp.Merged,
		Commits:      pullResp.Commits,
		Additions:    pullResp.Additions,
		Deletions:    pullResp.Deletions,
		ChangedFiles: pullResp.ChangedFiles,
		BaseRef:      pullResp.Base.Ref,
		HeadRef:      pullResp.Head.Ref,
	}
	if pullResp.UpdatedAt.After(thread.UpdatedAt) {
		thread.UpdatedAt = pullResp.UpdatedAt
	}

	thread.ReviewComments = make([]gitHubReviewComment, 0, len(reviewCommentsResp))
	for _, review := range reviewCommentsResp {
		thread.ReviewComments = append(thread.ReviewComments, gitHubReviewComment{
			Author:    defaultGitHubAuthor(review.User.Login),
			Body:      strings.TrimSpace(review.Body),
			URL:       review.HTMLURL,
			Path:      review.Path,
			Position:  review.Position,
			CreatedAt: review.CreatedAt,
			UpdatedAt: review.UpdatedAt,
		})
	}

	return thread, nil
}

// checkGitHubRateLimit inspects a response for rate-limit / abuse responses and
// returns a clear, non-retryable error if the request was throttled. It returns
// nil for any other status (including success).
func checkGitHubRateLimit(resp *http.Response) error {
	rateLimited := resp.StatusCode == http.StatusTooManyRequests
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		rateLimited = true
	}
	if !rateLimited {
		return nil
	}

	detail := "github api rate limit exceeded"
	if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
		detail = fmt.Sprintf("%s (resets at unix %s)", detail, reset)
	}
	if retry := resp.Header.Get("Retry-After"); retry != "" {
		detail = fmt.Sprintf("%s (retry after %ss)", detail, retry)
	}
	return fmt.Errorf("%s: HTTP %d", detail, resp.StatusCode)
}

// doGitHubRequest issues a single GitHub API GET, returning the response so the
// caller can decode it. It surfaces rate-limit and non-200 errors uniformly.
func doGitHubRequest(ctx context.Context, client *http.Client, endpoint string) (*http.Response, error) {
	req, err := newRequest(ctx, endpoint, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-GitHub-Api-Version", gitHubAPIVersion)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github request failed: %w", err)
	}

	if err := checkGitHubRateLimit(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("github api request failed: HTTP %d: %s", resp.StatusCode, decodeGitHubAPIError(resp.Body))
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

func fetchGitHubJSON(ctx context.Context, client *http.Client, endpoint string, target interface{}) error {
	resp, err := doGitHubRequest(ctx, client, endpoint)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(target); err != nil {
		return fmt.Errorf("failed to decode github response from %s: %w", endpoint, err)
	}
	return nil
}

// fetchGitHubPaginated retrieves every page of a list endpoint, following the
// Link header's rel="next" relation, up to gitHubMaxPages. Each page is decoded
// into a fresh []T and appended to the accumulated result.
func fetchGitHubPaginated[T any](ctx context.Context, client *http.Client, endpoint string) ([]T, error) {
	var all []T

	next := withPerPage(endpoint, gitHubPerPage)
	for page := 0; page < gitHubMaxPages && next != ""; page++ {
		resp, err := doGitHubRequest(ctx, client, next)
		if err != nil {
			return nil, err
		}

		var batch []T
		decodeErr := json.NewDecoder(limitedBody(resp.Body)).Decode(&batch)
		nextURL := parseGitHubNextLink(resp.Header.Get("Link"))
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("failed to decode github response from %s: %w", next, decodeErr)
		}

		all = append(all, batch...)

		// Stop early when the page is short: there is no further data even
		// if a Link header is (unexpectedly) present.
		if len(batch) < gitHubPerPage {
			break
		}
		next = nextURL
	}

	return all, nil
}

// withPerPage adds a per_page query parameter to a GitHub list endpoint,
// preserving any existing query values.
func withPerPage(endpoint string, perPage int) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	query := parsed.Query()
	query.Set("per_page", strconv.Itoa(perPage))
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// parseGitHubNextLink extracts the rel="next" URL from a GitHub Link header,
// returning "" when there is no next page.
func parseGitHubNextLink(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		section := strings.Split(strings.TrimSpace(part), ";")
		if len(section) < 2 {
			continue
		}
		linkURL := strings.TrimSpace(section[0])
		if !strings.HasPrefix(linkURL, "<") || !strings.HasSuffix(linkURL, ">") {
			continue
		}
		linkURL = linkURL[1 : len(linkURL)-1]
		for _, param := range section[1:] {
			param = strings.TrimSpace(param)
			if param == `rel="next"` || param == "rel=next" {
				return linkURL
			}
		}
	}
	return ""
}

func renderGitHubThreadMarkdown(thread *gitHubThread) string {
	var builder strings.Builder

	title := fmt.Sprintf("%s/%s #%d: %s", thread.Owner, thread.Repo, thread.Number, thread.Title)
	fmt.Fprintf(&builder, "# %s\n\n", title)
	fmt.Fprintf(&builder, "- Type: %s\n", formatGitHubThreadKind(thread.Kind))
	fmt.Fprintf(&builder, "- State: %s\n", thread.State)
	fmt.Fprintf(&builder, "- Author: @%s\n", thread.Author)
	if !thread.CreatedAt.IsZero() {
		fmt.Fprintf(&builder, "- Created: %s\n", thread.CreatedAt.Format(time.RFC3339))
	}
	if !thread.UpdatedAt.IsZero() {
		fmt.Fprintf(&builder, "- Updated: %s\n", thread.UpdatedAt.Format(time.RFC3339))
	}
	if thread.ClosedAt != nil && !thread.ClosedAt.IsZero() {
		fmt.Fprintf(&builder, "- Closed: %s\n", thread.ClosedAt.Format(time.RFC3339))
	}
	if len(thread.Labels) > 0 {
		fmt.Fprintf(&builder, "- Labels: %s\n", strings.Join(thread.Labels, ", "))
	}
	if thread.URL != "" {
		fmt.Fprintf(&builder, "- Link: %s\n", thread.URL)
	}
	builder.WriteString("\n")

	builder.WriteString("## Description\n\n")
	if strings.TrimSpace(thread.Body) == "" {
		builder.WriteString("_No description provided._\n\n")
	} else {
		builder.WriteString(thread.Body)
		builder.WriteString("\n\n")
	}

	if thread.PullRequest != nil {
		builder.WriteString("## Pull Request Details\n\n")
		fmt.Fprintf(&builder, "- Draft: %t\n", thread.PullRequest.Draft)
		fmt.Fprintf(&builder, "- Merged: %t\n", thread.PullRequest.Merged)
		fmt.Fprintf(&builder, "- Base branch: %s\n", thread.PullRequest.BaseRef)
		fmt.Fprintf(&builder, "- Head branch: %s\n", thread.PullRequest.HeadRef)
		fmt.Fprintf(&builder, "- Commits: %d\n", thread.PullRequest.Commits)
		fmt.Fprintf(&builder, "- Changed files: %d\n", thread.PullRequest.ChangedFiles)
		fmt.Fprintf(&builder, "- Additions: %d\n", thread.PullRequest.Additions)
		fmt.Fprintf(&builder, "- Deletions: %d\n\n", thread.PullRequest.Deletions)
	}

	fmt.Fprintf(&builder, "## Comments (%d)\n\n", len(thread.Comments))
	if len(thread.Comments) == 0 {
		builder.WriteString("_No comments available._\n\n")
	} else {
		for idx, comment := range thread.Comments {
			fmt.Fprintf(&builder, "### Comment %d by @%s\n\n", idx+1, comment.Author)
			if strings.TrimSpace(comment.Body) == "" {
				builder.WriteString("_No comment body available._\n\n")
			} else {
				builder.WriteString(comment.Body)
				builder.WriteString("\n\n")
			}
		}
	}

	if thread.Kind == gitHubThreadPullRequest {
		fmt.Fprintf(&builder, "## Review Comments (%d)\n\n", len(thread.ReviewComments))
		if len(thread.ReviewComments) == 0 {
			builder.WriteString("_No review comments available._\n")
		} else {
			for idx, review := range thread.ReviewComments {
				fmt.Fprintf(&builder, "### Review Comment %d by @%s\n\n", idx+1, review.Author)
				if review.Path != "" {
					fmt.Fprintf(&builder, "- File: `%s`\n", review.Path)
				}
				if review.Position > 0 {
					fmt.Fprintf(&builder, "- Position: %d\n", review.Position)
				}
				if review.Path != "" || review.Position > 0 {
					builder.WriteString("\n")
				}
				if strings.TrimSpace(review.Body) == "" {
					builder.WriteString("_No review comment body available._\n\n")
				} else {
					builder.WriteString(review.Body)
					builder.WriteString("\n\n")
				}
			}
		}
	}

	return cleanMarkdown(builder.String())
}

func parseGitHubIssueOrPRURL(parsedURL *url.URL) (owner, repo string, number int, kind gitHubThreadKind, ok bool) {
	if strings.ToLower(parsedURL.Hostname()) != "github.com" {
		return "", "", 0, "", false
	}

	segments := pathSegments(parsedURL.Path)
	if len(segments) < 4 {
		return "", "", 0, "", false
	}

	numberValue, err := strconv.Atoi(segments[3])
	if err != nil || numberValue <= 0 {
		return "", "", 0, "", false
	}

	switch segments[2] {
	case "issues":
		kind = gitHubThreadIssue
	case "pull":
		kind = gitHubThreadPullRequest
	default:
		return "", "", 0, "", false
	}

	return segments[0], segments[1], numberValue, kind, true
}

func decodeGitHubAPIError(body io.Reader) string {
	payload, err := io.ReadAll(io.LimitReader(body, 4096))
	if err != nil {
		return "unable to read error body"
	}
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return "empty error response"
	}

	var apiErr gitHubAPIError
	if err := json.Unmarshal(payload, &apiErr); err == nil && strings.TrimSpace(apiErr.Message) != "" {
		return apiErr.Message
	}
	return trimmed
}

func formatGitHubThreadKind(kind gitHubThreadKind) string {
	switch kind {
	case gitHubThreadPullRequest:
		return "Pull Request"
	default:
		return "Issue"
	}
}

func defaultGitHubAuthor(author string) string {
	if strings.TrimSpace(author) == "" {
		return "ghost"
	}
	return author
}
