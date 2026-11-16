package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sam-bretz/braindex/internal/inbox"
	"github.com/sam-bretz/braindex/internal/library"
)

const maxInboxBatch = 100
const maxInboxJSON = 2 << 20

type inboxHandoffItem struct {
	ItemID      string  `json:"item_id"`
	Kind        string  `json:"kind,omitempty"`
	Content     *string `json:"content,omitempty"`
	CapturedAt  string  `json:"captured_at,omitempty"`
	CapturePath string  `json:"capture_path,omitempty"`
	Error       string  `json:"error,omitempty"`
}

func (c command) inboxHandoff(store *library.Store, args []string) error {
	flags := newFlags("inbox handoff")
	include := flags.Bool("include-content", false, "include selected content or the local captured file path")
	ids, err := parse(flags, args)
	if err != nil {
		return err
	}
	if err := validateInboxIDs(ids); err != nil {
		return err
	}
	brains, err := store.Brains()
	if err != nil {
		return err
	}
	destinations := make([]string, 0, len(brains))
	for _, brain := range brains {
		destinations = append(destinations, brain.ID)
	}
	result := struct {
		ContentIncluded bool               `json:"content_included"`
		BrainIDs        []string           `json:"brain_ids"`
		Items           []inboxHandoffItem `json:"items"`
	}{ContentIncluded: *include, BrainIDs: destinations, Items: make([]inboxHandoffItem, 0, len(ids))}
	for _, id := range ids {
		entry := inboxHandoffItem{ItemID: id}
		item, err := inbox.Get(store, id)
		switch {
		case errors.Is(err, os.ErrNotExist):
			entry.Error = "inbox item does not exist"
		case err != nil:
			entry.Error = err.Error()
		case !proposableInboxItem(item):
			entry.Error = "item is being approved or resolved"
		default:
			entry.Kind, entry.CapturedAt = item.Kind, item.CapturedAt.Format("2006-01-02T15:04:05Z07:00")
			if *include {
				content, err := inboxHandoffContent(store, item)
				if err != nil {
					entry.Error = err.Error()
				} else if item.Kind == "file" {
					entry.CapturePath = content
				} else {
					entry.Content = &content
				}
			}
		}
		result.Items = append(result.Items, entry)
	}
	return c.json(result)
}

func inboxHandoffContent(store *library.Store, item inbox.Item) (string, error) {
	if item.Kind != "note" {
		view, err := inboxOriginal(store, item)
		return view.Original, err
	}
	file, err := os.Open(inbox.ContentPath(store, item))
	if err != nil {
		return "", err
	}
	defer file.Close()
	const maxNote = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(file, maxNote+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxNote {
		return "", errors.New("note exceeds 64 KiB; inspect it with inbox show ID --content")
	}
	return string(raw), nil
}

type inboxProposalResult struct {
	ItemID string `json:"item_id,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func (c command) inboxProposals(store *library.Store, ids []string) error {
	if err := validateInboxIDs(ids); err != nil {
		return err
	}
	var responses []json.RawMessage
	if err := decodeInboxJSON(c.in, &responses); err != nil {
		return fmt.Errorf("read proposal array from stdin: %w", err)
	}
	if len(responses) > maxInboxBatch {
		return errors.New("too many proposal responses")
	}

	byID := make(map[string][]json.RawMessage, len(ids))
	for _, id := range ids {
		byID[id] = nil
	}
	results := make([]inboxProposalResult, 0, len(ids))
	for _, raw := range responses {
		var header struct {
			ItemID string `json:"reviewed_item_id"`
		}
		if err := json.Unmarshal(raw, &header); err != nil || header.ItemID == "" {
			results = append(results, inboxProposalResult{Status: "failed", Error: "response requires a valid reviewed_item_id"})
			continue
		}
		if _, selected := byID[header.ItemID]; !selected {
			results = append(results, inboxProposalResult{ItemID: header.ItemID, Status: "failed", Error: "item was not selected"})
			continue
		}
		byID[header.ItemID] = append(byID[header.ItemID], raw)
	}

	for _, id := range ids {
		result := inboxProposalResult{ItemID: id}
		switch count := len(byID[id]); count {
		case 0:
			result.Status = "missing"
		case 1:
			if err := saveInboxProposal(store, id, byID[id][0]); err != nil {
				result.Status, result.Error = "failed", err.Error()
			} else {
				result.Status = "reviewed"
			}
		default:
			result.Status, result.Error = "failed", "multiple responses for the same item"
		}
		results = append(results, result)
	}
	return c.json(results)
}

func saveInboxProposal(store *library.Store, id string, raw json.RawMessage) error {
	var proposal inbox.Proposal
	if err := decodeInboxJSON(bytes.NewReader(raw), &proposal); err != nil {
		return err
	}
	for _, brain := range proposal.Brains {
		if err := library.ValidateID(brain); err != nil {
			return err
		}
	}
	if err := requireBrains(store, proposal.Brains); err != nil {
		return err
	}
	if proposal.NewBrain != "" {
		if err := library.ValidateID(proposal.NewBrain); err != nil {
			return err
		}
		brains, err := store.Brains()
		if err != nil {
			return err
		}
		for _, brain := range brains {
			if brain.ID == proposal.NewBrain {
				return fmt.Errorf("brain %q already exists; propose it in brains", brain.ID)
			}
		}
	}
	_, err := inbox.Propose(store, id, proposal)
	return err
}

func proposableInboxItem(item inbox.Item) bool {
	return reviewableInboxItem(item) && item.Status != "approving"
}

func validateInboxIDs(ids []string) error {
	if len(ids) < 1 || len(ids) > maxInboxBatch {
		return errors.New("choose between 1 and 100 inbox item IDs")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return fmt.Errorf("duplicate item ID %q", id)
		}
		seen[id] = true
	}
	return nil
}

func decodeInboxJSON(reader io.Reader, target any) error {
	if reader == nil {
		return errors.New("expected JSON input")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxInboxJSON+1))
	if err != nil {
		return err
	}
	if len(data) > maxInboxJSON {
		return errors.New("inbox JSON exceeds 2 MiB")
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("expected a JSON value other than null")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}
