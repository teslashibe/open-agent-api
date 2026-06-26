"""Async client for the Codex backend (chatgpt.com) WebSocket Responses API.

Reverse-engineered from codex_cli_rs 0.142.0. Uses the ChatGPT OAuth token in
~/.codex/auth.json (auth_mode = "chatgpt"); no sk- API key required.

Two modes:
  * faithful=True  (DEFAULT): replicate the real Codex CLI request as closely as a
    Python client can -- exact header set/order/values, the real ~21 KB instructions
    and 14 tools, the developer/environment scaffold, per-turn metadata, and a
    prewarm-then-turn connection sequence.
  * faithful=False: a minimal plain-chat request (small instructions, no tools).

NOTE: even in faithful mode the TLS fingerprint (JA3/JA4) is Python/OpenSSL, not
Rust/rustls, and the lib-generated WebSocket upgrade headers (Host/Upgrade/
Connection/Sec-WebSocket-*) follow `websockets` ordering. The application layer
(app headers, body, behavior) is matched; the transport layer cannot be from pure
Python. See README.
"""
from __future__ import annotations

import json
import os
import ssl
import time
import uuid
from datetime import datetime
from pathlib import Path
from typing import AsyncIterator, Iterable

import certifi
import websockets

WS_URL = "wss://chatgpt.com/backend-api/codex/responses"
DEFAULT_MODEL = "gpt-5.5"
CODEX_VERSION = "0.142.0"
CODEX_UA = f"codex_cli_rs/{CODEX_VERSION} (Mac OS 26.2.0; arm64) dumb"
CODEX_BETA = "responses_websockets=2026-02-06"
CODEX_BETA_FEATURES = "remote_compaction_v2"

_SSL = ssl.create_default_context(cafile=certifi.where())
_HERE = Path(__file__).resolve().parent


class CodexAuthError(RuntimeError):
    pass


class CodexBackendError(RuntimeError):
    def __init__(self, status, payload):
        self.status = status
        self.payload = payload
        super().__init__(f"backend error {status}: {payload}")


# --------------------------------------------------------------------------- #
# credentials + static profile
# --------------------------------------------------------------------------- #
def _codex_home() -> Path:
    return Path(os.environ.get("CODEX_HOME") or (Path.home() / ".codex"))


def load_credentials() -> tuple[str, str]:
    """(access_token, account_id), read fresh so token refreshes are picked up."""
    p = _codex_home() / "auth.json"
    if not p.exists():
        raise CodexAuthError(f"{p} not found. Run `codex login` first.")
    try:
        tokens = json.loads(p.read_text())["tokens"]
        return tokens["access_token"], tokens["account_id"]
    except (KeyError, json.JSONDecodeError) as e:
        raise CodexAuthError(f"could not parse credentials from {p}: {e}") from e


def _installation_id() -> str:
    p = _codex_home() / "installation_id"
    try:
        return p.read_text().strip()
    except OSError:
        return str(uuid.uuid4())


def _load_json(name: str) -> dict | None:
    p = _HERE / name
    if p.exists():
        return json.loads(p.read_text())
    return None


_PROFILE = _load_json("codex_profile.json")
_SCAFFOLD = _load_json("codex_scaffold.json")


# --------------------------------------------------------------------------- #
# session identity (codex-style)
# --------------------------------------------------------------------------- #
def _new_session_id() -> str:
    # codex uses uuidv7; fall back to uuid4 if unavailable
    return str(uuid.uuid7()) if hasattr(uuid, "uuid7") else str(uuid.uuid4())


def _turn_metadata(sid: str, request_kind: str, turn_id: str = "") -> str:
    return json.dumps(
        {
            "installation_id": _installation_id(),
            "session_id": sid,
            "thread_id": sid,
            "turn_id": turn_id,
            "window_id": f"{sid}:0",
            "request_kind": request_kind,
            "thread_source": "user",
            "sandbox": "seatbelt",
        }
    )


def _new_turn_id() -> str:
    return str(uuid.uuid7()) if hasattr(uuid, "uuid7") else str(uuid.uuid4())


def _headers(sid: str, faithful: bool, request_kind: str) -> dict:
    token, account = load_credentials()
    if not faithful:
        return {
            "authorization": f"Bearer {token}",
            "chatgpt-account-id": account,
            "originator": "codex_cli_rs",
            "user-agent": "codex_cli_rs/0.142.0 (api wrapper) dumb",
            "version": CODEX_VERSION,
            "openai-beta": CODEX_BETA,
            "session-id": sid,
        }
    # faithful: full set, app-header order as observed from real codex
    return {
        "chatgpt-account-id": account,
        "authorization": f"Bearer {token}",
        "user-agent": CODEX_UA,
        "originator": "codex_cli_rs",
        "openai-beta": CODEX_BETA,
        "version": CODEX_VERSION,
        "x-codex-beta-features": CODEX_BETA_FEATURES,
        "x-client-request-id": sid,
        "session-id": sid,
        "thread-id": sid,
        "x-codex-window-id": f"{sid}:0",
        "x-codex-turn-metadata": _turn_metadata(sid, request_kind),
    }


# --------------------------------------------------------------------------- #
# request body builders
# --------------------------------------------------------------------------- #
def _environment_context() -> str:
    cwd = os.getcwd()
    now = datetime.now().astimezone()
    tz = now.tzname() or "UTC"
    template = (_SCAFFOLD or {}).get("environment_context")
    if template:
        # replace the dynamic fields, keep codex's exact wrapping/whitespace
        import re

        s = re.sub(r"<cwd>.*?</cwd>", f"<cwd>{cwd}</cwd>", template, flags=re.S)
        s = re.sub(
            r"<current_date>.*?</current_date>",
            f"<current_date>{now.strftime('%Y-%m-%d')}</current_date>",
            s,
        )
        s = re.sub(r"<timezone>.*?</timezone>", f"<timezone>{tz}</timezone>", s)
        return s
    return f"<environment_context>\n  <cwd>{cwd}</cwd>\n</environment_context>"


def _split_messages(messages: Iterable[dict]) -> tuple[list[str], list[dict]]:
    """Return (system_texts, conversation_items) from OpenAI-style messages."""
    sys_texts: list[str] = []
    items: list[dict] = []
    for m in messages:
        role = m.get("role", "user")
        content = m.get("content", "")
        if isinstance(content, list):
            content = "".join(
                p.get("text", "") for p in content if isinstance(p, dict)
            )
        if role == "system":
            sys_texts.append(content)
            continue
        part_type = "output_text" if role == "assistant" else "input_text"
        items.append(
            {
                "type": "message",
                "role": role,
                "content": [{"type": part_type, "text": content}],
            }
        )
    return sys_texts, items


def build_faithful_request(
    messages, model, sid, request_kind, reasoning_effort, verbosity
) -> dict:
    prof = _PROFILE or {}
    sys_texts, convo = _split_messages(messages)

    turn_id = "" if request_kind == "prewarm" else _new_turn_id()
    if request_kind == "prewarm":
        inp = []
    else:
        inp = []
        if _SCAFFOLD and _SCAFFOLD.get("developer_item"):
            inp.append(_SCAFFOLD["developer_item"])
        # extra system messages from the caller, appended as developer text
        if sys_texts:
            inp.append(
                {
                    "type": "message",
                    "role": "developer",
                    "content": [
                        {"type": "input_text", "text": t} for t in sys_texts
                    ],
                }
            )
        inp.append(
            {
                "type": "message",
                "role": "user",
                "content": [{"type": "input_text", "text": _environment_context()}],
            }
        )
        inp.extend(convo)

    req = {
        "type": "response.create",
        "model": model or prof.get("model") or DEFAULT_MODEL,
        "instructions": prof.get("instructions", "You are Codex."),
        "input": inp,
        "tools": prof.get("tools", []),
        "tool_choice": prof.get("tool_choice", "auto"),
        "parallel_tool_calls": prof.get("parallel_tool_calls", True),
        "reasoning": {"effort": reasoning_effort},
        "store": False,
        "stream": True,
        "include": prof.get("include", ["reasoning.encrypted_content"]),
        "prompt_cache_key": sid,
        "text": {"verbosity": verbosity},
        "client_metadata": {
            "session_id": sid,
            "thread_id": sid,
            "turn_id": turn_id,
            "x-codex-turn-metadata": _turn_metadata(sid, request_kind, turn_id),
            "x-codex-ws-stream-request-start-ms": str(int(time.time() * 1000)),
            "x-codex-installation-id": _installation_id(),
            "x-codex-window-id": f"{sid}:0",
        },
    }
    # only the prewarm carries `generate: false`; the real turn omits it
    if request_kind == "prewarm":
        req["generate"] = False
    return req


def build_minimal_request(messages, model, reasoning_effort, verbosity) -> dict:
    sys_texts, convo = _split_messages(messages)
    return {
        "type": "response.create",
        "model": model or DEFAULT_MODEL,
        "instructions": "\n\n".join(sys_texts) or "You are a helpful assistant.",
        "input": convo,
        "stream": True,
        "store": False,
        "reasoning": {"effort": reasoning_effort},
        "text": {"verbosity": verbosity},
        "prompt_cache_key": str(uuid.uuid4()),
    }


# --------------------------------------------------------------------------- #
# transport
# --------------------------------------------------------------------------- #
async def _prewarm(sid: str, model: str, reasoning_effort, verbosity) -> None:
    """Open a prewarm connection exactly like codex does, send it, then close."""
    req = build_faithful_request([], model, sid, "prewarm", reasoning_effort, verbosity)
    try:
        async with websockets.connect(
            WS_URL,
            additional_headers=_headers(sid, True, "prewarm"),
            user_agent_header=None,
            max_size=None,
            ssl=_SSL,
            open_timeout=10,
        ) as ws:
            await ws.send(json.dumps(req))
            # codex fires-and-forgets the prewarm; give the server a moment
            try:
                await ws.recv()
            except Exception:
                pass
    except Exception:
        # prewarm is best-effort; never fail the real turn because of it
        pass


async def stream_chat(
    messages,
    model: str = DEFAULT_MODEL,
    reasoning_effort: str = "medium",
    verbosity: str = "medium",
    faithful: bool = True,
    prewarm: bool = True,
) -> AsyncIterator[dict]:
    """Yield normalized events: {"type": "delta"|"done"|"error", ...}."""
    sid = _new_session_id()

    if faithful:
        if prewarm:
            await _prewarm(sid, model, reasoning_effort, verbosity)
        req = build_faithful_request(
            messages, model, sid, "turn", reasoning_effort, verbosity
        )
        headers = _headers(sid, True, "turn")
    else:
        req = build_minimal_request(messages, model, reasoning_effort, verbosity)
        headers = _headers(sid, False, "turn")

    async with websockets.connect(
        WS_URL,
        additional_headers=headers,
        user_agent_header=None,  # we supply our own user-agent
        max_size=None,
        ssl=_SSL,
    ) as ws:
        await ws.send(json.dumps(req))
        async for raw in ws:
            ev = json.loads(raw)
            t = ev.get("type")
            if t == "response.output_text.delta":
                yield {"type": "delta", "text": ev.get("delta", "")}
            elif t == "response.completed":
                yield {
                    "type": "done",
                    "usage": ev["response"].get("usage"),
                    "model": ev["response"].get("model"),
                    "id": ev["response"].get("id"),
                }
                return
            elif t in ("response.failed", "error"):
                raise CodexBackendError(ev.get("status", 500), ev.get("error", ev))


async def chat(
    messages,
    model: str = DEFAULT_MODEL,
    reasoning_effort: str = "medium",
    verbosity: str = "medium",
    faithful: bool = True,
    prewarm: bool = True,
) -> dict:
    text = ""
    meta: dict = {}
    async for ev in stream_chat(
        messages, model, reasoning_effort, verbosity, faithful, prewarm
    ):
        if ev["type"] == "delta":
            text += ev["text"]
        elif ev["type"] == "done":
            meta = ev
    return {"text": text, **{k: meta.get(k) for k in ("usage", "model", "id")}}
