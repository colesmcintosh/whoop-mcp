// Command whoop-mcp is a single-tenant, self-hosted Model Context Protocol
// server for the Whoop API v2.
//
// You run it yourself — on your laptop or in a container — connect your
// own Whoop account once through the browser, and point any MCP-capable
// client at the server's /mcp endpoint. There is no multi-tenant hosting
// and no separate login CLI: the server runs the OAuth flow, stores the
// single resulting token, and refreshes it automatically.
//
// Optionally set AUTH_TOKEN to a secret value to require a bearer token on
// the /mcp endpoint and gate the browser connect flow. This is strongly
// recommended whenever the server is reachable from anything other than
// localhost.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/colesmcintosh/whoop-mcp/internal/auth"
	"github.com/colesmcintosh/whoop-mcp/internal/whoop"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

const (
	serverName    = "whoop-mcp"
	serverVersion = "0.3.0"

	// adminCookie holds the AUTH_TOKEN value once an operator has unlocked
	// the browser UI. Only relevant when AUTH_TOKEN is set.
	adminCookie = "whoop_admin"

	defaultListenAddr = ":8080"
)

type emptyInput struct{}

type idInput struct {
	ID string `json:"id" jsonschema:"the Whoop record id (UUID string)"`
}

type cycleIDInput struct {
	CycleID string `json:"cycle_id" jsonschema:"the Whoop cycle id (UUID string)"`
}

type listInput struct {
	Limit     int    `json:"limit,omitempty" jsonschema:"max records to return (1-25, default 10)"`
	Start     string `json:"start,omitempty" jsonschema:"inclusive lower bound on start time (RFC3339, e.g. 2026-05-01T00:00:00Z)"`
	End       string `json:"end,omitempty" jsonschema:"exclusive upper bound on start time (RFC3339)"`
	NextToken string `json:"next_token,omitempty" jsonschema:"pagination token from a prior response"`
}

func (l listInput) toParams() (whoop.ListParams, error) {
	p := whoop.ListParams{Limit: l.Limit, NextToken: l.NextToken}
	if l.Start != "" {
		t, err := time.Parse(time.RFC3339, l.Start)
		if err != nil {
			return p, fmt.Errorf("invalid start time: %w", err)
		}
		p.Start = t
	}
	if l.End != "" {
		t, err := time.Parse(time.RFC3339, l.End)
		if err != nil {
			return p, fmt.Errorf("invalid end time: %w", err)
		}
		p.End = t
	}
	return p, nil
}

func jsonResult(raw json.RawMessage) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}},
	}
}

// osExit is swappable so tests can invoke main() without terminating
// the test process.
var osExit = os.Exit

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "whoop-mcp:", err)
		osExit(1)
	}
}

// run is the testable entry point. main is a thin wrapper that maps
// errors to a non-zero exit status.
func run(ctx context.Context) error {
	cfg, err := auth.LoadConfigFromEnv()
	if err != nil {
		return err
	}
	addr := httpListenAddr()
	if addr == "" {
		addr = defaultListenAddr
	}
	return serve(ctx, addr, cfg)
}

// httpListenAddr resolves the listen address from MCP_HTTP_ADDR or PORT.
// Returns "" when neither is set so run() can apply the default.
func httpListenAddr() string {
	if a := os.Getenv("MCP_HTTP_ADDR"); a != "" {
		return a
	}
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ""
}

func newServer() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)
}

// serve runs the single-tenant HTTP server: landing/dashboard, the OAuth
// connect flow, disconnect, and the /mcp endpoint.
func serve(ctx context.Context, addr string, cfg *auth.Config) error {
	publicURL := strings.TrimRight(os.Getenv("PUBLIC_URL"), "/")

	cfg.RedirectURL = resolveRedirectURI(addr, publicURL, os.Getenv("WHOOP_REDIRECT_URI"))

	app := &app{
		cfg:       cfg,
		publicURL: publicURL,
		authToken: os.Getenv("AUTH_TOKEN"),
		oauth:     cfg.OAuth2Config(),
		states:    newStateStore(),
		loginRL:   newRateLimiter(0.1, 6), // ~6 attempts/min/IP, refill every 10s
	}

	if app.authToken == "" && !isLoopback(addr) {
		log.Printf("WARNING: AUTH_TOKEN is not set and the server is not bound to localhost (%s). "+
			"Anyone who can reach this server can read your Whoop data and hijack the connect flow. "+
			"Set AUTH_TOKEN to a secret value, or bind to localhost.", addr)
	}

	srv := &http.Server{Addr: addr, Handler: buildMux(app), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); _ = srv.Shutdown(context.Background()) }()
	log.Printf("whoop-mcp listening on %s (token storage: %s)", addr, auth.TokenStoreLocation())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func buildMux(a *app) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write([]byte(brandMarkSVG))
	})
	mux.Handle("/", securityHeaders(http.HandlerFunc(a.handleLanding)))
	mux.Handle("/unlock", securityHeaders(http.HandlerFunc(a.handleUnlock)))
	mux.Handle("/login", securityHeaders(http.HandlerFunc(a.handleLogin)))
	mux.Handle("/oauth/callback", securityHeaders(http.HandlerFunc(a.handleCallback)))
	mux.Handle("/disconnect", securityHeaders(http.HandlerFunc(a.handleDisconnect)))
	mux.HandleFunc("/mcp", a.handleMCP)
	mux.HandleFunc("/mcp/", a.handleMCP)
	return mux
}

type app struct {
	cfg          *auth.Config
	publicURL    string
	authToken    string
	oauth        *oauth2.Config
	states       *stateStore
	loginRL      *rateLimiter
	whoopBaseURL string // injectable in tests; defaults to whoop.BaseURL
}

func (a *app) newWhoopClient(ctx context.Context, src oauth2.TokenSource) *whoop.Client {
	if a.whoopBaseURL == "" {
		return whoop.New(ctx, src)
	}
	return whoop.NewWithBaseURL(ctx, src, a.whoopBaseURL)
}

// tokenSource returns a refreshing, self-persisting source backed by the
// single stored token. It errors when no token is stored yet (the Whoop
// account hasn't been connected).
func (a *app) tokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	tok, err := auth.LoadToken()
	if err != nil {
		return nil, err
	}
	base := a.oauth.TokenSource(ctx, tok)
	return &persistingTokenSource{base: base, last: tok}, nil
}

// persistingTokenSource writes rotated tokens back to the configured
// backend. Whoop invalidates the previous refresh token on every refresh,
// so persistence is mandatory for the connection to survive restarts.
type persistingTokenSource struct {
	base oauth2.TokenSource

	mu   sync.Mutex
	last *oauth2.Token
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.base.Token()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if tok == p.last {
		return tok, nil
	}
	if err := auth.SaveToken(tok); err != nil {
		return nil, fmt.Errorf("persist refreshed token: %w", err)
	}
	p.last = tok
	return tok, nil
}

// ---- request helpers ----

// authedAdmin reports whether the request may use the browser admin UI
// (landing, connect, disconnect). When AUTH_TOKEN is unset the UI is open.
func (a *app) authedAdmin(r *http.Request) bool {
	if a.authToken == "" {
		return true
	}
	c, err := r.Cookie(adminCookie)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(a.authToken)) == 1
}

// authedBearer reports whether the request carries the AUTH_TOKEN bearer
// credential. When AUTH_TOKEN is unset the endpoint is open.
func (a *app) authedBearer(r *http.Request) bool {
	if a.authToken == "" {
		return true
	}
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, prefix)), []byte(a.authToken)) == 1
}

func (a *app) cookieSecure(r *http.Request) bool {
	return strings.HasPrefix(a.publicURL, "https://") || r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// baseURL returns the externally-reachable base URL: PUBLIC_URL when set,
// otherwise derived from the incoming request so localhost deploys show a
// usable MCP URL without configuration.
func (a *app) baseURL(r *http.Request) string {
	if a.publicURL != "" {
		return a.publicURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ---- handlers ----

func (a *app) handleLanding(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !a.authedAdmin(r) {
		_ = unlockTpl.Execute(w, map[string]any{})
		return
	}
	_ = landingTpl.Execute(w, map[string]any{
		"Connected": auth.HasToken(),
		"MCPURL":    a.baseURL(r) + "/mcp",
		"AuthToken": a.authToken != "",
	})
}

// handleUnlock validates the operator-supplied AUTH_TOKEN and sets the
// admin cookie. Only meaningful when AUTH_TOKEN is set.
func (a *app) handleUnlock(w http.ResponseWriter, r *http.Request) {
	if a.authToken == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !a.loginRL.allow(clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many attempts; try again in a minute", http.StatusTooManyRequests)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.FormValue("token")), []byte(a.authToken)) != 1 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = unlockTpl.Execute(w, map[string]any{"Error": true})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookie,
		Value:    a.authToken,
		Path:     "/",
		MaxAge:   60 * 60 * 24 * 30,
		HttpOnly: true,
		Secure:   a.cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *app) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !a.authedAdmin(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if !a.loginRL.allow(clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many login attempts; try again in a minute", http.StatusTooManyRequests)
		return
	}
	state, err := randToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()
	a.states.put(state, verifier)
	http.SetCookie(w, &http.Cookie{
		Name:     "whoop_oauth_state",
		Value:    state,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   a.cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, a.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (a *app) handleCallback(w http.ResponseWriter, r *http.Request) {
	if !a.authedAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		http.Error(w, fmt.Sprintf("authorization error: %s — %s", errParam, q.Get("error_description")), http.StatusBadRequest)
		return
	}
	state := q.Get("state")
	cookie, _ := r.Cookie("whoop_oauth_state")
	if state == "" || cookie == nil || cookie.Value != state {
		http.Error(w, "invalid or expired state", http.StatusBadRequest)
		return
	}
	verifier, ok := a.states.consume(state)
	if !ok {
		http.Error(w, "invalid or expired state", http.StatusBadRequest)
		return
	}
	code := q.Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	tok, err := a.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		http.Error(w, "token exchange failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := auth.SaveToken(tok); err != nil {
		http.Error(w, "persist token: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleMCP serves the MCP StreamableHTTP endpoint backed by the single
// stored token. Gated by the AUTH_TOKEN bearer credential when set.
func (a *app) handleMCP(w http.ResponseWriter, r *http.Request) {
	if !a.authedBearer(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	src, err := a.tokenSource(r.Context())
	if err != nil {
		http.Error(w, "not connected: open "+a.baseURL(r)+"/login in a browser to connect your Whoop account", http.StatusServiceUnavailable)
		return
	}

	handler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		client := a.newWhoopClient(r.Context(), src)
		s := newServer()
		registerTools(s, client)
		return s
	}, nil)

	// Strip /mcp so MCP session paths start at /.
	r2 := r.Clone(r.Context())
	r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/mcp")
	if r2.URL.Path == "" {
		r2.URL.Path = "/"
	}
	handler.ServeHTTP(w, r2)
}

// handleDisconnect shows a confirmation page on GET and revokes + deletes
// the stored token on POST.
func (a *app) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if !a.authedAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = disconnectConfirmTpl.Execute(w, map[string]any{"Connected": auth.HasToken()})
	case http.MethodPost:
		var revokeErr error
		if src, err := a.tokenSource(r.Context()); err == nil {
			client := a.newWhoopClient(r.Context(), src)
			revokeErr = client.RevokeAccess(r.Context())
		}
		// Delete the local token either way — if revoke failed, the user
		// can finish revoking from Whoop's own account settings.
		_ = auth.DeleteToken()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = disconnectedTpl.Execute(w, map[string]any{"RevokeError": revokeErr})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---- infrastructure ----

// securityHeaders applies a baseline of safe response headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "interest-cohort=()")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// rateLimiter is a basic in-memory token-bucket per key.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64 // tokens per second
	burst   float64
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(rate, burst float64) *rateLimiter {
	return &rateLimiter{buckets: map[string]*tokenBucket{}, rate: rate, burst: burst}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	if len(rl.buckets) > 2048 {
		// Opportunistically evict stale entries before unbounded growth.
		cutoff := now.Add(-15 * time.Minute)
		for k, b := range rl.buckets {
			if b.last.Before(cutoff) {
				delete(rl.buckets, k)
			}
		}
	}
	b, ok := rl.buckets[key]
	if !ok {
		rl.buckets[key] = &tokenBucket{tokens: rl.burst - 1, last: now}
		return true
	}
	b.tokens = math.Min(rl.burst, b.tokens+now.Sub(b.last).Seconds()*rl.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// clientIP extracts the requesting client's IP, honoring a single hop
// of X-Forwarded-For (Railway and similar reverse proxies set this).
func clientIP(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-For"); h != "" {
		if i := strings.Index(h, ","); i > 0 {
			return strings.TrimSpace(h[:i])
		}
		return strings.TrimSpace(h)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isLoopback reports whether a listen address binds only the loopback
// interface. Used to decide whether an unset AUTH_TOKEN is risky.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "", "0.0.0.0", "::":
		return false
	case "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// resolveRedirectURI picks the OAuth redirect URI. An explicit value
// (WHOOP_REDIRECT_URI) wins; otherwise it is derived from PUBLIC_URL,
// falling back to a localhost callback for purely-local use. The result
// must match a redirect URI registered on the Whoop app exactly.
func resolveRedirectURI(addr, publicURL, explicit string) string {
	switch {
	case explicit != "":
		return explicit
	case publicURL != "":
		return publicURL + "/oauth/callback"
	default:
		return "http://localhost" + portSuffix(addr) + "/oauth/callback"
	}
}

// portSuffix returns the ":port" portion of a listen address, or "" if it
// has no port. Used to build a localhost redirect URI.
func portSuffix(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		return ":" + port
	}
	return ""
}

// stateStore tracks short-lived OAuth state values to defeat CSRF, plus
// the per-flow PKCE code verifier so callback can complete the exchange.
type stateStore struct {
	mu   sync.Mutex
	vals map[string]stateEntry
}

type stateEntry struct {
	verifier string
	at       time.Time
}

func newStateStore() *stateStore { return &stateStore{vals: map[string]stateEntry{}} }

func (s *stateStore) put(v, verifier string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-10 * time.Minute)
	for k, e := range s.vals {
		if e.at.Before(cutoff) {
			delete(s.vals, k)
		}
	}
	s.vals[v] = stateEntry{verifier: verifier, at: time.Now()}
}

func (s *stateStore) consume(v string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.vals[v]
	if !ok || time.Since(e.at) > 10*time.Minute {
		return "", false
	}
	delete(s.vals, v)
	return e.verifier, true
}

// randReadMain is swappable in tests to exercise the crypto/rand error path.
var randReadMain = rand.Read

func randToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := randReadMain(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func registerTools(s *mcp.Server, c *whoop.Client) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_profile",
		Description: "Get the authenticated user's basic profile (name, email, user id).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		body, err := c.GetProfile(ctx)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(body), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_body_measurement",
		Description: "Get the user's body measurements: height, weight, and max heart rate.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		body, err := c.GetBodyMeasurement(ctx)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(body), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_cycles",
		Description: "List physiological cycles in descending order by start time. Each cycle has strain and heart rate data.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, any, error) {
		p, err := in.toParams()
		if err != nil {
			return nil, nil, err
		}
		body, err := c.ListCycles(ctx, p)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(body), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_cycle",
		Description: "Get a single cycle by id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cycleIDInput) (*mcp.CallToolResult, any, error) {
		body, err := c.GetCycle(ctx, in.CycleID)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(body), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_cycle_recovery",
		Description: "Get the recovery score attached to a specific cycle.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cycleIDInput) (*mcp.CallToolResult, any, error) {
		body, err := c.GetCycleRecovery(ctx, in.CycleID)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(body), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_cycle_sleep",
		Description: "Get the sleep record attached to a specific cycle.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cycleIDInput) (*mcp.CallToolResult, any, error) {
		body, err := c.GetCycleSleep(ctx, in.CycleID)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(body), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_recovery",
		Description: "List recovery records (recovery score, resting HR, HRV, SpO2, skin temperature) paginated and filterable by time range.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, any, error) {
		p, err := in.toParams()
		if err != nil {
			return nil, nil, err
		}
		body, err := c.ListRecovery(ctx, p)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(body), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_sleep",
		Description: "List sleep records with stage durations and sleep performance.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, any, error) {
		p, err := in.toParams()
		if err != nil {
			return nil, nil, err
		}
		body, err := c.ListSleep(ctx, p)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(body), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_sleep",
		Description: "Get a single sleep record by id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in idInput) (*mcp.CallToolResult, any, error) {
		body, err := c.GetSleep(ctx, in.ID)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(body), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_workouts",
		Description: "List workouts with sport classification, strain, average heart rate, distance, and HR zone durations.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, any, error) {
		p, err := in.toParams()
		if err != nil {
			return nil, nil, err
		}
		body, err := c.ListWorkouts(ctx, p)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(body), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_workout",
		Description: "Get a single workout by id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in idInput) (*mcp.CallToolResult, any, error) {
		body, err := c.GetWorkout(ctx, in.ID)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(body), nil, nil
	})
}
