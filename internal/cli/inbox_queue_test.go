package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sam-bretz/braindex/internal/inbox"
	"github.com/sam-bretz/braindex/internal/index"
	"github.com/sam-bretz/braindex/internal/library"
	"github.com/sam-bretz/braindex/internal/retrieval"
)

func TestInboxInteractiveReviewPersistsOnlyConfirmedDecisions(t *testing.T) {
	store := inboxFixture(t)
	approved := captureInboxFixture(t, store, "note", "alphaqualifier: original research")
	moved := captureInboxFixture(t, store, "note", "betareview: optional input")
	dismissed := captureInboxFixture(t, store, "note", "gammaignore")
	pending := captureInboxFixture(t, store, "note", "deltaunresolved \x1b[31m")
	proposeInboxFixture(t, store, approved.ID, "quality alpha", "direct evidence", "sales")
	var transcript bytes.Buffer
	input := strings.NewReader("approve\nyes\nmove product\nno\nmove product\nyes\ndismiss\nyes\nquit\n")
	if err := reviewInboxQueue(store, input, &transcript, nil); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"alphaqualifier", "quality alpha", "direct evidence", "Item left unchanged.", "deltaunresolved"} {
		if !strings.Contains(transcript.String(), expected) {
			t.Fatalf("missing %q in terminal output: %s", expected, &transcript)
		}
	}
	if strings.Contains(transcript.String(), "\x1b") {
		t.Fatal("terminal output contains unescaped controls")
	}
	checkInboxStatus(t, store, approved.ID, "approved", "sales")
	checkInboxStatus(t, store, moved.ID, "approved", "product")
	checkInboxStatus(t, store, dismissed.ID, "dismissed")
	checkInboxStatus(t, store, pending.ID, "pending")

	transcript.Reset()
	if err := reviewInboxQueue(store, strings.NewReader("defer\n"), &transcript, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(transcript.String(), "1 staged item(s).") {
		t.Fatalf("quit failed to preserve the unresolved item: %s", &transcript)
	}
	checkInboxStatus(t, store, pending.ID, "pending")
	checkInboxSearch(t, store, "sales", "alphaqualifier", true)
	checkInboxSearch(t, store, "product", "betareview", true)
	checkInboxSearch(t, store, "sales", "gammaignore", false)
	checkInboxSearch(t, store, "product", "deltaunresolved", false)
}

func TestInboxSelectedHandoffAndPartialAgentProposals(t *testing.T) {
	store := inboxFixture(t)
	good := captureInboxFixture(t, store, "note", "selected private words")
	invalid := captureInboxFixture(t, store, "url", "https://example.org/review-this")
	missing := captureInboxFixture(t, store, "note", "no agent response")
	duplicate := captureInboxFixture(t, store, "note", "same agent response twice")
	unselected := captureInboxFixture(t, store, "note", "unselected private words")
	leaveOut := captureInboxFixture(t, store, "note", "agent may suggest omitting")
	newBrain := captureInboxFixture(t, store, "note", "agent may suggest a new brain")
	metadata := runInboxFixture(t, store, nil, "handoff", good.ID, invalid.ID)
	if strings.Contains(metadata, "private words") || strings.Contains(metadata, "https://example.org") ||
		!strings.Contains(metadata, `"content_included": false`) {
		t.Fatalf("unexpected metadata-only handoff: %s", metadata)
	}
	content := runInboxFixture(t, store, nil, "handoff", "--include-content", good.ID, invalid.ID)
	if !strings.Contains(content, "selected private words") || !strings.Contains(content, "https://example.org/review-this") ||
		strings.Contains(content, unselected.ID) || strings.Contains(content, "unselected private words") {
		t.Fatalf("unexpected content handoff: %s", content)
	}
	response, err := json.Marshal([]any{
		inbox.Proposal{ReviewedID: good.ID, Digest: "fixture digest", Rationale: "fixture rationale", Brains: []string{"sales"}},
		map[string]any{"reviewed_item_id": invalid.ID, "digest": 4},
		inbox.Proposal{ReviewedID: duplicate.ID, Digest: "d", Rationale: "r", LeaveOut: true},
		inbox.Proposal{ReviewedID: duplicate.ID, Digest: "d", Rationale: "r", LeaveOut: true},
		inbox.Proposal{ReviewedID: unselected.ID, Digest: "d", Rationale: "r", LeaveOut: true},
		inbox.Proposal{ReviewedID: leaveOut.ID, Digest: "discard candidate", Rationale: "outside research scope", LeaveOut: true},
		inbox.Proposal{ReviewedID: newBrain.ID, Digest: "topic", Rationale: "new area of study", NewBrain: "reading"},
	})
	if err != nil {
		t.Fatal(err)
	}
	output := runInboxFixture(t, store, bytes.NewReader(response), "proposals", good.ID, invalid.ID, missing.ID, duplicate.ID, leaveOut.ID, newBrain.ID)
	var results []inboxProposalResult
	if err := json.Unmarshal([]byte(output), &results); err != nil {
		t.Fatal(err)
	}
	status := make(map[string]string)
	for _, result := range results {
		status[result.ItemID] = result.Status
	}
	if status[good.ID] != "reviewed" || status[invalid.ID] != "failed" || status[missing.ID] != "missing" ||
		status[duplicate.ID] != "failed" || status[unselected.ID] != "failed" || status[leaveOut.ID] != "reviewed" ||
		status[newBrain.ID] != "reviewed" {
		t.Fatalf("unexpected proposal results: %+v", results)
	}
	reviewed := checkInboxStatus(t, store, good.ID, "reviewed")
	if reviewed.Proposal == nil || reviewed.Proposal.ReviewedID != good.ID || reviewed.Proposal.GeneratedBy != "agent" {
		t.Fatalf("proposal provenance missing: %+v", reviewed)
	}
	checkInboxStatus(t, store, unselected.ID, "pending")
	if sources, err := store.Sources(); err != nil || len(sources) != 0 {
		t.Fatalf("proposals created sources: %+v, %v", sources, err)
	}
	if brains, err := store.Brains(); err != nil || len(brains) != 2 {
		t.Fatalf("proposals created brains: %+v, %v", brains, err)
	}
}

func TestInboxMixedBatchCanResumeInterruptedApproval(t *testing.T) {
	store := inboxFixture(t)
	approved := captureInboxFixture(t, store, "note", "alphaqualifier")
	moved := captureInboxFixture(t, store, "note", "betareview")
	dismissed := captureInboxFixture(t, store, "note", "gammaignore")
	failed := captureInboxFixture(t, store, "note", "deltaunresolved")
	resume := captureInboxFixture(t, store, "note", "epsilonresume")
	proposeInboxFixture(t, store, approved.ID, "d", "r", "sales")
	proposeInboxFixture(t, store, moved.ID, "d", "r", "sales")
	interruption := errors.New("interrupted after import")
	// Simulate a process dying between publishing source contents and saving
	// its inbox receipt. Retrying must reuse the source and the new brain.
	_, err := inbox.ApprovePlacement(store, resume.ID, []string{"resume"}, "resume", func(item inbox.Item) (string, error) {
		if err := store.CreateBrain(library.Brain{ID: "resume"}); err != nil {
			return "", err
		}
		source, err := captureSource(store, item)
		if err != nil {
			return "", err
		}
		if _, err := store.PutSource(source, item.Brains); err != nil {
			return "", err
		}
		return "", interruption
	})
	if !errors.Is(err, interruption) {
		t.Fatalf("expected simulated interruption, got %v", err)
	}
	checkInboxStatus(t, store, resume.ID, "approving", "resume")

	plan := []inboxDecision{
		{ItemID: approved.ID, Action: "approve", UseProposal: true},
		{ItemID: moved.ID, Action: "approve", Brains: []string{"product"}},
		{ItemID: dismissed.ID, Action: "dismiss"},
		{ItemID: failed.ID, Action: "approve", Brains: []string{"unknown"}},
		{ItemID: resume.ID, Action: "approve", NewBrain: "resume"},
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	output := runInboxFixture(t, store, nil, "batch", planPath)
	if !strings.Contains(output, `"status": "ready"`) || !strings.Contains(output, `"status": "failed"`) {
		t.Fatalf("unexpected plan preview: %s", output)
	}
	checkInboxStatus(t, store, approved.ID, "reviewed")
	for range 2 {
		results, err := previewInboxPlan(store, plan)
		if err != nil {
			t.Fatal(err)
		}
		var preview bytes.Buffer
		if err := confirmInboxPlan(strings.NewReader("yes\n"), &preview, results); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(preview.String(), "to product") || !strings.Contains(preview.String(), "to sales") ||
			!strings.Contains(preview.String(), "dismiss "+dismissed.ID) {
			t.Fatalf("confirmation omitted planned decisions: %s", &preview)
		}
		applyInboxPlan(store, results)
		if results[0].Status != "approved" || results[1].Status != "approved" ||
			results[2].Status != "dismissed" || results[3].Status != "failed" || results[4].Status != "approved" {
			t.Fatalf("unexpected batch results: %+v", results)
		}
	}
	if sources, err := store.Sources(); err != nil || len(sources) != 3 {
		t.Fatalf("retry did not preserve exactly three sources: %+v, %v", sources, err)
	}
	checkInboxStatus(t, store, approved.ID, "approved", "sales")
	checkInboxStatus(t, store, moved.ID, "approved", "product")
	checkInboxStatus(t, store, dismissed.ID, "dismissed")
	checkInboxStatus(t, store, failed.ID, "pending")
	checkInboxStatus(t, store, resume.ID, "approved", "resume")
	checkInboxSearch(t, store, "sales", "alphaqualifier", true)
	checkInboxSearch(t, store, "sales", "betareview", false)
	checkInboxSearch(t, store, "product", "betareview", true)
	checkInboxSearch(t, store, "product", "gammaignore", false)
	checkInboxSearch(t, store, "resume", "epsilonresume", true)
}

func TestInboxReviewWritesAnExplicitPlanWithoutApplyingIt(t *testing.T) {
	store := inboxFixture(t)
	note := captureInboxFixture(t, store, "note", "private study note")
	proposeInboxFixture(t, store, note.ID, "d", "r", "sales")
	var output bytes.Buffer
	var plan []inboxDecision
	if err := reviewInboxQueue(store, strings.NewReader("approve\n"), &output, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].ItemID != note.ID || plan[0].UseProposal || !reflect.DeepEqual(plan[0].Brains, []string{"sales"}) {
		t.Fatalf("plan did not freeze the selected destination: %+v", plan)
	}
	checkInboxStatus(t, store, note.ID, "reviewed")
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := writeInboxPlan(path, plan); err != nil {
		t.Fatal(err)
	}
	if err := writeInboxPlan(path, plan); err == nil {
		t.Fatal("existing plan overwritten")
	}
	read, err := readInboxPlan(path)
	if err != nil || !reflect.DeepEqual(read, plan) {
		t.Fatalf("plan round trip: %+v %v", read, err)
	}
}

func inboxFixture(t *testing.T) *library.Store {
	t.Helper()
	store, err := library.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sales", "product"} {
		if err := store.CreateBrain(library.Brain{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func captureInboxFixture(t *testing.T, store *library.Store, kind, content string) inbox.Item {
	t.Helper()
	item, err := inbox.Capture(store, kind, content)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func proposeInboxFixture(t *testing.T, store *library.Store, id, digest, rationale, brain string) {
	t.Helper()
	if _, err := inbox.Propose(store, id, inbox.Proposal{Digest: digest, Rationale: rationale, Brains: []string{brain}}); err != nil {
		t.Fatal(err)
	}
}

func checkInboxStatus(t *testing.T, store *library.Store, id, status string, brains ...string) inbox.Item {
	t.Helper()
	item, err := inbox.Get(store, id)
	if err != nil || item.Status != status || !reflect.DeepEqual(item.Brains, brains) {
		t.Fatalf("item %s: got %+v, %v; want %s %v", id, item, err, status, brains)
	}
	return item
}

func checkInboxSearch(t *testing.T, store *library.Store, brain, query string, expectHits bool) {
	t.Helper()
	if err := index.Rebuild(store); err != nil {
		t.Fatal(err)
	}
	result, err := retrieval.Context(store, retrieval.Request{Query: query, Brains: []string{brain}, Mode: "keyword"})
	if err != nil || len(result.Brains) != 1 || (result.Brains[0].Status == "hits") != expectHits {
		t.Fatalf("search %s for %q: got %+v, %v; expected hits = %v", brain, query, result, err, expectHits)
	}
}

func runInboxFixture(t *testing.T, store *library.Store, input io.Reader, args ...string) string {
	t.Helper()
	var out, stderr bytes.Buffer
	if code := Run(append([]string{"--library", store.Root, "inbox"}, args...), input, &out, &stderr); code != 0 {
		t.Fatalf("inbox %v failed (%d): %s", args, code, &stderr)
	}
	return out.String()
}
