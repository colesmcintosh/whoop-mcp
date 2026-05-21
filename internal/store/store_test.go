package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestNewRejectsEmptyDir(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestNewID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if len(id) != 48 { // 24 bytes hex-encoded
			t.Fatalf("NewID length = %d, want 48", len(id))
		}
		if seen[id] {
			t.Fatalf("NewID returned duplicate: %s", id)
		}
		seen[id] = true
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	want := &Record{
		ID:        "abc123",
		Token:     &oauth2.Token{AccessToken: "at", RefreshToken: "rt"},
		UserEmail: "user@example.com",
		UserName:  "Test User",
		CreatedAt: now,
	}
	if err := s.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get("abc123")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != want.ID || got.UserEmail != want.UserEmail || got.UserName != want.UserName {
		t.Fatalf("Get mismatch: got %+v want %+v", got, want)
	}
	if got.Token == nil || got.Token.AccessToken != "at" || got.Token.RefreshToken != "rt" {
		t.Fatalf("token round-trip mismatch: %+v", got.Token)
	}
}

func TestGetMissingReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get("does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPathRejectsInvalidIDs(t *testing.T) {
	s := newTestStore(t)
	for _, bad := range []string{"", "../escape", "a/b", "a\\b", "with.dot"} {
		if _, err := s.Get(bad); err == nil {
			t.Fatalf("Get(%q) expected error", bad)
		}
		if err := s.Put(&Record{ID: bad}); err == nil {
			t.Fatalf("Put with id %q expected error", bad)
		}
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete("never-existed"); err != nil {
		t.Fatalf("delete missing should be nil, got %v", err)
	}
	rec := &Record{ID: "doomed", Token: &oauth2.Token{RefreshToken: "x"}}
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete("doomed"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("doomed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete, expected ErrNotFound, got %v", err)
	}
}

func TestPutPermissionsAndAtomic(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Put(&Record{ID: "perm", Token: &oauth2.Token{RefreshToken: "r"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "perm.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file mode = %o, want 0o600", mode)
	}
	// No leftover .tmp file.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("found leftover tmp file %q", e.Name())
		}
	}
}
