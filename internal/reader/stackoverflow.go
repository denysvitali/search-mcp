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

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
)

// stackOverflowAPIBaseURL is the Stack Exchange API root. It is a var (not a
// const) so tests can point it at an httptest server.
var stackOverflowAPIBaseURL = "https://api.stackexchange.com/2.3"

const (
	// stackOverflowAnswerLimit caps how many answers are rendered.
	stackOverflowAnswerLimit = 10
)

type stackOverflowOwner struct {
	DisplayName string `json:"display_name"`
}

type stackOverflowQuestion struct {
	QuestionID       int                `json:"question_id"`
	Title            string             `json:"title"`
	Body             string             `json:"body"`
	Score            int                `json:"score"`
	Tags             []string           `json:"tags"`
	Link             string             `json:"link"`
	IsAnswered       bool               `json:"is_answered"`
	AnswerCount      int                `json:"answer_count"`
	AcceptedAnswerID int                `json:"accepted_answer_id"`
	CreationDate     int64              `json:"creation_date"`
	Owner            stackOverflowOwner `json:"owner"`
}

type stackOverflowAnswer struct {
	AnswerID     int                `json:"answer_id"`
	Body         string             `json:"body"`
	Score        int                `json:"score"`
	IsAccepted   bool               `json:"is_accepted"`
	CreationDate int64              `json:"creation_date"`
	Owner        stackOverflowOwner `json:"owner"`
}

type stackOverflowResponse[T any] struct {
	Items          []T    `json:"items"`
	ErrorID        int    `json:"error_id"`
	ErrorName      string `json:"error_name"`
	ErrorMessage   string `json:"error_message"`
	QuotaRemaining int    `json:"quota_remaining"`
}

func isStackOverflowQuestionURL(parsedURL *url.URL) bool {
	if strings.ToLower(parsedURL.Hostname()) != "stackoverflow.com" {
		return false
	}
	_, ok := parseStackOverflowQuestionID(parsedURL)
	return ok
}

func parseStackOverflowQuestionID(parsedURL *url.URL) (int, bool) {
	segments := pathSegments(parsedURL.Path)
	if len(segments) < 2 || segments[0] != "questions" {
		return 0, false
	}
	id, err := strconv.Atoi(segments[1])
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func fetchStackOverflowContentAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	id, ok := parseStackOverflowQuestionID(parsedURL)
	if !ok {
		return "", fmt.Errorf("unsupported stack overflow question url: %s", parsedURL.String())
	}

	base := strings.TrimRight(stackOverflowAPIBaseURL, "/")
	questionEndpoint := fmt.Sprintf("%s/questions/%d?site=stackoverflow&filter=withbody", base, id)
	questions, err := fetchStackOverflowItems[stackOverflowQuestion](ctx, client, questionEndpoint)
	if err != nil {
		return "", err
	}
	if len(questions) == 0 {
		return "", fmt.Errorf("stack overflow question %d not found", id)
	}

	answersEndpoint := fmt.Sprintf("%s/questions/%d/answers?site=stackoverflow&filter=withbody&sort=votes", base, id)
	answers, err := fetchStackOverflowItems[stackOverflowAnswer](ctx, client, answersEndpoint)
	if err != nil {
		return "", err
	}

	return renderStackOverflowMarkdown(&questions[0], answers), nil
}

func fetchStackOverflowItems[T any](ctx context.Context, client *http.Client, endpoint string) ([]T, error) {
	req, err := newRequest(ctx, endpoint, "application/json")
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stack overflow request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stack overflow request failed: HTTP %d: %s", resp.StatusCode, readErrorBody(resp.Body))
	}

	var payload stackOverflowResponse[T]
	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("failed to decode stack overflow response: %w", err)
	}
	if payload.ErrorID != 0 || strings.TrimSpace(payload.ErrorName) != "" {
		return nil, fmt.Errorf("stack overflow api error %d (%s): %s", payload.ErrorID, strings.ToLower(payload.ErrorName), payload.ErrorMessage)
	}
	return payload.Items, nil
}

func renderStackOverflowMarkdown(question *stackOverflowQuestion, answers []stackOverflowAnswer) string {
	var builder strings.Builder

	title := strings.TrimSpace(question.Title)
	if title == "" {
		title = fmt.Sprintf("Stack Overflow question %d", question.QuestionID)
	}
	fmt.Fprintf(&builder, "# %s\n\n", title)
	fmt.Fprintf(&builder, "- Author: %s\n", defaultStackOverflowAuthor(question.Owner.DisplayName))
	fmt.Fprintf(&builder, "- Score: %d\n", question.Score)
	fmt.Fprintf(&builder, "- Answers: %d\n", question.AnswerCount)
	if !stackOverflowUnixTime(question.CreationDate).IsZero() {
		fmt.Fprintf(&builder, "- Created: %s\n", stackOverflowUnixTime(question.CreationDate).Format(time.RFC3339))
	}
	if len(question.Tags) > 0 {
		fmt.Fprintf(&builder, "- Tags: %s\n", strings.Join(question.Tags, ", "))
	}
	if strings.TrimSpace(question.Link) != "" {
		fmt.Fprintf(&builder, "- Link: %s\n", question.Link)
	}
	builder.WriteString("\n")

	builder.WriteString("## Question\n\n")
	writeStackOverflowBody(&builder, question.Body)

	// Order answers: accepted first, then by descending score (the API already
	// sorts by votes, but we hoist the accepted answer to the front).
	ordered := orderStackOverflowAnswers(answers)

	fmt.Fprintf(&builder, "## Answers (%d)\n\n", len(answers))
	if len(ordered) == 0 {
		builder.WriteString("_No answers available._\n")
		return cleanMarkdown(builder.String())
	}

	rendered := ordered
	if len(rendered) > stackOverflowAnswerLimit {
		rendered = rendered[:stackOverflowAnswerLimit]
	}
	for i, answer := range rendered {
		accepted := ""
		if answer.IsAccepted {
			accepted = " (accepted)"
		}
		fmt.Fprintf(&builder, "### Answer %d by %s (score: %d)%s\n\n", i+1, defaultStackOverflowAuthor(answer.Owner.DisplayName), answer.Score, accepted)
		writeStackOverflowBody(&builder, answer.Body)
	}

	if len(answers) > len(rendered) {
		fmt.Fprintf(&builder, "_... %d more answers omitted._\n", len(answers)-len(rendered))
	}

	return cleanMarkdown(builder.String())
}

// orderStackOverflowAnswers returns answers with any accepted answer first,
// then the rest by descending score.
func orderStackOverflowAnswers(answers []stackOverflowAnswer) []stackOverflowAnswer {
	ordered := make([]stackOverflowAnswer, 0, len(answers))
	var rest []stackOverflowAnswer
	for _, answer := range answers {
		if answer.IsAccepted {
			ordered = append(ordered, answer)
		} else {
			rest = append(rest, answer)
		}
	}
	ordered = append(ordered, rest...)
	return ordered
}

func writeStackOverflowBody(builder *strings.Builder, htmlBody string) {
	markdown := strings.TrimSpace(stackOverflowHTMLToMarkdown(htmlBody))
	if markdown == "" {
		builder.WriteString("_No content available._\n\n")
		return
	}
	builder.WriteString(markdown)
	builder.WriteString("\n\n")
}

// stackOverflowHTMLToMarkdown converts an HTML fragment to Markdown using the
// same converter the generic HTML path uses. On conversion failure it falls
// back to the raw HTML so content is never silently dropped.
func stackOverflowHTMLToMarkdown(htmlBody string) string {
	if strings.TrimSpace(htmlBody) == "" {
		return ""
	}
	conv := converter.NewConverter(
		converter.WithPlugins(
			base.NewBasePlugin(),
			commonmark.NewCommonmarkPlugin(),
		),
	)
	markdown, err := conv.ConvertString(htmlBody)
	if err != nil {
		return htmlBody
	}
	return markdown
}

func defaultStackOverflowAuthor(name string) string {
	if strings.TrimSpace(name) == "" {
		return "[unknown]"
	}
	return name
}

func stackOverflowUnixTime(seconds int64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}
