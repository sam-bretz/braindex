package retrieval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/sam-bretz/braindex/internal/library"
)

type Rule struct {
	ID      string   `json:"id"`
	Phrases []string `json:"phrases"`
	Brains  []string `json:"brains"`
}

type brainAccess struct {
	Allowed *bool `json:"allowed"`
}

func SaveRule(store *library.Store, rule Rule) error {
	if library.ValidateID(rule.ID) != nil {
		return fmt.Errorf("invalid topic ID %q", rule.ID)
	}
	if len(rule.Brains) == 0 {
		return errors.New("a topic rule requires at least one brain")
	}
	brains, err := store.Brains()
	if err != nil {
		return err
	}
	for _, id := range rule.Brains {
		if !hasBrain(brains, id) {
			return fmt.Errorf("unknown brain %q", id)
		}
	}
	for _, phrase := range rule.Phrases {
		if normalize(phrase) == "" {
			return fmt.Errorf("invalid empty topic phrase %q", phrase)
		}
	}
	rule.Brains = unique(rule.Brains)
	rule.Phrases = unique(rule.Phrases)
	return writeJSON(configPath(store, "rules", rule.ID), rule)
}

func Rules(store *library.Store) ([]Rule, error) {
	rules := []Rule{}
	dir := filepath.Join(store.MetaDir(), "rules")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return rules, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var rule Rule
		path := filepath.Join(dir, entry.Name())
		if err := readJSON(path, &rule); err != nil {
			return nil, err
		}
		if library.ValidateID(rule.ID) != nil || rule.ID+".json" != entry.Name() || len(rule.Brains) == 0 {
			return nil, fmt.Errorf("invalid topic rule in %s", entry.Name())
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func SetBrainAccess(store *library.Store, brainID string, allowed bool) error {
	if library.ValidateID(brainID) != nil {
		return fmt.Errorf("invalid brain ID %q", brainID)
	}
	brains, err := store.Brains()
	if err != nil {
		return err
	}
	if !hasBrain(brains, brainID) {
		return fmt.Errorf("unknown brain %q", brainID)
	}
	return writeJSON(configPath(store, "access", brainID), brainAccess{Allowed: &allowed})
}

func allowed(store *library.Store, brainID string) (bool, error) {
	if library.ValidateID(brainID) != nil {
		return false, fmt.Errorf("invalid brain ID %q", brainID)
	}
	var access brainAccess
	err := readJSON(configPath(store, "access", brainID), &access)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if access.Allowed == nil {
		return false, fmt.Errorf("brain %q access policy must specify allowed", brainID)
	}
	return *access.Allowed, nil
}

func selectBrains(rules []Rule, request Request) ([]string, []string, error) {
	topics := []string{}
	brains := append([]string{}, request.Brains...)
	if request.Topic != "" {
		for _, rule := range rules {
			if rule.ID == request.Topic {
				return []string{rule.ID}, unique(append(brains, rule.Brains...)), nil
			}
		}
		return nil, nil, fmt.Errorf("unknown topic %q", request.Topic)
	}
	if len(brains) > 0 {
		return topics, unique(brains), nil
	}
	query := " " + normalize(request.Query) + " "
	for _, rule := range rules {
		for _, phrase := range rule.Phrases {
			phrase = normalize(phrase)
			if phrase != "" && strings.Contains(query, " "+phrase+" ") {
				topics = append(topics, rule.ID)
				brains = append(brains, rule.Brains...)
				break
			}
		}
	}
	return topics, unique(brains), nil
}

func normalize(s string) string {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
	})
	return strings.Join(words, " ")
}

func unique(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func hasBrain(brains []library.Brain, id string) bool {
	return slices.ContainsFunc(brains, func(brain library.Brain) bool { return brain.ID == id })
}

func configPath(store *library.Store, kind, id string) string {
	return filepath.Join(store.MetaDir(), kind, id+".json")
}

func readJSON(filename string, target any) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(filename), err)
	}
	return nil
}

func writeJSON(filename string, value any) error {
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".write-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(append(data, '\n'))
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), filename)
}
