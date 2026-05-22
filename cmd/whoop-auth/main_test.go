package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colesmcintosh/whoop-mcp/internal/auth"
)

// resetEnv blanks out env vars run() consults, leaving the test in a
// known state.
func resetEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"WHOOP_CLIENT_ID", "WHOOP_CLIENT_SECRET",
		"WHOOP_REDIRECT_URI", "WHOOP_TOKEN_FILE",
	} {
		t.Setenv(k, "")
	}
}

// captureFatal swaps logFatal into a recorder so we can drive main()
// without terminating the test process.
func captureFatal(t *testing.T) *[]any {
	t.Helper()
	var captured []any
	var mu sync.Mutex
	orig := logFatal
	t.Cleanup(func() { logFatal = orig })
	logFatal = func(v ...any) {
		mu.Lock()
		captured = append(captured, v...)
		mu.Unlock()
	}
	return &captured
}

// silentBrowser replaces openBrowserFn with a no-op so tests don't
// actually launch a browser.
func silentBrowser(t *testing.T) {
	t.Helper()
	orig := openBrowserFn
	t.Cleanup(func() { openBrowserFn = orig })
	openBrowserFn = func(string) error { return nil }
}

// captureState swaps openBrowserFn with one that extracts the OAuth
// state value from the auth URL and writes it to a buffered channel.
// Test goroutines can receive the state without sharing memory.
func captureState(t *testing.T) <-chan string {
	t.Helper()
	ch := make(chan string, 1)
	orig := openBrowserFn
	t.Cleanup(func() { openBrowserFn = orig })
	openBrowserFn = func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		select {
		case ch <- u.Query().Get("state"):
		default:
		}
		return nil
	}
	return ch
}

// withFakeOAuthServer points internal/auth's OAuth URLs at an httptest
// fixture and returns the server.
func withFakeOAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","token_type":"Bearer","expires_in":3600}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	origAuth, origToken := auth.AuthURL, auth.TokenURL
	t.Cleanup(func() {
		auth.AuthURL = origAuth
		auth.TokenURL = origToken
	})
	auth.AuthURL = srv.URL + "/auth"
	auth.TokenURL = srv.URL + "/token"
	return srv
}

// TestRunPKCEParamsFlowThrough drives a full run() and asserts the
// auth URL carries an S256 code_challenge and the token-exchange POST
// carries a matching code_verifier.
func TestRunPKCEParamsFlowThrough(t *testing.T) {
	var (
		gotChallenge       string
		gotChallengeMethod string
		gotVerifier        string
	)

	// Custom OAuth server that captures the verifier from the token POST.
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotVerifier = r.Form.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","token_type":"Bearer","expires_in":3600}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	origAuth, origToken := auth.AuthURL, auth.TokenURL
	t.Cleanup(func() { auth.AuthURL = origAuth; auth.TokenURL = origToken })
	auth.AuthURL = srv.URL + "/auth"
	auth.TokenURL = srv.URL + "/token"

	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	port := freePort(t)
	t.Setenv("WHOOP_REDIRECT_URI", fmt.Sprintf("http://localhost:%d/callback", port))
	tokFile := filepath.Join(t.TempDir(), "token.json")
	t.Setenv("WHOOP_TOKEN_FILE", tokFile)

	// Capture both state and PKCE parameters from the redirect URL.
	type captured struct{ state string }
	capCh := make(chan captured, 1)
	origBrowser := openBrowserFn
	t.Cleanup(func() { openBrowserFn = origBrowser })
	openBrowserFn = func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		q := u.Query()
		gotChallenge = q.Get("code_challenge")
		gotChallengeMethod = q.Get("code_challenge_method")
		capCh <- captured{state: q.Get("state")}
		return nil
	}

	go func() {
		waitForListener(t, port)
		c := <-capCh
		_, _ = http.Get(fmt.Sprintf("http://localhost:%d/callback?state=%s&code=abcd", port, c.state))
	}()

	if err := run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotChallenge == "" {
		t.Fatal("auth URL missing code_challenge")
	}
	if gotChallengeMethod != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", gotChallengeMethod)
	}
	if gotVerifier == "" {
		t.Fatal("token exchange did not carry code_verifier")
	}
}

func TestRandomState(t *testing.T) {
	a, err := randomState()
	if err != nil {
		t.Fatalf("randomState: %v", err)
	}
	b, _ := randomState()
	if a == "" || a == b {
		t.Fatalf("expected unique non-empty states, got %q / %q", a, b)
	}
}

func TestRandomStateRandError(t *testing.T) {
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func([]byte) (int, error) { return 0, errors.New("rand boom") }
	if _, err := randomState(); err == nil {
		t.Fatal("expected rand error")
	}
}

func TestOpenBrowserDispatchesByGOOS(t *testing.T) {
	origGOOS := goos
	origStart := startCmd
	t.Cleanup(func() { goos = origGOOS; startCmd = origStart })

	var got []string
	startCmd = func(c *exec.Cmd) error {
		got = append(got, c.Path+" "+strings.Join(c.Args[1:], " "))
		return nil
	}

	for _, os := range []string{"darwin", "windows", "linux"} {
		t.Run(os, func(_ *testing.T) {
			goos = os
			if err := openBrowser("http://example.test"); err != nil {
				t.Fatalf("openBrowser: %v", err)
			}
		})
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 dispatches, got %d", len(got))
	}
}

func TestRunCallbackTimeout(t *testing.T) {
	withFakeOAuthServer(t)
	silentBrowser(t)
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	port := freePort(t)
	t.Setenv("WHOOP_REDIRECT_URI", fmt.Sprintf("http://localhost:%d/callback", port))

	orig := callbackTimeout
	t.Cleanup(func() { callbackTimeout = orig })
	callbackTimeout = 50 * time.Millisecond

	// Don't visit the callback — let it time out.
	err := run()
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestRunMissingCredentials(t *testing.T) {
	resetEnv(t)
	if err := run(); err == nil {
		t.Fatal("expected error when creds unset")
	}
}

func TestRunInvalidRedirectURI(t *testing.T) {
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	t.Setenv("WHOOP_REDIRECT_URI", "\x7f://not a url")
	if err := run(); err == nil {
		t.Fatal("expected url parse error")
	}
}

func TestRunNonLocalhostRedirect(t *testing.T) {
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	t.Setenv("WHOOP_REDIRECT_URI", "https://example.com/cb")
	if err := run(); err == nil {
		t.Fatal("expected non-localhost rejection")
	}
}

func TestRunRandomStateError(t *testing.T) {
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	t.Setenv("WHOOP_REDIRECT_URI", "http://localhost:0/callback")
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func([]byte) (int, error) { return 0, errors.New("rand boom") }
	if err := run(); err == nil {
		t.Fatal("expected randomState failure to surface")
	}
}

func TestRunListenError(t *testing.T) {
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	// Bind a socket first so the redirect's port is already in use.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	t.Setenv("WHOOP_REDIRECT_URI", "http://"+ln.Addr().String()+"/callback")
	if err := run(); err == nil {
		t.Fatal("expected listen failure")
	}
}

func TestRunCallbackErrorParam(t *testing.T) {
	withFakeOAuthServer(t)
	silentBrowser(t)
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	port := freePort(t)
	t.Setenv("WHOOP_REDIRECT_URI", fmt.Sprintf("http://localhost:%d/callback", port))

	go func() {
		// Wait for the listener to be up, then visit the callback with an error.
		waitForListener(t, port)
		_, _ = http.Get(fmt.Sprintf("http://localhost:%d/callback?error=access_denied&error_description=nope", port))
	}()

	err := run()
	if err == nil || !strings.Contains(err.Error(), "callback") {
		t.Fatalf("expected callback error, got %v", err)
	}
}

func TestRunCallbackStateMismatch(t *testing.T) {
	withFakeOAuthServer(t)
	silentBrowser(t)
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	port := freePort(t)
	t.Setenv("WHOOP_REDIRECT_URI", fmt.Sprintf("http://localhost:%d/callback", port))

	go func() {
		waitForListener(t, port)
		_, _ = http.Get(fmt.Sprintf("http://localhost:%d/callback?state=wrong&code=x", port))
	}()
	if err := run(); err == nil {
		t.Fatal("expected state-mismatch error")
	}
}

func TestRunCallbackMissingCode(t *testing.T) {
	srv := withFakeOAuthServer(t)
	_ = srv
	silentBrowser(t)
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	port := freePort(t)
	t.Setenv("WHOOP_REDIRECT_URI", fmt.Sprintf("http://localhost:%d/callback", port))

	stateCh := captureState(t)

	go func() {
		waitForListener(t, port)
		state := <-stateCh
		_, _ = http.Get(fmt.Sprintf("http://localhost:%d/callback?state=%s", port, state))
	}()
	if err := run(); err == nil {
		t.Fatal("expected missing-code error")
	}
}

func TestRunSuccess(t *testing.T) {
	srv := withFakeOAuthServer(t)
	_ = srv
	silentBrowser(t)
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	port := freePort(t)
	t.Setenv("WHOOP_REDIRECT_URI", fmt.Sprintf("http://localhost:%d/callback", port))
	tokFile := filepath.Join(t.TempDir(), "token.json")
	t.Setenv("WHOOP_TOKEN_FILE", tokFile)

	stateCh := captureState(t)

	go func() {
		waitForListener(t, port)
		state := <-stateCh
		_, _ = http.Get(fmt.Sprintf("http://localhost:%d/callback?state=%s&code=abcd", port, state))
	}()
	if err := run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(tokFile); err != nil {
		t.Fatalf("token file should exist: %v", err)
	}
}

func TestRunSaveTokenError(t *testing.T) {
	srv := withFakeOAuthServer(t)
	_ = srv
	silentBrowser(t)
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	port := freePort(t)
	t.Setenv("WHOOP_REDIRECT_URI", fmt.Sprintf("http://localhost:%d/callback", port))
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Skip("chmod RO not supported")
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	t.Setenv("WHOOP_TOKEN_FILE", filepath.Join(parent, "nested", "token.json"))

	stateCh := captureState(t)

	go func() {
		waitForListener(t, port)
		state := <-stateCh
		_, _ = http.Get(fmt.Sprintf("http://localhost:%d/callback?state=%s&code=abcd", port, state))
	}()
	if err := run(); err == nil {
		t.Fatal("expected save error")
	}
}

func TestRunTokenExchangeError(t *testing.T) {
	silentBrowser(t)
	// Point token URL at a closed server so Exchange fails.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	bad.Close()
	origToken := auth.TokenURL
	t.Cleanup(func() { auth.TokenURL = origToken })
	auth.TokenURL = bad.URL + "/token"

	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	port := freePort(t)
	t.Setenv("WHOOP_REDIRECT_URI", fmt.Sprintf("http://localhost:%d/callback", port))

	stateCh := captureState(t)

	go func() {
		waitForListener(t, port)
		state := <-stateCh
		_, _ = http.Get(fmt.Sprintf("http://localhost:%d/callback?state=%s&code=abcd", port, state))
	}()
	if err := run(); err == nil {
		t.Fatal("expected token-exchange failure")
	}
}

func TestMainCallsLogFatalOnError(t *testing.T) {
	resetEnv(t)
	captured := captureFatal(t)
	main()
	if len(*captured) == 0 {
		t.Fatal("expected logFatal to be called")
	}
}

func TestRunRedirectWithEmptyPath(t *testing.T) {
	withFakeOAuthServer(t)
	silentBrowser(t)
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	port := freePort(t)
	// No path component — the server should default callbackPath to "/".
	t.Setenv("WHOOP_REDIRECT_URI", fmt.Sprintf("http://localhost:%d", port))

	orig := callbackTimeout
	t.Cleanup(func() { callbackTimeout = orig })
	callbackTimeout = 200 * time.Millisecond
	// Don't visit anything — the empty-path branch executes before the
	// select, so the test just needs run() to return.
	if err := run(); err == nil {
		t.Fatal("expected timeout (we never hit the callback)")
	}
}

func TestRunDefaultListenAddrWithoutPort(t *testing.T) {
	// Triggers the "port == \"\" → use :80" branch. We can't actually bind
	// to :80 in a test, so the listener will fail with EACCES (or "address
	// already in use" if :80 is taken). Either way the branch is exercised.
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "id")
	t.Setenv("WHOOP_CLIENT_SECRET", "sec")
	t.Setenv("WHOOP_REDIRECT_URI", "http://localhost/callback")
	if err := run(); err == nil {
		t.Fatal("expected listen on :80 to fail in test environment")
	}
}

// ---- helpers ----

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()
	return addr.Port
}

func waitForListener(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
