package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
)

type brainHealth struct {
	ID          string `json:"id"`
	Sources     int    `json:"sources"`
	Searchable  int    `json:"searchable_sources"`
	Unavailable int    `json:"unavailable_sources"`
}

type libraryHealth struct {
	Library       string          `json:"library"`
	State         string          `json:"state"`
	Next          string          `json:"next,omitempty"`
	Brains        []brainHealth   `json:"brains"`
	Keyword       index.Health    `json:"keyword"`
	Embeddings    index.Health    `json:"embeddings"`
	OptionalTools map[string]bool `json:"optional_tools"`
}

func (c command) status(args []string) error {
	flags := newFlags("status")
	asJSON := flags.Bool("json", false, "show structured diagnostics")
	probe := flags.Bool("check-runtime", false, "check the configured embedding model over loopback")
	pos, err := parse(flags, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return errors.New("usage: braindex status [--json] [--check-runtime]")
	}
	root, err := filepath.Abs(c.root)
	if err != nil {
		return err
	}
	report := libraryHealth{Library: root, State: "uninitialized", Next: "braindex init",
		Brains: []brainHealth{}, Keyword: index.Health{State: "unavailable"}, Embeddings: index.Health{State: "unavailable"},
		OptionalTools: map[string]bool{}}
	for _, tool := range []string{"git", "heptabase", "ollama", "tesseract"} {
		_, err := exec.LookPath(tool)
		report.OptionalTools[tool] = err == nil
	}
	store, err := library.Open(root)
	if errors.Is(err, os.ErrNotExist) {
		return c.writeHealth(report, *asJSON)
	}
	if err != nil {
		return err
	}
	report.State, report.Next = "initialized", ""
	snapshot, err := store.Snapshot()
	if err != nil {
		return err
	}
	searchable := map[string]bool{}
	for _, source := range snapshot.Sources {
		for _, passage := range source.Passages {
			if strings.TrimSpace(passage.Text) != "" {
				searchable[source.ID] = true
				break
			}
		}
	}
	for _, brain := range snapshot.Brains {
		health := brainHealth{ID: brain.ID, Sources: len(brain.Sources)}
		for _, id := range brain.Sources {
			if searchable[id] {
				health.Searchable++
			} else {
				health.Unavailable++
			}
		}
		report.Brains = append(report.Brains, health)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	report.Keyword = index.InspectKeyword(store, snapshot.Sources)
	report.Embeddings = index.InspectEmbeddings(ctx, store, snapshot.Sources, *probe)
	return c.writeHealth(report, *asJSON)
}

func (c command) writeHealth(report libraryHealth, asJSON bool) error {
	if asJSON {
		return c.json(report)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Library: %s (%s)\n", report.Library, report.State)
	if report.Next != "" {
		fmt.Fprintf(&out, "Next: %s\n", report.Next)
	} else {
		fmt.Fprintf(&out, "Brains: %d\n", len(report.Brains))
		for _, brain := range report.Brains {
			fmt.Fprintf(&out, "  %s: %d searchable, %d unavailable\n", brain.ID, brain.Searchable, brain.Unavailable)
		}
		printIndexHealth(&out, "Keyword index", report.Keyword)
		printIndexHealth(&out, "Embeddings", report.Embeddings)
	}
	fmt.Fprint(&out, "Optional tools:")
	for _, tool := range []string{"git", "heptabase", "ollama", "tesseract"} {
		label := "not found"
		if report.OptionalTools[tool] {
			label = "found"
		}
		fmt.Fprintf(&out, " %s=%s;", tool, label)
	}
	out.WriteByte('\n')
	_, err := fmt.Fprint(c.out, out.String())
	return err
}

func printIndexHealth(out *strings.Builder, name string, report index.Health) {
	fmt.Fprintf(out, "%s: %s", name, report.State)
	if report.Model != "" {
		fmt.Fprintf(out, " (%s)", report.Model)
	}
	if report.Runtime != "" {
		fmt.Fprintf(out, " [runtime: %s]", report.Runtime)
	}
	if report.Next != "" {
		fmt.Fprintf(out, " — %s", report.Next)
	}
	out.WriteByte('\n')
}
