package server

import "github.com/gofiber/fiber/v2"

// accountUsagePage contains no credentials or account data. The browser sends
// the bearer token only to the existing same-origin usage endpoint.
func accountUsagePage(c *fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	c.Set("Cache-Control", "no-store")
	c.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	c.Set("Referrer-Policy", "no-referrer")
	return c.SendString(usagePageHTML)
}

const usagePageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Codex account usage</title>
<style>
:root { color-scheme: dark; font: 16px system-ui, sans-serif; background: #10151c; color: #e7ecf2; }
body { max-width: 850px; margin: 3rem auto; padding: 0 1rem; }
h1 { font-size: 1.6rem; margin-bottom: .25rem; }
p { color: #aebdca; }
.controls { display: flex; gap: .5rem; margin: 1.5rem 0; flex-wrap: wrap; }
input, button { font: inherit; padding: .6rem .8rem; border-radius: .4rem; border: 1px solid #657384; background: #19232e; color: inherit; }
input { min-width: 17rem; flex: 1; }
button { cursor: pointer; }
button:disabled { opacity: .6; cursor: wait; }
#status { min-height: 1.5rem; }
.cards { display: grid; gap: 1rem; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); }
.card { border: 1px solid #344555; border-radius: .7rem; padding: 1rem; background: #19232e; overflow-wrap: anywhere; }
.card h2 { font-size: 1rem; margin: 0 0 .6rem; }
.card p { margin: .4rem 0; }
.meter { height: .6rem; background: #344555; border-radius: 1rem; overflow: hidden; }
.meter span { display: block; height: 100%; background: #68bcab; }
.meter span.full { background: #f39a72; }
small { color: #aebdca; }
</style>
</head>
<body>
<h1>Codex account usage</h1>
<p>Read-only live usage. The token stays in this tab and is never saved. This page only contacts its own API.</p>
<div class="controls">
  <input id="token" type="password" autocomplete="off" placeholder="Gateway bearer token" aria-label="Gateway bearer token">
  <button id="refresh" type="button">Refresh</button>
</div>
<p id="status" role="status">Enter the gateway token to load account usage.</p>
<div id="cards" class="cards"></div>
<script>
(() => {
  const input = document.getElementById('token');
  const button = document.getElementById('refresh');
  const status = document.getElementById('status');
  const cards = document.getElementById('cards');
  const date = value => value ? new Date(value).toLocaleString() : 'Unknown';
  const field = (parent, text) => { const p = document.createElement('p'); p.textContent = text; parent.append(p); };
  async function refresh() {
    const token = input.value.trim();
    if (!token) { status.textContent = 'Enter the gateway token first.'; return; }
    button.disabled = true;
    status.textContent = 'Loading…';
    try {
      const response = await fetch('/v1/accounts/usage', {
        headers: { Authorization: 'Bearer ' + token }, cache: 'no-store', credentials: 'omit'
      });
      if (!response.ok) throw new Error(response.status === 401 ? 'Invalid gateway token.' : 'Usage request failed (HTTP ' + response.status + ').');
      const data = await response.json();
      cards.replaceChildren();
      for (const account of data.accounts || []) {
        const card = document.createElement('section');
        card.className = 'card';
        const heading = document.createElement('h2');
        heading.textContent = account.account_name || account.label;
        card.append(heading);
        field(card, 'Status: ' + (account.status || 'unknown'));
        for (const window of account.windows || []) {
          const used = Number(window.used_percent);
          field(card, (window.type || 'Usage') + ': ' + (Number.isFinite(used) ? used + '% used' : 'Unknown'));
          if (Number.isFinite(used)) {
            const meter = document.createElement('div');
            meter.className = 'meter';
            meter.setAttribute('role', 'progressbar');
            meter.setAttribute('aria-label', (window.type || 'Usage') + ' used');
            meter.setAttribute('aria-valuenow', String(used));
            meter.setAttribute('aria-valuemin', '0');
            meter.setAttribute('aria-valuemax', '100');
            const fill = document.createElement('span');
            if (used >= 100) fill.className = 'full';
            fill.style.width = Math.max(0, Math.min(100, used)) + '%';
            meter.append(fill);
            card.append(meter);
          }
          field(card, 'Resets: ' + date(window.reset_at));
        }
        field(card, 'Banked resets: ' + (account.banked_reset_count ?? 0));
        if (account.error_code) field(card, 'Error: ' + account.error_code);
        cards.append(card);
      }
      status.textContent = (data.accounts || []).length + ' accounts · Updated ' + new Date().toLocaleTimeString();
    } catch (error) {
      cards.replaceChildren();
      status.textContent = error.message;
    } finally {
      button.disabled = false;
    }
  }
  button.addEventListener('click', refresh);
  input.addEventListener('keydown', event => { if (event.key === 'Enter') refresh(); });
})();
</script>
</body>
</html>`
