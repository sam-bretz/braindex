package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
)

const transcribeUsage = "usage: braindex source transcribe SOURCE_ID... [--transcriber PATH] [--arg ARG] [--timeout DURATION] [--force]"

type podcastDownload func(context.Context, string, io.Writer) error

type transcriptResult struct {
	SourceID string `json:"source_id"`
	Status   string `json:"status"`
	Revision string `json:"revision,omitempty"`
	Error    string `json:"error,omitempty"`
	Next     string `json:"next,omitempty"`
}

func (c command) transcribePodcast(store *library.Store, args []string, download podcastDownload) error {
	return c.transcribePodcastSelected(store, args, nil, download)
}

func (c command) transcribePodcastSelected(store *library.Store, args, selected []string, download podcastDownload) error {
	flags := newFlags("source transcribe")
	executable := flags.String("transcriber", os.Getenv("BRAINDEX_TRANSCRIBER"), "local transcriber executable")
	timeout := flags.Duration("timeout", 20*time.Minute, "maximum time per episode, including download")
	force := flags.Bool("force", false, "replace an existing transcript after successful transcription")
	var arguments stringsFlag
	flags.Var(&arguments, "arg", "transcriber argument with {input}, {output}, or {dir} placeholders")
	ids, err := parse(flags, args)
	if err != nil {
		return err
	}
	if selected != nil {
		if len(ids) != 0 {
			return errors.New("adapter arguments cannot contain additional source IDs")
		}
		ids = selected
	}
	if len(ids) == 0 || len(ids) > 20 || *timeout <= 0 || *timeout > 2*time.Hour {
		return errors.New(transcribeUsage)
	}
	path, setupErr := localTranscriber(*executable)
	if setupErr != nil && selected == nil {
		return setupErr
	}
	if len(arguments) == 0 {
		arguments = stringsFlag{"{input}", "{output}"}
	}
	joined := strings.Join(arguments, " ")
	if !strings.Contains(joined, "{input}") || (!strings.Contains(joined, "{output}") && !strings.Contains(joined, "{dir}")) {
		return errors.New("transcriber arguments require {input} and either {output} or {dir}")
	}
	results := make([]transcriptResult, 0, len(ids))
	seen := make(map[string]bool)
	failed := 0
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		result, err := transcribeEpisode(ctx, store, id, path, arguments, *force, download, setupErr)
		cancel()
		if err != nil {
			result.Status, result.Error = "failed", err.Error()
			if result.Next == "" {
				result.Next = "check the error and retry this episode"
			}
			failed++
		}
		results = append(results, result)
	}
	var resultErr error
	if failed > 0 {
		resultErr = fmt.Errorf("%d podcast episode(s) failed; see results", failed)
	}
	if failed < len(results) {
		resultErr = errors.Join(resultErr, index.Rebuild(store))
	}
	return errors.Join(c.json(results), resultErr)
}

func localTranscriber(executable string) (string, error) {
	if executable == "" {
		return "", errors.New("set BRAINDEX_TRANSCRIBER or pass --transcriber PATH")
	}
	path, err := exec.LookPath(executable)
	if err != nil {
		return "", fmt.Errorf("locate local transcriber: %w", err)
	}
	return filepath.Abs(path)
}

func transcribeEpisode(ctx context.Context, store *library.Store, id, executable string, args []string, force bool, download podcastDownload, setupErr error) (transcriptResult, error) {
	result := transcriptResult{SourceID: id}
	source, err := store.Source(id)
	if err != nil {
		return result, err
	}
	if !strings.HasPrefix(id, "podcast-") {
		return result, errors.New("source must be a discovered podcast episode")
	}
	if source.Status == "ready" && !force {
		result.Status, result.Revision = "cached", source.Revision
		return result, nil
	}
	if source.AudioURL == "" {
		result.Next = "refresh the podcast feed or attach a local transcript"
		return result, errors.New("episode has no audio enclosure; refresh its podcast feed and try again")
	}

	if setupErr != nil {
		result.Next = "configure a local transcriber and retry"
		return result, setupErr
	}

	parent := filepath.Join(store.MetaDir(), "transcripts", "podcast", source.ID)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return result, err
	}
	work, err := os.MkdirTemp(parent, ".transcribe-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(work)
	input := filepath.Join(work, "episode"+podcastExtension(source.AudioURL))
	audio, err := os.OpenFile(input, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	downloadErr := download(ctx, source.AudioURL, audio)
	if err := errors.Join(downloadErr, audio.Close()); err != nil {
		return result, err
	}
	output := filepath.Join(work, "transcript.vtt")
	expander := strings.NewReplacer("{input}", input, "{output}", output, "{dir}", work)
	commandArgs := make([]string, len(args))
	for i, arg := range args {
		commandArgs[i] = expander.Replace(arg)
	}
	cmd := exec.CommandContext(ctx, executable, commandArgs...)
	cmd.Dir = work
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Run(); err != nil {
		return result, fmt.Errorf("run local transcriber: %w", errors.Join(err, ctx.Err()))
	}
	info, err := os.Lstat(output)
	if err != nil || !info.Mode().IsRegular() {
		return result, errors.New("transcriber must write a regular transcript.vtt file")
	}
	transcript, err := ingest.Extract(output)
	if err != nil {
		return result, err
	}
	if len(transcript.Passages) == 0 {
		return result, errors.New("transcriber produced no timestamped text")
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	digest := sha256.Sum256(raw)
	final := filepath.Join(parent, fmt.Sprintf("%x.vtt", digest))
	if err := os.Chmod(output, 0600); err != nil {
		return result, err
	}
	if err := os.Rename(output, final); err != nil {
		return result, err
	}
	saved, err := store.PutTranscript(id, final, transcript.Passages)
	if err != nil {
		return result, err
	}
	result.Status, result.Revision = "searchable", saved.Revision
	return result, nil
}

func podcastExtension(address string) string {
	if parsed, err := url.Parse(address); err == nil {
		extension := strings.ToLower(filepath.Ext(parsed.Path))
		switch extension {
		case ".mp3", ".mp4", ".m4a", ".aac", ".ogg", ".opus", ".wav", ".webm", ".flac":
			return extension
		}
	}
	return ".audio"
}
