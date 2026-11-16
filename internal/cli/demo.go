package cli

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
	"github.com/sam-bretz/braindex/internal/retrieval"
)

const demoQuestion = "How should I run founder-led sales discovery when budget tightening is slowing purchases?"

func (c command) demo(args []string) (err error) {
	if len(args) != 0 {
		return errors.New("usage: braindex demo")
	}

	root, err := os.MkdirTemp("", "braindex-demo-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(root)) }()
	store, err := library.Init(root)
	if err != nil {
		return err
	}

	samples := []struct {
		brain, rule, phrase, title, text string
	}{
		{"buying-climate", "budgets", "budget tightening", "Buying Climate Brief",
			"When budget tightening slows purchases, ask who can fund a small pilot this quarter."},
		{"field-notes", "discovery", "founder-led sales discovery", "Discovery Field Notes",
			"For founder-led sales discovery, open with the last real incident and ask what workaround the buyer used."},
	}
	for _, sample := range samples {
		if err := store.CreateBrain(library.Brain{ID: sample.brain, Name: sample.title}); err != nil {
			return err
		}
		source := library.Source{
			ID: sample.brain + "-demo", Title: sample.title, Status: "ready",
			Passages: ingest.LinePassages(sample.text),
		}
		if _, err := store.PutSource(source, []string{sample.brain}); err != nil {
			return err
		}
		rule := retrieval.Rule{ID: sample.rule, Phrases: []string{sample.phrase}, Brains: []string{sample.brain}}
		if err := retrieval.SaveRule(store, rule); err != nil {
			return err
		}
	}
	if err := index.Rebuild(store); err != nil {
		return err
	}
	result, err := retrieval.Context(store, retrieval.Request{Query: demoQuestion, Mode: "keyword"})
	if err != nil {
		return err
	}
	if result.Routing.Scope != "matched_rules" || len(result.Brains) != len(samples) {
		return errors.New("demo question did not select both brains")
	}

	var output strings.Builder
	fmt.Fprintln(&output, "Braindex demo with synthetic sources")
	fmt.Fprintf(&output, "\nQuestion: %s\n", demoQuestion)
	fmt.Fprintf(&output, "Topics matched: %s\n", strings.Join(result.Topics, ", "))
	for _, brain := range result.Brains {
		if len(brain.Excerpts) != 1 {
			return fmt.Errorf("demo expected one citation from brain %s", brain.ID)
		}
		citation := brain.Excerpts[0]
		fmt.Fprintf(&output, "\n[%s] %s, %s\n  %s\n", brain.ID, citation.Title, citation.Locator, citation.Text)
	}

	citation := result.Brains[0].Excerpts[0]
	source, err := store.SourceRevision(citation.SourceID, citation.Revision)
	if err != nil {
		return err
	}
	passage := slices.IndexFunc(source.Passages, func(p library.Passage) bool {
		return p.Locator == citation.Locator && p.Text == citation.Text
	})
	if passage < 0 {
		return errors.New("demo citation does not match the reopened revision")
	}
	fmt.Fprintf(&output, "\nReopened source: %s, %s\n  %s\n", source.Title, source.Passages[passage].Locator, source.Passages[passage].Text)
	_, err = fmt.Fprint(c.out, output.String())
	return err
}
