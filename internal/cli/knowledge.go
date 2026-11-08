package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
	"github.com/sam-bretz/braindex/internal/retrieval"
)

type stringsFlag []string

func (s *stringsFlag) String() string { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("value cannot be empty")
	}
	*s = append(*s, value)
	return nil
}

func (c command) source(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: braindex source add|list|show|transcript")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		flags := newFlags("source add")
		var brains stringsFlag
		flags.Var(&brains, "brain", "destination brain ID; repeat to add multiple")
		redistribution := flags.String("redistribution", "", "mark the source as redistributable")
		pos, err := parse(flags, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 || len(brains) == 0 || (*redistribution != "" && *redistribution != "allowed") {
			return errors.New("usage: braindex source add PATH --brain ID [--redistribution allowed]")
		}
		source, err := ingest.Extract(pos[0])
		if err != nil {
			return err
		}
		source.Redistribution = *redistribution
		return c.saveSource(store, source, brains)
	case "list":
		if len(args) != 1 {
			return errors.New("usage: braindex source list")
		}
		sources, err := store.Sources()
		if err != nil {
			return err
		}
		for i := range sources {
			sources[i].Passages = nil
		}
		return c.json(sources)
	case "transcript":
		return c.transcript(store, args[1:])
	case "show":
		if len(args) != 2 && len(args) != 3 {
			return errors.New("usage: braindex source show ID [REVISION]")
		}
		var source library.Source
		if len(args) == 3 {
			source, err = store.SourceRevision(args[1], args[2])
		} else {
			source, err = store.Source(args[1])
		}
		if err != nil {
			return err
		}
		return c.json(source)
	default:
		return fmt.Errorf("unknown source command %q", args[0])
	}
}

func (c command) saveSource(store *library.Store, source library.Source, brains []string) error {
	saved, err := store.PutSource(source, brains)
	if err != nil {
		return err
	}
	saved.Passages = nil
	return c.json(saved)
}

func (c command) rule(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: braindex rule add|set|list|access")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "add", "set":
		flags := newFlags("rule " + args[0])
		var brains, phrases stringsFlag
		flags.Var(&brains, "brain", "brain ID")
		flags.Var(&phrases, "match", "matching phrase")
		pos, err := parse(flags, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 || len(brains) == 0 {
			return errors.New("usage: braindex rule set TOPIC --brain ID [--match PHRASE]")
		}
		return retrieval.SaveRule(store, retrieval.Rule{ID: pos[0], Phrases: phrases, Brains: brains})
	case "list":
		if len(args) != 1 {
			return errors.New("usage: braindex rule list")
		}
		rules, err := retrieval.Rules(store)
		if err != nil {
			return err
		}
		return c.json(rules)
	case "access":
		if len(args) != 3 || (args[2] != "allow" && args[2] != "deny") {
			return errors.New("usage: braindex rule access BRAIN allow|deny")
		}
		return retrieval.SetBrainAccess(store, args[1], args[2] == "allow")
	default:
		return fmt.Errorf("unknown rule command %q", args[0])
	}
}

func (c command) context(args []string) error {
	flags := newFlags("context")
	topic := flags.String("topic", "", "explicit topic ID")
	limit := flags.Int("limit", 0, "maximum excerpts per brain")
	flags.Bool("json", false, "return JSON")
	mode := flags.String("mode", "auto", "auto, keyword, or semantic")
	all := flags.Bool("all-brains", false, "search every brain allowed by your access rules")
	var brains stringsFlag
	flags.Var(&brains, "brain", "explicit brain ID")
	pos, err := parse(flags, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: braindex context QUESTION [--topic TOPIC] [--brain ID | --all-brains] [--mode MODE] [--json]")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	result, err := retrieval.Context(store, retrieval.Request{Query: pos[0], Topic: *topic, Brains: brains, Limit: *limit, AllBrains: *all, Mode: *mode})
	if err != nil {
		return err
	}
	return c.json(result)
}

func (c command) index(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: braindex index rebuild | embed --model MODEL")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "rebuild":
		if len(args) != 1 {
			return errors.New("usage: braindex index rebuild")
		}
		return index.Rebuild(store)
	case "embed":
		flags := newFlags("index embed")
		model := flags.String("model", "nomic-embed-text", "locally installed Ollama model")
		minimum := flags.Float64("min-similarity", 0.55, "minimum cosine similarity")
		pos, err := parse(flags, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 0 {
			return errors.New("usage: braindex index embed [--model MODEL] [--min-similarity SCORE]")
		}
		if err := index.Rebuild(store); err != nil {
			return err
		}
		return index.RebuildEmbeddings(context.Background(), store, *model, *minimum)
	default:
		return errors.New("usage: braindex index rebuild | embed --model MODEL")
	}
}
