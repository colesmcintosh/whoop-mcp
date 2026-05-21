package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colesmcintosh/whoop-mcp/internal/auth"
	"github.com/colesmcintosh/whoop-mcp/internal/store"
	"github.com/colesmcintosh/whoop-mcp/internal/whoop"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

func chmodRO(dir string) error { return os.Chmod(dir, 0o500) }
func chmodRW(dir string) error { return os.Chmod(dir, 0o700) }

// testApp wires an app against fake OAuth and Whoop API servers.
type testApp struct {
	app         *app
	oauthServer *httptest.Server
	whoopServer *httptest.Server
	tokenCalls  *int
	apiCalls    *[]string
	revokeCalls *int
}

func newTestApp(t *testing.T, opts ...func(*testApp)) *testApp {
	t.Helper()

	tokenCalls := 0
	apiCalls := []string{}
	revokeCalls := 0

	oauthMux := http.NewServeMux()
	oauthMux.HandleFunc("/auth", func(w http.ResponseWriter, _ *http.Request) {
		// Not directly used; the redirect goes through the browser, but
		// the test exercises /oauth/callback directly. Stubbed so any
		// stray request returns something sane.
		w.WriteHeader(http.StatusOK)
	})
	oauthMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"r2","token_type":"Bearer","expires_in":3600}`))
	})
	oauthServer := httptest.NewServer(oauthMux)
	t.Cleanup(oauthServer.Close)

	whoopMux := http.NewServeMux()
	whoopMux.HandleFunc("/v2/user/profile/basic", func(w http.ResponseWriter, _ *http.Request) {
		apiCalls = append(apiCalls, "profile")
		_, _ = w.Write([]byte(`{"first_name":"Ada","last_name":"Lovelace","email":"ada@example.com"}`))
	})
	whoopMux.HandleFunc("/v2/user/measurement/body", func(w http.ResponseWriter, _ *http.Request) {
		apiCalls = append(apiCalls, "body")
		_, _ = w.Write([]byte(`{"height_meter":1.8}`))
	})
	whoopMux.HandleFunc("/v2/cycle", func(w http.ResponseWriter, _ *http.Request) {
		apiCalls = append(apiCalls, "cycles")
		_, _ = w.Write([]byte(`{"records":[]}`))
	})
	whoopMux.HandleFunc("/v2/cycle/", func(w http.ResponseWriter, r *http.Request) {
		apiCalls = append(apiCalls, "cycle:"+r.URL.Path)
		_, _ = w.Write([]byte(`{"id":"c1"}`))
	})
	whoopMux.HandleFunc("/v2/recovery", func(w http.ResponseWriter, _ *http.Request) {
		apiCalls = append(apiCalls, "recovery")
		_, _ = w.Write([]byte(`{"records":[]}`))
	})
	whoopMux.HandleFunc("/v2/activity/sleep", func(w http.ResponseWriter, _ *http.Request) {
		apiCalls = append(apiCalls, "sleep")
		_, _ = w.Write([]byte(`{"records":[]}`))
	})
	whoopMux.HandleFunc("/v2/activity/sleep/", func(w http.ResponseWriter, r *http.Request) {
		apiCalls = append(apiCalls, "sleep:"+r.URL.Path)
		_, _ = w.Write([]byte(`{"id":"s1"}`))
	})
	whoopMux.HandleFunc("/v2/activity/workout", func(w http.ResponseWriter, _ *http.Request) {
		apiCalls = append(apiCalls, "workout")
		_, _ = w.Write([]byte(`{"records":[]}`))
	})
	whoopMux.HandleFunc("/v2/activity/workout/", func(w http.ResponseWriter, r *http.Request) {
		apiCalls = append(apiCalls, "workout:"+r.URL.Path)
		_, _ = w.Write([]byte(`{"id":"w1"}`))
	})
	whoopMux.HandleFunc("/v2/user/access", func(w http.ResponseWriter, _ *http.Request) {
		revokeCalls++
		w.WriteHeader(http.StatusNoContent)
	})
	whoopServer := httptest.NewServer(whoopMux)
	t.Cleanup(whoopServer.Close)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	cfg := &auth.Config{
		ClientID:     "cid",
		ClientSecret: "csec",
		RedirectURL:  "http://example.test/oauth/callback",
	}
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Scopes:       []string{"read:profile"},
		Endpoint: oauth2.Endpoint{
			AuthURL:   oauthServer.URL + "/auth",
			TokenURL:  oauthServer.URL + "/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}

	a := &app{
		cfg:          cfg,
		store:        st,
		publicURL:    "http://example.test",
		oauth:        oauthCfg,
		states:       newStateStore(),
		loginRL:      newRateLimiter(0.1, 6),
		whoopBaseURL: whoopServer.URL,
	}

	ta := &testApp{
		app:         a,
		oauthServer: oauthServer,
		whoopServer: whoopServer,
		tokenCalls:  &tokenCalls,
		apiCalls:    &apiCalls,
		revokeCalls: &revokeCalls,
	}
	for _, opt := range opts {
		opt(ta)
	}
	return ta
}

func (ta *testApp) seedUser(t *testing.T, id string, tok *oauth2.Token) *store.Record {
	t.Helper()
	if tok == nil {
		tok = &oauth2.Token{
			AccessToken:  "valid",
			RefreshToken: "rt",
			Expiry:       time.Now().Add(time.Hour),
		}
	}
	rec := &store.Record{ID: id, Token: tok, CreatedAt: time.Now()}
	if err := ta.app.store.Put(rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return rec
}

// ---- Helpers ----

func TestSecurityHeadersWritesExpectedHeaders(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	for _, k := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Permissions-Policy", "Content-Security-Policy"} {
		if w.Header().Get(k) == "" {
			t.Errorf("missing %s header", k)
		}
	}
}

func TestRateLimiterBlocksAfterBurst(t *testing.T) {
	rl := newRateLimiter(0, 2)
	if !rl.allow("ip") {
		t.Fatal("first should be allowed")
	}
	if !rl.allow("ip") {
		t.Fatal("second should be allowed")
	}
	if rl.allow("ip") {
		t.Fatal("third should be blocked")
	}
	if !rl.allow("other-ip") {
		t.Fatal("different key should be unaffected")
	}
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	rl := newRateLimiter(10, 1)
	if !rl.allow("k") {
		t.Fatal("first allowed")
	}
	if rl.allow("k") {
		t.Fatal("immediate second blocked")
	}
	time.Sleep(150 * time.Millisecond)
	if !rl.allow("k") {
		t.Fatal("after refill should be allowed")
	}
}

func TestRateLimiterEvictsStale(t *testing.T) {
	rl := newRateLimiter(1, 1)
	// Pre-fill with > 2048 entries with old timestamps so the next allow()
	// trips the eviction branch.
	for i := 0; i < 2050; i++ {
		rl.buckets[string(rune(i))+"-key"] = &tokenBucket{
			tokens: 0,
			last:   time.Now().Add(-30 * time.Minute),
		}
	}
	rl.allow("new")
	if len(rl.buckets) > 100 {
		t.Fatalf("expected stale entries evicted, got %d", len(rl.buckets))
	}
}

func TestClientIPFromXForwardedFor(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":          "1.2.3.4",
		"1.2.3.4, 5.6.7.8": "1.2.3.4",
		"":                 "",
	}
	for xff, want := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
			if got := clientIP(r); got != want {
				t.Errorf("xff=%q got %q want %q", xff, got, want)
			}
		} else {
			if got := clientIP(r); got != "10.0.0.1" {
				t.Errorf("no xff: got %q", got)
			}
		}
	}
}

func TestClientIPMalformedRemoteAddr(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "garbage-no-colon"
	if got := clientIP(r); got != "garbage-no-colon" {
		t.Fatalf("got %q", got)
	}
}

func TestStateStorePutConsume(t *testing.T) {
	s := newStateStore()
	s.put("abc")
	if !s.consume("abc") {
		t.Fatal("first consume should succeed")
	}
	if s.consume("abc") {
		t.Fatal("second consume should fail (single use)")
	}
	if s.consume("never-put") {
		t.Fatal("unknown state should fail")
	}
}

func TestStateStoreExpires(t *testing.T) {
	s := newStateStore()
	s.put("old")
	// Tamper with the timestamp to simulate >10min age.
	s.mu.Lock()
	s.vals["old"] = time.Now().Add(-11 * time.Minute)
	s.mu.Unlock()
	if s.consume("old") {
		t.Fatal("expired state should not consume")
	}
	// The next put should clean expired entries.
	s.put("fresh")
	s.mu.Lock()
	_, oldStillThere := s.vals["old"]
	s.mu.Unlock()
	if oldStillThere {
		t.Fatal("expired entries should be evicted by put")
	}
}

func TestRandToken(t *testing.T) {
	a, err := randToken()
	if err != nil {
		t.Fatalf("randToken: %v", err)
	}
	b, _ := randToken()
	if a == "" || a == b {
		t.Fatalf("expected unique non-empty tokens, got %q / %q", a, b)
	}
}

func TestToParams(t *testing.T) {
	in := listInput{Limit: 5, NextToken: "tok", Start: "2026-05-01T00:00:00Z", End: "2026-05-10T00:00:00Z"}
	p, err := in.toParams()
	if err != nil {
		t.Fatalf("toParams: %v", err)
	}
	if p.Limit != 5 || p.NextToken != "tok" {
		t.Fatalf("bad params: %+v", p)
	}
	if p.Start.IsZero() || p.End.IsZero() {
		t.Fatalf("times not parsed: %+v", p)
	}

	if _, err := (listInput{Start: "not a time"}).toParams(); err == nil {
		t.Fatal("expected start parse error")
	}
	if _, err := (listInput{End: "not a time"}).toParams(); err == nil {
		t.Fatal("expected end parse error")
	}
}

func TestHTTPListenAddr(t *testing.T) {
	t.Setenv("MCP_HTTP_ADDR", "")
	t.Setenv("PORT", "")
	if got := httpListenAddr(); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	t.Setenv("PORT", "9000")
	if got := httpListenAddr(); got != ":9000" {
		t.Fatalf("PORT: got %q", got)
	}
	t.Setenv("MCP_HTTP_ADDR", "127.0.0.1:1234")
	if got := httpListenAddr(); got != "127.0.0.1:1234" {
		t.Fatalf("MCP_HTTP_ADDR: got %q", got)
	}
}

func TestJSONResultProducesTextContent(t *testing.T) {
	res := jsonResult(json.RawMessage(`{"x":1}`))
	if len(res.Content) != 1 {
		t.Fatalf("content len = %d", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("not TextContent: %T", res.Content[0])
	}
	if tc.Text != `{"x":1}` {
		t.Fatalf("text = %q", tc.Text)
	}
}

func TestNewServerAndRegisterTools(_ *testing.T) {
	s := newServer()
	c := whoop.NewWithBaseURL(context.Background(), &fakeSrc{}, "http://example")
	registerTools(s, c) // must not panic
}

// ---- HTTP handler tests ----

func TestHealthzEndpoint(t *testing.T) {
	ta := newTestApp(t)
	mux := buildMux(ta.app)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestFaviconEndpoint(t *testing.T) {
	ta := newTestApp(t)
	mux := buildMux(ta.app)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/favicon.svg", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Fatalf("content-type = %q", got)
	}
	if !strings.Contains(w.Body.String(), "<svg") {
		t.Fatal("body missing svg")
	}
}

func TestHandleLandingRoot(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	ta.app.handleLanding(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Whoop MCP") {
		t.Fatal("body missing brand")
	}
}

func TestHandleLandingNotFound(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	ta.app.handleLanding(w, httptest.NewRequest("GET", "/some-other", nil))
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandleLoginRedirects(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/login", nil)
	ta.app.handleLogin(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, ta.oauthServer.URL+"/auth") {
		t.Fatalf("redirect = %q", loc)
	}
	// state cookie set
	if !strings.Contains(w.Header().Get("Set-Cookie"), "whoop_oauth_state") {
		t.Fatal("missing state cookie")
	}
}

func TestHandleLoginRateLimited(t *testing.T) {
	ta := newTestApp(t)
	ta.app.loginRL = newRateLimiter(0, 1)
	w := httptest.NewRecorder()
	ta.app.handleLogin(w, httptest.NewRequest("GET", "/login", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("first should succeed, got %d", w.Code)
	}
	w = httptest.NewRecorder()
	ta.app.handleLogin(w, httptest.NewRequest("GET", "/login", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second should be 429, got %d", w.Code)
	}
}

func TestHandleLoginRandTokenError(t *testing.T) {
	ta := newTestApp(t)
	orig := randReadMain
	t.Cleanup(func() { randReadMain = orig })
	randReadMain = func([]byte) (int, error) { return 0, errors.New("rand boom") }
	w := httptest.NewRecorder()
	ta.app.handleLogin(w, httptest.NewRequest("GET", "/login", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleCallbackErrorParam(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	ta.app.handleCallback(w, httptest.NewRequest("GET", "/oauth/callback?error=access_denied&error_description=nope", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleCallbackBadState(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/oauth/callback?state=mismatch&code=x", nil)
	r.AddCookie(&http.Cookie{Name: "whoop_oauth_state", Value: "different"})
	ta.app.handleCallback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleCallbackMissingCode(t *testing.T) {
	ta := newTestApp(t)
	ta.app.states.put("st")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/oauth/callback?state=st", nil)
	r.AddCookie(&http.Cookie{Name: "whoop_oauth_state", Value: "st"})
	ta.app.handleCallback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleCallbackSuccess(t *testing.T) {
	ta := newTestApp(t)
	ta.app.states.put("ok")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/oauth/callback?state=ok&code=abcd", nil)
	r.AddCookie(&http.Cookie{Name: "whoop_oauth_state", Value: "ok"})
	ta.app.handleCallback(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "/connect/") {
		t.Fatal("body missing /connect URL")
	}
	if !strings.Contains(w.Body.String(), "Ada") {
		t.Fatal("body missing user name from profile fetch")
	}
	if *ta.tokenCalls == 0 {
		t.Fatal("OAuth token endpoint was not called")
	}
}

func TestHandleCallbackTokenExchangeError(t *testing.T) {
	ta := newTestApp(t)
	// Replace the token endpoint with one that errors.
	ta.app.oauth = &oauth2.Config{
		ClientID:     ta.app.oauth.ClientID,
		ClientSecret: ta.app.oauth.ClientSecret,
		RedirectURL:  ta.app.oauth.RedirectURL,
		Scopes:       ta.app.oauth.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   ta.oauthServer.URL + "/auth",
			TokenURL:  "http://127.0.0.1:0/nope",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	ta.app.states.put("ok")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/oauth/callback?state=ok&code=abcd", nil)
	r.AddCookie(&http.Cookie{Name: "whoop_oauth_state", Value: "ok"})
	ta.app.handleCallback(w, r)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleCallbackPersistError(t *testing.T) {
	ta := newTestApp(t)
	ta.app.states.put("ok")
	// Read-only store dir so any Put fails.
	dir := ta.app.store.Dir()
	if err := chmodRO(dir); err != nil {
		t.Skipf("chmod RO not supported: %v", err)
	}
	t.Cleanup(func() { _ = chmodRW(dir) })
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/oauth/callback?state=ok&code=abcd", nil)
	r.AddCookie(&http.Cookie{Name: "whoop_oauth_state", Value: "ok"})
	ta.app.handleCallback(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected persist failure, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleCallbackRandError(t *testing.T) {
	ta := newTestApp(t)
	ta.app.states.put("ok")
	orig := storeNewID
	t.Cleanup(func() { storeNewID = orig })
	storeNewID = func() (string, error) { return "", errors.New("rand boom") }
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/oauth/callback?state=ok&code=abcd", nil)
	r.AddCookie(&http.Cookie{Name: "whoop_oauth_state", Value: "ok"})
	ta.app.handleCallback(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleConnectBogusID(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	ta.app.handleConnect(w, httptest.NewRequest("POST", "/connect/notreal", nil))
	if w.Code != 404 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleConnectEmptyID(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	ta.app.handleConnect(w, httptest.NewRequest("GET", "/connect/", nil))
	if w.Code != 404 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleConnectRoutesToMCP(t *testing.T) {
	ta := newTestApp(t)
	rec := ta.seedUser(t, "uid", nil)
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/connect/"+rec.ID, body)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	ta.app.handleConnect(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"protocolVersion"`) {
		t.Fatalf("MCP init not returned: %s", w.Body.String())
	}
}

func TestHandleDisconnectBogusID(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	ta.app.handleDisconnect(w, httptest.NewRequest("GET", "/disconnect/notreal", nil))
	if w.Code != 404 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleDisconnectInvalidID(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	ta.app.handleDisconnect(w, httptest.NewRequest("GET", "/disconnect/", nil))
	if w.Code != 404 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleDisconnectGETConfirmation(t *testing.T) {
	ta := newTestApp(t)
	rec := ta.seedUser(t, "uid", nil)
	w := httptest.NewRecorder()
	ta.app.handleDisconnect(w, httptest.NewRequest("GET", "/disconnect/"+rec.ID, nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Disconnect") {
		t.Fatal("missing confirmation copy")
	}
}

func TestHandleDisconnectPOSTRevokesAndDeletes(t *testing.T) {
	ta := newTestApp(t)
	rec := ta.seedUser(t, "uid", nil)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/disconnect/"+rec.ID, nil)
	ta.app.handleDisconnect(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if *ta.revokeCalls == 0 {
		t.Fatal("Whoop revoke endpoint not called")
	}
	if _, err := ta.app.store.Get(rec.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("record should be deleted")
	}
}

func TestHandleDisconnectPOSTRevokeErrorStillDeletes(t *testing.T) {
	ta := newTestApp(t)
	// Point the whoop client at a server that returns 500 on revoke.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", 500)
	}))
	t.Cleanup(bad.Close)
	ta.app.whoopBaseURL = bad.URL
	rec := ta.seedUser(t, "uid", nil)
	w := httptest.NewRecorder()
	ta.app.handleDisconnect(w, httptest.NewRequest("POST", "/disconnect/"+rec.ID, nil))
	if w.Code != 200 {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if _, err := ta.app.store.Get(rec.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("record should be deleted even if revoke failed")
	}
}

func TestHandleDisconnectMethodNotAllowed(t *testing.T) {
	ta := newTestApp(t)
	rec := ta.seedUser(t, "uid", nil)
	w := httptest.NewRecorder()
	ta.app.handleDisconnect(w, httptest.NewRequest("PUT", "/disconnect/"+rec.ID, nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

// ---- userTokenSource / persistingUserSource ----

func TestPersistingUserSourcePersistsRefresh(t *testing.T) {
	ta := newTestApp(t)
	rec := ta.seedUser(t, "uid", &oauth2.Token{
		AccessToken:  "old",
		RefreshToken: "rt-old",
		Expiry:       time.Now().Add(-time.Hour), // expired → triggers refresh
	})
	src := ta.app.userTokenSource(rec)
	tok, err := src.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken != "fresh" {
		t.Fatalf("expected refreshed token, got %+v", tok)
	}
	updated, err := ta.app.store.Get(rec.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if updated.Token.AccessToken != "fresh" {
		t.Fatalf("store not updated: %+v", updated.Token)
	}
}

func TestPersistingUserSourceNoOpForCachedToken(t *testing.T) {
	ta := newTestApp(t)
	rec := ta.seedUser(t, "uid", nil) // not expired
	src := ta.app.userTokenSource(rec)
	// Call twice; the underlying source should return the same cached token,
	// and we should not invoke the OAuth token endpoint at all.
	_, _ = src.Token()
	_, _ = src.Token()
	if *ta.tokenCalls != 0 {
		t.Fatalf("expected no token refreshes, got %d", *ta.tokenCalls)
	}
}

func TestPersistingUserSourceErrorPropagates(t *testing.T) {
	src := &persistingUserSource{base: &errSrc{err: errors.New("refresh failed")}, store: nil, id: "x"}
	if _, err := src.Token(); err == nil || !strings.Contains(err.Error(), "refresh failed") {
		t.Fatalf("expected refresh error, got %v", err)
	}
}

func TestPersistingUserSourceStoreReloadError(t *testing.T) {
	ta := newTestApp(t)
	tok := &oauth2.Token{AccessToken: "new"}
	src := &persistingUserSource{
		base:  &staticTokenSrc{tok: tok},
		store: ta.app.store,
		id:    "missing",
	}
	if _, err := src.Token(); err == nil {
		t.Fatal("expected error from store.Get of missing id")
	}
}

func TestPersistingUserSourceStoreSaveError(t *testing.T) {
	ta := newTestApp(t)
	rec := ta.seedUser(t, "uid", &oauth2.Token{AccessToken: "old"})
	// Now strip write permission on the store dir.
	dir := ta.app.store.Dir()
	if err := chmodRO(dir); err != nil {
		t.Skipf("chmod RO not supported: %v", err)
	}
	t.Cleanup(func() { _ = chmodRW(dir) })
	src := &persistingUserSource{
		base:  &staticTokenSrc{tok: &oauth2.Token{AccessToken: "new"}},
		store: ta.app.store,
		id:    rec.ID,
	}
	if _, err := src.Token(); err == nil {
		t.Fatal("expected save error to surface")
	}
}

// ---- Test scaffolding ----

// buildMux mirrors what serveMultiTenant builds, so we can test the
// routing in isolation.
func buildMux(a *app) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(brandMarkSVG))
	})
	mux.Handle("/", securityHeaders(http.HandlerFunc(a.handleLanding)))
	mux.Handle("/login", securityHeaders(http.HandlerFunc(a.handleLogin)))
	mux.Handle("/oauth/callback", securityHeaders(http.HandlerFunc(a.handleCallback)))
	mux.HandleFunc("/connect/", a.handleConnect)
	mux.Handle("/disconnect/", securityHeaders(http.HandlerFunc(a.handleDisconnect)))
	return mux
}

type fakeSrc struct{}

func (fakeSrc) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "x", Expiry: time.Now().Add(time.Hour)}, nil
}

type staticTokenSrc struct{ tok *oauth2.Token }

func (s *staticTokenSrc) Token() (*oauth2.Token, error) { return s.tok, nil }

type errSrc struct{ err error }

func (e *errSrc) Token() (*oauth2.Token, error) { return nil, e.err }

// Keep io referenced when only used inline in a single test.
var _ = io.Discard

// ---- run() entry-point tests ----

func resetEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"WHOOP_CLIENT_ID", "WHOOP_CLIENT_SECRET", "WHOOP_REDIRECT_URI", "WHOOP_TOKEN_FILE", "WHOOP_INITIAL_REFRESH_TOKEN", "PORT", "MCP_HTTP_ADDR", "PUBLIC_URL", "USER_STORE_DIR"} {
		t.Setenv(k, "")
	}
}

func TestRunRejectsMissingCredentials(t *testing.T) {
	resetEnv(t)
	if err := run(context.Background()); err == nil {
		t.Fatal("expected error when credentials are unset")
	}
}

func TestRunHTTPMode(t *testing.T) {
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "x")
	t.Setenv("WHOOP_CLIENT_SECRET", "y")
	t.Setenv("USER_STORE_DIR", t.TempDir())
	t.Setenv("PUBLIC_URL", "http://127.0.0.1:0")
	t.Setenv("MCP_HTTP_ADDR", "127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}

func TestRunStdioMissingToken(t *testing.T) {
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "x")
	t.Setenv("WHOOP_CLIENT_SECRET", "y")
	tokFile := filepath.Join(t.TempDir(), "missing.json")
	t.Setenv("WHOOP_TOKEN_FILE", tokFile)
	if err := run(context.Background()); err == nil {
		t.Fatal("expected token-source error in stdio mode")
	}
}

func TestRunStdioReturnsOnCancel(t *testing.T) {
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "x")
	t.Setenv("WHOOP_CLIENT_SECRET", "y")
	tokFile := filepath.Join(t.TempDir(), "token.json")
	t.Setenv("WHOOP_TOKEN_FILE", tokFile)
	if err := auth.SaveToken(&oauth2.Token{
		AccessToken:  "x",
		RefreshToken: "r",
		Expiry:       time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
		// Either nil or a stdin-closed error is fine — we just need run
		// to return without hanging.
	case <-time.After(3 * time.Second):
		t.Fatal("run(stdio) did not return after cancel")
	}
}

func TestMainInvokesRunAndExitsOnError(t *testing.T) {
	resetEnv(t)
	// Missing credentials → run() returns an error → main() should
	// call osExit(1).
	var exited int
	origExit := osExit
	t.Cleanup(func() { osExit = origExit })
	osExit = func(code int) { exited = code }
	main()
	if exited != 1 {
		t.Fatalf("osExit code = %d, want 1", exited)
	}
}

func TestRunStdioSeedError(t *testing.T) {
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "x")
	t.Setenv("WHOOP_CLIENT_SECRET", "y")
	tokFile := filepath.Join(t.TempDir(), "missing.json")
	t.Setenv("WHOOP_TOKEN_FILE", tokFile)
	t.Setenv("WHOOP_INITIAL_REFRESH_TOKEN", "seed-rt")
	// Make the parent dir read-only so SeedRefreshTokenIfMissing -> SaveToken fails.
	parent := filepath.Dir(tokFile)
	if err := chmodRO(parent); err != nil {
		t.Skip("chmod RO not supported")
	}
	t.Cleanup(func() { _ = chmodRW(parent) })
	if err := run(context.Background()); err == nil {
		t.Fatal("expected seed token error")
	}
}

// ---- Tool invocation tests via in-process MCP transport ----

func TestAllToolsViaMCP(t *testing.T) {
	ta := newTestApp(t)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	mcpSrv := newServer()
	src := &staticTokenSrc{tok: &oauth2.Token{AccessToken: "x", Expiry: time.Now().Add(time.Hour)}}
	wc := whoop.NewWithBaseURL(context.Background(), src, ta.whoopServer.URL)
	registerTools(mcpSrv, wc)

	ctx := context.Background()
	if _, err := mcpSrv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	cases := []struct {
		name string
		args map[string]any
	}{
		{"get_profile", nil},
		{"get_body_measurement", nil},
		{"list_cycles", map[string]any{"limit": 10}},
		{"list_cycles", map[string]any{"start": "2026-05-01T00:00:00Z"}},
		{"get_cycle", map[string]any{"cycle_id": "c1"}},
		{"get_cycle_recovery", map[string]any{"cycle_id": "c1"}},
		{"get_cycle_sleep", map[string]any{"cycle_id": "c1"}},
		{"list_recovery", map[string]any{"limit": 5}},
		{"list_sleep", map[string]any{"limit": 5}},
		{"get_sleep", map[string]any{"id": "s1"}},
		{"list_workouts", map[string]any{"limit": 5}},
		{"get_workout", map[string]any{"id": "w1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if res.IsError {
				t.Fatalf("tool returned error: %v", res.Content)
			}
		})
	}
}

func TestListToolInvalidTimeReturnsError(t *testing.T) {
	ta := newTestApp(t)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	mcpSrv := newServer()
	src := &staticTokenSrc{tok: &oauth2.Token{AccessToken: "x", Expiry: time.Now().Add(time.Hour)}}
	wc := whoop.NewWithBaseURL(context.Background(), src, ta.whoopServer.URL)
	registerTools(mcpSrv, wc)
	ctx := context.Background()
	if _, err := mcpSrv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_cycles",
		Arguments: map[string]any{"start": "not-a-time"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool to report an error for bad time")
	}
}

func TestToolErrorsFromWhoop(t *testing.T) {
	// Whoop server returns 500 for every path.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", 500)
	}))
	t.Cleanup(bad.Close)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	mcpSrv := newServer()
	src := &staticTokenSrc{tok: &oauth2.Token{AccessToken: "x", Expiry: time.Now().Add(time.Hour)}}
	wc := whoop.NewWithBaseURL(context.Background(), src, bad.URL)
	registerTools(mcpSrv, wc)
	ctx := context.Background()
	if _, err := mcpSrv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	cases := []struct {
		name string
		args map[string]any
	}{
		{"get_profile", nil},
		{"get_body_measurement", nil},
		{"list_cycles", nil},
		{"get_cycle", map[string]any{"cycle_id": "c1"}},
		{"get_cycle_recovery", map[string]any{"cycle_id": "c1"}},
		{"get_cycle_sleep", map[string]any{"cycle_id": "c1"}},
		{"list_recovery", nil},
		{"list_sleep", nil},
		{"get_sleep", map[string]any{"id": "s1"}},
		{"list_workouts", nil},
		{"get_workout", map[string]any{"id": "w1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected tool error for %s", tc.name)
			}
		})
	}
}

func TestListToolsInvalidTime(t *testing.T) {
	ta := newTestApp(t)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	mcpSrv := newServer()
	src := &staticTokenSrc{tok: &oauth2.Token{AccessToken: "x", Expiry: time.Now().Add(time.Hour)}}
	wc := whoop.NewWithBaseURL(context.Background(), src, ta.whoopServer.URL)
	registerTools(mcpSrv, wc)
	ctx := context.Background()
	if _, err := mcpSrv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = sess.Close() }()

	for _, name := range []string{"list_recovery", "list_sleep", "list_workouts"} {
		t.Run(name, func(t *testing.T) {
			res, err := sess.CallTool(ctx, &mcp.CallToolParams{
				Name:      name,
				Arguments: map[string]any{"start": "not-a-time"},
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if !res.IsError {
				t.Fatal("expected parse error")
			}
		})
	}
}

func TestServeMultiTenantDefaultStoreDirAndRoutes(t *testing.T) {
	// Default store dir kicks in when USER_STORE_DIR unset and writable. We
	// can't actually use /data/users in CI; instead, leave USER_STORE_DIR
	// pointing at a writable path so we cover the routes branch but
	// exercise the default-dir branch separately with chmod RO.
	t.Setenv("USER_STORE_DIR", t.TempDir())
	t.Setenv("PUBLIC_URL", "http://127.0.0.1:0")
	cfg := &auth.Config{ClientID: "x", ClientSecret: "y", RedirectURL: "http://x/cb"}
	ctx, cancel := context.WithCancel(context.Background())

	// Pick a port we control so we can hit it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- serveMultiTenant(ctx, addr, cfg) }()
	// Poll the health endpoint until the server accepts requests.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Exercise the inline route handlers.
	for _, path := range []string{"/healthz", "/favicon.svg"} {
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: status %d", path, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serveMultiTenant: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serveMultiTenant did not shut down")
	}
}

func TestServeMultiTenantDefaultStoreDir(t *testing.T) {
	// Exercise the storeDir == "" branch. Default /data/users isn't
	// writable, so we expect serveMultiTenant to return a store-creation
	// error.
	t.Setenv("USER_STORE_DIR", "")
	t.Setenv("PUBLIC_URL", "https://example.test")
	cfg := &auth.Config{ClientID: "x", ClientSecret: "y", RedirectURL: "http://x/cb"}
	err := serveMultiTenant(context.Background(), "127.0.0.1:0", cfg)
	if err == nil {
		t.Skip("default /data/users is writable on this host; cannot exercise the failure branch")
	}
}

func TestServeMultiTenantListenError(t *testing.T) {
	t.Setenv("USER_STORE_DIR", t.TempDir())
	t.Setenv("PUBLIC_URL", "http://127.0.0.1:0")
	// Bind a socket so the address is in use, then try to start the server
	// on the same address — ListenAndServe should fail.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	cfg := &auth.Config{ClientID: "x", ClientSecret: "y", RedirectURL: "http://x/cb"}
	if err := serveMultiTenant(context.Background(), ln.Addr().String(), cfg); err == nil {
		t.Fatal("expected listen error")
	}
}

// ---- Remaining branches ----

func TestNewWhoopClientUsesDefaultBaseURL(t *testing.T) {
	a := &app{} // no whoopBaseURL set
	c := a.newWhoopClient(context.Background(), &staticTokenSrc{tok: &oauth2.Token{AccessToken: "x"}})
	if c == nil {
		t.Fatal("expected client")
	}
}

func TestHandleConnectStripsSubPath(t *testing.T) {
	ta := newTestApp(t)
	rec := ta.seedUser(t, "uid", nil)
	// MCP sessions use sub-paths; even a GET that doesn't initialize should
	// at minimum route to the handler (which returns 4xx or 405) rather than
	// landing in the 404 branch we already test.
	w := httptest.NewRecorder()
	ta.app.handleConnect(w, httptest.NewRequest("GET", "/connect/"+rec.ID+"/messages", nil))
	if w.Code == 404 {
		t.Fatalf("expected MCP handler routing, got 404")
	}
}

func TestServeMultiTenantMissingPublicURL(t *testing.T) {
	t.Setenv("USER_STORE_DIR", t.TempDir())
	t.Setenv("PUBLIC_URL", "")
	cfg := &auth.Config{ClientID: "x", ClientSecret: "y", RedirectURL: "http://x/cb"}
	if err := serveMultiTenant(context.Background(), ":0", cfg); err == nil {
		t.Fatal("expected error when PUBLIC_URL unset")
	}
}

func TestServeMultiTenantStoreError(t *testing.T) {
	parent := t.TempDir()
	if err := chmodRO(parent); err != nil {
		t.Skip("chmod RO not supported")
	}
	t.Cleanup(func() { _ = chmodRW(parent) })
	t.Setenv("USER_STORE_DIR", filepath.Join(parent, "nested", "users"))
	t.Setenv("PUBLIC_URL", "https://example.test")
	cfg := &auth.Config{ClientID: "x", ClientSecret: "y", RedirectURL: "http://x/cb"}
	if err := serveMultiTenant(context.Background(), ":0", cfg); err == nil {
		t.Fatal("expected error when store dir cannot be created")
	}
}

func TestServeMultiTenantBootsAndShutsDown(t *testing.T) {
	t.Setenv("USER_STORE_DIR", t.TempDir())
	t.Setenv("PUBLIC_URL", "http://127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	cfg := &auth.Config{ClientID: "x", ClientSecret: "y", RedirectURL: "http://x/cb"}
	done := make(chan error, 1)
	go func() { done <- serveMultiTenant(ctx, "127.0.0.1:0", cfg) }()
	time.Sleep(150 * time.Millisecond) // let it bind
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveMultiTenant: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serveMultiTenant did not shut down")
	}
}
