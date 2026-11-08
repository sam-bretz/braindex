package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"unicode"

	"github.com/sam-bretz/braindex/internal/library"
)

var buildVersion = ""

const usage = `Usage: braindex [--library PATH] COMMAND

Commands:
  version                           Show the build version
  status                            Check library and search index health
  init [PATH]                       Initialize a library
  brain create ID [--name NAME]      Create a brain
  brain list                        List brains
  source add|list|show|transcript   Manage indexed sources
  source transcribe SOURCE_ID...   Transcribe podcast episodes locally
  source youtube-captions ID...    Fetch selected YouTube captions
  discover web|rss|podcast|youtube  Discover and import sources
  discover person NAME --brain ID   Review public work by a person
  link URL                          Preview and route a source link
  follow add|list|refresh|process|save  Track and process followed sources
  gmail login|labels               Connect Gmail and find labels
  sync gmail|heptabase              Sync a selected external collection
  rule set|list|access              Manage topic routing and access
  backup status                     Report missing or external originals
  index rebuild|embed               Rebuild keyword and optional local embedding indexes
  context QUESTION [--explain]      Retrieve cited passages
  capture note|file|url VALUE       Save material for later review
  inbox review|handoff|proposals|batch  Review and place captured material
  package export|validate|install|preview|compare|upgrade  Manage brain packages
  catalog list|install              Discover pinned hosted packages
  mcp serve                         Serve read-only context over MCP
  hook codex                        Handle a Codex prompt hook

The library defaults to BRAINDEX_LIBRARY or the working directory.
`

type command struct {
	root string
	in   io.Reader
	out  io.Writer
}

func Run(args []string, in io.Reader, out, errOut io.Writer) int {
	root := os.Getenv("BRAINDEX_LIBRARY")
	if root == "" {
		root = "."
	}
	if len(args) > 0 && args[0] == "--library" {
		if len(args) < 2 || args[1] == "" {
			fmt.Fprintln(errOut, "braindex: --library requires a path")
			return 2
		}
		root, args = args[1], args[2:]
	}
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		version := buildVersion
		if version == "" {
			if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
				version = info.Main.Version
			} else {
				version = "development"
			}
		}
		fmt.Fprintln(out, "braindex", version)
		return 0
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, usage)
		return 0
	}
	c := command{root: root, in: in, out: out}
	if err := c.run(args[0], args[1:]); err != nil {
		fmt.Fprintln(errOut, "braindex:", err)
		return 1
	}
	return 0
}

func (c command) run(name string, args []string) error {
	switch name {
	case "init":
		root := c.root
		switch len(args) {
		case 0:
		case 1:
			root = args[0]
		default:
			return errors.New("usage: braindex init [PATH]")
		}
		store, err := library.Init(root)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.out, store.Root)
		return nil
	case "brain":
		return c.brain(args)
	case "source":
		return c.source(args)
	case "discover":
		return c.discover(args)
	case "link":
		return c.link(args)
	case "follow":
		return c.follow(args)
	case "sync":
		return c.sync(args)
	case "gmail":
		return c.gmail(args)
	case "rule":
		return c.rule(args)
	case "index":
		return c.index(args)
	case "status":
		return c.status(args)
	case "backup":
		return c.backup(args)
	case "context":
		return c.context(args)
	case "capture":
		return c.capture(args)
	case "inbox":
		return c.inbox(args)
	case "package":
		return c.brainPackage(args)
	case "catalog":
		return c.catalog(args)
	case "mcp":
		return c.mcp(args)
	case "hook":
		return c.hook(args)
	default:
		return fmt.Errorf("unknown command %q; run braindex help", name)
	}
}

func (c command) brain(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: braindex brain create|list")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		flags := newFlags("brain create")
		name := flags.String("name", "", "display name")
		kind := flags.String("kind", "", "optional label")
		pos, err := parse(flags, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("usage: braindex brain create ID [--name NAME] [--kind LABEL]")
		}
		if *name == "" {
			*name = pos[0]
		}
		if err := store.CreateBrain(library.Brain{ID: pos[0], Name: *name, Kind: *kind}); err != nil {
			return err
		}
		fmt.Fprintln(c.out, pos[0])
		return nil
	case "list":
		if len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
			return errors.New("usage: braindex brain list [--json]")
		}
		brains, err := store.Brains()
		if err != nil {
			return err
		}
		if len(args) == 2 {
			return c.json(brains)
		}
		for _, brain := range brains {
			fmt.Fprintf(c.out, "%s\t%s\n", brain.ID, strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return ' '
				}
				return r
			}, brain.Name))
		}
		return nil
	default:
		return fmt.Errorf("unknown brain command %q", args[0])
	}
}

func (c command) json(value any) error {
	encoder := json.NewEncoder(c.out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func newFlags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

// parse lets users place named options before or after positional arguments.
func parse(flags *flag.FlagSet, args []string) ([]string, error) {
	var options, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		option := flags.Lookup(name)
		if option == nil {
			return nil, fmt.Errorf("unknown option %q", arg)
		}
		options = append(options, arg)
		boolean, ok := option.Value.(interface{ IsBoolFlag() bool })
		if !hasValue && !(ok && boolean.IsBoolFlag()) {
			i++
			if i == len(args) {
				return nil, fmt.Errorf("%s requires a value", arg)
			}
			options = append(options, args[i])
		}
	}
	if err := flags.Parse(options); err != nil {
		return nil, err
	}
	return positional, nil
}
