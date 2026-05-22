package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

// withMockKeyring swaps in an in-memory keyring for the test and clears
// any prior entry under (service, account). It also flips the backend
// env var to "keyring" for the test's duration.
func withMockKeyring(t *testing.T) {
	t.Helper()
	keyring.MockInit()
	t.Setenv("WHOOP_TOKEN_BACKEND", "keyring")
}

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

func TestOAuth2Config(t *testing.T) {
	cfg := &Config{ClientID: "id", ClientSecret: "secret", RedirectURL: "http://localhost/cb"}
	oc := cfg.OAuth2Config()
	if oc.ClientID != "id" || oc.ClientSecret != "secret" {
		t.Fatalf("credentials missing in oauth config: %+v", oc)
	}
	if oc.Endpoint.AuthURL != AuthURL || oc.Endpoint.TokenURL != TokenURL {
		t.Fatalf("wrong endpoints: %+v", oc.Endpoint)
	}
	if len(oc.Scopes) == 0 {
		t.Fatal("expected default scopes")
	}
}

func TestTokenStorePathOverride(t *testing.T) {
	t.Setenv("WHOOP_TOKEN_FILE", "/tmp/override.json")
	got, err := TokenStorePath()
	if err != nil {
		t.Fatalf("TokenStorePath: %v", err)
	}
	if got != "/tmp/override.json" {
		t.Fatalf("got %q, want override path", got)
	}
}

func TestTokenStorePathDefaultsToUserConfigDir(t *testing.T) {
	t.Setenv("WHOOP_TOKEN_FILE", "")
	// Force os.UserConfigDir to succeed: just call and expect a non-empty path.
	got, err := TokenStorePath()
	if err != nil {
		// In some sandboxed environments UserConfigDir may fail; that's the
		// branch we're targeting in TestTokenStorePathUserConfigDirError.
		t.Skip("UserConfigDir unavailable in this environment")
	}
	if !strings.HasSuffix(got, filepath.Join("whoop-mcp", "token.json")) {
		t.Fatalf("unexpected path: %s", got)
	}
}

func TestTokenStorePathUserConfigDirError(t *testing.T) {
	t.Setenv("WHOOP_TOKEN_FILE", "")
	orig := userConfigDir
	t.Cleanup(func() { userConfigDir = orig })
	userConfigDir = func() (string, error) { return "", errors.New("no config dir") }
	if _, err := TokenStorePath(); err == nil {
		t.Fatal("expected error from userConfigDir")
	}
}

func TestSaveTokenPropagatesPathError(t *testing.T) {
	t.Setenv("WHOOP_TOKEN_FILE", "")
	orig := userConfigDir
	t.Cleanup(func() { userConfigDir = orig })
	userConfigDir = func() (string, error) { return "", errors.New("no config dir") }
	if err := SaveToken(&oauth2.Token{RefreshToken: "r"}); err == nil {
		t.Fatal("expected save error from path failure")
	}
}

func TestSaveTokenPropagatesMarshalError(t *testing.T) {
	withTempTokenFile(t)
	orig := marshalIndent
	t.Cleanup(func() { marshalIndent = orig })
	marshalIndent = func(any, string, string) ([]byte, error) {
		return nil, errors.New("marshal boom")
	}
	err := SaveToken(&oauth2.Token{RefreshToken: "r"})
	if err == nil || !strings.Contains(err.Error(), "marshal boom") {
		t.Fatalf("expected marshal error to surface, got %v", err)
	}
}

func TestLoadTokenPropagatesPathError(t *testing.T) {
	t.Setenv("WHOOP_TOKEN_FILE", "")
	orig := userConfigDir
	t.Cleanup(func() { userConfigDir = orig })
	userConfigDir = func() (string, error) { return "", errors.New("no config dir") }
	if _, err := LoadToken(); err == nil {
		t.Fatal("expected load error from path failure")
	}
}

func TestSaveTokenPropagatesMkdirError(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	t.Setenv("WHOOP_TOKEN_FILE", filepath.Join(parent, "nested", "token.json"))
	if err := SaveToken(&oauth2.Token{RefreshToken: "r"}); err == nil {
		t.Fatal("expected mkdir failure for read-only parent")
	}
}

func TestLoadTokenParseError(t *testing.T) {
	path := withTempTokenFile(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	if _, err := LoadToken(); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestExpiresIn(t *testing.T) {
	if d := ExpiresIn(nil); d != 0 {
		t.Fatalf("nil token: got %v, want 0", d)
	}
	if d := ExpiresIn(&oauth2.Token{}); d != 0 {
		t.Fatalf("zero expiry: got %v, want 0", d)
	}
	future := time.Now().Add(time.Hour)
	d := ExpiresIn(&oauth2.Token{Expiry: future})
	if d <= 0 || d > time.Hour+time.Second {
		t.Fatalf("expected ~1h, got %v", d)
	}
}

// fakeTokenSource is a controllable oauth2.TokenSource.
type fakeTokenSource struct {
	tok *oauth2.Token
	err error
}

func (f *fakeTokenSource) Token() (*oauth2.Token, error) { return f.tok, f.err }

func TestConfigTokenSourceMissingToken(t *testing.T) {
	withTempTokenFile(t) // path exists but empty/missing
	cfg := &Config{ClientID: "id", ClientSecret: "s", RedirectURL: "http://l/cb"}
	if _, err := cfg.TokenSource(context.Background()); err == nil {
		t.Fatal("expected error when no token file exists")
	}
}

func TestConfigTokenSourceSuccess(t *testing.T) {
	withTempTokenFile(t)
	if err := SaveToken(&oauth2.Token{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	cfg := &Config{ClientID: "id", ClientSecret: "s", RedirectURL: "http://l/cb"}
	src, err := cfg.TokenSource(context.Background())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	tok, err := src.Token()
	if err != nil {
		t.Fatalf("Token(): %v", err)
	}
	if tok.AccessToken != "a" {
		t.Fatalf("unexpected token: %+v", tok)
	}
}

func TestPersistingTokenSourceSavesOnRefresh(t *testing.T) {
	path := withTempTokenFile(t)
	fresh := &oauth2.Token{AccessToken: "new", RefreshToken: "r2"}
	src := &persistingTokenSource{src: &fakeTokenSource{tok: fresh}}
	tok, err := src.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != fresh {
		t.Fatal("expected to return the underlying token unchanged")
	}
	loaded, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if loaded.AccessToken != "new" {
		t.Fatalf("token not persisted; loaded=%+v path=%s", loaded, path)
	}
}

func TestPersistingTokenSourcePropagatesError(t *testing.T) {
	src := &persistingTokenSource{src: &fakeTokenSource{err: errors.New("refresh failed")}}
	if _, err := src.Token(); err == nil || !strings.Contains(err.Error(), "refresh failed") {
		t.Fatalf("expected refresh error, got %v", err)
	}
}

func TestPersistingTokenSourcePropagatesSaveError(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("WHOOP_TOKEN_FILE", filepath.Join(parent, "nested", "token.json"))
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	fresh := &oauth2.Token{AccessToken: "new", RefreshToken: "r2"}
	src := &persistingTokenSource{src: &fakeTokenSource{tok: fresh}}
	if _, err := src.Token(); err == nil {
		t.Fatal("expected save error to surface")
	}
}

func TestResolveBackendDefaultsToFile(t *testing.T) {
	t.Setenv("WHOOP_TOKEN_BACKEND", "")
	if b := ResolveBackend(); b != BackendFile {
		t.Fatalf("default backend = %q, want file", b)
	}
}

func TestResolveBackendKeyringCaseInsensitive(t *testing.T) {
	t.Setenv("WHOOP_TOKEN_BACKEND", "KeYrInG")
	if b := ResolveBackend(); b != BackendKeyring {
		t.Fatalf("backend = %q, want keyring", b)
	}
}

func TestTokenStoreLocationFile(t *testing.T) {
	t.Setenv("WHOOP_TOKEN_BACKEND", "")
	t.Setenv("WHOOP_TOKEN_FILE", "/tmp/whatever.json")
	if got := TokenStoreLocation(); got != "/tmp/whatever.json" {
		t.Fatalf("got %q, want file path", got)
	}
}

func TestTokenStoreLocationFileError(t *testing.T) {
	t.Setenv("WHOOP_TOKEN_BACKEND", "")
	t.Setenv("WHOOP_TOKEN_FILE", "")
	orig := userConfigDir
	t.Cleanup(func() { userConfigDir = orig })
	userConfigDir = func() (string, error) { return "", errors.New("nope") }
	if got := TokenStoreLocation(); got != "<token file>" {
		t.Fatalf("got %q, want fallback string", got)
	}
}

func TestTokenStoreLocationKeyring(t *testing.T) {
	withMockKeyring(t)
	t.Setenv("WHOOP_KEYRING_ACCOUNT", "")
	got := TokenStoreLocation()
	if !strings.Contains(got, "OS keychain") || !strings.Contains(got, "account=default") {
		t.Fatalf("got %q, want OS keychain description with default account", got)
	}
}

func TestTokenStoreLocationKeyringCustomAccount(t *testing.T) {
	withMockKeyring(t)
	t.Setenv("WHOOP_KEYRING_ACCOUNT", "alice")
	got := TokenStoreLocation()
	if !strings.Contains(got, "account=alice") {
		t.Fatalf("got %q, want alice account", got)
	}
}

func TestKeyringSaveLoadRoundTrip(t *testing.T) {
	withMockKeyring(t)
	tok := &oauth2.Token{AccessToken: "a", RefreshToken: "r"}
	if err := SaveToken(tok); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	got, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got.AccessToken != "a" || got.RefreshToken != "r" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestKeyringLoadMissing(t *testing.T) {
	withMockKeyring(t)
	if _, err := LoadToken(); err == nil {
		t.Fatal("expected error when keyring is empty")
	}
}

func TestKeyringSaveTokenSetError(t *testing.T) {
	withMockKeyring(t)
	orig := keyringSet
	t.Cleanup(func() { keyringSet = orig })
	keyringSet = func(string, string, string) error { return errors.New("set boom") }
	if err := SaveToken(&oauth2.Token{AccessToken: "x"}); err == nil {
		t.Fatal("expected keyring set error")
	}
}

func TestKeyringSaveTokenMarshalError(t *testing.T) {
	withMockKeyring(t)
	orig := marshalIndent
	t.Cleanup(func() { marshalIndent = orig })
	marshalIndent = func(any, string, string) ([]byte, error) {
		return nil, errors.New("marshal boom")
	}
	if err := SaveToken(&oauth2.Token{AccessToken: "x"}); err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestKeyringLoadTokenParseError(t *testing.T) {
	withMockKeyring(t)
	if err := keyring.Set(keyringService, keyringAccount(), "{not json"); err != nil {
		t.Fatalf("seed keyring: %v", err)
	}
	if _, err := LoadToken(); err == nil {
		t.Fatal("expected JSON parse error")
	}
}

func TestKeyringSeedRefreshTokenIfMissing(t *testing.T) {
	withMockKeyring(t)
	if err := SeedRefreshTokenIfMissing("seed-rt"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tok, err := LoadToken()
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if tok.RefreshToken != "seed-rt" {
		t.Fatalf("RefreshToken = %q, want seed-rt", tok.RefreshToken)
	}
	// Existing entry: no overwrite.
	if err := SeedRefreshTokenIfMissing("other-rt"); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	tok2, _ := LoadToken()
	if tok2.RefreshToken != "seed-rt" {
		t.Fatalf("seed overwrote existing entry: %q", tok2.RefreshToken)
	}
}

func TestErrNoTokenSentinelExported(t *testing.T) {
	if ErrNoToken == nil || ErrNoToken.Error() == "" {
		t.Fatal("ErrNoToken should be a non-nil sentinel")
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
