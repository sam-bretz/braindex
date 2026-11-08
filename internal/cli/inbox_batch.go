package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/sam-bretz/braindex/internal/inbox"
	"github.com/sam-bretz/braindex/internal/library"
)

type inboxDecisionResult struct {
	Decision inboxDecision `json:"decision"`
	Status   string        `json:"status"`
	Error    string        `json:"error,omitempty"`
	Item     *inbox.Item   `json:"item,omitempty"`
}

func (c command) inboxBatch(store *library.Store, args []string) error {
	flags := newFlags("inbox batch")
	apply := flags.Bool("apply", false, "apply after confirming the summary at a terminal")
	paths, err := parse(flags, args)
	if err != nil {
		return err
	}
	if len(paths) != 1 {
		return errors.New("usage: braindex inbox batch PLAN.json [--apply]")
	}
	plan, err := readInboxPlan(paths[0])
	if err != nil {
		return err
	}
	results, err := previewInboxPlan(store, plan)
	if err != nil {
		return err
	}
	if *apply {
		tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			return errors.New("applying an inbox batch requires a user at an interactive terminal")
		}
		defer tty.Close()
		if err := confirmInboxPlan(tty, tty, results); err != nil {
			return err
		}
		applyInboxPlan(store, results)
	}
	return c.json(results)
}

func readInboxPlan(path string) ([]inboxDecision, error) {
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
		return nil, errors.New("inbox batch requires a regular JSON file")
	}
	var plan []inboxDecision
	if err := decodeInboxJSON(file, &plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func previewInboxPlan(store *library.Store, plan []inboxDecision) ([]inboxDecisionResult, error) {
	ids := make([]string, 0, len(plan))
	for _, decision := range plan {
		ids = append(ids, decision.ItemID)
	}
	if err := validateInboxIDs(ids); err != nil {
		return nil, err
	}
	results := make([]inboxDecisionResult, 0, len(plan))
	for _, decision := range plan {
		decision, err := prepareInboxDecision(store, decision)
		result := inboxDecisionResult{Decision: decision, Status: "ready"}
		if err != nil {
			result.Status, result.Error = "failed", err.Error()
		}
		results = append(results, result)
	}
	return results, nil
}

func confirmInboxPlan(input io.Reader, output io.Writer, results []inboxDecisionResult) error {
	fmt.Fprintln(output, "Inbox decision preview")
	for _, result := range results {
		if result.Error != "" {
			fmt.Fprintf(output, "Skip %s: %s\n", inboxPreviewText(result.Decision.ItemID), inboxPreviewText(result.Error))
			continue
		}
		fmt.Fprintln(output, describeInboxDecision(result.Decision))
	}
	fmt.Fprint(output, "Type yes to apply the ready decisions: ")
	reader := bufio.NewScanner(input)
	if !reader.Scan() {
		if err := reader.Err(); err != nil {
			return err
		}
		return errors.New("batch cancelled")
	}
	if strings.TrimSpace(reader.Text()) != "yes" {
		return errors.New("batch cancelled")
	}
	return nil
}

func applyInboxPlan(store *library.Store, results []inboxDecisionResult) {
	for i := range results {
		result := &results[i]
		if result.Status != "ready" {
			continue
		}
		item, err := applyInboxDecision(store, result.Decision)
		if err != nil {
			result.Status, result.Error = "failed", err.Error()
		} else {
			result.Status, result.Item = item.Status, &item
		}
	}
}
