package library

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrPackageBaseline = errors.New("original package is required to compare this older installation")

type PackageSource struct {
	Key    string
	Source Source
}

type PackageMember struct {
	Title      string `json:"title"`
	SourceID   string `json:"source_id"`
	Revision   string `json:"revision"`
	Content    string `json:"content_sha256"`
	Searchable bool   `json:"searchable"`
}

type PackageBaseline struct {
	Receipt PackageReceipt
	Sources []PackageSource
}

type PackageState struct {
	Receipt      PackageReceipt
	Members      map[string]PackageMember
	LocalSources []string
	Conflicts    []string
}

// InstallBrain publishes validated package membership and its receipt together.
func (s *Store) InstallBrain(brain Brain, sources []PackageSource, receipt PackageReceipt) error {
	if err := ValidateID(brain.ID); err != nil {
		return err
	}
	if strings.TrimSpace(brain.Name) == "" || receipt.PackageID == "" ||
		receipt.Version == "" || receipt.Origin == "" || receipt.Digest == "" {
		return errors.New("brain name and package receipt are required")
	}
	return s.update(func(c *catalog) error {
		if _, exists := c.Brains[brain.ID]; exists {
			if c.Packages[brain.ID] == receipt {
				return nil
			}
			return fmt.Errorf("brain %q: %w", brain.ID, ErrExists)
		}
		return s.publishPackage(c, brain, nil, sources, receipt)
	})
}

func (s *Store) InstalledPackage(brainID string) (PackageReceipt, error) {
	c, err := s.load()
	if err != nil {
		return PackageReceipt{}, err
	}
	return installedReceipt(c, brainID)
}

func installedReceipt(c *catalog, brainID string) (PackageReceipt, error) {
	receipt, exists := c.Packages[brainID]
	_, brainExists := c.Brains[brainID]
	if !exists || !brainExists {
		return PackageReceipt{}, fmt.Errorf("brain %q has no package receipt: %w", brainID, ErrNotFound)
	}
	return receipt, nil
}

func (s *Store) PackageState(brainID string, baseline *PackageBaseline) (PackageState, error) {
	c, err := s.load()
	if err != nil {
		return PackageState{}, err
	}
	return s.packageState(c, brainID, baseline)
}

func (s *Store) packageState(c *catalog, brainID string, baseline *PackageBaseline) (PackageState, error) {
	receipt, err := installedReceipt(c, brainID)
	if err != nil {
		return PackageState{}, err
	}
	members, tracked := c.PackageMembers[brainID]
	if !tracked {
		if baseline == nil {
			return PackageState{}, ErrPackageBaseline
		}
		old := baseline.Receipt
		if old.PackageID != receipt.PackageID || old.Version != receipt.Version || old.Digest != receipt.Digest {
			return PackageState{}, errors.New("original package does not match the installed package receipt")
		}
		members, err = s.PackageInventory(baseline.Sources)
		if err != nil {
			return PackageState{}, err
		}
	}
	state := PackageState{
		Receipt: receipt, Members: members, LocalSources: []string{}, Conflicts: []string{},
	}
	brain := c.Brains[brainID]
	selected := make(map[string]bool, len(brain.Sources))
	for _, id := range brain.Sources {
		selected[id] = true
	}
	managed := make(map[string]bool, len(members))
	for key, member := range members {
		managed[member.SourceID] = true
		if !selected[member.SourceID] || c.Sources[member.SourceID].Current != member.Revision {
			state.Conflicts = append(state.Conflicts, key)
		}
	}
	for _, id := range brain.Sources {
		if !managed[id] {
			state.LocalSources = append(state.LocalSources, id)
		}
	}
	sort.Strings(state.Conflicts)
	return state, nil
}

func (s *Store) PackageInventory(sources []PackageSource) (map[string]PackageMember, error) {
	members := make(map[string]PackageMember, len(sources))
	seen := make(map[string]bool, len(sources))
	for _, item := range sources {
		if err := ValidateID(item.Key); err != nil {
			return nil, err
		}
		source := item.Source
		if source.ID == "" || source.Path != "" {
			return nil, errors.New("package sources require an ID and cannot reference local paths")
		}
		if _, exists := members[item.Key]; exists || seen[source.ID] {
			return nil, errors.New("duplicate package source")
		}
		seen[source.ID] = true
		source, err := s.prepareSource(source)
		if err != nil {
			return nil, err
		}
		member := PackageMember{Title: source.Title, SourceID: source.ID, Revision: hashJSON(source), Searchable: len(source.Passages) > 0}
		source.ID = ""
		member.Content = hashJSON(source)
		members[item.Key] = member
	}
	return members, nil
}

// UpgradePackage rechecks the reviewed receipt and local edits under the
// catalog lock, then publishes the new membership and receipt together.
func (s *Store) UpgradePackage(brainID string, expected, next PackageReceipt, sources []PackageSource, baseline *PackageBaseline) error {
	return s.update(func(c *catalog) error {
		current, err := installedReceipt(c, brainID)
		if err != nil {
			return err
		}
		if current == next {
			return nil
		}
		if current != expected {
			return errors.New("installed package changed after review; compare again")
		}
		if current.PackageID != next.PackageID || current.Version == next.Version {
			return errors.New("candidate must be a different version of the installed package")
		}
		state, err := s.packageState(c, brainID, baseline)
		if err != nil {
			return err
		}
		if len(state.Conflicts) != 0 {
			return fmt.Errorf("package sources have local changes: %s", strings.Join(state.Conflicts, ", "))
		}
		return s.publishPackage(c, c.Brains[brainID], state.LocalSources, sources, next)
	})
}

func (s *Store) publishPackage(c *catalog, brain Brain, local []string, sources []PackageSource, receipt PackageReceipt) error {
	members, err := s.PackageInventory(sources)
	if err != nil {
		return err
	}
	for _, member := range members {
		if existing, ok := c.Sources[member.SourceID]; ok && existing.Current != member.Revision {
			return fmt.Errorf("package source %q has local changes", member.SourceID)
		}
	}
	brain.Sources = append([]string{}, local...)
	for _, item := range sources {
		source, err := s.prepareSource(item.Source)
		if err != nil {
			return err
		}
		if _, err := s.recordSource(c, source, ""); err != nil {
			return err
		}
		brain.Sources = append(brain.Sources, source.ID)
	}
	brain.Sources = uniqueSorted(brain.Sources)
	c.Brains[brain.ID] = brain
	if c.Packages == nil {
		c.Packages = make(map[string]PackageReceipt)
	}
	if c.PackageMembers == nil {
		c.PackageMembers = make(map[string]map[string]PackageMember)
	}
	c.Packages[brain.ID] = receipt
	c.PackageMembers[brain.ID] = members
	return nil
}
