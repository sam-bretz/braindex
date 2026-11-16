package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type Follow struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"`
	Brain        string       `json:"brain"`
	URL          string       `json:"url,omitempty"`
	Author       string       `json:"author,omitempty"`
	Hint         string       `json:"hint,omitempty"`
	Adapter      string       `json:"adapter,omitempty"`
	AllowPrivate bool         `json:"allow_private,omitempty"`
	Items        []FollowItem `json:"items,omitempty"`
}

type FollowItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	URL      string `json:"url,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Revision string `json:"revision,omitempty"`
	Status   string `json:"status"`
}

func (s *Store) followPath(id string) string {
	return filepath.Join(s.MetaDir(), "follows", id+".json")
}

func (s *Store) Follows() ([]Follow, error) {
	dir := filepath.Join(s.MetaDir(), "follows")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Follow{}, nil
	}
	if err != nil {
		return nil, err
	}
	follows := make([]Follow, 0)
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok {
			continue
		}
		follow, err := s.Follow(id)
		if err != nil {
			return nil, err
		}
		follows = append(follows, follow)
	}
	return follows, nil
}

func (s *Store) Follow(id string) (Follow, error) {
	var follow Follow
	if err := ValidateID(id); err != nil {
		return follow, err
	}
	err := readJSON(s.followPath(id), &follow)
	if errors.Is(err, os.ErrNotExist) {
		return follow, fmt.Errorf("follow %q: %w", id, ErrNotFound)
	}
	return follow, err
}

func (s *Store) AddFollow(follow Follow) error {
	return s.withFollowLock(follow.ID, func() error {
		if _, err := s.Follow(follow.ID); !errors.Is(err, ErrNotFound) {
			if err != nil {
				return err
			}
			return fmt.Errorf("follow %q: %w", follow.ID, ErrExists)
		}
		return writeJSON(s.followPath(follow.ID), follow)
	})
}

// UpdateFollow serializes refresh and approval for one subscription. The
// caller can update library sources while holding this per-follow lock.
func (s *Store) UpdateFollow(id string, update func(*Follow) error) error {
	return s.withFollowLock(id, func() error {
		follow, err := s.Follow(id)
		if err != nil {
			return err
		}
		if err := update(&follow); err != nil {
			return err
		}
		return writeJSON(s.followPath(id), follow)
	})
}

func (s *Store) withFollowLock(id string, fn func() error) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	path := s.followPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	return fn()
}
