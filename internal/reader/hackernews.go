package reader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// hackerNewsAPIBaseURL is the Firebase HN API root. It is a var (not a const)
// so tests can point it at an httptest server.
var hackerNewsAPIBaseURL = "https://hacker-news.firebaseio.com/v0"

const (
	// hackerNewsTopCommentLimit caps how many top-level comments are fetched
	// and rendered for a story.
	hackerNewsTopCommentLimit = 20

	// hackerNewsCommentDepthLimit caps how deep into reply chains we recurse.
	// A value of 1 means only top-level comments are fetched.
	hackerNewsCommentDepthLimit = 2

	// hackerNewsRepliesPerCommentLimit caps how many direct replies are
	// fetched per comment at each level below the top.
	hackerNewsRepliesPerCommentLimit = 5
)

type hackerNewsItem struct {
	ID     int    `json:"id"`
	Type   string `json:"type"`
	By     string `json:"by"`
	Time   int64  `json:"time"`
	Text   string `json:"text"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Score  int    `json:"score"`
	Kids   []int  `json:"kids"`
	Parent int    `json:"parent"`
	Dead   bool   `json:"dead"`
	Delet  bool   `json:"deleted"`
}

type hackerNewsComment struct {
	By        string
	Text      string
	CreatedAt time.Time
	Replies   []hackerNewsComment
}

func isHackerNewsItemURL(parsedURL *url.URL) bool {
	if strings.ToLower(parsedURL.Hostname()) != "news.ycombinator.com" {
		return false
	}
	if strings.TrimRight(parsedURL.Path, "/") != "/item" {
		return false
	}
	return parsedURL.Query().Get("id") != ""
}

func fetchHackerNewsContentAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	idStr := parsedURL.Query().Get("id")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		return "", fmt.Errorf("invalid hacker news item id: %q", idStr)
	}

	story, err := fetchHackerNewsItem(ctx, client, id)
	if err != nil {
		return "", err
	}

	comments := fetchHackerNewsComments(ctx, client, story.Kids, 0, hackerNewsTopCommentLimit)
	return renderHackerNewsMarkdown(story, comments, len(story.Kids)), nil
}

func fetchHackerNewsItem(ctx context.Context, client *http.Client, id int) (*hackerNewsItem, error) {
	endpoint := fmt.Sprintf("%s/item/%d.json", strings.TrimRight(hackerNewsAPIBaseURL, "/"), id)
	req, err := newRequest(ctx, endpoint, "application/json")
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hacker news request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hacker news request failed: HTTP %d: %s", resp.StatusCode, readErrorBody(resp.Body))
	}

	var item hackerNewsItem
	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(&item); err != nil {
		return nil, fmt.Errorf("failed to decode hacker news response: %w", err)
	}
	if item.ID == 0 {
		return nil, fmt.Errorf("hacker news item %d not found", id)
	}
	return &item, nil
}

// fetchHackerNewsComments fetches comment items by id, capping the count at
// limit and recursing into replies only while depth < hackerNewsCommentDepthLimit.
func fetchHackerNewsComments(ctx context.Context, client *http.Client, ids []int, depth, limit int) []hackerNewsComment {
	comments := make([]hackerNewsComment, 0, len(ids))
	for _, id := range ids {
		if limit > 0 && len(comments) >= limit {
			break
		}
		item, err := fetchHackerNewsItem(ctx, client, id)
		if err != nil {
			continue
		}
		if item.Dead || item.Delet || item.Type != "comment" {
			continue
		}

		comment := hackerNewsComment{
			By:        defaultHackerNewsAuthor(item.By),
			Text:      strings.TrimSpace(item.Text),
			CreatedAt: hackerNewsUnixTime(item.Time),
		}
		if depth+1 < hackerNewsCommentDepthLimit && len(item.Kids) > 0 {
			comment.Replies = fetchHackerNewsComments(ctx, client, item.Kids, depth+1, hackerNewsRepliesPerCommentLimit)
		}
		comments = append(comments, comment)
	}
	return comments
}

func renderHackerNewsMarkdown(story *hackerNewsItem, comments []hackerNewsComment, totalComments int) string {
	var builder strings.Builder

	title := strings.TrimSpace(story.Title)
	if title == "" {
		title = fmt.Sprintf("Hacker News item %d", story.ID)
	}
	fmt.Fprintf(&builder, "# %s\n\n", title)
	fmt.Fprintf(&builder, "- Author: %s\n", defaultHackerNewsAuthor(story.By))
	fmt.Fprintf(&builder, "- Score: %d\n", story.Score)
	if !hackerNewsUnixTime(story.Time).IsZero() {
		fmt.Fprintf(&builder, "- Created: %s\n", hackerNewsUnixTime(story.Time).Format(time.RFC3339))
	}
	if strings.TrimSpace(story.URL) != "" {
		fmt.Fprintf(&builder, "- URL: %s\n", story.URL)
	}
	fmt.Fprintf(&builder, "- Link: https://news.ycombinator.com/item?id=%d\n", story.ID)
	builder.WriteString("\n")

	if strings.TrimSpace(story.Text) != "" {
		builder.WriteString("## Text\n\n")
		builder.WriteString(strings.TrimSpace(story.Text))
		builder.WriteString("\n\n")
	}

	builder.WriteString("## Comments\n\n")
	if len(comments) == 0 {
		builder.WriteString("_No comments available._\n")
		return cleanMarkdown(builder.String())
	}

	for i, comment := range comments {
		fmt.Fprintf(&builder, "### Comment %d by %s\n\n", i+1, comment.By)
		writeHackerNewsCommentBody(&builder, comment)
		renderHackerNewsReplies(&builder, comment.Replies)
	}

	if totalComments > len(comments) {
		fmt.Fprintf(&builder, "_... %d more top-level comments omitted._\n", totalComments-len(comments))
	}

	return cleanMarkdown(builder.String())
}

func renderHackerNewsReplies(builder *strings.Builder, replies []hackerNewsComment) {
	if len(replies) == 0 {
		return
	}
	builder.WriteString("#### Replies\n\n")
	for idx, reply := range replies {
		fmt.Fprintf(builder, "%d. **%s**\n\n", idx+1, reply.By)
		writeHackerNewsCommentBody(builder, reply)
	}
}

func writeHackerNewsCommentBody(builder *strings.Builder, comment hackerNewsComment) {
	if strings.TrimSpace(comment.Text) == "" {
		builder.WriteString("_No comment body available._\n\n")
		return
	}
	builder.WriteString(comment.Text)
	builder.WriteString("\n\n")
}

func defaultHackerNewsAuthor(author string) string {
	if strings.TrimSpace(author) == "" {
		return "[deleted]"
	}
	return author
}

func hackerNewsUnixTime(seconds int64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}
