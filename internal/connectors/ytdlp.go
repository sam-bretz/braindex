package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
)

const MaxYouTubeEntries = 500

var (
	ErrYouTubeRateLimited        = errors.New("YouTube rate limited the request")
	ErrYouTubeCaptionUnavailable = errors.New("no caption track for the selected language")
	youtubeVideoID               = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	youtubeCaptionLanguage       = regexp.MustCompile(`^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$`)
)

type YouTubeExtractor struct {
	Executable string
	JSRuntime  string
}

func (x YouTubeExtractor) Discover(ctx context.Context, input string, limit int) ([]library.Source, error) {
	address, err := youtubeSeriesURL(input)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxYouTubeEntries {
		return nil, fmt.Errorf("YouTube limit must be between 1 and %d", MaxYouTubeEntries)
	}
	output, err := x.run(ctx, "--flat-playlist", "--dump-single-json", "--skip-download", "--playlist-end", strconv.Itoa(limit), "--", address)
	if err != nil {
		return nil, err
	}
	var playlist struct {
		Channel  string `json:"channel"`
		Uploader string `json:"uploader"`
		Entries  []struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			Channel    string `json:"channel"`
			Uploader   string `json:"uploader"`
			UploadDate string `json:"upload_date"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(output, &playlist); err != nil {
		return nil, fmt.Errorf("read yt-dlp video list: %w", err)
	}
	seen := make(map[string]bool)
	sources := make([]library.Source, 0, min(limit, len(playlist.Entries)))
	for _, video := range playlist.Entries {
		if !youtubeVideoID.MatchString(video.ID) || seen[video.ID] {
			continue
		}
		seen[video.ID] = true
		date, _ := time.Parse("20060102", video.UploadDate)
		source := library.Source{
			ID: ingest.StableID("youtube", video.ID), URL: "https://youtu.be/" + video.ID,
			Title: first(video.Title, video.ID), Author: first(video.Channel, video.Uploader, playlist.Channel, playlist.Uploader), Status: "unavailable",
		}
		if !date.IsZero() {
			source.Date = date.Format(time.DateOnly)
		}
		sources = append(sources, source)
		if len(sources) == limit {
			break
		}
	}
	return sources, nil
}

// YouTubeVideoID resolves a single YouTube video address to a canonical ID.
func YouTubeVideoID(input string) (string, error) {
	parsed, err := publicYouTubeURL(input)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	id := parsed.Query().Get("v")
	switch {
	case parsed.Hostname() == "youtu.be" && len(parts) == 1:
		id = parts[0]
	case len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed"):
		id = parts[1]
	case parsed.Path != "/watch":
		id = ""
	}
	if !youtubeVideoID.MatchString(id) {
		return "", errors.New("source must link to a single YouTube video")
	}
	return id, nil
}

func ValidateYouTubeCaptionLanguage(language string) error {
	if len(language) > 35 || !youtubeCaptionLanguage.MatchString(language) {
		return errors.New("invalid caption language; use a language code such as en or pt-BR")
	}
	return nil
}

// FetchCaption downloads the selected track into a caller-owned temporary
// directory and returns its file path. The caller validates and publishes it.
func (x YouTubeExtractor) FetchCaption(ctx context.Context, videoID, language, dir string) (string, error) {
	if !youtubeVideoID.MatchString(videoID) {
		return "", errors.New("invalid YouTube video ID")
	}
	if err := ValidateYouTubeCaptionLanguage(language); err != nil {
		return "", err
	}
	if _, err := x.run(ctx, "--skip-download", "--no-playlist", "--ignore-no-formats-error",
		"--write-subs", "--write-auto-subs", "--sub-langs", language, "--sub-format", "vtt",
		"--sleep-subtitles", "1", "--max-filesize", "16M", "--output", filepath.Join(dir, "captions.%(ext)s"),
		"--", "https://youtu.be/"+videoID); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "captions."+language+".vtt")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrYouTubeCaptionUnavailable
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return "", errors.New("yt-dlp caption output must be a regular VTT file up to 16 MiB")
	}
	return path, nil
}

func (x YouTubeExtractor) run(ctx context.Context, args ...string) ([]byte, error) {
	executable := x.Executable
	if executable == "" {
		executable = "yt-dlp"
	}
	common := []string{"--ignore-config", "--no-plugin-dirs", "--no-cache-dir", "--no-progress", "--quiet", "--color", "never",
		"--socket-timeout", "10", "--retries", "1", "--extractor-retries", "1",
		"--fragment-retries", "0", "--file-access-retries", "0", "--retry-sleep", "http:5", "--retry-sleep", "extractor:5"}
	if x.JSRuntime != "" {
		common = append(common, "--js-runtimes", x.JSRuntime)
	}
	cmd := exec.CommandContext(ctx, executable, append(common, args...)...)
	cmd.WaitDelay = time.Second
	stdout, stderr := limitedExtractorOutput{limit: 4 << 20}, limitedExtractorOutput{limit: 16 << 10}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("yt-dlp: %w", ctx.Err())
	}
	diagnostic := strings.ToLower(stderr.String())
	if strings.Contains(diagnostic, "http error 429") || strings.Contains(diagnostic, "too many requests") {
		return nil, ErrYouTubeRateLimited
	}
	if err != nil {
		if strings.Contains(diagnostic, "video unavailable") || strings.Contains(diagnostic, "this video is private") {
			return nil, fmt.Errorf("%w: YouTube video is unavailable", ErrYouTubeCaptionUnavailable)
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("install yt-dlp or point BRAINDEX_YT_DLP at its executable")
		}
		return nil, fmt.Errorf("yt-dlp failed (%w): %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.truncated {
		return nil, errors.New("yt-dlp output exceeds 4 MiB; use a smaller discovery limit")
	}
	return stdout.Bytes(), nil
}

type limitedExtractorOutput struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (o *limitedExtractorOutput) Write(data []byte) (int, error) {
	count := min(len(data), o.limit-o.Len())
	o.truncated = o.truncated || count < len(data)
	_, _ = o.Buffer.Write(data[:count])
	return len(data), nil
}

func publicYouTubeURL(input string) (*url.URL, error) {
	address, err := url.Parse(input)
	if err == nil && address.Scheme == "https" && address.User == nil && address.Port() == "" {
		address.Host = strings.ToLower(address.Host)
		switch address.Host {
		case "www.youtube.com", "youtube.com", "m.youtube.com", "youtu.be":
			return address, nil
		}
	}
	return nil, errors.New("expected a public HTTPS YouTube URL")
}

func youtubeSeriesURL(input string) (string, error) {
	if providerID.MatchString(input) {
		return "https://www.youtube.com/playlist?list=" + input, nil
	}
	address, err := publicYouTubeURL(input)
	if err != nil {
		return "", err
	}
	if playlist := address.Query().Get("list"); providerID.MatchString(playlist) {
		return "https://www.youtube.com/playlist?list=" + playlist, nil
	}
	parts := strings.Split(strings.Trim(address.Path, "/"), "/")
	if parts[len(parts)-1] == "videos" && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	isHandle := len(parts) == 1 && strings.HasPrefix(parts[0], "@") && len(parts[0]) > 1
	isChannel := len(parts) == 2 && (parts[0] == "channel" || parts[0] == "c" || parts[0] == "user") && parts[1] != ""
	if address.Host != "youtu.be" && (isHandle || isChannel) {
		channel := url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/" + strings.Join(parts, "/") + "/videos"}
		return channel.String(), nil
	}
	return "", errors.New("expected a YouTube playlist URL or channel URL")
}
