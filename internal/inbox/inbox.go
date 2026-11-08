package inbox

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/sam-bretz/braindex/internal/library"
)

const maxCapture = 32 << 20

type Proposal struct {
	Digest      string   `json:"digest"`
	Rationale   string   `json:"rationale"`
	Brains      []string `json:"brains,omitempty"`
	NewBrain    string   `json:"new_brain,omitempty"`
	LeaveOut    bool     `json:"leave_out,omitempty"`
	ReviewedID  string   `json:"reviewed_item_id"`
	GeneratedBy string   `json:"generated_by"`
}

type Item struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Original   string    `json:"original,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
	Status     string    `json:"status"`
	Proposal   *Proposal `json:"proposal,omitempty"`
	Brains     []string  `json:"brains,omitempty"`
	SourceID   string    `json:"source_id,omitempty"`
}

func dir(store *library.Store) string { return filepath.Join(store.MetaDir(), "inbox") }

func Capture(store *library.Store, kind, input string) (Item, error) {
	if strings.TrimSpace(input) == "" {
		return Item{}, errors.New("capture input is required")
	}
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return Item{}, err
	}
	item := Item{ID: hex.EncodeToString(id), Kind: kind, CapturedAt: time.Now().UTC(), Status: "pending"}
	var content io.Reader
	switch kind {
	case "note":
		content = strings.NewReader(input)
	case "url":
		parsed, err := url.Parse(input)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
			return Item{}, errors.New("capture requires an HTTP(S) URL without credentials")
		}
		item.Original = input
	case "file":
		path, err := filepath.Abs(input)
		if err != nil {
			return Item{}, err
		}
		inputFile, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return Item{}, err
		}
		defer inputFile.Close()
		info, err := inputFile.Stat()
		if err != nil {
			return Item{}, err
		}
		if !info.Mode().IsRegular() {
			return Item{}, errors.New("capture requires a regular file")
		}
		item.Original, content = path, inputFile
	default:
		return Item{}, fmt.Errorf("unknown capture kind %q", kind)
	}
	if err := os.MkdirAll(dir(store), 0700); err != nil {
		return Item{}, err
	}
	staging, err := os.MkdirTemp(dir(store), ".capture-")
	if err != nil {
		return Item{}, err
	}
	defer os.RemoveAll(staging)
	if content != nil {
		if err := copyContent(filepath.Join(staging, contentName(item)), content); err != nil {
			return Item{}, err
		}
	}
	if err := save(staging, item); err != nil {
		return Item{}, err
	}
	if err := os.Rename(staging, filepath.Join(dir(store), item.ID)); err != nil {
		return Item{}, err
	}
	return item, nil
}

func copyContent(path string, content io.Reader) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	size, copyErr := io.Copy(file, io.LimitReader(content, maxCapture+1))
	closeErr := file.Close()
	if size > maxCapture {
		return errors.New("capture exceeds 32 MiB")
	}
	return errors.Join(copyErr, closeErr)
}

func contentName(item Item) string {
	if item.Kind == "note" {
		return "content.txt"
	}
	return "content" + filepath.Ext(item.Original)
}

func ContentPath(store *library.Store, item Item) string {
	return filepath.Join(dir(store), item.ID, contentName(item))
}

func List(store *library.Store) ([]Item, error) {
	items := []Item{}
	entries, err := os.ReadDir(dir(store))
	if errors.Is(err, os.ErrNotExist) {
		return items, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		item, err := Get(store, entry.Name())
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func Get(store *library.Store, id string) (Item, error) {
	if len(id) != 24 {
		return Item{}, errors.New("invalid inbox item ID")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return Item{}, errors.New("invalid inbox item ID")
	}
	data, err := os.ReadFile(filepath.Join(dir(store), id, "item.json"))
	if err != nil {
		return Item{}, err
	}
	var item Item
	if err := json.Unmarshal(data, &item); err != nil {
		return Item{}, err
	}
	if item.ID != id {
		return Item{}, errors.New("inbox item ID mismatch")
	}
	return item, nil
}

func Propose(store *library.Store, id string, proposal Proposal) (Item, error) {
	hasDestination := len(proposal.Brains) > 0 || proposal.NewBrain != ""
	if strings.TrimSpace(proposal.Digest) == "" || strings.TrimSpace(proposal.Rationale) == "" ||
		hasDestination == proposal.LeaveOut || (proposal.ReviewedID != "" && proposal.ReviewedID != id) {
		return Item{}, errors.New("proposal requires a digest, rationale, and either destinations or leave_out")
	}
	proposal.ReviewedID = id
	proposal.GeneratedBy = "agent"
	return change(store, id, func(item *Item) error {
		if item.Status == "approving" || item.Status == "approved" || item.Status == "dismissed" {
			return errors.New("item is already being approved or resolved")
		}
		item.Proposal = &proposal
		item.Status = "reviewed"
		return nil
	})
}

func Disposition(store *library.Store, id, status string) (Item, error) {
	if status != "deferred" && status != "dismissed" {
		return Item{}, errors.New("invalid disposition")
	}
	return change(store, id, func(item *Item) error {
		if item.Status == "approving" || item.Status == "approved" || item.Status == "dismissed" {
			return errors.New("item is already being approved or resolved")
		}
		item.Status = status
		return nil
	})
}

// Approve imports the original via the supplied callback after the CLI obtains
// confirmation. The callback must be safe to retry if saving the receipt fails.
func Approve(store *library.Store, id string, brains []string, promote func(Item) (string, error)) (Item, error) {
	return change(store, id, func(item *Item) error {
		switch item.Status {
		case "approved":
			return nil
		case "dismissed":
			return errors.New("item is dismissed")
		case "approving":
			if !slices.Equal(item.Brains, brains) {
				return errors.New("approval was interrupted; retry with the recorded destinations")
			}
		default:
			item.Status, item.Brains = "approving", brains
			if err := save(filepath.Join(dir(store), id), *item); err != nil {
				return err
			}
		}
		sourceID, err := promote(*item)
		if err != nil {
			return err
		}
		item.Status, item.SourceID, item.Brains = "approved", sourceID, brains
		return nil
	})
}

func change(store *library.Store, id string, update func(*Item) error) (Item, error) {
	if _, err := Get(store, id); err != nil {
		return Item{}, err
	}
	path := filepath.Join(dir(store), id)
	lock, err := os.OpenFile(filepath.Join(path, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Item{}, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return Item{}, err
	}
	item, err := Get(store, id)
	if err != nil {
		return Item{}, err
	}
	if err := update(&item); err != nil {
		return Item{}, err
	}
	return item, save(path, item)
}

func save(path string, item Item) error {
	file, err := os.CreateTemp(path, ".item-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	encodeErr := json.NewEncoder(file).Encode(item)
	closeErr := file.Close()
	if err := errors.Join(encodeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(path, "item.json"))
}
