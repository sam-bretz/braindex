package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sam-bretz/braindex/internal/brainpackage"
	"github.com/sam-bretz/braindex/internal/library"
)

func (c command) brainPackage(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: braindex package export|validate|install|preview|compare|upgrade")
	}
	switch args[0] {
	case "export":
		flags := newFlags("package export")
		version := flags.String("version", "", "package version")
		packageID := flags.String("id", "", "package identity")
		publisher := flags.String("publisher", "", "publisher name")
		var include stringsFlag
		flags.Var(&include, "include", "redistributable source ID to bundle")
		pos, err := parse(flags, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 2 || *version == "" {
			return errors.New("usage: braindex package export BRAIN DIRECTORY --version VERSION [--include SOURCE_ID]")
		}
		store, err := library.Open(c.root)
		if err != nil {
			return err
		}
		manifest, err := brainpackage.Export(store, brainpackage.ExportOptions{
			BrainID: pos[0], Directory: pos[1], PackageID: *packageID, Version: *version, Publisher: *publisher, Include: include,
		})
		if err != nil {
			return err
		}
		return c.json(manifest)
	case "validate":
		if len(args) != 2 {
			return errors.New("usage: braindex package validate DIRECTORY")
		}
		manifest, err := brainpackage.Validate(args[1])
		if err != nil {
			return err
		}
		return c.json(manifest)
	case "preview", "compare", "upgrade":
		return c.reviewPackage(args[0], args[1:])
	case "install":
		flags := newFlags("package install")
		brainID := flags.String("brain", "", "local brain ID")
		git := flags.Bool("git", false, "install from a Git repository")
		version := flags.String("version", "", "pinned Git package version")
		pos, err := parse(flags, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 || (*git != (*version != "")) {
			return errors.New("usage: braindex package install DIRECTORY_OR_REPO [--brain ID] [--git --version VERSION]")
		}
		store, err := library.Open(c.root)
		if err != nil {
			return err
		}
		var manifest brainpackage.Manifest
		if *git {
			manifest, err = installGit(store, pos[0], *version, *brainID)
		} else {
			manifest, err = brainpackage.Install(store, pos[0], *brainID, "")
		}
		if err != nil {
			return err
		}
		return c.json(manifest)
	default:
		return fmt.Errorf("unknown package command %q", args[0])
	}
}

func installGit(store *library.Store, repository, version, brainID string) (brainpackage.Manifest, error) {
	candidate, err := loadGitPackage(repository, version)
	if err != nil {
		return brainpackage.Manifest{}, err
	}
	if err := candidate.Install(store, brainID); err != nil {
		return brainpackage.Manifest{}, err
	}
	return candidate.Manifest, nil
}

func loadGitPackage(repository, version string) (brainpackage.Candidate, error) {
	if len(version) > 128 {
		return brainpackage.Candidate{}, errors.New("invalid package version")
	}
	parsed, err := url.Parse(repository)
	if err != nil {
		return brainpackage.Candidate{}, errors.New("invalid Git repository")
	}
	if parsed.IsAbs() {
		if parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" {
			return brainpackage.Candidate{}, errors.New("use an HTTPS Git URL without credentials, query, or fragment")
		}
	} else {
		repository, err = filepath.Abs(repository)
		if err != nil {
			return brainpackage.Candidate{}, err
		}
	}
	directory, err := os.MkdirTemp("", "braindex-git-")
	if err != nil {
		return brainpackage.Candidate{}, err
	}
	defer os.RemoveAll(directory)
	checkout := filepath.Join(directory, "package")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	runGit := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + directory,
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_TERMINAL_PROMPT=0",
			"GIT_ALLOW_PROTOCOL=file:https",
		}
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git package %s: %w", args[0], err)
		}
		return output, nil
	}
	tag := "refs/tags/v" + version
	if _, err := runGit("check-ref-format", tag); err != nil {
		return brainpackage.Candidate{}, errors.New("invalid Git package version")
	}
	if _, err := runGit("-c", "core.hooksPath=/dev/null", "clone", "--quiet", "--no-local", "--no-checkout", "--filter=blob:none", "--depth=1", "--branch", "v"+version, "--", repository, checkout); err != nil {
		return brainpackage.Candidate{}, err
	}
	commit, err := runGit("-C", checkout, "rev-parse", "--verify", tag+"^{commit}")
	if err != nil {
		return brainpackage.Candidate{}, err
	}
	head, err := runGit("-C", checkout, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return brainpackage.Candidate{}, err
	}
	if strings.TrimSpace(string(head)) != strings.TrimSpace(string(commit)) {
		return brainpackage.Candidate{}, errors.New("Git checkout did not resolve to the requested tag")
	}
	tree, err := runGit("-C", checkout, "ls-tree", "-rlz", "HEAD", "--", "brain.json", "sources")
	if err != nil {
		return brainpackage.Candidate{}, err
	}
	paths, err := packageGitPaths(tree)
	if err != nil {
		return brainpackage.Candidate{}, err
	}
	// Only package data enters the worktree, even if the tag also contains
	// unrelated repository contents or package-manager configuration.
	pathspec := filepath.Join(directory, "package-paths")
	if err := os.WriteFile(pathspec, []byte(strings.Join(paths, "\x00")+"\x00"), 0600); err != nil {
		return brainpackage.Candidate{}, err
	}
	if _, err := runGit("-C", checkout, "-c", "core.hooksPath=/dev/null", "checkout", "HEAD", "--pathspec-from-file="+pathspec, "--pathspec-file-nul"); err != nil {
		return brainpackage.Candidate{}, err
	}
	candidate, err := brainpackage.Open(checkout)
	if err != nil {
		return brainpackage.Candidate{}, err
	}
	if candidate.Manifest.Version != version {
		return brainpackage.Candidate{}, fmt.Errorf("requested version %q does not match manifest version %q", version, candidate.Manifest.Version)
	}
	candidate.Commit = strings.TrimSpace(string(commit))
	candidate.Origin = repository + "#" + candidate.Commit
	return candidate, nil
}

func packageGitPaths(tree []byte) ([]string, error) {
	const maxEntries, maxTotal = 2048, 129 << 20
	var total int64
	var paths []string
	var manifest bool
	for _, record := range strings.Split(strings.TrimSuffix(string(tree), "\x00"), "\x00") {
		header, name, ok := strings.Cut(record, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 4 || fields[0] != "100644" || fields[1] != "blob" {
			return nil, errors.New("Git package entries must be regular data files")
		}
		limit := int64(16 << 20)
		if name == "brain.json" {
			manifest = true
			limit = 1 << 20
		} else {
			id := strings.TrimSuffix(strings.TrimPrefix(name, "sources/"), ".json")
			if name != "sources/"+id+".json" || library.ValidateID(id) != nil {
				return nil, errors.New("unexpected file in Git package data")
			}
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size < 0 || size > limit || size > maxTotal-total || len(paths) == maxEntries {
			return nil, errors.New("Git package exceeds content limits")
		}
		total += size
		paths = append(paths, name)
	}
	if !manifest {
		return nil, errors.New("Git package is missing brain.json")
	}
	return paths, nil
}
