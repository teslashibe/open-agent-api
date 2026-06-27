"""mitmproxy addon: log Cursor OpenAI-compat request headers."""

from mitmproxy import http

INTERESTING_PREFIXES = (
    "x-cursor-",
    "x-request-",
    "x-client-",
    "x-session-",
    "x-conversation-",
    "openai-",
    "authorization",
    "user-agent",
    "cookie",
)


def _interesting_headers(headers) -> list[str]:
    out = []
    for name, value in headers.items():
        lower = name.lower()
        if any(lower == p or lower.startswith(p) for p in INTERESTING_PREFIXES):
            out.append(f"{name}={value}")
        elif lower.startswith("x-"):
            out.append(f"{name}={value}")
    return out


def request(flow: http.HTTPFlow) -> None:
    if "/v1/chat/completions" not in flow.request.path:
        return
    headers = _interesting_headers(flow.request.headers)
    body_len = len(flow.request.content or b"")
    print(
        f"cursor_capture method={flow.request.method} path={flow.request.path} "
        f"body_bytes={body_len} headers={'; '.join(headers) if headers else '-'}",
        flush=True,
    )
