package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colesmcintosh/whoop-mcp/internal/auth"
	"github.com/colesmcintosh/whoop-mcp/internal/whoop"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

func chmodRO(dir string) error { return os.Chmod(dir, 0o500) }
func chmodRW(dir string) error { return os.Chmod(dir, 0o700) }

// skipIfRoot skips tests that rely on filesystem permission denial, which
// root bypasses (so a read-only directory is still writable).
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod-based permission denial is ineffective")
	}
}

// testApp wires an app against fake OAuth and Whoop API servers, with the
// single-token store pointed at a per-test temp file.
type testApp struct {
	app         *app
	oauthServer *httptest.Server
	whoopServer *httptest.Server
	tokenFile   string
	tokenCalls  *int
	apiCalls    *[]string
	revokeCalls *int
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()

	tokenCalls := 0
	apiCalls := []string{}
	revokeCalls := 0

	oauthMux := http.NewServeMux()
	oauthMux.HandleFunc("/auth", func(w http.ResponseWriter, _ *http.Request) {
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

	// Single-token store on disk, per-test.
	tokenFile := filepath.Join(t.TempDir(), "token.json")
	t.Setenv("WHOOP_TOKEN_BACKEND", "file")
	t.Setenv("WHOOP_TOKEN_FILE", tokenFile)

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
		publicURL:    "http://example.test",
		oauth:        oauthCfg,
		states:       newStateStore(),
		loginRL:      newRateLimiter(0.1, 6),
		whoopBaseURL: whoopServer.URL,
	}

	return &testApp{
		app:         a,
		oauthServer: oauthServer,
		whoopServer: whoopServer,
		tokenFile:   tokenFile,
		tokenCalls:  &tokenCalls,
		apiCalls:    &apiCalls,
		revokeCalls: &revokeCalls,
	}
}

// seedToken persists a stored token so the app counts as connected.
func (ta *testApp) seedToken(t *testing.T, tok *oauth2.Token) {
	t.Helper()
	if tok == nil {
		tok = &oauth2.Token{
			AccessToken:  "valid",
			RefreshToken: "rt",
			Expiry:       time.Now().Add(time.Hour),
		}
	}
	if err := auth.SaveToken(tok); err != nil {
		t.Fatalf("seed token: %v", err)
	}
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

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080": true,
		"localhost:8080": true,
		"[::1]:8080":     true,
		":8080":          false,
		"0.0.0.0:8080":   false,
		"192.168.1.5:80": false,
	}
	for addr, want := range cases {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestPortSuffix(t *testing.T) {
	if got := portSuffix(":8080"); got != ":8080" {
		t.Fatalf("got %q", got)
	}
	if got := portSuffix("127.0.0.1:9000"); got != ":9000" {
		t.Fatalf("got %q", got)
	}
	if got := portSuffix("noport"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestBaseURLPrefersPublicURL(t *testing.T) {
	a := &app{publicURL: "https://my.host"}
	if got := a.baseURL(httptest.NewRequest("GET", "/", nil)); got != "https://my.host" {
		t.Fatalf("got %q", got)
	}
}

func TestBaseURLDerivesFromRequest(t *testing.T) {
	a := &app{}
	r := httptest.NewRequest("GET", "http://localhost:8080/", nil)
	if got := a.baseURL(r); got != "http://localhost:8080" {
		t.Fatalf("got %q", got)
	}
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Host = "proxied.host"
	r2.Header.Set("X-Forwarded-Proto", "https")
	if got := a.baseURL(r2); got != "https://proxied.host" {
		t.Fatalf("got %q", got)
	}
}

func TestAuthedAdmin(t *testing.T) {
	open := &app{authToken: ""}
	if !open.authedAdmin(httptest.NewRequest("GET", "/", nil)) {
		t.Fatal("open server should always be admin-authed")
	}
	locked := &app{authToken: "secret"}
	if locked.authedAdmin(httptest.NewRequest("GET", "/", nil)) {
		t.Fatal("missing cookie should not be authed")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: adminCookie, Value: "secret"})
	if !locked.authedAdmin(r) {
		t.Fatal("correct cookie should be authed")
	}
	rw := httptest.NewRequest("GET", "/", nil)
	rw.AddCookie(&http.Cookie{Name: adminCookie, Value: "wrong"})
	if locked.authedAdmin(rw) {
		t.Fatal("wrong cookie should not be authed")
	}
}

func TestAuthedBearer(t *testing.T) {
	open := &app{authToken: ""}
	if !open.authedBearer(httptest.NewRequest("GET", "/mcp", nil)) {
		t.Fatal("open server should always be bearer-authed")
	}
	locked := &app{authToken: "secret"}
	if locked.authedBearer(httptest.NewRequest("GET", "/mcp", nil)) {
		t.Fatal("missing header should not be authed")
	}
	r := httptest.NewRequest("GET", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer secret")
	if !locked.authedBearer(r) {
		t.Fatal("correct bearer should be authed")
	}
	rw := httptest.NewRequest("GET", "/mcp", nil)
	rw.Header.Set("Authorization", "Bearer nope")
	if locked.authedBearer(rw) {
		t.Fatal("wrong bearer should not be authed")
	}
}

func TestStateStorePutConsume(t *testing.T) {
	s := newStateStore()
	s.put("abc", "verifier-abc")
	got, ok := s.consume("abc")
	if !ok || got != "verifier-abc" {
		t.Fatalf("first consume = %q,%v; want verifier-abc,true", got, ok)
	}
	if _, ok := s.consume("abc"); ok {
		t.Fatal("second consume should fail (single use)")
	}
	if _, ok := s.consume("never-put"); ok {
		t.Fatal("unknown state should fail")
	}
}

func TestStateStoreExpires(t *testing.T) {
	s := newStateStore()
	s.put("old", "v-old")
	s.mu.Lock()
	s.vals["old"] = stateEntry{verifier: "v-old", at: time.Now().Add(-11 * time.Minute)}
	s.mu.Unlock()
	if _, ok := s.consume("old"); ok {
		t.Fatal("expired state should not consume")
	}
	s.put("fresh", "v-fresh")
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

func TestHandleLandingNotConnected(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	ta.app.handleLanding(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Connect Whoop") {
		t.Fatal("not-connected landing should invite connecting")
	}
}

func TestHandleLandingConnected(t *testing.T) {
	ta := newTestApp(t)
	ta.seedToken(t, nil)
	w := httptest.NewRecorder()
	ta.app.handleLanding(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "/mcp") {
		t.Fatal("connected landing should show the MCP URL")
	}
	if !strings.Contains(body, "Disconnect") {
		t.Fatal("connected landing should offer disconnect")
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

func TestHandleLandingLockedShowsUnlock(t *testing.T) {
	ta := newTestApp(t)
	ta.app.authToken = "secret"
	w := httptest.NewRecorder()
	ta.app.handleLanding(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "access token") {
		t.Fatal("locked landing should render the unlock form")
	}
}

func TestHandleUnlockNoAuthTokenRedirects(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	ta.app.handleUnlock(w, httptest.NewRequest("POST", "/unlock", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want redirect", w.Code)
	}
}

func TestHandleUnlockWrongToken(t *testing.T) {
	ta := newTestApp(t)
	ta.app.authToken = "secret"
	form := strings.NewReader("token=nope")
	r := httptest.NewRequest("POST", "/unlock", form)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	ta.app.handleUnlock(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestHandleUnlockCorrectTokenSetsCookie(t *testing.T) {
	ta := newTestApp(t)
	ta.app.authToken = "secret"
	form := strings.NewReader("token=secret")
	r := httptest.NewRequest("POST", "/unlock", form)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	ta.app.handleUnlock(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want redirect", w.Code)
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), adminCookie) {
		t.Fatal("expected admin cookie to be set")
	}
}

func TestHandleUnlockMethodNotAllowed(t *testing.T) {
	ta := newTestApp(t)
	ta.app.authToken = "secret"
	w := httptest.NewRecorder()
	ta.app.handleUnlock(w, httptest.NewRequest("GET", "/unlock", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
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
	if !strings.Contains(w.Header().Get("Set-Cookie"), "whoop_oauth_state") {
		t.Fatal("missing state cookie")
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if u.Query().Get("code_challenge") == "" {
		t.Fatal("redirect missing code_challenge")
	}
	if m := u.Query().Get("code_challenge_method"); m != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", m)
	}
}

func TestHandleLoginLockedRedirectsHome(t *testing.T) {
	ta := newTestApp(t)
	ta.app.authToken = "secret"
	w := httptest.NewRecorder()
	ta.app.handleLogin(w, httptest.NewRequest("GET", "/login", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Fatalf("locked login should redirect home, got %q", loc)
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

func TestHandleCallbackSendsPKCEVerifier(t *testing.T) {
	var gotVerifier string
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotVerifier = r.Form.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","token_type":"Bearer","expires_in":3600}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ta := newTestApp(t)
	ta.app.oauth = &oauth2.Config{
		ClientID:     ta.app.oauth.ClientID,
		ClientSecret: ta.app.oauth.ClientSecret,
		RedirectURL:  ta.app.oauth.RedirectURL,
		Scopes:       ta.app.oauth.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   srv.URL + "/auth",
			TokenURL:  srv.URL + "/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}

	wLogin := httptest.NewRecorder()
	ta.app.handleLogin(wLogin, httptest.NewRequest("GET", "/login", nil))
	if wLogin.Code != http.StatusFound {
		t.Fatalf("login status = %d", wLogin.Code)
	}
	cookieHeader := wLogin.Header().Get("Set-Cookie")
	state := strings.TrimPrefix(strings.SplitN(cookieHeader, ";", 2)[0], "whoop_oauth_state=")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/oauth/callback?state="+state+"&code=abcd", nil)
	r.AddCookie(&http.Cookie{Name: "whoop_oauth_state", Value: state})
	ta.app.handleCallback(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if gotVerifier == "" {
		t.Fatal("token exchange did not carry code_verifier")
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

func TestHandleCallbackCookieMatchesButStateNotStored(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/oauth/callback?state=ghost&code=x", nil)
	r.AddCookie(&http.Cookie{Name: "whoop_oauth_state", Value: "ghost"})
	ta.app.handleCallback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleCallbackMissingCode(t *testing.T) {
	ta := newTestApp(t)
	ta.app.states.put("st", "v")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/oauth/callback?state=st", nil)
	r.AddCookie(&http.Cookie{Name: "whoop_oauth_state", Value: "st"})
	ta.app.handleCallback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleCallbackSuccessStoresToken(t *testing.T) {
	ta := newTestApp(t)
	ta.app.states.put("ok", "v")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/oauth/callback?state=ok&code=abcd", nil)
	r.AddCookie(&http.Cookie{Name: "whoop_oauth_state", Value: "ok"})
	ta.app.handleCallback(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Fatalf("expected redirect home, got %q", loc)
	}
	if *ta.tokenCalls == 0 {
		t.Fatal("OAuth token endpoint was not called")
	}
	if !auth.HasToken() {
		t.Fatal("token should be stored after successful callback")
	}
}

func TestHandleCallbackLockedRejected(t *testing.T) {
	ta := newTestApp(t)
	ta.app.authToken = "secret"
	ta.app.states.put("ok", "v")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/oauth/callback?state=ok&code=abcd", nil)
	r.AddCookie(&http.Cookie{Name: "whoop_oauth_state", Value: "ok"})
	ta.app.handleCallback(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without admin cookie", w.Code)
	}
}

func TestHandleCallbackTokenExchangeError(t *testing.T) {
	ta := newTestApp(t)
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
	ta.app.states.put("ok", "v")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/oauth/callback?state=ok&code=abcd", nil)
	r.AddCookie(&http.Cookie{Name: "whoop_oauth_state", Value: "ok"})
	ta.app.handleCallback(w, r)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleCallbackPersistError(t *testing.T) {
	skipIfRoot(t)
	ta := newTestApp(t)
	ta.app.states.put("ok", "v")
	// Point the token file at a path whose parent is read-only so SaveToken fails.
	parent := t.TempDir()
	if err := chmodRO(parent); err != nil {
		t.Skipf("chmod RO not supported: %v", err)
	}
	t.Cleanup(func() { _ = chmodRW(parent) })
	t.Setenv("WHOOP_TOKEN_FILE", filepath.Join(parent, "nested", "token.json"))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/oauth/callback?state=ok&code=abcd", nil)
	r.AddCookie(&http.Cookie{Name: "whoop_oauth_state", Value: "ok"})
	ta.app.handleCallback(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected persist failure, got %d body=%s", w.Code, w.Body.String())
	}
}

// ---- /mcp endpoint ----

func TestHandleMCPNotConnected(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	ta.app.handleMCP(w, httptest.NewRequest("POST", "/mcp", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when no token stored", w.Code)
	}
}

func TestHandleMCPBearerRequired(t *testing.T) {
	ta := newTestApp(t)
	ta.app.authToken = "secret"
	ta.seedToken(t, nil)
	w := httptest.NewRecorder()
	ta.app.handleMCP(w, httptest.NewRequest("POST", "/mcp", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without bearer", w.Code)
	}
}

func TestHandleMCPRoutesToMCP(t *testing.T) {
	ta := newTestApp(t)
	ta.seedToken(t, nil)
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/mcp", body)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	ta.app.handleMCP(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"protocolVersion"`) {
		t.Fatalf("MCP init not returned: %s", w.Body.String())
	}
}

func TestHandleMCPRoutesWithBearer(t *testing.T) {
	ta := newTestApp(t)
	ta.app.authToken = "secret"
	ta.seedToken(t, nil)
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/mcp", body)
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	ta.app.handleMCP(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

// ---- disconnect ----

func TestHandleDisconnectGETConfirmation(t *testing.T) {
	ta := newTestApp(t)
	ta.seedToken(t, nil)
	w := httptest.NewRecorder()
	ta.app.handleDisconnect(w, httptest.NewRequest("GET", "/disconnect", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Disconnect") {
		t.Fatal("missing confirmation copy")
	}
}

func TestHandleDisconnectPOSTRevokesAndDeletes(t *testing.T) {
	ta := newTestApp(t)
	ta.seedToken(t, nil)
	w := httptest.NewRecorder()
	ta.app.handleDisconnect(w, httptest.NewRequest("POST", "/disconnect", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if *ta.revokeCalls == 0 {
		t.Fatal("Whoop revoke endpoint not called")
	}
	if auth.HasToken() {
		t.Fatal("token should be deleted")
	}
}

func TestHandleDisconnectPOSTRevokeErrorStillDeletes(t *testing.T) {
	ta := newTestApp(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", 500)
	}))
	t.Cleanup(bad.Close)
	ta.app.whoopBaseURL = bad.URL
	ta.seedToken(t, nil)
	w := httptest.NewRecorder()
	ta.app.handleDisconnect(w, httptest.NewRequest("POST", "/disconnect", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if auth.HasToken() {
		t.Fatal("token should be deleted even if revoke failed")
	}
}

func TestHandleDisconnectPOSTWhenNotConnected(t *testing.T) {
	ta := newTestApp(t)
	w := httptest.NewRecorder()
	ta.app.handleDisconnect(w, httptest.NewRequest("POST", "/disconnect", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleDisconnectMethodNotAllowed(t *testing.T) {
	ta := newTestApp(t)
	ta.seedToken(t, nil)
	w := httptest.NewRecorder()
	ta.app.handleDisconnect(w, httptest.NewRequest("PUT", "/disconnect", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleDisconnectLockedRejected(t *testing.T) {
	ta := newTestApp(t)
	ta.app.authToken = "secret"
	w := httptest.NewRecorder()
	ta.app.handleDisconnect(w, httptest.NewRequest("GET", "/disconnect", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// ---- persistingTokenSource ----

func TestPersistingTokenSourcePersistsRefresh(t *testing.T) {
	ta := newTestApp(t)
	ta.seedToken(t, &oauth2.Token{
		AccessToken:  "old",
		RefreshToken: "rt-old",
		Expiry:       time.Now().Add(-time.Hour), // expired → triggers refresh
	})
	src, err := ta.app.tokenSource(context.Background())
	if err != nil {
		t.Fatalf("tokenSource: %v", err)
	}
	tok, err := src.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken != "fresh" {
		t.Fatalf("expected refreshed token, got %+v", tok)
	}
	loaded, err := auth.LoadToken()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded.AccessToken != "fresh" {
		t.Fatalf("store not updated: %+v", loaded)
	}
}

func TestPersistingTokenSourceNoOpForCachedToken(t *testing.T) {
	ta := newTestApp(t)
	ta.seedToken(t, nil) // not expired
	src, err := ta.app.tokenSource(context.Background())
	if err != nil {
		t.Fatalf("tokenSource: %v", err)
	}
	_, _ = src.Token()
	_, _ = src.Token()
	if *ta.tokenCalls != 0 {
		t.Fatalf("expected no token refreshes, got %d", *ta.tokenCalls)
	}
}

func TestTokenSourceErrorsWhenNoToken(t *testing.T) {
	ta := newTestApp(t)
	if _, err := ta.app.tokenSource(context.Background()); err == nil {
		t.Fatal("expected error when no token stored")
	}
}

func TestPersistingTokenSourceErrorPropagates(t *testing.T) {
	src := &persistingTokenSource{base: &errSrc{err: errors.New("refresh failed")}}
	if _, err := src.Token(); err == nil || !strings.Contains(err.Error(), "refresh failed") {
		t.Fatalf("expected refresh error, got %v", err)
	}
}

func TestPersistingTokenSourceSaveError(t *testing.T) {
	skipIfRoot(t)
	parent := t.TempDir()
	t.Setenv("WHOOP_TOKEN_BACKEND", "file")
	t.Setenv("WHOOP_TOKEN_FILE", filepath.Join(parent, "nested", "token.json"))
	if err := chmodRO(parent); err != nil {
		t.Skipf("chmod RO not supported: %v", err)
	}
	t.Cleanup(func() { _ = chmodRW(parent) })
	src := &persistingTokenSource{base: &staticTokenSrc{tok: &oauth2.Token{AccessToken: "new"}}}
	if _, err := src.Token(); err == nil {
		t.Fatal("expected save error to surface")
	}
}

// ---- run() / serve() entry-point tests ----

func resetEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"WHOOP_CLIENT_ID", "WHOOP_CLIENT_SECRET", "WHOOP_REDIRECT_URI", "WHOOP_TOKEN_FILE", "WHOOP_TOKEN_BACKEND", "PORT", "MCP_HTTP_ADDR", "PUBLIC_URL", "AUTH_TOKEN"} {
		t.Setenv(k, "")
	}
}

func TestRunRejectsMissingCredentials(t *testing.T) {
	resetEnv(t)
	if err := run(context.Background()); err == nil {
		t.Fatal("expected error when credentials are unset")
	}
}

func TestRunHTTPModeBootsAndCancels(t *testing.T) {
	resetEnv(t)
	t.Setenv("WHOOP_CLIENT_ID", "x")
	t.Setenv("WHOOP_CLIENT_SECRET", "y")
	t.Setenv("WHOOP_TOKEN_FILE", filepath.Join(t.TempDir(), "token.json"))
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

func TestMainInvokesRunAndExitsOnError(t *testing.T) {
	resetEnv(t)
	var exited int
	origExit := osExit
	t.Cleanup(func() { osExit = origExit })
	osExit = func(code int) { exited = code }
	main()
	if exited != 1 {
		t.Fatalf("osExit code = %d, want 1", exited)
	}
}

func TestServeBootsAndShutsDown(t *testing.T) {
	t.Setenv("WHOOP_TOKEN_FILE", filepath.Join(t.TempDir(), "token.json"))
	t.Setenv("PUBLIC_URL", "http://127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	cfg := &auth.Config{ClientID: "x", ClientSecret: "y", RedirectURL: "http://x/cb"}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, "127.0.0.1:0", cfg) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not shut down")
	}
}

func TestServeRoutesReachable(t *testing.T) {
	t.Setenv("WHOOP_TOKEN_FILE", filepath.Join(t.TempDir(), "token.json"))
	t.Setenv("PUBLIC_URL", "")
	cfg := &auth.Config{ClientID: "x", ClientSecret: "y", RedirectURL: "http://x/cb"}
	ctx, cancel := context.WithCancel(context.Background())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- serve(ctx, addr, cfg) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, path := range []string{"/healthz", "/favicon.svg", "/"} {
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
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not shut down")
	}
}

func TestServeListenError(t *testing.T) {
	t.Setenv("WHOOP_TOKEN_FILE", filepath.Join(t.TempDir(), "token.json"))
	t.Setenv("PUBLIC_URL", "http://127.0.0.1:0")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	cfg := &auth.Config{ClientID: "x", ClientSecret: "y", RedirectURL: "http://x/cb"}
	if err := serve(context.Background(), ln.Addr().String(), cfg); err == nil {
		t.Fatal("expected listen error")
	}
}

func TestResolveRedirectURI(t *testing.T) {
	// Explicit value wins.
	if got := resolveRedirectURI("127.0.0.1:0", "https://my.host", "https://override/cb"); got != "https://override/cb" {
		t.Errorf("explicit: got %q", got)
	}
	// Derived from PUBLIC_URL.
	if got := resolveRedirectURI("127.0.0.1:0", "https://my.host", ""); got != "https://my.host/oauth/callback" {
		t.Errorf("public url: got %q", got)
	}
	// Falls back to a localhost callback using the listen port.
	if got := resolveRedirectURI(":8080", "", ""); got != "http://localhost:8080/oauth/callback" {
		t.Errorf("localhost fallback: got %q", got)
	}
}

func TestNewWhoopClientUsesDefaultBaseURL(t *testing.T) {
	a := &app{} // no whoopBaseURL set
	c := a.newWhoopClient(context.Background(), &staticTokenSrc{tok: &oauth2.Token{AccessToken: "x"}})
	if c == nil {
		t.Fatal("expected client")
	}
}

// ---- Test scaffolding ----

type fakeSrc struct{}

func (fakeSrc) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "x", Expiry: time.Now().Add(time.Hour)}, nil
}

type staticTokenSrc struct{ tok *oauth2.Token }

func (s *staticTokenSrc) Token() (*oauth2.Token, error) { return s.tok, nil }

type errSrc struct{ err error }

func (e *errSrc) Token() (*oauth2.Token, error) { return nil, e.err }

var _ = io.Discard

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
