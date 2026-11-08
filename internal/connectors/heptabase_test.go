package connectors

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sam-bretz/braindex/internal/library"
)

func TestHeptabaseWhiteboardSync(t *testing.T) {
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBrain(library.Brain{ID: "evals"}); err != nil {
		t.Fatal(err)
	}
	pdf, err := os.ReadFile("../ingest/testdata/two-pages.pdf")
	if err != nil {
		t.Fatal(err)
	}
	noteText := "Rubric version one"
	run := func(_ context.Context, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "whiteboard cards board-id":
			return []byte(`{"cards":[{"cardId":"note-id","cardType":"note"},{"cardId":"note-id","cardType":"note"},{"cardId":"pdf-id","cardType":"pdf"},{"cardId":"audio-id","cardType":"audio"},{"cardId":"trashed","availability":"trashed"}]}`), nil
		case "note read note-id":
			content, err := json.Marshal(heptaNode{Type: "doc", Content: []heptaNode{{Type: "paragraph", Content: []heptaNode{{Type: "text", Text: noteText}}}}})
			if err != nil {
				return nil, err
			}
			return json.Marshal(map[string]string{"id": "note-id", "title": "Rubric", "content": string(content)})
		case "file list --card-id pdf-id":
			return []byte(`{"files":[{"fileUri":"heptabase://pdfCard/pdf-id/file/file-id","purpose":"content","mimeType":"application/pdf","size":1024}]}`), nil
		}
		if len(args) == 5 && args[0] == "file" && args[1] == "export" {
			path := filepath.Join(args[4], "fixture.pdf")
			if err := os.WriteFile(path, pdf, 0600); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]string{"path": path})
		}
		t.Fatalf("unexpected CLI request: %v", args)
		return nil, nil
	}
	sync := func() []library.Source {
		t.Helper()
		var saved []library.Source
		err := syncHeptabase(context.Background(), run, HeptabaseScope{Whiteboard: "board-id"}, func(source library.Source) error {
			item, err := store.PutSource(source, []string{"evals"})
			saved = append(saved, item)
			return err
		})
		if err != nil || len(saved) != 3 {
			t.Fatalf("sync saved %d sources: %v", len(saved), err)
		}
		return saved
	}
	first, second := sync(), sync()
	if first[0].ID != second[0].ID || first[0].Revision != second[0].Revision || first[2].Status != "unavailable" ||
		!strings.Contains(first[1].Passages[0].Locator, "page 1") {
		t.Fatal("sync did not preserve identity, PDF citations, or unavailable status")
	}
	noteText = "Rubric version two"
	updated := sync()
	if first[0].ID != updated[0].ID || first[0].Revision == updated[0].Revision {
		t.Fatal("note edit did not produce a new revision for the same source")
	}
	brains, err := store.Brains()
	if err != nil || len(brains[0].Sources) != 3 {
		t.Fatalf("unexpected brain memberships: %+v, %v", brains, err)
	}
}
