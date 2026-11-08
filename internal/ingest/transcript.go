package ingest

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/sam-bretz/braindex/internal/library"
)

var timing = regexp.MustCompile(`^((?:[0-9]{2,}:)?[0-9]{2}:[0-9]{2}[.,][0-9]{3})[ \t]+-->[ \t]+((?:[0-9]{2,}:)?[0-9]{2}:[0-9]{2}[.,][0-9]{3})(?:[ \t].*)?$`)
var voice = regexp.MustCompile(`^<v(?:\.[^ >]+)?[ \t]+([^>]+)>(.*)$`)

func transcriptPassages(raw string) ([]library.Passage, error) {
	if !utf8.ValidString(raw) {
		return nil, fmt.Errorf("expected UTF-8 transcript")
	}
	raw = strings.TrimPrefix(strings.ReplaceAll(raw, "\r\n", "\n"), "\uFEFF")
	var passages []library.Passage
	for _, block := range strings.Split(raw, "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 2 || strings.HasPrefix(lines[0], "NOTE") {
			continue
		}
		for i := 0; i < len(lines)-1 && i < 2; i++ {
			match := timing.FindStringSubmatch(strings.TrimSpace(lines[i]))
			if match == nil {
				continue
			}
			text := strings.Join(lines[i+1:], " ")
			label := ""
			if match := voice.FindStringSubmatch(text); match != nil && strings.Count(text, "<v") == 1 {
				label, text = strings.TrimSpace(match[1]), match[2]
			}
			_, clean, err := HTMLText(strings.NewReader(text))
			if err != nil {
				return nil, err
			}
			if clean != "" {
				locator := strings.ReplaceAll(match[1]+"–"+match[2], ",", ".")
				if label != "" {
					locator += " (" + label + ")"
				}
				passages = append(passages, library.Passage{Text: clean, Locator: locator})
			}
			break
		}
	}
	return passages, nil
}

// AttachTranscript adds a local WebVTT or SRT transcript to a discovered episode.
func AttachTranscript(source library.Source, path string) (library.Source, error) {
	extension := strings.ToLower(filepath.Ext(path))
	if extension != ".vtt" && extension != ".srt" {
		return library.Source{}, fmt.Errorf("expected a WebVTT or SRT transcript")
	}
	transcript, err := Extract(path)
	if err != nil {
		return library.Source{}, err
	}
	source.Path = transcript.Path
	source.Revision = ""
	source.Passages = transcript.Passages
	source.Status = transcript.Status
	return source, nil
}
