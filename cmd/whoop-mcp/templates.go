package main

import "html/template"

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
  --ok-bg: #ecfdf5;
  --ok-ink: #065f46;
  --ok-line: #bbf7d0;
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
.hero-cta { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
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
.pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: var(--ok-bg);
  border: 1px solid var(--ok-line);
  color: var(--ok-ink);
  border-radius: 999px;
  padding: 4px 11px;
  font-size: 12px;
  font-weight: 600;
}
.pill::before {
  content: "";
  width: 7px; height: 7px;
  border-radius: 50%;
  background: var(--accent);
}
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
.field {
  width: 100%;
  font-size: 14px;
  border: 1px solid var(--line);
  border-radius: var(--radius-sm);
  padding: 11px 13px;
  margin: 0 0 12px;
  background: var(--surface);
  color: var(--ink);
}
.err {
  color: #b42318;
  font-size: 13px;
  margin: 0 0 12px;
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

// brandMarkSVG is an original logomark — a rounded badge with an abstract
// pulse line. Used in the header and as the favicon.
const brandMarkSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" fill="none" role="img" aria-label="Whoop MCP"><rect width="32" height="32" rx="9" fill="currentColor"/><path d="M6 17 L10 17 L12 12 L15 22 L18 9 L21 19 L23 17 L26 17" stroke="#fff" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg>`

const htmlHead = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Whoop MCP</title>
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<style>` + sharedCSS + `</style>
</head>`

const brandHeader = `<a class="brand" href="/" aria-label="Whoop MCP">
    <span class="brand-mark">` + brandMarkSVG + `</span>
    <span class="brand-name">Whoop MCP</span>
  </a>`

// landingTpl is the dashboard. Before connecting it invites the operator
// to connect their Whoop account; after connecting it shows the fixed MCP
// endpoint URL and how to wire it into a client.
var landingTpl = template.Must(template.New("landing").Parse(htmlHead + `
<body>
<main class="page">
  ` + brandHeader + `
{{if .Connected}}
  <section class="result-grid">
    <div>
      <p class="kicker"><span class="pill">Connected</span></p>
      <h1>Your Whoop MCP endpoint.</h1>
      <p class="lead">Add this as a remote MCP server in any MCP-capable client. It serves your Whoop data using the account connected on this server.</p>

      <div class="card">
        <div class="url-row">
          <code class="url" id="mcp-url">{{.MCPURL}}</code>
          <button type="button" class="copy-btn" id="copy-btn" aria-label="Copy URL">Copy</button>
        </div>
        {{if .AuthToken}}<div class="warn"><b>Auth required.</b> This server has an access token set. Send it as an <code>Authorization: Bearer &lt;token&gt;</code> header from your MCP client.</div>{{end}}
      </div>

      <p style="margin-top:18px"><a class="btn btn-ghost" href="/disconnect">Disconnect Whoop</a></p>
    </div>

    <div>
      <h2>How to add it</h2>
      <ol class="steps">
        <li>In your MCP client, open the connectors / MCP servers section.</li>
        <li>Add a new <b>remote</b> or <b>HTTP</b> server.</li>
        <li>Paste the URL above.{{if .AuthToken}} Set the bearer token in the auth field.{{else}} Leave any auth fields blank.{{end}}</li>
        <li>Save, then ask something like "what's my latest recovery?"</li>
      </ol>
    </div>
  </section>
{{else}}
  <section class="hero">
    <div>
      <p class="kicker">Model Context Protocol</p>
      <h1>Your Whoop data, self-hosted.</h1>
      <p class="lead">Connect your Whoop account once. This server then serves your data to any client that speaks the Model Context Protocol — running entirely on infrastructure you control.</p>
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
{{end}}
  <div class="footer">
    <span>Self-hosted · open source</span>
    <span>Not affiliated with WHOOP, Inc.</span>
  </div>
</main>
{{if .Connected}}
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
{{end}}
</body></html>`))

// unlockTpl gates the browser UI when AUTH_TOKEN is set: the operator must
// paste the token to obtain the admin cookie.
var unlockTpl = template.Must(template.New("unlock").Parse(htmlHead + `
<body>
<main class="page">
  ` + brandHeader + `
  <section class="hero">
    <div>
      <p class="kicker">Locked</p>
      <h1>Enter your access token.</h1>
      <p class="lead">This server is protected. Paste the <code>AUTH_TOKEN</code> you configured to manage the Whoop connection.</p>
      {{if .Error}}<p class="err">That token didn't match. Try again.</p>{{end}}
      <form method="POST" action="/unlock">
        <input class="field" type="password" name="token" placeholder="Access token" autofocus autocomplete="off">
        <button type="submit" class="btn">Unlock</button>
      </form>
    </div>
    <div></div>
  </section>
  <div class="footer">
    <span>Self-hosted · open source</span>
    <span>Not affiliated with WHOOP, Inc.</span>
  </div>
</main>
</body></html>`))

var disconnectConfirmTpl = template.Must(template.New("disconnect-confirm").Parse(htmlHead + `
<body>
<main class="page">
  ` + brandHeader + `
  <section class="hero">
    <div>
      <p class="kicker">Disconnect</p>
      <h1>Disconnect your Whoop account?</h1>
      <p class="lead">This invalidates the OAuth grant with Whoop and deletes the stored token. The MCP endpoint stops returning data until you <a href="/login">reconnect</a>.</p>
      <form method="POST" action="/disconnect">
        <button type="submit" class="btn">Disconnect</button>
        <a class="btn btn-ghost" href="/" style="margin-left:8px">Cancel</a>
      </form>
    </div>
    <div></div>
  </section>
  <div class="footer">
    <span>Self-hosted · open source</span>
    <span>Not affiliated with WHOOP, Inc.</span>
  </div>
</main>
</body></html>`))

var disconnectedTpl = template.Must(template.New("disconnected").Parse(htmlHead + `
<body>
<main class="page">
  ` + brandHeader + `
  <section class="hero">
    <div>
      <p class="kicker">Disconnected</p>
      <h1>Your Whoop account is disconnected.</h1>
      <p class="lead">{{if .RevokeError}}The stored token was deleted. Whoop's revoke call did not succeed: <code>{{.RevokeError}}</code>. You can also revoke from your Whoop account settings.{{else}}Your OAuth grant has been revoked and the stored token deleted.{{end}}</p>
      <a class="btn" href="/login">Reconnect Whoop</a>
    </div>
    <div></div>
  </section>
  <div class="footer">
    <span>Self-hosted · open source</span>
    <span>Not affiliated with WHOOP, Inc.</span>
  </div>
</main>
</body></html>`))
