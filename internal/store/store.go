// Package store persists per-user Whoop OAuth tokens to disk so the
// multi-tenant MCP server can look up each connector's credentials by
// opaque user id.
//
// One JSON file per user lives under the store directory; writes are
// atomic via write-then-rename. This is intentionally minimal — for
// higher concurrency a real database would be a better fit, but for
// small-scale personal sharing the file-per-user layout keeps
// dependencies (and failure modes) small.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Record is one user's persisted state.
type Record struct {
	ID         string        `json:"id"`
	Token      *oauth2.Token `json:"token"`
	UserEmail  string        `json:"user_email,omitempty"`
	UserName   string        `json:"user_name,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	LastSeenAt time.Time     `json:"last_seen_at,omitempty"`
}

// Store is a filesystem-backed user store.
type Store struct {
	dir string
}

// New creates the store directory if needed and returns a Store rooted there.
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("store: empty directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: mkdir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// randRead and marshalIndent are swappable in tests to exercise the
// crypto/rand and json.MarshalIndent error paths respectively.
var (
	randRead      = rand.Read
	marshalIndent = json.MarshalIndent
)

// Dir returns the directory the store is rooted at. Exposed for tests
// that need to manipulate the underlying files (e.g. to simulate I/O
// errors by changing permissions).
func (s *Store) Dir() string { return s.dir }

// NewID returns a fresh random user id, suitable for use in URLs.
func NewID() (string, error) {
	buf := make([]byte, 24)
	if _, err := randRead(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ErrNotFound is returned by Get when no record exists for the id.
var ErrNotFound = errors.New("store: user not found")

func (s *Store) path(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return "", fmt.Errorf("store: invalid id %q", id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// Get loads the record for id, returning ErrNotFound if it doesn't exist.
func (s *Store) Get(id string) (*Record, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("store: parse %s: %w", id, err)
	}
	return &rec, nil
}

// Delete removes the record for id. It is not an error to delete a
// record that does not exist.
func (s *Store) Delete(id string) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Put atomically writes the record to disk.
func (s *Store) Put(rec *Record) error {
	path, err := s.path(rec.ID)
	if err != nil {
		return err
	}
	data, err := marshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
