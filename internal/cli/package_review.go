package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sam-bretz/braindex/internal/brainpackage"
	"github.com/sam-bretz/braindex/internal/catalog"
	"github.com/sam-bretz/braindex/internal/library"
)

func (c command) reviewPackage(action string, args []string) error {
	flags := newFlags("package " + action)
	git := flags.Bool("git", false, "load a pinned Git repository")
	catalogID := flags.String("catalog", "", "package ID in a catalog")
	version := flags.String("version", "", "pinned remote package version")
	asJSON := flags.Bool("json", false, "print structured output")
	confirm := flags.String("confirm", "", "manifest SHA-256 from preview or compare")
	baseline := flags.String("baseline", "", "original package directory for an older installation")
	pos, err := parse(flags, args)
	if err != nil {
		return err
	}
	remote := *git || *catalogID != ""
	arity := 2
	usage := "BRAIN DIRECTORY_OR_URL [--git | --catalog PACKAGE_ID] [--version VERSION] [--baseline DIRECTORY] [--json]"
	switch action {
	case "preview":
		arity = 1
		usage = "DIRECTORY_OR_URL [--git | --catalog PACKAGE_ID] [--version VERSION] [--json]"
	case "upgrade":
		usage += " --confirm SHA256"
	}
	if len(pos) != arity || (remote != (*version != "")) || (*git && *catalogID != "") ||
		(action == "preview" && *baseline != "") || (action == "upgrade") != (*confirm != "") {
		return fmt.Errorf("usage: braindex package %s %s", action, usage)
	}
	address := pos[len(pos)-1]
	candidate, err := loadPackageCandidate(address, *git, *catalogID, *version)
	if err != nil {
		return err
	}
	if action == "preview" {
		preview := candidate.Preview()
		if *asJSON {
			return c.json(preview)
		}
		return c.printPackagePreview(preview)
	}
	if action == "upgrade" && *confirm != candidate.Digest {
		return errors.New("confirmation does not match candidate manifest SHA-256; preview or compare again")
	}
	store, err := library.Open(c.root)
	if err != nil {
		return err
	}
	brainID := pos[0]
	comparison, err := candidate.Compare(store, brainID, nil)
	var original *brainpackage.Candidate
	if errors.Is(err, library.ErrPackageBaseline) {
		installed, err := store.InstalledPackage(brainID)
		if err != nil {
			return err
		}
		var verified brainpackage.Candidate
		switch {
		case *baseline != "":
			verified, err = brainpackage.Open(*baseline)
		case remote:
			verified, err = loadPackageCandidate(address, *git, *catalogID, installed.Version)
		default:
			return fmt.Errorf("%w; supply --baseline DIRECTORY", library.ErrPackageBaseline)
		}
		if err != nil {
			return fmt.Errorf("load original installed package: %w", err)
		}
		original = &verified
		comparison, err = candidate.Compare(store, brainID, original)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if action == "compare" {
		if *asJSON {
			return c.json(comparison)
		}
		return c.printPackageComparison(comparison)
	}
	receipt, err := candidate.Upgrade(store, comparison, original)
	if err != nil {
		return err
	}
	if *asJSON {
		return c.json(receipt)
	}
	fmt.Fprintf(c.out, "Installed %s@%s in %s\nManifest SHA-256: %s\nRun braindex index rebuild to refresh search.\n",
		receipt.PackageID, receipt.Version, brainID, receipt.Digest)
	return nil
}

func (c command) printPackageComparison(comparison brainpackage.Comparison) error {
	fmt.Fprintf(c.out, "%s: %s → %s\nManifest SHA-256: %s\n", comparison.BrainID,
		comparison.Installed.Version, comparison.Candidate.Version, comparison.Candidate.Digest)
	for _, change := range comparison.Changes {
		fmt.Fprintf(c.out, "  %s %q (%s, searchable before: %t, after: %t)\n", change.Change,
			firstNonempty(change.AfterTitle, change.BeforeTitle), change.ID, change.BeforeSearchable, change.AfterSearchable)
		if change.Change == "changed" {
			fmt.Fprintf(c.out, "    revision %.12s → %.12s\n", change.BeforeRevision, change.AfterRevision)
		}
	}
	fmt.Fprintf(c.out, "%d local sources to keep, %d conflicts\n", len(comparison.LocalSources), len(comparison.Conflicts))
	for _, id := range comparison.LocalSources {
		fmt.Fprintf(c.out, "  local %s\n", id)
	}
	for _, id := range comparison.Conflicts {
		fmt.Fprintf(c.out, "  local changes to package source %s\n", id)
	}
	return nil
}

func loadPackageCandidate(address string, git bool, catalogID, version string) (brainpackage.Candidate, error) {
	switch {
	case git:
		return loadGitPackage(address, version)
	case catalogID != "":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return catalog.Load(ctx, address, catalogID, version)
	default:
		return brainpackage.Open(address)
	}
}

func (c command) printPackagePreview(preview brainpackage.Preview) error {
	fmt.Fprintf(c.out, "%q (%s@%s)\n", preview.Title, preview.PackageID, preview.Version)
	if preview.Publisher != "" {
		fmt.Fprintf(c.out, "Declared publisher: %q (unverified)\n", preview.Publisher)
	}
	fmt.Fprintf(c.out, "Origin: %q\nManifest SHA-256: %s\n", preview.Origin, preview.Digest)
	if preview.Commit != "" {
		fmt.Fprintf(c.out, "Git commit: %s\n", preview.Commit)
	}
	if preview.ArchiveDigest != "" {
		fmt.Fprintf(c.out, "Archive SHA-256: %s\n", preview.ArchiveDigest)
	}
	fmt.Fprintf(c.out, "%d searchable after indexing, %d reference-only\n", preview.Searchable, preview.References)
	for _, item := range preview.Sources {
		kind := "reference"
		if item.Searchable {
			kind = "bundled text"
		}
		fmt.Fprintf(c.out, "  %s [%s] %q\n", item.ID, kind, firstNonempty(item.Title, item.URL))
		if item.Title != "" && item.URL != "" && item.Title != item.URL {
			fmt.Fprintf(c.out, "    %q\n", item.URL)
		}
	}
	return nil
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
