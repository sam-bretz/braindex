package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sam-bretz/braindex/internal/inbox"
	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
)

func (c command) capture(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: braindex capture note|file|url VALUE")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	value := args[1]
	if args[0] == "note" && value == "-" {
		const maxNote = 1 << 20
		text, err := io.ReadAll(io.LimitReader(c.in, maxNote+1))
		if err != nil {
			return err
		}
		if len(text) > maxNote {
			return errors.New("piped note exceeds 1 MiB")
		}
		value = string(text)
	}
	item, err := inbox.Capture(store, args[0], value)
	if err != nil {
		return err
	}
	return c.json(item)
}

func (c command) inbox(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: braindex inbox list|show|review|propose|approve|defer|dismiss")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("usage: braindex inbox list")
		}
		items, err := inbox.List(store)
		if err != nil {
			return err
		}
		return c.json(items)
	case "show":
		if len(args) < 2 || len(args) > 3 || (len(args) == 3 && args[2] != "--content") {
			return errors.New("usage: braindex inbox show ID [--content]")
		}
		item, err := inbox.Get(store, args[1])
		if err != nil {
			return err
		}
		if len(args) == 2 {
			return c.json(item)
		}
		if item.Kind == "url" {
			_, err := fmt.Fprintln(c.out, item.Original)
			return err
		}
		if item.Kind == "file" {
			_, err := fmt.Fprintln(c.out, inbox.ContentPath(store, item))
			return err
		}
		file, err := os.Open(inbox.ContentPath(store, item))
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(c.out, file)
		return err
	case "review":
		if len(args) != 2 {
			return errors.New("usage: braindex inbox review ID")
		}
		item, err := inbox.Get(store, args[1])
		if err != nil {
			return err
		}
		review := struct {
			Item     inbox.Item `json:"item"`
			Original string     `json:"original"`
		}{Item: item, Original: item.Original}
		switch item.Kind {
		case "file":
			review.Original = inbox.ContentPath(store, item)
		case "note":
			text, err := os.ReadFile(inbox.ContentPath(store, item))
			if err != nil {
				return err
			}
			review.Original = string(text)
		}
		return c.json(review)
	case "propose":
		if len(args) != 2 {
			return errors.New("usage: braindex inbox propose ID < proposal.json")
		}
		decoder := json.NewDecoder(io.LimitReader(c.in, 64<<10))
		decoder.DisallowUnknownFields()
		var proposal inbox.Proposal
		if err := decoder.Decode(&proposal); err != nil {
			return err
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return errors.New("expected exactly one proposal JSON object")
		}
		item, err := inbox.Propose(store, args[1], proposal)
		if err != nil {
			return err
		}
		return c.json(item)
	case "defer", "dismiss":
		if len(args) != 2 {
			return fmt.Errorf("usage: braindex inbox %s ID", args[0])
		}
		status := "deferred"
		if args[0] == "dismiss" {
			status = "dismissed"
		}
		item, err := inbox.Disposition(store, args[1], status)
		if err != nil {
			return err
		}
		return c.json(item)
	case "approve":
		return c.approveInbox(store, args[1:])
	default:
		return fmt.Errorf("unknown inbox command %q", args[0])
	}
}

func (c command) approveInbox(store *library.Store, args []string) error {
	flags := newFlags("inbox approve")
	newBrain := flags.String("new-brain", "", "new brain ID")
	useProposal := flags.Bool("use-proposal", false, "use the agent's proposed destinations")
	var brains stringsFlag
	flags.Var(&brains, "brain", "destination brain ID")
	pos, err := parse(flags, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || (*useProposal && (len(brains) > 0 || *newBrain != "")) {
		return errors.New("usage: braindex inbox approve ID [--brain ID] [--new-brain ID] [--use-proposal]")
	}
	item, err := inbox.Get(store, pos[0])
	if err != nil {
		return err
	}
	if item.Status == "approved" {
		return c.json(item)
	}
	if *useProposal {
		if item.Proposal == nil || item.Proposal.LeaveOut {
			return errors.New("item has no proposed destinations")
		}
		brains, *newBrain = item.Proposal.Brains, item.Proposal.NewBrain
	}
	if *newBrain != "" {
		if err := library.ValidateID(*newBrain); err != nil {
			return err
		}
		brains = append(brains, *newBrain)
	}
	if len(brains) == 0 {
		return errors.New("choose at least one destination brain")
	}
	for _, brain := range brains {
		if err := library.ValidateID(brain); err != nil {
			return err
		}
	}
	existing := []string{}
	for _, id := range brains {
		if id != *newBrain {
			existing = append(existing, id)
		}
	}
	if err := requireBrains(store, existing); err != nil {
		return err
	}
	source, err := captureSource(store, item)
	if err != nil {
		return err
	}
	if err := confirmPromotion(item.ID, brains); err != nil {
		return err
	}
	approved, err := inbox.Approve(store, item.ID, brains, func(original inbox.Item) (string, error) {
		if *newBrain != "" {
			if err := store.CreateBrain(library.Brain{ID: *newBrain}); err != nil {
				return "", err
			}
		}
		var saved library.Source
		var err error
		if original.Kind == "url" {
			saved, err = store.PutReference(source, brains)
		} else {
			saved, err = store.PutSource(source, brains)
		}
		return saved.ID, err
	})
	if err != nil {
		return err
	}
	return c.json(approved)
}

func captureSource(store *library.Store, item inbox.Item) (library.Source, error) {
	if item.Kind == "url" {
		return library.Source{URL: item.Original, Title: item.Original}, nil
	}
	source, err := ingest.Extract(inbox.ContentPath(store, item))
	if err != nil {
		return library.Source{}, err
	}
	// The captured bytes are a snapshot, independent of the current original.
	// Let the library deduplicate identical passages without changing the
	// current revision associated with the original path or provider ID.
	source.ID, source.Path, source.URL = "", "", ""
	if item.Kind == "note" {
		source.Title = "Captured note"
	} else {
		source.Title = filepath.Base(item.Original)
	}
	return source, nil
}

func confirmPromotion(id string, brains []string) error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return errors.New("approval requires a user at an interactive terminal")
	}
	defer tty.Close()
	fmt.Fprintf(tty, "Add inbox item %s to %s? Type yes to approve: ", id, strings.Join(brains, ", "))
	response, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(response) != "yes" {
		return errors.New("approval cancelled")
	}
	return nil
}
