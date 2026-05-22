// Command whoop-auth runs the OAuth 2.0 authorization-code flow against
// the Whoop API and persists the resulting token to disk so the MCP
// server can use it for subsequent requests.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"time"

	"github.com/colesmcintosh/whoop-mcp/internal/auth"
	"golang.org/x/oauth2"
)

// Swappable for tests:
//   - logFatal lets a test capture the failure and avoid terminating
//     the process.
//   - openBrowserFn lets a test substitute a no-op for the real
//     browser launch.
//   - randRead is the source of randomness for the OAuth state value.
//   - goos lets a test exercise every branch of openBrowser.
//   - callbackTimeout shortens the OAuth wait window in tests.
//   - generateVerifier is the PKCE code-verifier source; swappable so a
//     test can pin it to a known value (or force regeneration).
var (
	logFatal         = log.Fatal
	openBrowserFn    = openBrowser
	randRead         = rand.Read
	goos             = runtime.GOOS
	callbackTimeout  = 5 * time.Minute
	generateVerifier = oauth2.GenerateVerifier
	// startCmd actually launches the resolved command. Indirected so the
	// switch-dispatch test can exercise every branch without spawning
	// the host's default browser.
	startCmd = func(c *exec.Cmd) error { return c.Start() }
)

func main() {
	if err := run(); err != nil {
		logFatal(err)
	}
}

func run() error {
	cfg, err := auth.LoadConfigFromEnv()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	redirectURL, err := url.Parse(cfg.RedirectURL)
	if err != nil {
		return fmt.Errorf("invalid WHOOP_REDIRECT_URI: %w", err)
	}
	if redirectURL.Hostname() != "localhost" && redirectURL.Hostname() != "127.0.0.1" {
		return fmt.Errorf("redirect URI must point at localhost for this CLI; got %s", cfg.RedirectURL)
	}
	listenAddr := redirectURL.Host
	if redirectURL.Port() == "" {
		listenAddr = redirectURL.Hostname() + ":80"
	}
	callbackPath := redirectURL.Path
	if callbackPath == "" {
		callbackPath = "/"
	}

	state, err := randomState()
	if err != nil {
		return fmt.Errorf("generate state: %w", err)
	}

	oauthCfg := cfg.OAuth2Config()
	verifier := generateVerifier()
	authURL := oauthCfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAddr, err)
	}
	defer func() { _ = listener.Close() }()

	type result struct {
		code string
		err  error
	}
	resultCh := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errParam := q.Get("error"); errParam != "" {
			msg := fmt.Errorf("authorization error: %s — %s", errParam, q.Get("error_description"))
			http.Error(w, msg.Error(), http.StatusBadRequest)
			resultCh <- result{err: msg}
			return
		}
		if got := q.Get("state"); got != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			resultCh <- result{err: fmt.Errorf("state mismatch: got %q", got)}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			resultCh <- result{err: fmt.Errorf("missing code in callback")}
			return
		}
		_, _ = fmt.Fprint(w, "<html><body><h2>Whoop MCP authorized.</h2><p>You may close this tab.</p></body></html>")
		resultCh <- result{code: code}
	})

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			resultCh <- result{err: fmt.Errorf("serve: %w", err)}
		}
	}()

	fmt.Println("Opening browser to authorize Whoop access...")
	fmt.Printf("If your browser doesn't open, visit:\n  %s\n\n", authURL)
	_ = openBrowserFn(authURL)

	ctx, cancel := context.WithTimeout(context.Background(), callbackTimeout)
	defer cancel()

	var code string
	select {
	case res := <-resultCh:
		if res.err != nil {
			return fmt.Errorf("callback: %w", res.err)
		}
		code = res.code
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for browser callback")
	}

	_ = server.Shutdown(context.Background())

	tok, err := oauthCfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return fmt.Errorf("exchange code for token: %w", err)
	}
	if err := auth.SaveToken(tok); err != nil {
		return fmt.Errorf("save token: %w", err)
	}

	fmt.Printf("\nToken saved to %s\n", auth.TokenStoreLocation())
	if !tok.Expiry.IsZero() {
		fmt.Printf("Access token expires at %s (in %s)\n", tok.Expiry.Format(time.RFC3339), time.Until(tok.Expiry).Round(time.Second))
	}
	if tok.RefreshToken != "" {
		fmt.Println("Refresh token stored — the MCP server will renew access tokens automatically.")
	}
	return nil
}

func randomState() (string, error) {
	buf := make([]byte, 24)
	if _, err := randRead(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch goos {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return startCmd(cmd)
}
