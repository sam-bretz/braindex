package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
)

const heptaMaxResponse = 8 << 20

var heptaID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type HeptabaseScope struct {
	Tag, Whiteboard string
}

type heptaCard struct {
	ID           string `json:"id"`
	CardID       string `json:"cardId"`
	Type         string `json:"cardType"`
	Title        string `json:"title"`
	Created      string `json:"createdTime"`
	Availability string `json:"availability"`
}

type heptaRunner func(context.Context, ...string) ([]byte, error)

// SyncHeptabase reads only the selected collection through the desktop CLI.
func SyncHeptabase(ctx context.Context, scope HeptabaseScope, save func(library.Source) error) error {
	if _, err := exec.LookPath("heptabase"); err != nil {
		return errors.New("install the Heptabase CLI and enable it in Heptabase Settings → AI Features")
	}
	return syncHeptabase(ctx, runHeptabase, scope, save)
}

func runHeptabase(ctx context.Context, args ...string) ([]byte, error) {
	var stdout cappedHeptaOutput
	cmd := exec.CommandContext(ctx, "heptabase", args...)
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if stdout.overflow {
			return nil, errors.New("Heptabase CLI response exceeds 8 MiB")
		}
		return nil, fmt.Errorf("Heptabase %s %s failed; check the desktop app and CLI access", args[0], args[1])
	}
	return stdout.Bytes(), nil
}

type cappedHeptaOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *cappedHeptaOutput) Write(p []byte) (int, error) {
	if len(p) > heptaMaxResponse-b.Len() {
		b.overflow = true
		return 0, errors.New("Heptabase output limit exceeded")
	}
	return b.Buffer.Write(p)
}

func syncHeptabase(ctx context.Context, run heptaRunner, scope HeptabaseScope, save func(library.Source) error) error {
	if (scope.Tag == "") == (scope.Whiteboard == "") || save == nil {
		return errors.New("select exactly one Heptabase tag or whiteboard ID")
	}
	group, id := "tag", scope.Tag
	if scope.Whiteboard != "" {
		group, id = "whiteboard", scope.Whiteboard
	}
	if !heptaID.MatchString(id) {
		return errors.New("invalid Heptabase collection ID")
	}
	var list struct {
		Cards []heptaCard `json:"cards"`
	}
	if err := heptaRead(ctx, run, &list, group, "cards", id); err != nil {
		return err
	}
	if list.Cards == nil {
		return errors.New("Heptabase did not return a card list")
	}
	seen := map[string]bool{}
	var failures []error
	for _, card := range list.Cards {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		if card.CardID != "" {
			card.ID = card.CardID
		}
		if !heptaID.MatchString(card.ID) || seen[card.ID] || card.Availability == "deleted" || card.Availability == "trashed" {
			continue
		}
		seen[card.ID] = true
		source, err := heptaSource(ctx, run, card)
		if err == nil {
			err = save(source)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("Heptabase card %s: %w", card.ID, err))
		}
	}
	return errors.Join(failures...)
}

func heptaRead(ctx context.Context, run heptaRunner, dst any, args ...string) error {
	data, err := run(ctx, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("invalid Heptabase %s %s response", args[0], args[1])
	}
	return nil
}

func heptaSource(ctx context.Context, run heptaRunner, card heptaCard) (library.Source, error) {
	if card.Type == "" {
		var details struct {
			Type string `json:"cardType"`
		}
		if err := heptaRead(ctx, run, &details, "card", "properties", card.ID); err != nil {
			return library.Source{}, err
		}
		card.Type = details.Type
	}
	source := library.Source{ID: ingest.StableID("heptabase", card.ID), Title: card.Title, Date: card.Created, Status: "unavailable"}
	if source.Title == "" {
		source.Title = "Heptabase card " + card.ID
	}
	switch card.Type {
	case "note", "card":
		var note struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Content string `json:"content"`
		}
		if err := heptaRead(ctx, run, &note, "note", "read", card.ID); err != nil {
			return library.Source{}, err
		}
		if note.ID != card.ID {
			return library.Source{}, errors.New("Heptabase note ID does not match the requested card")
		}
		if note.Title != "" {
			source.Title = note.Title
		}
		var doc heptaNode
		if err := json.Unmarshal([]byte(note.Content), &doc); err != nil || doc.Type != "doc" {
			return library.Source{}, errors.New("Heptabase note returned an invalid document")
		}
		for i, block := range doc.Content {
			var text strings.Builder
			block.writeText(&text)
			if value := strings.TrimSpace(text.String()); value != "" {
				source.Passages = append(source.Passages, library.Passage{
					Text: value, Locator: fmt.Sprintf("Heptabase card %s, block %d", card.ID, i+1),
				})
			}
		}
	case "pdf", "pdfCard":
		passages, err := heptaPDF(ctx, run, card.ID)
		if err != nil {
			return library.Source{}, err
		}
		source.Passages = passages
	}
	source.Status = ingest.TextStatus(source.Passages)
	return source, nil
}

type heptaNode struct {
	Type    string      `json:"type"`
	Text    string      `json:"text"`
	Content []heptaNode `json:"content"`
}

func (n heptaNode) writeText(out *strings.Builder) {
	out.WriteString(n.Text)
	for _, child := range n.Content {
		child.writeText(out)
	}
	switch n.Type {
	case "hard_break", "paragraph", "heading", "code_block", "table_cell", "table_header":
		out.WriteByte('\n')
	}
}

func heptaPDF(ctx context.Context, run heptaRunner, id string) ([]library.Passage, error) {
	var listing struct {
		Files []struct {
			URI     string `json:"fileUri"`
			Purpose string `json:"purpose"`
			MIME    string `json:"mimeType"`
			Size    int64  `json:"size"`
		} `json:"files"`
	}
	if err := heptaRead(ctx, run, &listing, "file", "list", "--card-id", id); err != nil {
		return nil, err
	}
	for _, file := range listing.Files {
		if file.Purpose != "content" || file.MIME != "application/pdf" || file.Size > 64<<20 || !strings.HasPrefix(file.URI, "heptabase://") {
			continue
		}
		directory, err := os.MkdirTemp("", "braindex-heptabase-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(directory)
		var exported struct {
			Path string `json:"path"`
		}
		if err := heptaRead(ctx, run, &exported, "file", "export", file.URI, "--output-dir", directory); err != nil {
			return nil, fmt.Errorf("export local PDF (open it in Heptabase if it is not cached): %w", err)
		}
		resolved, err := filepath.EvalSymlinks(exported.Path)
		if err != nil {
			return nil, errors.New("Heptabase returned an unreadable PDF export path")
		}
		root, err := filepath.EvalSymlinks(directory)
		if err != nil || filepath.Dir(resolved) != root {
			return nil, errors.New("Heptabase PDF export path escaped the temporary directory")
		}
		source, err := ingest.Extract(resolved)
		if err != nil {
			return nil, fmt.Errorf("extract local Heptabase PDF: %w", err)
		}
		for i := range source.Passages {
			source.Passages[i].Locator = "Heptabase card " + id + ", " + source.Passages[i].Locator
		}
		return source.Passages, nil
	}
	return nil, nil
}
