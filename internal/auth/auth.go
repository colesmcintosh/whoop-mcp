// Package auth holds Whoop OAuth 2.0 configuration and token-storage
// helpers shared by the stdio and HTTP entry points.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/oauth2"
)

// Whoop OAuth 2.0 endpoints.
const (
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

// TokenStorePath returns the path where the token is persisted.
func TokenStorePath() (string, error) {
	if override := os.Getenv("WHOOP_TOKEN_FILE"); override != "" {
		return override, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "whoop-mcp", "token.json"), nil
}

// SaveToken persists the token to disk with 0600 permissions.
func SaveToken(tok *oauth2.Token) error {
	path, err := TokenStorePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// SeedRefreshTokenIfMissing writes a token file containing only the given
// refresh token if no token file exists yet. On the first refresh, Whoop
// will mint a fresh access+refresh token pair which the persisting token
// source then writes back to disk.
//
// This is the bootstrap mechanism for hosted deployments where running
// the browser-based OAuth flow on the server is impractical: authorize
// once locally with whoop-auth, copy the refresh_token from
// ~/Library/Application Support/whoop-mcp/token.json into a Railway env
// var, and the server will seed the file on first start.
func SeedRefreshTokenIfMissing(refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	if _, err := LoadToken(); err == nil {
		return nil
	}
	tok := &oauth2.Token{RefreshToken: refreshToken}
	return SaveToken(tok)
}

// LoadToken reads a token previously saved with SaveToken.
func LoadToken() (*oauth2.Token, error) {
	path, err := TokenStorePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// persistingTokenSource wraps an oauth2.TokenSource and writes refreshed
// tokens back to disk. Whoop invalidates the previous refresh token on
// every refresh, so persistence is mandatory.
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
		return nil, fmt.Errorf("load token (run whoop-auth first): %w", err)
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
