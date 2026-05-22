// Command whoop-mcp serves the Whoop API to MCP clients.
//
// Two modes:
//   - stdio (default): single-tenant. Uses the token file written by
//     the whoop-auth CLI. Intended for local use.
//   - HTTP (when PORT or MCP_HTTP_ADDR is set): multi-tenant. Hosts a
//     browser-based OAuth flow at /login + /oauth/callback. Each user
//     who completes the flow gets a personal /connect/<id> URL they
//     can paste into an MCP client to read THEIR own Whoop data.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/colesmcintosh/whoop-mcp/internal/auth"
	"github.com/colesmcintosh/whoop-mcp/internal/store"
	"github.com/colesmcintosh/whoop-mcp/internal/whoop"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

const (
	serverName    = "whoop-mcp"
	serverVersion = "0.2.0"
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

	if addr := httpListenAddr(); addr != "" {
		return serveMultiTenant(ctx, addr, cfg)
	}

	// Stdio (single-tenant) mode.
	if seed := os.Getenv("WHOOP_INITIAL_REFRESH_TOKEN"); seed != "" {
		if err := auth.SeedRefreshTokenIfMissing(seed); err != nil {
			return fmt.Errorf("seed token: %w", err)
		}
	}
	src, err := cfg.TokenSource(ctx)
	if err != nil {
		return err
	}
	client := whoop.New(ctx, src)
	s := newServer()
	registerTools(s, client)
	return s.Run(ctx, &mcp.StdioTransport{})
}

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

// serveMultiTenant runs the public HTTP server: landing page, OAuth
// login + callback, and per-user MCP endpoints.
func serveMultiTenant(ctx context.Context, addr string, cfg *auth.Config) error {
	storeDir := os.Getenv("USER_STORE_DIR")
	if storeDir == "" {
		storeDir = "/data/users"
	}
	st, err := store.New(storeDir)
	if err != nil {
		return err
	}

	publicURL := strings.TrimRight(os.Getenv("PUBLIC_URL"), "/")
	if publicURL == "" {
		return fmt.Errorf("PUBLIC_URL must be set (the externally-reachable https URL of this server)")
	}

	app := &app{
		cfg:       cfg,
		store:     st,
		publicURL: publicURL,
		oauth:     cfg.OAuth2Config(),
		states:    newStateStore(),
		loginRL:   newRateLimiter(0.1, 6), // ~6 starts/min/IP, refill every 10s
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write([]byte(brandMarkSVG))
	})
	mux.Handle("/", securityHeaders(http.HandlerFunc(app.handleLanding)))
	mux.Handle("/login", securityHeaders(http.HandlerFunc(app.handleLogin)))
	mux.Handle("/oauth/callback", securityHeaders(http.HandlerFunc(app.handleCallback)))
	mux.HandleFunc("/connect/", app.handleConnect)
	mux.Handle("/disconnect/", securityHeaders(http.HandlerFunc(app.handleDisconnect)))

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); _ = srv.Shutdown(context.Background()) }()
	log.Printf("whoop-mcp listening on %s (public URL %s)", addr, publicURL)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

type app struct {
	cfg          *auth.Config
	store        *store.Store
	publicURL    string
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

// randReadMain and storeNewID are swappable in tests to exercise the
// error paths that depend on crypto/rand and the store id generator.
var (
	randReadMain = rand.Read
	storeNewID   = store.NewID
)

func randToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := randReadMain(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

const sharedCSS = `
:root {
  --bg: #f7f7f5;
  --surface: #ffffff;
  --ink: #0b0d0c;
  --ink-2: #3a3d3b;
  --muted: #6b716e;
  --line: #e7e7e2;
  --accent: #0a7a5b;
  --warn-bg: #fff7ed;
  --warn-ink: #7c3a0d;
  --warn-line: #fde0bd;
  --radius: 14px;
  --radius-sm: 8px;
}
* { box-sizing: border-box; }
html, body { margin: 0; padding: 0; height: 100%; }
body {
  background: var(--bg);
  color: var(--ink);
  font-family: ui-sans-serif, -apple-system, "Segoe UI", Inter, system-ui, sans-serif;
  font-size: 15px;
  line-height: 1.5;
  -webkit-font-smoothing: antialiased;
  text-rendering: optimizeLegibility;
  min-height: 100vh;
  min-height: 100svh;
  display: flex;
  flex-direction: column;
}
.page {
  width: 100%;
  max-width: 1040px;
  margin: 0 auto;
  padding: 28px 28px 24px;
  flex: 1;
  display: flex;
  flex-direction: column;
}
.brand {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  color: var(--ink);
  text-decoration: none;
}
.brand-mark {
  display: inline-flex;
  color: var(--ink);
  line-height: 0;
}
.brand-mark svg { height: 24px; width: 24px; display: block; }
.brand-name {
  font-weight: 600;
  font-size: 15px;
  letter-spacing: -0.01em;
  color: var(--ink);
}
h1 {
  font-size: 38px;
  letter-spacing: -0.025em;
  line-height: 1.08;
  margin: 0 0 12px;
  font-weight: 600;
}
h2 {
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--muted);
  margin: 0 0 10px;
}
p { color: var(--ink-2); margin: 0 0 12px; }
.lead { color: var(--ink-2); font-size: 16px; max-width: 46ch; margin-bottom: 22px; }
.btn {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  background: var(--ink);
  color: #fff;
  padding: 11px 18px;
  border-radius: 999px;
  text-decoration: none;
  font-weight: 500;
  border: 0;
  cursor: pointer;
  font-size: 14px;
  transition: transform 0.06s ease, background 0.15s ease;
}
.btn:hover { background: #1a1d1b; }
.btn:active { transform: translateY(1px); }
.btn-ghost {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
}
.btn-ghost:hover { background: var(--surface); }
.card {
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 22px;
}
.kicker {
  color: var(--accent);
  font-weight: 600;
  font-size: 12px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  margin: 0 0 8px;
}

/* Landing: two-column hero+features */
.hero {
  display: grid;
  grid-template-columns: minmax(0, 1.05fr) minmax(0, 0.95fr);
  gap: 48px;
  align-items: center;
  flex: 1;
  margin-top: 24px;
}
.hero-cta { display: flex; gap: 10px; align-items: center; }
.hero-meta { color: var(--muted); font-size: 13px; margin-top: 14px; }

.features {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 10px;
}
.feature {
  border: 1px solid var(--line);
  background: var(--surface);
  border-radius: var(--radius-sm);
  padding: 12px 14px;
  font-size: 13px;
  color: var(--ink-2);
  line-height: 1.45;
}
.feature b { color: var(--ink); display: block; margin-bottom: 2px; font-weight: 600; font-size: 14px; }

/* Result: card-centric layout */
.result-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.2fr) minmax(0, 0.8fr);
  gap: 28px;
  align-items: start;
  flex: 1;
  margin-top: 24px;
}
.url-row {
  display: flex;
  align-items: stretch;
  gap: 8px;
  margin: 4px 0 0;
}
.url {
  flex: 1 1 auto;
  font-family: ui-monospace, "JetBrains Mono", "Menlo", monospace;
  font-size: 12.5px;
  background: #f1f1ec;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--radius-sm);
  padding: 11px 13px;
  word-break: break-all;
  user-select: all;
  line-height: 1.4;
}
.copy-btn {
  flex: 0 0 auto;
  background: var(--ink);
  color: #fff;
  border: 0;
  border-radius: var(--radius-sm);
  padding: 0 16px;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: background 0.15s ease;
  min-width: 80px;
}
.copy-btn:hover { background: #1a1d1b; }
.warn {
  background: var(--warn-bg);
  border: 1px solid var(--warn-line);
  color: var(--warn-ink);
  border-radius: var(--radius-sm);
  padding: 10px 12px;
  font-size: 13px;
  margin: 14px 0 0;
  line-height: 1.45;
}
ol.steps {
  counter-reset: step;
  list-style: none;
  padding: 0;
  margin: 6px 0 0;
}
ol.steps li {
  counter-increment: step;
  position: relative;
  padding-left: 30px;
  margin: 0 0 10px;
  color: var(--ink-2);
  font-size: 13.5px;
  line-height: 1.5;
}
ol.steps li:last-child { margin-bottom: 0; }
ol.steps li::before {
  content: counter(step);
  position: absolute;
  left: 0; top: 1px;
  width: 20px; height: 20px;
  border-radius: 50%;
  background: var(--ink);
  color: #fff;
  display: inline-flex; align-items: center; justify-content: center;
  font-size: 11px; font-weight: 600;
}

.footer {
  margin-top: 20px;
  padding-top: 14px;
  border-top: 1px solid var(--line);
  color: var(--muted);
  font-size: 12.5px;
  display: flex;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}
a { color: var(--ink); }

@media (max-width: 820px) {
  .hero, .result-grid {
    grid-template-columns: 1fr;
    gap: 24px;
    align-items: start;
  }
  h1 { font-size: 30px; }
  .lead { margin-bottom: 16px; }
  .page { padding: 22px 20px; }
}
@media (max-width: 520px) {
  h1 { font-size: 26px; }
  .url-row { flex-direction: column; }
  .copy-btn { padding: 10px; }
  .features { grid-template-columns: 1fr; }
}
`

// brandMarkSVG is an original logomark — a rounded badge with an
// abstract pulse line. Used in the header and as the favicon.
const brandMarkSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" fill="none" role="img" aria-label="Whoop MCP"><rect width="32" height="32" rx="9" fill="currentColor"/><path d="M6 17 L10 17 L12 12 L15 22 L18 9 L21 19 L23 17 L26 17" stroke="#fff" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`

var landingTpl = template.Must(template.New("landing").Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Whoop MCP — A Model Context Protocol server for Whoop</title>
<meta name="description" content="A Model Context Protocol server for Whoop. Connect your account and read your Whoop data from any MCP client.">
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<style>` + sharedCSS + `</style>
</head>
<body>
<main class="page">
  <a class="brand" href="/" aria-label="Whoop MCP">
    <span class="brand-mark">` + brandMarkSVG + `</span>
    <span class="brand-name">Whoop MCP</span>
  </a>

  <section class="hero">
    <div>
      <p class="kicker">Model Context Protocol</p>
      <h1>Your Whoop data, in any MCP client.</h1>
      <p class="lead">Connect your Whoop account and get a personal MCP URL. Drop it into any client that speaks the Model Context Protocol — every URL only sees the account it was issued for.</p>
      <div class="hero-cta">
        <a class="btn" href="/login">
          Connect Whoop
          <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true"><path d="M3 7h8m0 0L7.5 3.5M11 7l-3.5 3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </a>
        <span class="hero-meta">Read-only · OAuth via Whoop</span>
      </div>
    </div>
    <div>
      <h2>What you'll get</h2>
      <div class="features">
        <div class="feature"><b>Recovery</b>Score, HRV, RHR, SpO₂, skin temp.</div>
        <div class="feature"><b>Sleep</b>Duration, stages, performance.</div>
        <div class="feature"><b>Cycles</b>Daily strain &amp; heart rate.</div>
        <div class="feature"><b>Workouts</b>Strain, HR zones, distance.</div>
      </div>
    </div>
  </section>

  <div class="footer">
    <span>Open source</span>
    <span>Not affiliated with WHOOP, Inc.</span>
  </div>
</main>
</body></html>`))

var resultTpl = template.Must(template.New("result").Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connected — Whoop MCP</title>
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<style>` + sharedCSS + `</style>
</head>
<body>
<main class="page">
  <a class="brand" href="/" aria-label="Whoop MCP">
    <span class="brand-mark">` + brandMarkSVG + `</span>
    <span class="brand-name">Whoop MCP</span>
  </a>

  <section class="result-grid">
    <div>
      <p class="kicker">Connected{{if .Name}} · {{.Name}}{{end}}</p>
      <h1>Your personal MCP URL.</h1>
      <p class="lead">Add this as a remote MCP server in any MCP-capable client. It's tied to your Whoop account — only this URL can read your data.</p>

      <div class="card">
        <div class="url-row">
          <code class="url" id="mcp-url">{{.URL}}</code>
          <button type="button" class="copy-btn" id="copy-btn" aria-label="Copy URL">Copy</button>
        </div>
        <div class="warn"><b>Treat this URL like a password.</b> Anyone with it can read your Whoop data.</div>
      </div>
    </div>

    <div>
      <h2>How to add it</h2>
      <ol class="steps">
        <li>In your MCP client, open the connectors / MCP servers section.</li>
        <li>Add a new <b>remote</b> or <b>HTTP</b> server.</li>
        <li>Paste the URL above; leave any auth fields blank.</li>
        <li>Save, then ask something like "what's my latest recovery?"</li>
      </ol>
    </div>
  </section>

  <div class="footer">
    <span>Lost this URL? Just <a href="/login">reconnect</a> for a fresh one. To revoke, visit <code>/disconnect/&lt;id&gt;</code>.</span>
    <span>Not affiliated with WHOOP, Inc.</span>
  </div>
</main>
<script>
  (function(){
    var btn = document.getElementById('copy-btn');
    var url = document.getElementById('mcp-url');
    btn.addEventListener('click', function(){
      var text = url.textContent.trim();
      var done = function(){ btn.textContent = 'Copied'; setTimeout(function(){ btn.textContent = 'Copy'; }, 1600); };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done, function(){ fallback(text); done(); });
      } else { fallback(text); done(); }
    });
    function fallback(text){
      var ta = document.createElement('textarea');
      ta.value = text; document.body.appendChild(ta); ta.select();
      try { document.execCommand('copy'); } catch(e){}
      document.body.removeChild(ta);
    }
  })();
</script>
</body></html>`))

var disconnectConfirmTpl = template.Must(template.New("disconnect-confirm").Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Disconnect — Whoop MCP</title>
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<style>` + sharedCSS + `</style>
</head>
<body>
<main class="page">
  <a class="brand" href="/" aria-label="Whoop MCP">
    <span class="brand-mark">` + brandMarkSVG + `</span>
    <span class="brand-name">Whoop MCP</span>
  </a>

  <section class="hero">
    <div>
      <p class="kicker">Disconnect{{if .Name}} · {{.Name}}{{end}}</p>
      <h1>Revoke this connection?</h1>
      <p class="lead">This invalidates the OAuth grant with Whoop and deletes the stored tokens. The personal MCP URL stops working immediately. You can always <a href="/login">reconnect</a> later.</p>
      <form method="POST" action="/disconnect/{{.ID}}">
        <button type="submit" class="btn">Disconnect</button>
        <a class="btn btn-ghost" href="/" style="margin-left:8px">Cancel</a>
      </form>
    </div>
    <div></div>
  </section>

  <div class="footer">
    <span>Open source</span>
    <span>Not affiliated with WHOOP, Inc.</span>
  </div>
</main>
</body></html>`))

var disconnectedTpl = template.Must(template.New("disconnected").Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Disconnected — Whoop MCP</title>
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<style>` + sharedCSS + `</style>
</head>
<body>
<main class="page">
  <a class="brand" href="/" aria-label="Whoop MCP">
    <span class="brand-mark">` + brandMarkSVG + `</span>
    <span class="brand-name">Whoop MCP</span>
  </a>

  <section class="hero">
    <div>
      <p class="kicker">Disconnected</p>
      <h1>This connection is revoked.</h1>
      <p class="lead">{{if .RevokeError}}Tokens were deleted locally. Whoop's revoke call did not succeed: <code>{{.RevokeError}}</code>. You can also revoke from your Whoop account settings.{{else}}Your OAuth grant has been revoked and the stored tokens are deleted.{{end}}</p>
      <a class="btn" href="/login">Connect a new account</a>
    </div>
    <div></div>
  </section>

  <div class="footer">
    <span>Open source</span>
    <span>Not affiliated with WHOOP, Inc.</span>
  </div>
</main>
</body></html>`))

func (a *app) handleLanding(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = landingTpl.Execute(w, nil)
}

func (a *app) handleLogin(w http.ResponseWriter, r *http.Request) {
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
		Secure:   strings.HasPrefix(a.publicURL, "https://"),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, a.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (a *app) handleCallback(w http.ResponseWriter, r *http.Request) {
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

	id, err := storeNewID()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rec := &store.Record{
		ID:        id,
		Token:     tok,
		CreatedAt: time.Now().UTC(),
	}
	// Best-effort: fetch profile to label the record.
	src := a.userTokenSource(rec)
	client := a.newWhoopClient(ctx, src)
	if body, err := client.GetProfile(ctx); err == nil {
		var p struct {
			FirstName string `json:"first_name"`
			LastName  string `json:"last_name"`
			Email     string `json:"email"`
		}
		if json.Unmarshal(body, &p) == nil {
			rec.UserName = strings.TrimSpace(p.FirstName + " " + p.LastName)
			rec.UserEmail = p.Email
		}
	}
	if err := a.store.Put(rec); err != nil {
		http.Error(w, "persist user: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = resultTpl.Execute(w, map[string]string{
		"Name": rec.UserName,
		"URL":  a.publicURL + "/connect/" + id,
	})
}

// handleConnect routes /connect/<id> and /connect/<id>/* to the MCP
// StreamableHTTPHandler for that user.
func (a *app) handleConnect(w http.ResponseWriter, r *http.Request) {
	// /connect/<id>[/...]
	rest := strings.TrimPrefix(r.URL.Path, "/connect/")
	id := rest
	if i := strings.Index(rest, "/"); i >= 0 {
		id = rest[:i]
	}
	if id == "" {
		http.NotFound(w, r)
		return
	}
	rec, err := a.store.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	handler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		src := a.userTokenSource(rec)
		client := a.newWhoopClient(r.Context(), src)
		s := newServer()
		registerTools(s, client)
		return s
	}, nil)

	// Strip /connect/<id> from the path so MCP session paths start at /.
	prefix := "/connect/" + id
	r2 := r.Clone(r.Context())
	r2.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
	if r2.URL.Path == "" {
		r2.URL.Path = "/"
	}
	handler.ServeHTTP(w, r2)
}

// handleDisconnect shows a confirmation page on GET and revokes +
// deletes the user record on POST. The id in the URL is the credential
// (same model as /connect/<id>) so no separate auth is needed.
func (a *app) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/disconnect/")
	if id == "" || strings.ContainsAny(id, `/\`) {
		http.NotFound(w, r)
		return
	}
	rec, err := a.store.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = disconnectConfirmTpl.Execute(w, map[string]string{"ID": id, "Name": rec.UserName})
	case http.MethodPost:
		src := a.userTokenSource(rec)
		client := a.newWhoopClient(r.Context(), src)
		revokeErr := client.RevokeAccess(r.Context())
		// Delete the local record either way — if revoke failed, the
		// user can finish revoking via Whoop's own account settings.
		_ = a.store.Delete(id)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = disconnectedTpl.Execute(w, map[string]any{"RevokeError": revokeErr})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// userTokenSource returns an oauth2 TokenSource that refreshes against
// Whoop and writes the rotated token back into the user's store record.
func (a *app) userTokenSource(rec *store.Record) oauth2.TokenSource {
	base := a.oauth.TokenSource(context.Background(), rec.Token)
	return &persistingUserSource{base: base, store: a.store, id: rec.ID, last: rec.Token}
}

type persistingUserSource struct {
	base  oauth2.TokenSource
	store *store.Store
	id    string

	mu   sync.Mutex
	last *oauth2.Token
}

func (p *persistingUserSource) Token() (*oauth2.Token, error) {
	tok, err := p.base.Token()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if tok == p.last {
		return tok, nil
	}
	rec, err := p.store.Get(p.id)
	if err != nil {
		return nil, fmt.Errorf("reload user: %w", err)
	}
	rec.Token = tok
	rec.LastSeenAt = time.Now().UTC()
	if err := p.store.Put(rec); err != nil {
		return nil, fmt.Errorf("persist user token: %w", err)
	}
	p.last = tok
	return tok, nil
}

// quiet "unused" lints for vars we may grow into later.
var _ = url.PathEscape
var _ = path.Join

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
