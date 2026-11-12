package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/sam-bretz/braindex/internal/inbox"
	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
	"github.com/sam-bretz/braindex/internal/retrieval"
)

const (
	maxSuggestionItems    = 20
	maxSuggestionBrains   = 100
	maxSuggestionNote     = 4096
	maxSuggestionExamples = 2
	maxSuggestionSources  = 12
	minimumSuggestion     = 0.58
	minimumSuggestionGap  = 0.07
)

type inboxSuggestion struct {
	ItemID        string   `json:"item_id"`
	Status        string   `json:"status"`
	Reason        string   `json:"reason"`
	BrainID       string   `json:"brain_id,omitempty"`
	Score         *float64 `json:"score,omitempty"`
	RunnerUpScore *float64 `json:"runner_up_score,omitempty"`
	Truncated     bool     `json:"truncated,omitempty"`
}

type inboxSuggestionReport struct {
	Model       string            `json:"model"`
	Suggestions []inboxSuggestion `json:"suggestions"`
}

type inboxBrainProfile struct {
	id, text string
}

func (c command) inboxSuggest(store *library.Store, args []string) error {
	flags := newFlags("inbox suggest")
	model := flags.String("model", "", "locally installed Ollama embedding model")
	ids, err := parse(flags, args)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(*model)
	if name == "" || len(ids) == 0 || len(ids) > maxSuggestionItems {
		return errors.New("usage: braindex inbox suggest ID... --model LOCAL_MODEL (up to 20 notes)")
	}
	if err := validateInboxIDs(ids); err != nil {
		return err
	}

	report := inboxSuggestionReport{Model: name, Suggestions: make([]inboxSuggestion, len(ids))}
	var queries []string
	var selected []int
	for i, id := range ids {
		suggestion := inboxSuggestion{ItemID: id, Status: "abstained"}
		item, err := inbox.Get(store, id)
		switch {
		case err != nil:
			suggestion.Reason = "item_unavailable"
		case !proposableInboxItem(item):
			suggestion.Reason = "resolved_item"
		case item.Kind != "note":
			suggestion.Reason = "unsupported_kind"
		default:
			text, truncated, err := suggestionNote(store, item)
			suggestion.Truncated = truncated
			switch {
			case err != nil:
				suggestion.Reason = "item_unavailable"
			case text == "":
				suggestion.Reason = "empty_note"
			default:
				selected = append(selected, i)
				queries = append(queries, text)
			}
		}
		report.Suggestions[i] = suggestion
	}
	if len(selected) == 0 {
		return c.json(report)
	}
	profiles, err := suggestionProfiles(store)
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		for _, i := range selected {
			report.Suggestions[i].Reason = "no_allowed_brains"
		}
		return c.json(report)
	}
	documents := make([]string, len(profiles))
	for i, profile := range profiles {
		documents[i] = profile.text
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	scores, err := index.LocalSimilarityScores(ctx, name, queries, documents)
	if err != nil {
		return fmt.Errorf("compute local inbox suggestions: %w", err)
	}
	for i, row := range scores {
		scoreInboxSuggestion(&report.Suggestions[selected[i]], profiles, row)
	}
	return c.json(report)
}

func scoreInboxSuggestion(suggestion *inboxSuggestion, profiles []inboxBrainProfile, scores []float64) {
	best, second := 0, -1
	for i := 1; i < len(scores); i++ {
		if scores[i] > scores[best] {
			best, second = i, best
		} else if second == -1 || scores[i] > scores[second] {
			second = i
		}
	}
	suggestion.Score = &scores[best]
	if second != -1 {
		suggestion.RunnerUpScore = &scores[second]
	}
	switch {
	case scores[best] < minimumSuggestion:
		suggestion.Reason = "weak_match"
	case second != -1 && scores[best]-scores[second] < minimumSuggestionGap:
		suggestion.Reason = "ambiguous_match"
	default:
		suggestion.Status, suggestion.Reason, suggestion.BrainID = "suggested", "best_match", profiles[best].id
	}
}

func suggestionProfiles(store *library.Store) ([]inboxBrainProfile, error) {
	brains, err := store.Brains()
	if err != nil {
		return nil, err
	}
	var allowed []library.Brain
	privateSources := make(map[string]bool)
	for _, brain := range brains {
		canUse, err := retrieval.BrainAllowed(store, brain.ID)
		if err != nil {
			return nil, err
		}
		if canUse {
			allowed = append(allowed, brain)
		} else {
			for _, id := range brain.Sources {
				privateSources[id] = true
			}
		}
	}
	if len(allowed) > maxSuggestionBrains {
		return nil, errors.New("inbox suggestion supports up to 100 allowed brains")
	}
	profiles := make([]inboxBrainProfile, 0, len(allowed))
	for _, brain := range allowed {
		parts := []string{suggestionExcerpt(brain.Name, 160), strings.ReplaceAll(brain.ID, "-", " ")}
		var examples int
		for _, id := range brain.Sources[:min(len(brain.Sources), maxSuggestionSources)] {
			if privateSources[id] {
				continue
			}
			source, err := store.Source(id)
			if err != nil {
				return nil, err
			}
			if source.Status != "ready" || len(source.Passages) == 0 {
				continue
			}
			example := suggestionExcerpt(source.Passages[0].Text, 360)
			if example == "" {
				continue
			}
			parts = append(parts, example)
			examples++
			if examples == maxSuggestionExamples {
				break
			}
		}
		profiles = append(profiles, inboxBrainProfile{id: brain.ID, text: strings.Join(parts, "\n")})
	}
	return profiles, nil
}

func suggestionExcerpt(value string, limit int) string {
	count := 0
	for offset := range value {
		if count == limit {
			value = value[:offset]
			break
		}
		count++
	}
	return strings.Join(strings.Fields(value), " ")
}

// Read only the regular captured note. An item's Original may be a URL or an
// arbitrary local file name, so it is never opened or added to model input.
func suggestionNote(store *library.Store, item inbox.Item) (string, bool, error) {
	root, err := os.OpenRoot(store.MetaDir())
	if err != nil {
		return "", false, err
	}
	defer root.Close()
	captures, err := suggestionDirectory(root, "inbox")
	if err != nil {
		return "", false, err
	}
	defer captures.Close()
	selected, err := suggestionDirectory(captures, item.ID)
	if err != nil {
		return "", false, err
	}
	defer selected.Close()

	name := filepath.Base(inbox.ContentPath(store, item))
	info, err := selected.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return "", false, errors.New("selected capture is not a regular note")
	}
	file, err := selected.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil || !os.SameFile(info, current) {
		return "", false, errors.New("selected capture changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSuggestionNote+1))
	if err != nil {
		return "", false, err
	}
	truncated := len(data) > maxSuggestionNote
	if truncated {
		data = data[:maxSuggestionNote]
		for len(data) > 0 && !utf8.Valid(data) && len(data) > maxSuggestionNote-utf8.UTFMax {
			data = data[:len(data)-1]
		}
	}
	if !utf8.Valid(data) {
		return "", truncated, errors.New("note is not valid UTF-8")
	}
	return strings.TrimSpace(string(data)), truncated, nil
}

func suggestionDirectory(root *os.Root, name string) (*os.Root, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.IsDir() {
		return nil, errors.New("capture directory must be a regular directory")
	}
	sub, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	current, err := sub.Stat(".")
	if err != nil || !os.SameFile(info, current) {
		sub.Close()
		return nil, errors.New("capture directory changed while opening")
	}
	return sub, nil
}
