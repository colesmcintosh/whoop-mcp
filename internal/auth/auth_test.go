package auth

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/oauth2"
)

// withTempTokenFile points TokenStorePath at a fresh temp file for the test.
func withTempTokenFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token.json")
	t.Setenv("WHOOP_TOKEN_FILE", path)
	return path
}

func TestLoadConfigFromEnvRequiresCredentials(t *testing.T) {
	t.Setenv("WHOOP_CLIENT_ID", "")
	t.Setenv("WHOOP_CLIENT_SECRET", "")
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error when credentials are unset")
	}
}

func TestLoadConfigFromEnvDefaultsRedirect(t *testing.T) {
	t.Setenv("WHOOP_CLIENT_ID", "cid")
	t.Setenv("WHOOP_CLIENT_SECRET", "csec")
	t.Setenv("WHOOP_REDIRECT_URI", "")
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if cfg.RedirectURL == "" {
		t.Fatal("RedirectURL should default to localhost callback when unset")
	}
}

func TestSaveLoadTokenRoundTrip(t *testing.T) {
	path := withTempTokenFile(t)
	tok := &oauth2.Token{AccessToken: "a", RefreshToken: "r"}
	if err := SaveToken(tok); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("token file mode = %o, want 0o600", mode)
	}
	got, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got.AccessToken != "a" || got.RefreshToken != "r" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestSeedRefreshTokenIfMissing(t *testing.T) {
	path := withTempTokenFile(t)

	// Empty input is a no-op.
	if err := SeedRefreshTokenIfMissing(""); err != nil {
		t.Fatalf("empty seed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no token file after empty seed, stat err = %v", err)
	}

	// Seeding when no file exists writes the refresh token.
	if err := SeedRefreshTokenIfMissing("seed-rt"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tok, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken after seed: %v", err)
	}
	if tok.RefreshToken != "seed-rt" {
		t.Fatalf("RefreshToken = %q, want %q", tok.RefreshToken, "seed-rt")
	}

	// Seeding when a file already exists is a no-op (does not overwrite).
	if err := SeedRefreshTokenIfMissing("different-rt"); err != nil {
		t.Fatalf("seed-on-existing: %v", err)
	}
	tok2, _ := LoadToken()
	if tok2.RefreshToken != "seed-rt" {
		t.Fatalf("seed overwrote existing token; got %q", tok2.RefreshToken)
	}
}
