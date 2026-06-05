package reader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	redditCommentDepthLimit    = 1
	redditTopCommentLimit      = 20
	redditReplyPerCommentLimit = 5
)

// redditAPIScheme and redditAPIHost are the scheme/host used to build the JSON
// endpoint. They are vars (not consts) so tests can point them at an httptest
// server.
var (
	redditAPIScheme = "https"
	redditAPIHost   = "www.reddit.com"
)

type redditThread struct {
	ID          string
	Subreddit   string
	Title       string
	Author      string
	Score       int
	NumComments int
	CreatedAt   time.Time
	Permalink   string
	URL         string
	Body        string
	Comments    []redditComment
	// TotalComments is the number of top-level comments seen before the
	// redditTopCommentLimit cap was applied, used to report how many were
	// omitted from the rendered output.
	TotalComments int
}

type redditComment struct {
	ID        string
	Author    string
	Score     int
	Body      string
	CreatedAt time.Time
	Replies   []redditComment
	// TotalReplies is the number of direct replies seen before the
	// redditReplyPerCommentLimit cap was applied.
	TotalReplies int
}

type redditListing struct {
	Data redditListingData `json:"data"`
}

type redditListingData struct {
	Children []redditThing `json:"children"`
}

type redditThing struct {
	Kind string          `json:"kind"`
	Data redditThingData `json:"data"`
}

type redditThingData struct {
	ID          string          `json:"id"`
	Subreddit   string          `json:"subreddit"`
	Title       string          `json:"title"`
	SelfText    string          `json:"selftext"`
	Author      string          `json:"author"`
	Score       int             `json:"score"`
	NumComments int             `json:"num_comments"`
	CreatedUTC  float64         `json:"created_utc"`
	Permalink   string          `json:"permalink"`
	URL         string          `json:"url"`
	Body        string          `json:"body"`
	Replies     json.RawMessage `json:"replies"`
}

func isRedditThreadURL(parsedURL *url.URL) bool {
	host := strings.ToLower(parsedURL.Hostname())
	if host != "reddit.com" && host != "www.reddit.com" {
		return false
	}

	segments := pathSegments(parsedURL.Path)
	for idx, segment := range segments {
		if segment == "comments" && idx+1 < len(segments) {
			return true
		}
	}
	return false
}

func fetchRedditContentAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	thread, err := fetchRedditThread(ctx, client, parsedURL)
	if err != nil {
		return "", err
	}
	return renderRedditThreadMarkdown(thread), nil
}

func fetchRedditThread(ctx context.Context, client *http.Client, parsedURL *url.URL) (*redditThread, error) {
	jsonEndpoint := redditJSONEndpoint(parsedURL)
	req, err := newRequest(ctx, jsonEndpoint, "application/json")
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reddit request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reddit request failed: HTTP %d: %s", resp.StatusCode, readErrorBody(resp.Body))
	}

	var payload []redditListing
	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("failed to decode reddit JSON response: %w", err)
	}
	if len(payload) < 2 || len(payload[0].Data.Children) == 0 {
		return nil, fmt.Errorf("unexpected Reddit JSON response shape")
	}

	post := payload[0].Data.Children[0].Data
	comments, totalComments := parseRedditComments(payload[1].Data.Children, 0, redditCommentDepthLimit, redditTopCommentLimit)
	thread := &redditThread{
		ID:            post.ID,
		Subreddit:     post.Subreddit,
		Title:         post.Title,
		Author:        defaultRedditAuthor(post.Author),
		Score:         post.Score,
		NumComments:   post.NumComments,
		CreatedAt:     redditUnixTime(post.CreatedUTC),
		Permalink:     post.Permalink,
		URL:           post.URL,
		Body:          strings.TrimSpace(post.SelfText),
		Comments:      comments,
		TotalComments: totalComments,
	}

	return thread, nil
}

func redditJSONEndpoint(parsedURL *url.URL) string {
	endpoint := *parsedURL
	endpoint.Scheme = redditAPIScheme
	endpoint.Host = redditAPIHost
	trimmedPath := strings.TrimRight(endpoint.Path, "/")
	if trimmedPath == "" {
		trimmedPath = "/"
	}
	if !strings.HasSuffix(trimmedPath, ".json") {
		trimmedPath += ".json"
	}
	endpoint.Path = trimmedPath
	return endpoint.String()
}

// parseRedditComments converts a listing's children into redditComment values.
// limit caps how many comments are retained at this level (0 or negative means
// no cap); replies are only descended while depth < maxDepth. It also returns
// the total number of comment-kind ("t1") children seen before capping, so
// callers can report how many were omitted.
func parseRedditComments(children []redditThing, depth, maxDepth, limit int) ([]redditComment, int) {
	comments := make([]redditComment, 0, len(children))
	total := 0
	for _, child := range children {
		if child.Kind != "t1" {
			continue
		}
		total++
		if limit > 0 && len(comments) >= limit {
			continue
		}

		comment := redditComment{
			ID:        child.Data.ID,
			Author:    defaultRedditAuthor(child.Data.Author),
			Score:     child.Data.Score,
			Body:      strings.TrimSpace(child.Data.Body),
			CreatedAt: redditUnixTime(child.Data.CreatedUTC),
		}
		if depth < maxDepth {
			comment.Replies, comment.TotalReplies = parseRedditReplies(child.Data.Replies, depth+1, maxDepth, redditReplyPerCommentLimit)
		}
		comments = append(comments, comment)
	}
	return comments, total
}

func parseRedditReplies(rawReplies json.RawMessage, depth, maxDepth, limit int) ([]redditComment, int) {
	trimmed := bytes.TrimSpace(rawReplies)
	// Reddit encodes "no replies" as an empty string, null, or an omitted
	// value; treat all of those as zero replies.
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) {
		return nil, 0
	}

	var listing redditListing
	if err := json.Unmarshal(trimmed, &listing); err != nil {
		// Malformed/unexpected replies payload (e.g. a "more" stub or
		// truncated JSON). We can't recover the subtree, so we skip it
		// rather than failing the whole thread render.
		return nil, 0
	}
	return parseRedditComments(listing.Data.Children, depth, maxDepth, limit)
}

func renderRedditThreadMarkdown(thread *redditThread) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "# %s\n\n", thread.Title)
	fmt.Fprintf(&builder, "- Subreddit: r/%s\n", thread.Subreddit)
	fmt.Fprintf(&builder, "- Author: u/%s\n", thread.Author)
	fmt.Fprintf(&builder, "- Score: %d\n", thread.Score)
	fmt.Fprintf(&builder, "- Comment count: %d\n", thread.NumComments)
	if !thread.CreatedAt.IsZero() {
		fmt.Fprintf(&builder, "- Created: %s\n", thread.CreatedAt.Format(time.RFC3339))
	}
	if thread.Permalink != "" {
		fmt.Fprintf(&builder, "- Link: https://www.reddit.com%s\n", thread.Permalink)
	}
	builder.WriteString("\n")

	builder.WriteString("## Post\n\n")
	if strings.TrimSpace(thread.Body) == "" {
		builder.WriteString("_No post body available._\n\n")
	} else {
		builder.WriteString(thread.Body)
		builder.WriteString("\n\n")
	}

	builder.WriteString("## Comments\n\n")
	if len(thread.Comments) == 0 {
		builder.WriteString("_No comments available._\n")
		return cleanMarkdown(builder.String())
	}

	for i, comment := range thread.Comments {
		fmt.Fprintf(&builder, "### Comment %d by u/%s (score: %d)\n\n", i+1, comment.Author, comment.Score)
		if strings.TrimSpace(comment.Body) == "" {
			builder.WriteString("_No comment body available._\n\n")
		} else {
			builder.WriteString(comment.Body)
			builder.WriteString("\n\n")
		}

		if len(comment.Replies) == 0 {
			continue
		}

		builder.WriteString("#### Replies\n\n")
		for idx, reply := range comment.Replies {
			fmt.Fprintf(&builder, "%d. **u/%s** (score: %d)\n\n", idx+1, reply.Author, reply.Score)
			if strings.TrimSpace(reply.Body) == "" {
				builder.WriteString("_No reply body available._\n\n")
			} else {
				builder.WriteString(reply.Body)
				builder.WriteString("\n\n")
			}
		}

		if comment.TotalReplies > len(comment.Replies) {
			fmt.Fprintf(&builder, "_... %d more replies omitted._\n\n", comment.TotalReplies-len(comment.Replies))
		}
	}

	if thread.TotalComments > len(thread.Comments) {
		fmt.Fprintf(&builder, "_... %d more top-level comments omitted._\n", thread.TotalComments-len(thread.Comments))
	}

	return cleanMarkdown(builder.String())
}

func defaultRedditAuthor(author string) string {
	if strings.TrimSpace(author) == "" {
		return "[deleted]"
	}
	return author
}

func redditUnixTime(seconds float64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	// Split into whole seconds and fractional nanoseconds instead of
	// multiplying the whole value by 1e9, which loses precision once the
	// epoch (~1.7e9) is scaled into the float64 mantissa.
	whole := int64(seconds)
	nanos := int64((seconds - float64(whole)) * float64(time.Second))
	return time.Unix(whole, nanos).UTC()
}
