package reader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// lobstersBaseURL is the lobste.rs origin used for the story JSON API. It is
// a var so tests can point it at an httptest server.
var lobstersBaseURL = "https://lobste.rs"

// maxLobstersComments caps how many comments are rendered for a story.
const maxLobstersComments = 50

// lobstersUser tolerates both API shapes: a plain username string and the
// older object form {"username": "..."}.
type lobstersUser string

func (u *lobstersUser) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		*u = lobstersUser(name)
		return nil
	}
	var obj struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	*u = lobstersUser(obj.Username)
	return nil
}

type lobstersStory struct {
	Title        string            `json:"title"`
	URL          string            `json:"url"`
	Description  string            `json:"description"`
	Score        int               `json:"score"`
	CreatedAt    string            `json:"created_at"`
	Submitter    lobstersUser      `json:"submitter_user"`
	CommentCount int               `json:"comment_count"`
	Comments     []lobstersComment `json:"comments"`
	ShortIDURL   string            `json:"short_id_url"`
}

type lobstersComment struct {
	Comment     string       `json:"comment"`
	Score       int          `json:"score"`
	IndentLevel int          `json:"indent_level"`
	CreatedAt   string       `json:"created_at"`
	Author      lobstersUser `json:"commenting_user"`
}

// isLobstersStoryURL matches lobste.rs/s/{id}[/{slug}].
func isLobstersStoryURL(parsedURL *url.URL) bool {
	host := strings.ToLower(parsedURL.Hostname())
	if host != "lobste.rs" && host != "www.lobste.rs" {
		return false
	}
	segments := pathSegments(parsedURL.Path)
	return len(segments) >= 2 && segments[0] == "s" && segments[1] != ""
}

func fetchLobstersContentAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	segments := pathSegments(parsedURL.Path)
	endpoint := fmt.Sprintf("%s/s/%s.json", strings.TrimRight(lobstersBaseURL, "/"), segments[1])

	req, err := newRequest(ctx, endpoint, "application/json")
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("lobsters request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("lobsters request failed: HTTP %d: %s", resp.StatusCode, readErrorBody(resp.Body))
	}

	var story lobstersStory
	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(&story); err != nil {
		return "", fmt.Errorf("failed to decode lobsters response: %w", err)
	}
	return renderLobstersMarkdown(story), nil
}

func renderLobstersMarkdown(story lobstersStory) string {
	var b strings.Builder
	title := strings.TrimSpace(story.Title)
	if title == "" {
		title = "Lobsters story"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- Author: %s\n", string(story.Submitter))
	fmt.Fprintf(&b, "- Score: %d\n", story.Score)
	if story.CreatedAt != "" {
		fmt.Fprintf(&b, "- Created: %s\n", story.CreatedAt)
	}
	if strings.TrimSpace(story.URL) != "" {
		fmt.Fprintf(&b, "- URL: %s\n", story.URL)
	}
	if story.ShortIDURL != "" {
		fmt.Fprintf(&b, "- Link: %s\n", story.ShortIDURL)
	}
	b.WriteString("\n")

	if desc := feedDescriptionMarkdown(story.Description); desc != "" {
		b.WriteString("## Text\n\n")
		b.WriteString(desc)
		b.WriteString("\n\n")
	}

	b.WriteString("## Comments\n\n")
	if len(story.Comments) == 0 {
		b.WriteString("_No comments available._\n")
		return cleanMarkdown(b.String())
	}
	for i, comment := range story.Comments {
		if i >= maxLobstersComments {
			fmt.Fprintf(&b, "_... %d more comments omitted._\n", len(story.Comments)-i)
			break
		}
		indent := strings.Repeat(">", comment.IndentLevel)
		if indent != "" {
			indent += " "
		}
		fmt.Fprintf(&b, "%s**%s** (score %d):\n", indent, string(comment.Author), comment.Score)
		body := feedDescriptionMarkdown(comment.Comment)
		if body == "" {
			body = "_No comment body available._"
		}
		for _, line := range strings.Split(body, "\n") {
			fmt.Fprintf(&b, "%s%s\n", indent, line)
		}
		b.WriteString("\n")
	}
	return cleanMarkdown(b.String())
}
