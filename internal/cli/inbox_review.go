package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/sam-bretz/braindex/internal/inbox"
	"github.com/sam-bretz/braindex/internal/library"
)

type inboxOriginalView struct {
	Item     inbox.Item `json:"item"`
	Original string     `json:"original"`
}

func inboxOriginal(store *library.Store, item inbox.Item) (inboxOriginalView, error) {
	view := inboxOriginalView{Item: item, Original: item.Original}
	switch item.Kind {
	case "file":
		view.Original = inbox.ContentPath(store, item)
	case "note":
		text, err := os.ReadFile(inbox.ContentPath(store, item))
		if err != nil {
			return inboxOriginalView{}, err
		}
		view.Original = string(text)
	}
	return view, nil
}

type inboxDecision struct {
	ItemID      string   `json:"item_id"`
	Action      string   `json:"action"`
	Brains      []string `json:"brains,omitempty"`
	NewBrain    string   `json:"new_brain,omitempty"`
	UseProposal bool     `json:"use_proposal,omitempty"`
}

func prepareInboxDecision(store *library.Store, decision inboxDecision) (inboxDecision, error) {
	item, err := inbox.Get(store, decision.ItemID)
	if err != nil {
		return decision, err
	}
	switch decision.Action {
	case "defer", "dismiss":
		if len(decision.Brains) > 0 || decision.NewBrain != "" || decision.UseProposal {
			return decision, errors.New("only an approval can specify destinations")
		}
		if item.Status == "approved" || item.Status == "approving" || (item.Status == "dismissed" && decision.Action != "dismiss") {
			return decision, fmt.Errorf("item is %s", item.Status)
		}
		return decision, nil
	case "approve":
		if item.Status == "dismissed" {
			return decision, errors.New("item is dismissed")
		}
	default:
		return decision, fmt.Errorf("invalid decision action %q", decision.Action)
	}

	if decision.UseProposal {
		if len(decision.Brains) > 0 || decision.NewBrain != "" {
			return decision, errors.New("choose either the proposal or explicit destinations")
		}
		if item.Proposal == nil || item.Proposal.LeaveOut {
			return decision, errors.New("item has no proposed destinations")
		}
		decision.Brains, decision.NewBrain = item.Proposal.Brains, item.Proposal.NewBrain
		decision.UseProposal = false
	}
	decision.Brains = append([]string(nil), decision.Brains...)
	if decision.NewBrain != "" {
		decision.Brains = append(decision.Brains, decision.NewBrain)
	}
	slices.Sort(decision.Brains)
	decision.Brains = slices.Compact(decision.Brains)
	if len(decision.Brains) == 0 {
		return decision, errors.New("choose at least one destination brain")
	}
	for _, id := range decision.Brains {
		if err := library.ValidateID(id); err != nil {
			return decision, err
		}
	}
	if item.Status == "approving" || item.Status == "approved" {
		recorded := slices.Clone(item.Brains)
		slices.Sort(recorded)
		if !slices.Equal(decision.Brains, recorded) ||
			(item.Status == "approving" && item.NewBrain != "" && item.NewBrain != decision.NewBrain) {
			return decision, errors.New("retry with the recorded approval destinations")
		}
	}

	existing := make([]string, 0, len(decision.Brains))
	for _, id := range decision.Brains {
		if id != decision.NewBrain {
			existing = append(existing, id)
		}
	}
	if err := requireBrains(store, existing); err != nil {
		return decision, err
	}
	if decision.NewBrain != "" && item.Status != "approving" && item.Status != "approved" {
		brains, err := store.Brains()
		if err != nil {
			return decision, err
		}
		for _, brain := range brains {
			if brain.ID == decision.NewBrain {
				return decision, fmt.Errorf("brain %q already exists; use it as an existing destination", brain.ID)
			}
		}
	}
	return decision, nil
}

func applyInboxDecision(store *library.Store, decision inboxDecision) (inbox.Item, error) {
	item, err := inbox.Get(store, decision.ItemID)
	if err != nil {
		return inbox.Item{}, err
	}
	if decision.Action != "approve" {
		status := "dismissed"
		if decision.Action == "defer" {
			status = "deferred"
		}
		if item.Status == status {
			return item, nil
		}
		return inbox.Disposition(store, item.ID, status)
	}
	if item.Status == "approved" {
		if !slices.Equal(decision.Brains, sortedInboxBrains(item.Brains)) {
			return inbox.Item{}, errors.New("item was approved for different destinations")
		}
		return item, nil
	}
	source, err := captureSource(store, item)
	if err != nil {
		return inbox.Item{}, err
	}
	// Keep the recorded order on approvals interrupted by older versions.
	if item.Status == "approving" && slices.Equal(decision.Brains, sortedInboxBrains(item.Brains)) {
		decision.Brains = item.Brains
	}
	return inbox.ApprovePlacement(store, item.ID, decision.Brains, decision.NewBrain, func(original inbox.Item) (string, error) {
		if original.NewBrain != "" {
			if err := store.CreateBrain(library.Brain{ID: original.NewBrain}); err != nil && !errors.Is(err, library.ErrExists) {
				return "", err
			}
		}
		var saved library.Source
		var err error
		if original.Kind == "url" {
			saved, err = store.PutReference(source, original.Brains)
		} else {
			saved, err = store.PutSource(source, original.Brains)
		}
		return saved.ID, err
	})
}

func sortedInboxBrains(brains []string) []string {
	sorted := slices.Clone(brains)
	slices.Sort(sorted)
	return sorted
}

func (c command) interactiveInboxReview(store *library.Store, args []string) error {
	flags := newFlags("inbox review")
	planPath := flags.String("plan", "", "save decisions to a new plan file instead of applying them")
	pos, err := parse(flags, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || (len(args) > 0 && *planPath == "") {
		return errors.New("usage: braindex inbox review [ID | --plan FILE]")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return errors.New("inbox review requires a user at an interactive terminal")
	}
	defer tty.Close()
	if *planPath == "" {
		return reviewInboxQueue(store, tty, tty, nil)
	}
	var plan []inboxDecision
	if err := reviewInboxQueue(store, tty, tty, &plan); err != nil {
		return err
	}
	if len(plan) == 0 {
		fmt.Fprintln(tty, "No decisions recorded.")
		return nil
	}
	if err := writeInboxPlan(*planPath, plan); err != nil {
		return err
	}
	fmt.Fprintf(tty, "Saved %d decision(s) to %s. Use inbox batch to preview and apply.\n", len(plan), inboxPreviewText(*planPath))
	return nil
}

func writeInboxPlan(path string, plan []inboxDecision) error {
	// Write before opening the target, and never overwrite an existing plan.
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > maxInboxJSON {
		return errors.New("inbox plan exceeds 2 MiB")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(data, '\n'))
	if err := errors.Join(writeErr, file.Close()); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func reviewInboxQueue(store *library.Store, input io.Reader, output io.Writer, plan *[]inboxDecision) error {
	items, err := inbox.List(store)
	if err != nil {
		return err
	}
	items = slices.DeleteFunc(items, func(item inbox.Item) bool { return !reviewableInboxItem(item) })
	slices.SortStableFunc(items, func(a, b inbox.Item) int { return a.CapturedAt.Compare(b.CapturedAt) })
	fmt.Fprintf(output, "%d staged item(s).\n", len(items))
	if plan != nil {
		fmt.Fprintln(output, "Plan mode records choices for later review. Type quit to finish the plan.")
	}
	reader := bufio.NewScanner(input)
	for _, item := range items {
		view, err := inboxOriginal(store, item)
		if err != nil {
			fmt.Fprintf(output, "\n%s: %q\n", item.ID, err)
			continue
		}
		fmt.Fprintf(output, "\n%s (%s, %s)\nOriginal: %s\n", item.ID, item.Kind, item.Status, inboxPreviewText(view.Original))
		if item.Kind == "file" {
			fmt.Fprintf(output, "Captured from: %s\n", inboxPreviewText(item.Original))
		}
		if proposal := item.Proposal; proposal != nil {
			destinations := strings.Join(proposal.Brains, ", ")
			if proposal.NewBrain != "" {
				destinations = strings.TrimSpace(destinations + " new " + proposal.NewBrain)
			}
			if proposal.LeaveOut {
				destinations = "leave out"
			}
			fmt.Fprintf(output, "Digest: %s\nRationale: %s\nProposal: %s\n", inboxPreviewText(proposal.Digest), inboxPreviewText(proposal.Rationale), inboxPreviewText(destinations))
		}
		for {
			fmt.Fprint(output, "Choose approve, move BRAIN..., new BRAIN, defer, dismiss, skip, or quit: ")
			if !reader.Scan() {
				return reader.Err()
			}
			fields := strings.Fields(reader.Text())
			if len(fields) == 0 || (len(fields) == 1 && fields[0] == "skip") {
				break
			}
			if len(fields) == 1 && fields[0] == "quit" {
				return nil
			}
			decision, err := queueDecision(item, fields)
			if err == nil {
				decision, err = prepareInboxDecision(store, decision)
			}
			if err != nil {
				fmt.Fprintf(output, "%q\n", err)
				continue
			}
			if plan != nil {
				*plan = append(*plan, decision)
				fmt.Fprintf(output, "Planned: %s\n", describeInboxDecision(decision))
				if len(*plan) == maxInboxBatch {
					fmt.Fprintln(output, "Plan reached 100 decisions.")
					return nil
				}
				break
			}
			fmt.Fprintf(output, "%s? Type yes to confirm: ", describeInboxDecision(decision))
			if !reader.Scan() {
				return reader.Err()
			}
			if strings.TrimSpace(reader.Text()) != "yes" {
				fmt.Fprintln(output, "Item left unchanged.")
				continue
			}
			saved, err := applyInboxDecision(store, decision)
			if err != nil {
				fmt.Fprintf(output, "Item could not be updated: %q\n", err)
				break
			}
			fmt.Fprintf(output, "%s %s\n", saved.ID, saved.Status)
			break
		}
	}
	return nil
}

func reviewableInboxItem(item inbox.Item) bool {
	switch item.Status {
	case "pending", "reviewed", "deferred", "approving":
		return true
	default:
		return false
	}
}

func queueDecision(item inbox.Item, fields []string) (inboxDecision, error) {
	decision := inboxDecision{ItemID: item.ID}
	switch {
	case len(fields) == 1 && fields[0] == "approve":
		decision.Action = "approve"
		if item.Status == "approving" {
			decision.Brains, decision.NewBrain = item.Brains, item.NewBrain
		} else {
			decision.UseProposal = true
		}
	case len(fields) > 1 && fields[0] == "move":
		decision.Action, decision.Brains = "approve", fields[1:]
	case len(fields) == 2 && fields[0] == "new":
		decision.Action, decision.NewBrain = "approve", fields[1]
	case len(fields) == 1 && (fields[0] == "defer" || fields[0] == "dismiss"):
		decision.Action = fields[0]
	default:
		return decision, errors.New("unknown review action")
	}
	return decision, nil
}

func describeInboxDecision(decision inboxDecision) string {
	description := decision.Action + " " + decision.ItemID
	if decision.Action == "approve" {
		description += " to " + strings.Join(decision.Brains, ", ")
		if decision.NewBrain != "" {
			description += " (create " + decision.NewBrain + ")"
		}
	}
	return description
}

func inboxPreviewText(text string) string {
	const max = 1200
	count := 0
	for offset := range text {
		if count == max {
			return strconv.QuoteToGraphic(text[:offset] + "…")
		}
		count++
	}
	return strconv.QuoteToGraphic(text)
}
