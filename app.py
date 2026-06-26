"""OpenAI-compatible chat API backed by the Codex (chatgpt.com) WebSocket.

Endpoints:
  GET  /health
  GET  /v1/models
  POST /v1/chat/completions   (supports stream=true via SSE)

Point any OpenAI client at http://127.0.0.1:8088/v1 with a dummy api key.
"""
from __future__ import annotations

import json
import time
import uuid

from fastapi import FastAPI, HTTPException
from fastapi.responses import StreamingResponse
from pydantic import BaseModel

import codex_backend as cx

app = FastAPI(title="Codex Chat API", version="0.1.0")

MODELS = ["gpt-5.5", "gpt-5.3-codex", "gpt-5"]


class Message(BaseModel):
    role: str
    content: str | list = ""


class ChatRequest(BaseModel):
    model: str = cx.DEFAULT_MODEL
    messages: list[Message]
    stream: bool = False
    reasoning_effort: str = "medium"
    verbosity: str = "medium"
    # codex-exact app-layer fingerprint is the DEFAULT
    faithful: bool = True
    prewarm: bool = True


@app.get("/health")
async def health():
    try:
        cx.load_credentials()
        return {"status": "ok", "auth": "loaded"}
    except cx.CodexAuthError as e:
        return {"status": "degraded", "auth": str(e)}


@app.get("/v1/models")
async def models():
    return {
        "object": "list",
        "data": [
            {"id": m, "object": "model", "created": 0, "owned_by": "codex"}
            for m in MODELS
        ],
    }


def _oai_chunk(cid, model, delta=None, finish=None):
    return {
        "id": cid,
        "object": "chat.completion.chunk",
        "created": int(time.time()),
        "model": model,
        "choices": [
            {
                "index": 0,
                "delta": delta or {},
                "finish_reason": finish,
            }
        ],
    }


@app.post("/v1/chat/completions")
async def chat_completions(req: ChatRequest):
    messages = [m.model_dump() for m in req.messages]
    cid = "chatcmpl-" + uuid.uuid4().hex
    model = req.model

    if req.stream:

        async def gen():
            yield f"data: {json.dumps(_oai_chunk(cid, model, delta={'role': 'assistant'}))}\n\n"
            try:
                async for ev in cx.stream_chat(
                    messages, model, req.reasoning_effort, req.verbosity,
                    req.faithful, req.prewarm,
                ):
                    if ev["type"] == "delta":
                        chunk = _oai_chunk(cid, model, delta={"content": ev["text"]})
                        yield f"data: {json.dumps(chunk)}\n\n"
                    elif ev["type"] == "done":
                        yield f"data: {json.dumps(_oai_chunk(cid, model, finish='stop'))}\n\n"
            except cx.CodexBackendError as e:
                err = _oai_chunk(cid, model, delta={"content": f"[error: {e.payload}]"}, finish="stop")
                yield f"data: {json.dumps(err)}\n\n"
            yield "data: [DONE]\n\n"

        return StreamingResponse(gen(), media_type="text/event-stream")

    try:
        result = await cx.chat(
            messages, model, req.reasoning_effort, req.verbosity,
            req.faithful, req.prewarm,
        )
    except cx.CodexAuthError as e:
        raise HTTPException(status_code=401, detail=str(e))
    except cx.CodexBackendError as e:
        raise HTTPException(status_code=e.status if isinstance(e.status, int) else 502, detail=e.payload)

    usage = result.get("usage") or {}
    return {
        "id": cid,
        "object": "chat.completion",
        "created": int(time.time()),
        "model": result.get("model") or model,
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": result["text"]},
                "finish_reason": "stop",
            }
        ],
        "usage": {
            "prompt_tokens": usage.get("input_tokens", 0),
            "completion_tokens": usage.get("output_tokens", 0),
            "total_tokens": usage.get("total_tokens", 0),
        },
    }
