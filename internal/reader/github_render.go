package reader

import (
	"fmt"
	"strings"
	"time"
)

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
