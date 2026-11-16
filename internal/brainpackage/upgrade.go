package brainpackage

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sam-bretz/braindex/internal/library"
)

type SourceChange struct {
	ID               string `json:"id"`
	Change           string `json:"change"`
	BeforeTitle      string `json:"before_title,omitempty"`
	AfterTitle       string `json:"after_title,omitempty"`
	BeforeSearchable bool   `json:"before_searchable"`
	AfterSearchable  bool   `json:"after_searchable"`
	BeforeRevision   string `json:"before_revision,omitempty"`
	AfterRevision    string `json:"after_revision,omitempty"`
}

type Comparison struct {
	BrainID      string                 `json:"brain_id"`
	Installed    library.PackageReceipt `json:"installed"`
	Candidate    Preview                `json:"candidate"`
	Changes      []SourceChange         `json:"changes"`
	LocalSources []string               `json:"local_sources"`
	Conflicts    []string               `json:"conflicts"`
}

func (c Candidate) Compare(store *library.Store, brainID string, original *Candidate) (Comparison, error) {
	state, err := store.PackageState(brainID, packageBaseline(original))
	if errors.Is(err, library.ErrPackageBaseline) {
		installed, lookupErr := store.InstalledPackage(brainID)
		if lookupErr != nil {
			return Comparison{}, lookupErr
		}
		if originErr := c.checkOrigin(installed); originErr != nil {
			return Comparison{}, originErr
		}
		if installed == c.receipt() {
			state, err = store.PackageState(brainID, packageBaseline(&c))
		}
	}
	if err != nil {
		return Comparison{}, err
	}
	if err := c.checkOrigin(state.Receipt); err != nil {
		return Comparison{}, err
	}
	if state.Receipt.Version == c.Manifest.Version && state.Receipt != c.receipt() {
		return Comparison{}, errors.New("candidate version matches installed version but its package or pin changed")
	}
	next, err := store.PackageInventory(c.packageSources())
	if err != nil {
		return Comparison{}, err
	}
	result := Comparison{
		BrainID: brainID, Installed: state.Receipt, Candidate: c.Preview(),
		Changes: []SourceChange{}, LocalSources: state.LocalSources, Conflicts: state.Conflicts,
	}
	for id, before := range state.Members {
		after, exists := next[id]
		if !exists {
			result.Changes = append(result.Changes, sourceChange(id, "removed", before, after))
		} else if before.Content != after.Content {
			result.Changes = append(result.Changes, sourceChange(id, "changed", before, after))
		}
	}
	for id, after := range next {
		if _, exists := state.Members[id]; !exists {
			result.Changes = append(result.Changes, sourceChange(id, "added", library.PackageMember{}, after))
		}
	}
	sort.Slice(result.Changes, func(i, j int) bool {
		return result.Changes[i].ID < result.Changes[j].ID
	})
	return result, nil
}

func sourceChange(id, change string, before, after library.PackageMember) SourceChange {
	return SourceChange{
		ID: id, Change: change, BeforeTitle: before.Title, AfterTitle: after.Title,
		BeforeSearchable: before.Searchable, AfterSearchable: after.Searchable,
		BeforeRevision: before.Revision, AfterRevision: after.Revision,
	}
}

func (c Candidate) Upgrade(store *library.Store, comparison Comparison, original *Candidate) (library.PackageReceipt, error) {
	if err := c.checkOrigin(comparison.Installed); err != nil {
		return library.PackageReceipt{}, err
	}
	next := c.receipt()
	if comparison.Candidate.Digest != next.Digest || comparison.Candidate.Origin != next.Origin {
		return library.PackageReceipt{}, errors.New("candidate changed after review")
	}
	if err := store.UpgradePackage(comparison.BrainID, comparison.Installed, next, c.packageSources(), packageBaseline(original)); err != nil {
		return library.PackageReceipt{}, err
	}
	return next, nil
}

func (c Candidate) checkOrigin(installed library.PackageReceipt) error {
	if c.Manifest.PackageID != installed.PackageID {
		return fmt.Errorf("candidate package %q differs from installed package %q", c.Manifest.PackageID, installed.PackageID)
	}
	var same bool
	switch {
	case c.Commit != "":
		oldBase, oldCommit, ok := splitPin(installed.Origin)
		commitBytes, err := hex.DecodeString(oldCommit)
		same = ok && err == nil && (len(commitBytes) == 20 || len(commitBytes) == 32) &&
			oldBase == strings.TrimSuffix(c.Origin, "#"+c.Commit)
	case c.ArchiveDigest != "":
		oldSuffix := "#" + installed.PackageID + "@" + installed.Version
		newSuffix := "#" + c.Manifest.PackageID + "@" + c.Manifest.Version
		same = strings.HasSuffix(installed.Origin, oldSuffix) &&
			strings.TrimSuffix(installed.Origin, oldSuffix) == strings.TrimSuffix(c.Origin, newSuffix)
	default:
		same = c.Origin == installed.Origin
	}
	if !same {
		return errors.New("candidate package origin differs from installed package origin")
	}
	return nil
}

func splitPin(origin string) (string, string, bool) {
	i := strings.LastIndexByte(origin, '#')
	if i < 0 {
		return origin, "", false
	}
	return origin[:i], origin[i+1:], true
}

func packageBaseline(original *Candidate) *library.PackageBaseline {
	if original == nil {
		return nil
	}
	return &library.PackageBaseline{Receipt: original.receipt(), Sources: original.packageSources()}
}
