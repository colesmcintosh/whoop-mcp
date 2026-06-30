// Package auth holds Whoop OAuth 2.0 configuration and token-storage
// helpers shared by the stdio and HTTP entry points.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

// Whoop OAuth 2.0 endpoints. These are vars (not consts) so tests can
// point them at an httptest fixture. In production they should not be
// mutated.
var (
	AuthURL  = "https://api.prod.whoop.com/oauth/oauth2/auth"
	TokenURL = "https://api.prod.whoop.com/oauth/oauth2/token"
)

// DefaultScopes are the scopes whoop-mcp requests during the OAuth
// flow: read-only access to every endpoint the MCP tools surface, plus
// "offline" so Whoop issues a refresh token.
var DefaultScopes = []string{
	"read:recovery",
	"read:cycles",
	"read:sleep",
	"read:workout",
	"read:profile",
	"read:body_measurement",
	"offline",
}

// Config holds the Whoop OAuth client credentials.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// LoadConfigFromEnv reads credentials from environment variables.
func LoadConfigFromEnv() (*Config, error) {
	clientID := os.Getenv("WHOOP_CLIENT_ID")
	clientSecret := os.Getenv("WHOOP_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("WHOOP_CLIENT_ID and WHOOP_CLIENT_SECRET must be set")
	}
	redirect := os.Getenv("WHOOP_REDIRECT_URI")
	if redirect == "" {
		redirect = "http://localhost:8080/callback"
	}
	return &Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirect,
	}, nil
}

// OAuth2Config builds an oauth2.Config for the Whoop API.
func (c *Config) OAuth2Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		RedirectURL:  c.RedirectURL,
		Scopes:       DefaultScopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   AuthURL,
			TokenURL:  TokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// Backend names a place where the stdio-mode OAuth token is persisted.
type Backend string

// Supported token backends, selected via WHOOP_TOKEN_BACKEND.
const (
	BackendFile    Backend = "file"
	BackendKeyring Backend = "keyring"
)

const (
	keyringService    = "whoop-mcp"
	keyringDefaultAcc = "default"
)

// ResolveBackend returns the token backend selected by WHOOP_TOKEN_BACKEND.
// Default is BackendFile so existing setups keep working unchanged.
func ResolveBackend() Backend {
	switch strings.ToLower(os.Getenv("WHOOP_TOKEN_BACKEND")) {
	case "keyring":
		return BackendKeyring
	default:
		return BackendFile
	}
}

func keyringAccount() string {
	if a := os.Getenv("WHOOP_KEYRING_ACCOUNT"); a != "" {
		return a
	}
	return keyringDefaultAcc
}

// userConfigDir, marshalIndent, keyringSet, keyringGet, keyringDelete are
// swappable in tests to exercise the OS- and encoding-level error paths.
var (
	userConfigDir = os.UserConfigDir
	marshalIndent = json.MarshalIndent
	keyringSet    = keyring.Set
	keyringGet    = keyring.Get
	keyringDelete = keyring.Delete
)

// TokenStorePath returns the file-backend path where the token is
// persisted. When the keyring backend is selected it still returns the
// file path for callers that want to display the fallback location;
// reads and writes do not actually touch this path.
func TokenStorePath() (string, error) {
	if override := os.Getenv("WHOOP_TOKEN_FILE"); override != "" {
		return override, nil
	}
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "whoop-mcp", "token.json"), nil
}

// TokenStoreLocation returns a human-readable description of where the
// token is stored, suitable for log output.
func TokenStoreLocation() string {
	if ResolveBackend() == BackendKeyring {
		return fmt.Sprintf("OS keychain (service=%s, account=%s)", keyringService, keyringAccount())
	}
	path, err := TokenStorePath()
	if err != nil {
		return "<token file>"
	}
	return path
}

// SaveToken persists the token using the configured backend.
//
// File backend: writes JSON to TokenStorePath() with 0600 permissions.
// Keyring backend: writes the JSON payload to the OS keychain
// (macOS Keychain / GNOME Keyring / Windows Credential Manager).
func SaveToken(tok *oauth2.Token) error {
	data, err := marshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	if ResolveBackend() == BackendKeyring {
		return keyringSet(keyringService, keyringAccount(), string(data))
	}
	path, err := TokenStorePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// DeleteToken removes the stored token from the configured backend. It is
// not an error to delete a token that does not exist. Used when the user
// disconnects their Whoop account.
func DeleteToken() error {
	if ResolveBackend() == BackendKeyring {
		if err := keyringDelete(keyringService, keyringAccount()); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return err
		}
		return nil
	}
	path, err := TokenStorePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// HasToken reports whether a token is currently stored. It is a thin
// wrapper over LoadToken used by the server to decide whether the Whoop
// account is connected yet.
func HasToken() bool {
	_, err := LoadToken()
	return err == nil
}

// LoadToken reads a token previously saved with SaveToken.
func LoadToken() (*oauth2.Token, error) {
	var data []byte
	if ResolveBackend() == BackendKeyring {
		s, err := keyringGet(keyringService, keyringAccount())
		if err != nil {
			return nil, err
		}
		data = []byte(s)
	} else {
		path, err := TokenStorePath()
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		data = b
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// ErrNoToken is reported by LoadToken-ish callers when no token has been
// stored yet, regardless of which backend is selected.
var ErrNoToken = errors.New("no token stored")

// persistingTokenSource wraps an oauth2.TokenSource and writes refreshed
// tokens back to the configured backend. Whoop invalidates the previous
// refresh token on every refresh, so persistence is mandatory.
type persistingTokenSource struct {
	src oauth2.TokenSource
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.src.Token()
	if err != nil {
		return nil, err
	}
	if err := SaveToken(tok); err != nil {
		return nil, fmt.Errorf("persist refreshed token: %w", err)
	}
	return tok, nil
}

// TokenSource returns an oauth2.TokenSource backed by the stored token,
// refreshing and persisting automatically when it expires.
func (c *Config) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	tok, err := LoadToken()
	if err != nil {
		return nil, fmt.Errorf("load token (connect your Whoop account at /login first): %w", err)
	}
	src := c.OAuth2Config().TokenSource(ctx, tok)
	return &persistingTokenSource{src: src}, nil
}

// ExpiresIn reports time until the token expires, or zero if unknown.
func ExpiresIn(tok *oauth2.Token) time.Duration {
	if tok == nil || tok.Expiry.IsZero() {
		return 0
	}
	return time.Until(tok.Expiry)
}
