package reader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
}

type redditComment struct {
	ID        string
	Author    string
	Score     int
	Body      string
	CreatedAt time.Time
	Replies   []redditComment
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
		return nil, fmt.Errorf("Reddit request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("Reddit request failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload []redditListing
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("failed to decode Reddit JSON response: %w", err)
	}
	if len(payload) < 2 || len(payload[0].Data.Children) == 0 {
		return nil, fmt.Errorf("unexpected Reddit JSON response shape")
	}

	post := payload[0].Data.Children[0].Data
	thread := &redditThread{
		ID:          post.ID,
		Subreddit:   post.Subreddit,
		Title:       post.Title,
		Author:      defaultRedditAuthor(post.Author),
		Score:       post.Score,
		NumComments: post.NumComments,
		CreatedAt:   redditUnixTime(post.CreatedUTC),
		Permalink:   post.Permalink,
		URL:         post.URL,
		Body:        strings.TrimSpace(post.SelfText),
		Comments:    parseRedditComments(payload[1].Data.Children, 0, redditCommentDepthLimit),
	}

	return thread, nil
}

func redditJSONEndpoint(parsedURL *url.URL) string {
	endpoint := *parsedURL
	endpoint.Scheme = "https"
	endpoint.Host = "www.reddit.com"
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

func parseRedditComments(children []redditThing, depth, maxDepth int) []redditComment {
	comments := make([]redditComment, 0, len(children))
	for _, child := range children {
		if child.Kind != "t1" {
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
			comment.Replies = parseRedditReplies(child.Data.Replies, depth+1, maxDepth)
		}
		comments = append(comments, comment)
	}
	return comments
}

func parseRedditReplies(rawReplies json.RawMessage, depth, maxDepth int) []redditComment {
	trimmed := bytes.TrimSpace(rawReplies)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) {
		return nil
	}

	var listing redditListing
	if err := json.Unmarshal(trimmed, &listing); err != nil {
		return nil
	}
	return parseRedditComments(listing.Data.Children, depth, maxDepth)
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

	topLevelCount := minInt(len(thread.Comments), redditTopCommentLimit)
	for i := 0; i < topLevelCount; i++ {
		comment := thread.Comments[i]
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
		replyCount := minInt(len(comment.Replies), redditReplyPerCommentLimit)
		for idx := 0; idx < replyCount; idx++ {
			reply := comment.Replies[idx]
			fmt.Fprintf(&builder, "%d. **u/%s** (score: %d)\n\n", idx+1, reply.Author, reply.Score)
			if strings.TrimSpace(reply.Body) == "" {
				builder.WriteString("_No reply body available._\n\n")
			} else {
				builder.WriteString(reply.Body)
				builder.WriteString("\n\n")
			}
		}

		if len(comment.Replies) > replyCount {
			fmt.Fprintf(&builder, "_... %d more replies omitted._\n\n", len(comment.Replies)-replyCount)
		}
	}

	if len(thread.Comments) > topLevelCount {
		fmt.Fprintf(&builder, "_... %d more top-level comments omitted._\n", len(thread.Comments)-topLevelCount)
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
	nanoSeconds := int64(seconds * float64(time.Second))
	return time.Unix(0, nanoSeconds).UTC()
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
