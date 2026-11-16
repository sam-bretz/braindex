package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sam-bretz/braindex/internal/connectors"
	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
)

const personUsage = "usage: braindex discover person NAME --brain ID [--hint TOPIC] [--seed URL] [--results FILE] [--json | --select 1,3] [--import-articles]"

func (c command) discoverPerson(args []string) error {
	flags := newFlags("discover person")
	brain := flags.String("brain", "", "destination brain ID")
	hint := flags.String("hint", "", "public topic, role, or affiliation to distinguish people with the same name")
	seed := flags.String("seed", "", "known article URL to include in review")
	results := flags.String("results", "", "review candidates from a local JSON file")
	asJSON := flags.Bool("json", false, "preview candidates as JSON")
	selection := flags.String("select", "", "comma-separated candidate numbers to save")
	importArticles := flags.Bool("import-articles", false, "fetch text from selected articles")
	pos, err := parse(flags, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *brain == "" || (*asJSON && *selection != "") {
		return errors.New(personUsage)
	}
	if *selection != "" && *results == "" {
		return errors.New("--select requires a saved --results file; review live results interactively")
	}
	name := strings.TrimSpace(pos[0])
	if name == "" || len(name)+len(*hint) > 400 || len(strings.Fields(name+" "+*hint)) > 60 {
		return errors.New("provide a name and an optional short, public hint")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	if err := requireBrains(store, []string{*brain}); err != nil {
		return err
	}
	var candidates []connectors.PersonCandidate
	if *seed != "" {
		candidates = connectors.PersonCandidates([]connectors.PersonCandidate{{Kind: "article", Title: "Starting article", URL: *seed}})
		if len(candidates) == 0 {
			return errors.New("--seed requires an HTTP(S) URL without credentials")
		}
	}
	var found []connectors.PersonCandidate
	if *results != "" {
		found, err = readPersonResults(*results)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		found, err = connectors.SearchPerson(ctx, name, *hint, os.Getenv("BRAVE_SEARCH_API_KEY"))
	}
	if err != nil {
		return err
	}
	candidates = connectors.PersonCandidates(append(candidates, found...))
	if *asJSON {
		return c.json(candidates)
	}
	if len(candidates) == 0 {
		_, err := fmt.Fprintln(c.out, "No candidates found. Try a different public hint.")
		return err
	}

	fmt.Fprintf(c.out, "Review %d candidate links for brain %s. Verify identity before saving.\n", len(candidates), *brain)
	if *importArticles {
		fmt.Fprintln(c.out, "Selected articles will be fetched. Other selections will be saved as references.")
	} else {
		fmt.Fprintln(c.out, "Selections will be saved as references.")
	}
	for i, candidate := range candidates {
		fmt.Fprintf(c.out, "%2d. [%s] %s\n    %s\n", i+1, candidate.Kind, candidate.Title, candidate.URL)
	}
	if *selection == "" && c.in != nil {
		fmt.Fprint(c.out, "Select numbers (for example 1,3), or press Enter to skip: ")
		input := bufio.NewScanner(c.in)
		input.Buffer(make([]byte, 256), 4096)
		if input.Scan() {
			*selection = input.Text()
		}
		if err := input.Err(); err != nil {
			return err
		}
	}
	selected, err := personSelections(*selection, len(candidates))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, number := range selected {
		candidate := candidates[number-1]
		source, err := c.savePersonCandidate(ctx, store, *brain, candidate, *importArticles)
		if err != nil {
			return fmt.Errorf("save candidate %d: %w", number, err)
		}
		status := "reference only"
		if source.Status == "ready" {
			status = "text available"
		}
		fmt.Fprintf(c.out, "Saved %d to %s (%s, %s).\n", number, *brain, source.ID, status)
	}
	if len(selected) != 0 {
		fmt.Fprintln(c.out, "Use discover web to import article text, or attach an available transcript to an episode. Rebuild the index after adding searchable text.")
	}
	return nil
}

func (c command) savePersonCandidate(ctx context.Context, store *library.Store, brain string, candidate connectors.PersonCandidate, importArticle bool) (library.Source, error) {
	reference := library.Source{Title: candidate.Title, URL: candidate.URL}
	if candidate.Kind == "article" {
		reference.ID = ingest.StableID("web", candidate.URL)
		if importArticle {
			source, err := connectors.FetchPublicWeb(ctx, candidate.URL)
			if err == nil && source.Status == "ready" {
				return store.PutSource(source, []string{brain})
			}
			fmt.Fprintf(c.out, "Article text unavailable for %s; saving the link.\n", candidate.URL)
		}
	}
	return store.PutReference(reference, []string{brain})
}

func readPersonResults(path string) ([]connectors.PersonCandidate, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("--results requires a regular JSON file")
	}
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("--results exceeds 1 MiB")
	}
	var candidates []connectors.PersonCandidate
	if err := json.Unmarshal(data, &candidates); err != nil {
		return nil, fmt.Errorf("read person results: %w", err)
	}
	if len(candidates) > 100 {
		return nil, errors.New("--results accepts up to 100 candidates")
	}
	return candidates, nil
}

func personSelections(input string, count int) ([]int, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}
	var selections []int
	seen := make(map[int]bool)
	for _, part := range strings.Split(input, ",") {
		number, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || number < 1 || number > count {
			return nil, fmt.Errorf("choose comma-separated candidate numbers from 1 to %d", count)
		}
		if !seen[number] {
			seen[number] = true
			selections = append(selections, number)
		}
	}
	return selections, nil
}
