#!/usr/bin/env bash
# Sync Antigravity CLI keyring tokens into ~/.gemini/antigravity_oauth_creds.json
# so Docker / open-agent-api can refresh and call gemini-3.* models.
set -euo pipefail

OUT="${GEMINI_HOME:-$HOME/.gemini}/antigravity_oauth_creds.json"
CLIENT_ID="1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
CLIENT_SECRET="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"

if ! command -v security >/dev/null 2>&1; then
  echo "sync-antigravity-auth: macOS 'security' required" >&2
  exit 1
fi

raw="$(security find-generic-password -s gemini -a antigravity -w 2>/dev/null || true)"
if [[ -z "$raw" ]]; then
  echo "sync-antigravity-auth: no keyring item (service=gemini account=antigravity). Run: agy" >&2
  exit 1
fi

python3 - "$OUT" "$CLIENT_ID" "$CLIENT_SECRET" "$raw" <<'PY'
import base64, json, sys, time, urllib.parse, urllib.request
from pathlib import Path

out, client_id, client_secret, raw = sys.argv[1:5]
if not raw.startswith("go-keyring-base64:"):
    raise SystemExit("unexpected keyring encoding (want go-keyring-base64:)")
stored = json.loads(base64.b64decode(raw[len("go-keyring-base64:"):]))
token = stored.get("token") or {}
refresh = token.get("refresh_token")
if not refresh:
    raise SystemExit("keyring token missing refresh_token; re-login with agy")

data = urllib.parse.urlencode({
    "client_id": client_id,
    "client_secret": client_secret,
    "refresh_token": refresh,
    "grant_type": "refresh_token",
}).encode()
req = urllib.request.Request("https://oauth2.googleapis.com/token", data=data, method="POST")
with urllib.request.urlopen(req, timeout=30) as resp:
    body = json.loads(resp.read().decode())
access = body.get("access_token")
if not access:
    raise SystemExit(f"refresh failed: {body}")

payload = {
    "access_token": access,
    "refresh_token": refresh,
    "token_type": body.get("token_type", "Bearer"),
    "expiry_date": int(time.time() * 1000) + int(body.get("expires_in", 3600)) * 1000,
}
path = Path(out)
path.parent.mkdir(parents=True, exist_ok=True)
path.write_text(json.dumps(payload))
path.chmod(0o600)
print(f"wrote {path}")
PY
