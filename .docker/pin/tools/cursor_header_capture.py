"""mitmproxy addon: log redacted Cursor OpenAI-compat stream shape."""

import json

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
    body_fields, message_count, tool_count, stream = _request_body_shape(flow.request.content or b"")
    print(
        f"cursor_capture method={flow.request.method} path={flow.request.path} "
        f"body_bytes={body_len} body_fields={','.join(body_fields) if body_fields else '-'} "
        f"message_count={message_count} tool_count={tool_count} stream={stream} "
        f"headers={'; '.join(headers) if headers else '-'}",
        flush=True,
    )


def response(flow: http.HTTPFlow) -> None:
    if "/v1/chat/completions" not in flow.request.path:
        return
    content_type = flow.response.headers.get("content-type", "")
    if "text/event-stream" not in content_type:
        return
    shape = _response_stream_shape(flow.response.content or b"")
    print(
        "cursor_response_shape "
        f"events={shape['events']} tool_frames={shape['tool_frames']} "
        f"empty_tool_frames={shape['empty_tool_frames']} finish={shape['finish']} "
        f"done={shape['done']} tool_indexes={','.join(shape['tool_indexes']) if shape['tool_indexes'] else '-'} "
        f"tool_ids_present={shape['tool_ids_present']} tool_names_present={shape['tool_names_present']} "
        f"tool_args_json_valid={shape['tool_args_json_valid']}",
        flush=True,
    )


def _request_body_shape(raw: bytes) -> tuple[list[str], int, int, str]:
    try:
        body = json.loads(raw.decode("utf-8"))
    except Exception:
        return [], 0, 0, "unknown"
    if not isinstance(body, dict):
        return [], 0, 0, "unknown"
    fields = sorted(body.keys())
    messages = body.get("messages")
    tools = body.get("tools")
    message_count = len(messages) if isinstance(messages, list) else 0
    tool_count = len(tools) if isinstance(tools, list) else 0
    stream = str(body.get("stream", "missing")).lower()
    return fields, message_count, tool_count, stream


def _response_stream_shape(raw: bytes) -> dict[str, object]:
    shape = {
        "events": 0,
        "tool_frames": 0,
        "empty_tool_frames": 0,
        "finish": "missing",
        "done": False,
        "tool_indexes": [],
        "tool_ids_present": True,
        "tool_names_present": True,
        "tool_args_json_valid": True,
    }
    for event in raw.decode("utf-8", errors="replace").split("\n\n"):
        if not event.startswith("data: "):
            continue
        payload = event.removeprefix("data: ")
        if payload == "[DONE]":
            shape["done"] = True
            continue
        shape["events"] += 1
        try:
            chunk = json.loads(payload)
        except Exception:
            continue
        for choice in chunk.get("choices", []):
            finish = choice.get("finish_reason")
            if finish:
                shape["finish"] = finish
            delta = choice.get("delta") or {}
            for tool_call in delta.get("tool_calls") or []:
                shape["tool_frames"] += 1
                function = tool_call.get("function") or {}
                has_useful_field = any(
                    [
                        tool_call.get("id"),
                        tool_call.get("type"),
                        function.get("name"),
                        function.get("arguments"),
                    ]
                )
                if not has_useful_field:
                    shape["empty_tool_frames"] += 1
                shape["tool_indexes"].append(str(tool_call.get("index", "missing")))
                if not tool_call.get("id"):
                    shape["tool_ids_present"] = False
                if not function.get("name"):
                    shape["tool_names_present"] = False
                try:
                    json.loads(function.get("arguments", ""))
                except Exception:
                    shape["tool_args_json_valid"] = False
    return shape
