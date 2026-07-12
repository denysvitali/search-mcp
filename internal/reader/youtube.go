package reader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// youTubeWatchBaseURL is the watch endpoint used to obtain the public player
// response. Tests replace it with an httptest server.
var youTubeWatchBaseURL = "https://www.youtube.com/watch"

const maxYouTubeTranscriptEvents = 2000

type youTubePlayerResponse struct {
	VideoDetails struct {
		Title      string `json:"title"`
		Author     string `json:"author"`
		LengthSecs string `json:"lengthSeconds"`
	} `json:"videoDetails"`
	Captions struct {
		TrackList struct {
			Tracks []youTubeCaptionTrack `json:"captionTracks"`
		} `json:"playerCaptionsTracklistRenderer"`
	} `json:"captions"`
}

type youTubeCaptionTrack struct {
	BaseURL      string `json:"baseUrl"`
	LanguageCode string `json:"languageCode"`
	Name         struct {
		SimpleText string `json:"simpleText"`
		Runs       []struct {
			Text string `json:"text"`
		} `json:"runs"`
	} `json:"name"`
}

type youTubeTranscript struct {
	Events []struct {
		StartMS int64 `json:"tStartMs"`
		Segs    []struct {
			Text string `json:"utf8"`
		} `json:"segs"`
	} `json:"events"`
}

// isYouTubeVideoURL recognizes watch, short-link, and shorts video URLs.
func isYouTubeVideoURL(parsedURL *url.URL) bool {
	return youTubeVideoID(parsedURL) != ""
}

func youTubeVideoID(parsedURL *url.URL) string {
	host := strings.ToLower(parsedURL.Hostname())
	segments := pathSegments(parsedURL.Path)
	switch host {
	case "youtu.be", "www.youtu.be":
		if len(segments) > 0 {
			return segments[0]
		}
	case "youtube.com", "www.youtube.com", "m.youtube.com":
		if parsedURL.Path == "/watch" {
			return parsedURL.Query().Get("v")
		}
		if len(segments) >= 2 && segments[0] == "shorts" {
			return segments[1]
		}
	}
	return ""
}

// fetchYouTubeContentAsMarkdown reads the public player response embedded in
// the watch page. When captions are available, it fetches the first English
// track (or the first track) and returns a timestamped transcript.
func fetchYouTubeContentAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	videoID := youTubeVideoID(parsedURL)
	endpoint, err := url.Parse(youTubeWatchBaseURL)
	if err != nil {
		return "", fmt.Errorf("invalid YouTube watch URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("v", videoID)
	endpoint.RawQuery = query.Encode()

	req, err := newRequest(ctx, endpoint.String(), "text/html")
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("YouTube watch request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("YouTube watch request failed: HTTP %d: %s", resp.StatusCode, readErrorBody(resp.Body))
	}

	body, err := io.ReadAll(limitedBody(resp.Body))
	if err != nil {
		return "", fmt.Errorf("read YouTube watch response: %w", err)
	}
	var player youTubePlayerResponse
	if err := json.Unmarshal(extractJSONObject(body, "ytInitialPlayerResponse"), &player); err != nil {
		return "", fmt.Errorf("decode YouTube player response: %w", err)
	}

	track := preferredYouTubeTrack(player.Captions.TrackList.Tracks)
	transcript := ""
	if track.BaseURL != "" {
		transcript, err = fetchYouTubeTranscript(ctx, client, track.BaseURL)
		if err != nil {
			return "", err
		}
	}
	return renderYouTubeMarkdown(parsedURL, player, track, transcript), nil
}

func preferredYouTubeTrack(tracks []youTubeCaptionTrack) youTubeCaptionTrack {
	for _, track := range tracks {
		if strings.HasPrefix(strings.ToLower(track.LanguageCode), "en") {
			return track
		}
	}
	if len(tracks) > 0 {
		return tracks[0]
	}
	return youTubeCaptionTrack{}
}

func fetchYouTubeTranscript(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	endpoint, err := validateURL(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid YouTube caption URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("fmt", "json3")
	endpoint.RawQuery = query.Encode()
	req, err := newRequest(ctx, endpoint.String(), "application/json")
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("YouTube transcript request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("YouTube transcript request failed: HTTP %d: %s", resp.StatusCode, readErrorBody(resp.Body))
	}
	var transcript youTubeTranscript
	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(&transcript); err != nil {
		return "", fmt.Errorf("decode YouTube transcript: %w", err)
	}
	var b strings.Builder
	for i, event := range transcript.Events {
		if i >= maxYouTubeTranscriptEvents {
			fmt.Fprintf(&b, "\n_[... %d more transcript events omitted.]_", len(transcript.Events)-i)
			break
		}
		var text strings.Builder
		for _, segment := range event.Segs {
			text.WriteString(segment.Text)
		}
		line := strings.TrimSpace(text.String())
		if line != "" {
			fmt.Fprintf(&b, "[%s] %s\n", formatYouTubeTimestamp(event.StartMS), line)
		}
	}
	return strings.TrimSpace(b.String()), nil
}

func renderYouTubeMarkdown(parsedURL *url.URL, player youTubePlayerResponse, track youTubeCaptionTrack, transcript string) string {
	title := strings.TrimSpace(player.VideoDetails.Title)
	if title == "" {
		title = "YouTube video"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- Link: %s\n", parsedURL.String())
	if player.VideoDetails.Author != "" {
		fmt.Fprintf(&b, "- Channel: %s\n", player.VideoDetails.Author)
	}
	if seconds, err := strconv.Atoi(player.VideoDetails.LengthSecs); err == nil && seconds >= 0 {
		fmt.Fprintf(&b, "- Duration: %s\n", formatYouTubeTimestamp(int64(seconds*1000)))
	}
	if transcript == "" {
		b.WriteString("\n_No public transcript is available for this video._\n")
		return cleanMarkdown(b.String())
	}
	trackName := strings.TrimSpace(track.Name.SimpleText)
	if trackName == "" {
		for _, run := range track.Name.Runs {
			trackName += run.Text
		}
	}
	b.WriteString("\n## Transcript")
	if trackName != "" {
		fmt.Fprintf(&b, " (%s)", trackName)
	}
	b.WriteString("\n\n")
	b.WriteString(transcript)
	b.WriteString("\n")
	return cleanMarkdown(b.String())
}

func formatYouTubeTimestamp(milliseconds int64) string {
	seconds := milliseconds / 1000
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}

// extractJSONObject returns the first object following marker, correctly
// tracking strings and escapes so nested player-response JSON is preserved.
func extractJSONObject(body []byte, marker string) []byte {
	start := strings.Index(string(body), marker)
	if start < 0 {
		return nil
	}
	start += len(marker)
	for start < len(body) && body[start] != '{' {
		start++
	}
	if start == len(body) {
		return nil
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(body); i++ {
		switch body[i] {
		case '\\':
			if inString {
				escaped = !escaped
			}
		case '"':
			if !escaped {
				inString = !inString
			}
			escaped = false
		case '{':
			if !inString {
				depth++
			}
		case '}':
			if !inString {
				depth--
				if depth == 0 {
					return body[start : i+1]
				}
			}
		default:
			escaped = false
		}
	}
	return nil
}
