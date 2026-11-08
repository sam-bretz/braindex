package ingest

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sam-bretz/braindex/internal/library"
)

var timing = regexp.MustCompile(`^((?:[0-9]{2,}:)?[0-9]{2}:[0-9]{2}[.,][0-9]{3})[ \t]+-->[ \t]+((?:[0-9]{2,}:)?[0-9]{2}:[0-9]{2}[.,][0-9]{3})(?:[ \t].*)?$`)
var voice = regexp.MustCompile(`(?s)^<v(?:\.[^ >]+)?[ \t]+([^>]+)>(.*)$`)
var inlineTimestamp = regexp.MustCompile(`<(?:[0-9]{2,}:)?[0-9]{2}:[0-9]{2}\.[0-9]{3}>`)

type transcriptCue struct {
	start, end        string
	startMS, endMS    int64
	text, carry, name string
}

func transcriptPassages(raw string) ([]library.Passage, error) {
	if !utf8.ValidString(raw) {
		return nil, fmt.Errorf("expected UTF-8 transcript")
	}
	raw = strings.TrimPrefix(strings.ReplaceAll(raw, "\r\n", "\n"), "\uFEFF")
	checkRolling := strings.HasPrefix(raw, "WEBVTT") && inlineTimestamp.MatchString(raw)
	var cues []transcriptCue
	hasInlineTiming := false
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
			payload := strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
			label := ""
			if match := voice.FindStringSubmatch(payload); match != nil && strings.Count(payload, "<v") == 1 {
				label, payload = strings.TrimSpace(match[1]), match[2]
			}
			cue := transcriptCue{
				start: strings.ReplaceAll(match[1], ",", "."),
				end:   strings.ReplaceAll(match[2], ",", "."),
				name:  label,
			}
			var err error
			cue.text, err = cleanCaptionText(payload)
			if err != nil {
				return nil, err
			}
			if cue.text == "" {
				break
			}
			if checkRolling {
				hasInlineTiming = hasInlineTiming || inlineTimestamp.MatchString(payload)
				cue.startMS, cue.endMS = captionMillis(cue.start), captionMillis(cue.end)
				if first, _, multiline := strings.Cut(payload, "\n"); multiline && !inlineTimestamp.MatchString(first) {
					cue.carry, err = cleanCaptionText(first)
					if err != nil {
						return nil, err
					}
				}
			}
			cues = append(cues, cue)
			break
		}
	}

	// Word timing alone can also indicate karaoke-style VTT. Only coalesce
	// cues when we also see the repaint or carryover pattern of rolling text.
	if hasInlineTiming {
		for i := 1; i < len(cues); i++ {
			if novelCaption(cues[i-1], cues[i]) != cues[i].text {
				return mergeRollingCaptions(cues), nil
			}
		}
	}
	var passages []library.Passage
	for _, cue := range cues {
		passages = append(passages, cue.passage())
	}
	return passages, nil
}

func cleanCaptionText(payload string) (string, error) {
	// WebVTT timestamp tags are not HTML tags. Remove them before the HTML
	// parser strips formatting tags and decodes entities.
	payload = inlineTimestamp.ReplaceAllString(payload, "")
	_, text, err := HTMLText(strings.NewReader(strings.ReplaceAll(payload, "\n", " ")))
	return text, err
}

func (cue transcriptCue) passage() library.Passage {
	locator := cue.start + "–" + cue.end
	if cue.name != "" {
		locator += " (" + cue.name + ")"
	}
	return library.Passage{Text: cue.text, Locator: locator}
}

// captionMillis converts the timing regex's validated MM:SS.mmm and
// HH:MM:SS.mmm forms. Reject oversized hour fields rather than overflowing.
func captionMillis(stamp string) int64 {
	clock, fraction, _ := strings.Cut(stamp, ".")
	var seconds int64
	for _, component := range strings.Split(clock, ":") {
		value, err := strconv.ParseUint(component, 10, 32)
		if err != nil {
			return -1
		}
		seconds = seconds*60 + int64(value)
	}
	millis, _ := strconv.ParseInt(fraction, 10, 64)
	return seconds*1000 + millis
}

func novelCaption(previous, cue transcriptCue) string {
	if previous.name != cue.name || previous.startMS < 0 || previous.endMS < previous.startMS ||
		cue.startMS < previous.startMS || cue.endMS < cue.startMS ||
		cue.startMS < previous.endMS-250 || cue.startMS > previous.endMS+250 {
		return cue.text
	}
	hasSuffix := func(text string) bool {
		return previous.text == text || strings.HasSuffix(previous.text, " "+text)
	}
	// YouTube displays the previous cue's final line for a few milliseconds
	// between animated cues, then keeps that same line above the new words.
	if cue.endMS-cue.startMS <= 100 && hasSuffix(cue.text) {
		return ""
	}
	if cue.carry != "" && hasSuffix(cue.carry) {
		if rest, ok := strings.CutPrefix(cue.text, cue.carry+" "); ok {
			return rest
		}
	}
	return cue.text
}

func mergeRollingCaptions(cues []transcriptCue) []library.Passage {
	const maxDurationMS, maxGapMS, maxTextBytes = 25_000, 2_000, 500
	var passages []library.Passage
	group := cues[0]
	for i, cue := range cues[1:] {
		cue.text = novelCaption(cues[i], cue)
		if cue.text == "" {
			continue
		}
		if cue.name != group.name || group.startMS < 0 || cue.startMS < group.startMS ||
			cue.endMS < cue.startMS || cue.startMS-group.endMS > maxGapMS ||
			max(group.endMS, cue.endMS)-group.startMS > maxDurationMS ||
			len(group.text)+1+len(cue.text) > maxTextBytes {
			passages = append(passages, group.passage())
			group = cue
			continue
		}
		group.text += " " + cue.text
		if cue.endMS > group.endMS {
			group.end, group.endMS = cue.end, cue.endMS
		}
	}
	return append(passages, group.passage())
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
