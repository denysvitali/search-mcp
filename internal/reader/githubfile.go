package reader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// githubRawBaseURL is the raw file host for GitHub blob URLs. It is a var so
// tests can point it at an httptest server.
var githubRawBaseURL = "https://raw.githubusercontent.com"

// codeFenceLanguages maps file extensions to Markdown fence hints.
var codeFenceLanguages = map[string]string{
	".go": "go", ".py": "python", ".js": "javascript", ".ts": "typescript",
	".rs": "rust", ".c": "c", ".h": "c", ".cpp": "cpp", ".hpp": "cpp",
	".java": "java", ".rb": "ruby", ".sh": "bash", ".bash": "bash",
	".yaml": "yaml", ".yml": "yaml", ".json": "json", ".toml": "toml",
	".html": "html", ".css": "css", ".sql": "sql", ".proto": "proto",
	".kt": "kotlin", ".swift": "swift", ".php": "php", ".zig": "zig",
}

// isGitHubBlobURL matches github.com/{owner}/{repo}/blob/{ref}/{path...}.
func isGitHubBlobURL(parsedURL *url.URL) bool {
	host := strings.ToLower(parsedURL.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return false
	}
	segments := pathSegments(parsedURL.Path)
	return len(segments) >= 5 && segments[2] == "blob"
}

// fetchGitHubFileAsMarkdown fetches the file via raw.githubusercontent.com
// and renders it as a fenced code block (or verbatim for Markdown/plain text).
func fetchGitHubFileAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	segments := pathSegments(parsedURL.Path)
	owner, repo, ref := segments[0], segments[1], segments[3]
	filePath := strings.Join(segments[4:], "/")

	rawURL := fmt.Sprintf("%s/%s/%s/%s/%s",
		strings.TrimRight(githubRawBaseURL, "/"), owner, repo, ref, filePath)
	req, err := newRequest(ctx, rawURL, "text/plain")
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("github raw request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github raw request failed: HTTP %d: %s", resp.StatusCode, readErrorBody(resp.Body))
	}
	body, err := io.ReadAll(limitedBody(resp.Body))
	if err != nil {
		return "", fmt.Errorf("failed to read github raw response: %w", err)
	}
	if isBinaryResponse(resp.Header.Get("Content-Type"), body) {
		return "", fmt.Errorf("refusing to return binary file %s", filePath)
	}

	header := fmt.Sprintf("# %s/%s: %s (at %s)\n\n", owner, repo, filePath, ref)
	ext := strings.ToLower(path.Ext(filePath))
	if ext == ".md" || ext == ".markdown" || ext == ".txt" || ext == ".rst" {
		return header + strings.TrimSpace(string(body)) + "\n", nil
	}
	fence := codeFenceLanguages[ext]
	return fmt.Sprintf("%s```%s\n%s\n```\n", header, fence, strings.TrimRight(string(body), "\n")), nil
}
