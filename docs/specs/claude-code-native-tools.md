# Spec: Claude Code native tool calling for Cursor BYOK

Status: implemented on branch `claude-native-tools` · 2026-09-27 · Claude Code 2.1.283 · Cursor 3.21.16

## Goal

Bring the Claude Code provider up to the Cursor tool-calling contract that the Codex
path already meets. We will stop prompt-injecting a text tool protocol. Instead, the
model gets Cursor's tools as real tools, emits native `tool_use` blocks, and the
gateway sees full native history on every turn. At the same time we update the
Claude Code pin and add the Claude 5.x model slugs.

## What we verified

Every item below was run locally against `claude` 2.1.283 (haiku, Opus 5.5, Fable 5.1).

| # | Finding | Evidence |
|---|---|---|
| 1 | `claude -p --output-format stream-json --include-partial-messages` output has not changed in a way that matters. The current parser keeps working. | `claude-haiku-4-5`, `claude-opus-5-5` and `claude-fable-5-1` all worked with `--effort max`. |
| 2 | Cursor's tools can be exposed to the CLI through an MCP server passed with `--mcp-config`. The model then emits native `tool_use` blocks, including **parallel** calls. The blocks stream as `content_block_start{tool_use}` + `input_json_delta`, followed by a full `assistant` event with complete `input`. | The init event listed `mcp__cursor__read_file`, `mcp__cursor__list_dir`; two parallel calls were streamed. |
| 3 | `--permission-mode dontAsk --max-turns 1` means the tool is **never executed**. The MCP server received `initialize`, `tools/list` and no `tools/call`. The process exits after `message_stop{stop_reason:tool_use}`, in about 4s end to end on haiku. | `mcp.log` |
| 4 | A freeform tool (`apply_patch` exposed as `{input: string}`) works. | V4A patch returned in `input.input`. |
| 5 | **Native history replay works.** We write a synthetic transcript to the CLI's project dir, pass it with `--resume <uuid>`, then send the tail over `--input-format stream-json` (last assistant `tool_use` + user `tool_result`). This produces exactly one model call, the model sees prior turns, and Cursor's call IDs are preserved. | The model recalled a fact from turn 1 and used the tool result. |
| 6 | If the transcript ends in an unanswered `tool_use`, the CLI inserts an "interrupted" `tool_result` when resuming. Sending **only** user lines over stdin starts one model turn per line. The tail must therefore be sent as `assistant` + a single `user` line. | Observed. The CLI also adds a harmless "No response requested." filler assistant turn that exists only inside that one request. |
| 7 | Host config can be isolated: `--system-prompt` + `--setting-sources=` + `--strict-mcp-config` load no hooks, plugins or `CLAUDE.md`. **`--safe-mode` is not usable**: it also drops the `--mcp-config` server. The CLI still injects an environment block (cwd, platform, scratchpad path). | A `CLAUDE.md` test instruction was ignored. The cwd was leaked into the model's context. |
| 8 | Thinking deltas arrive with empty `thinking` text (only `estimated_tokens` plus a signature), so there is no reasoning text to stream. | stream-json |

### Cursor (from reverse-engineering the Cursor.app 3.21.16 bundle)

- Agent requests to the BYOK base URL come from **Cursor's backend**, not from the app (`agent.v1.AgentService/Run` carries `ApiKeyCredentials{api_key, base_url}`). A public tunnel is required. Only the "Verify" button calls the URL from the client, with a non-streaming `max_tokens:10` request.
- **Routing by model name** (`byokModelUtils`): a name starting with `claude-` is sent with the **Anthropic** key slot, and `gemini-*` with Google. Everything else uses the OpenAI key and base URL. So `claude-opus-5-5` never reaches the gateway through the OpenAI override. **Cursor-facing slugs must be `api/claude-*` or `anthropic/claude-*`.** Exact catalog slugs are also remapped server-side, so they should be avoided.
- The harness picks the prompt and tools from the model name. `includes("claude")` means vendor anthropic, which gives the tools **Shell, Read, Grep, Glob, LS, StrReplace, Write, Delete, TodoWrite, ReadLints, SemanticSearch, WebSearch, WebFetch, Task, EditNotebook…**, all `type:"function"`. No custom `ApplyPatch` is sent: that grammar tool is only given to GPT-5-family models. The schemas are in the appendix.
- For names containing `opus-5`, the harness defaults the effort to `high`. The suffixes `-low|-medium|-high|-xhigh|-max` override that, and Cursor may send `reasoning_effort` in the body. The gateway lets the request's `reasoning_effort` win over the alias, so `xhigh` and `max` must be passed through.
- There is no `reasoning_content` handling anywhere in the client. Thinking-capable prompts use `<think>` tags, which Cursor strips.

### Prior art ([research notes](#appendix-b-prior-art))

- The closest design is `schmarta/claude-code-openai-server`: an MCP bridge plus `tool_use` taken from stream-json. It keeps a live CLI process per conversation and blocks inside `tools/call`. **It has no licence, so it is reference only.**
- `MooseGooseConsulting/claude-sub-proxy` (MIT) uses the same idea through the Agent SDK, but replays history as text.
- Proxies that call `api.anthropic.com` directly with the subscription OAuth token (ccproxy, CLIProxyAPI) are **out of scope** because of Anthropic's published terms (see Risks).

## Gap analysis: Codex contract vs current Claude bridge

| Contract item (Codex path) | Claude today | Fix |
|---|---|---|
| Native tool schemas sent upstream | Tools pasted into the prompt as JSON; the model must emit a ```` ```cursor_tool_call ```` fence | Serve the tools through an MCP bridge (native `tool_use`) |
| Parallel tool calls | Prompt says "Use exactly one of the tools" | Native parallel calls; honor `parallel_tool_calls:false` in the system prompt |
| Model stops after a tool call | The fence can be followed by more prose, including invented tool results, which go to Cursor as content | Native: the turn ends at `stop_reason:tool_use`; kill the process at `message_stop` |
| History as `function_call` / `function_call_output` pairs | Flattened to `User:/Assistant:/Tool:` text. The tool result is **written twice** (`client.go` promptFromMessages). Past calls are shown in a different format ("Cursor tool call X (id): …") from the one the model is told to emit | Native `tool_use`/`tool_result` replay (synthetic transcript + stdin tail) |
| Prompt caching across turns | None: one ever-growing user message means the whole conversation is re-billed every turn | Native replay gives an identical prefix every turn, which should hit the server-side prompt cache (to be verified, see Validation) |
| Stable, unique call IDs | `call_claude_<fnv(index:name:args)>` with an index that resets every turn, so an identical call in a later turn **gets the same ID** | Use the upstream `toolu_…` ID and normalize incoming IDs (below) |
| `tool_choice` (`required` forced on user turns by `applyAgentTurnToolChoice`; `none`; forced function) | Ignored | `none` means no MCP bridge; `required`/forced function become a system-prompt directive; degenerate retry still applies |
| System prompt | Cursor's system message is flattened into the user text **under** Claude Code's own coding-agent system prompt | `--system-prompt` = Cursor system/developer messages + bridge preamble |
| Host config isolation | `~/.claude` is mounted read-write, so host hooks, plugins, `CLAUDE.md` and MCP servers run on every request | `--setting-sources=` `--strict-mcp-config`, own config dir |
| Effort ladder | `low\|medium\|high` only | Add `xhigh\|max` |
| Custom→function wire (`CODEX_CUSTOM_TOOL_WIRE`) | Handled by the existing server layer | No change: the Claude vendor gets no custom tools from Cursor, but the `{input}` wrapper stays for other clients |
| Cursor-safe SSE (one complete frame, `finish_reason:tool_calls`) | Handled by the existing `stream_processor` | No change; add Claude cases to the contract tests |
| Quota fallback | Codex-only by design | Map `rate_limit_event` / 429 to `ErrorKindRateLimit` so metrics and queue cooling see it |

## Design

### Principle: stateless per request

Each `/v1/chat/completions` call starts one short-lived `claude` process that makes
**one model call**, the same shape as the Codex path.

We rejected the live-process-per-conversation design, where the process blocks in
`tools/call` until Cursor returns results:
- It breaks with multiple replicas and gateway restarts.
- It holds a process and an MCP call open for as long as Cursor takes to run the tool, which can be minutes.
- It desyncs when Cursor edits or retries history.
- It gains nothing that native replay doesn't already give, because prompt caching is keyed by content on Anthropic's side, not by process.

### Request flow

```
Cursor ──► POST /v1/chat/completions (model=api/claude-opus-5-5-high, tools, messages)
  server.go: alias → upstream claude-opus-5-5, effort high; context mgmt; queue
  claude.Client.Stream:
    1. specs  := parseToolSpecs(req.Tools)            (existing)
    2. conv   := buildConversation(req.Messages)      (new: system / history / tail)
    3. dir    := per-request run dir  $CLAUDE_RUN_DIR/<reqid>/  (cwd, tools.json, mcp.json)
    4. sid    := writeTranscript(conv.history)        (only if history non-empty)
    5. exec claude … --resume <sid>? --input-format stream-json
         stdin  ← conv.tail as stream-json lines
    6. parse stream-json:
         text_delta            → StreamEvent{Delta}
         content_block_start{tool_use} + input_json_delta → accumulate
         assistant{tool_use} (complete input) → ToolCallDelta{Final:true, id=toolu_…, name=strip(mcp__c__)}
         message_delta{stop_reason}  → finish reason
         message_stop                → Done; if stop_reason==tool_use: cancel process
         rate_limit_event / result{is_error} → typed errors
    7. cleanup run dir + transcript (defer)
  stream_processor (unchanged) → one complete delta.tool_calls per call → finish_reason:tool_calls
```

### 1. MCP tool bridge: `open-agent-api claude-mcp` (new subcommand, stdio)

- About 150 lines of Go in `internal/claude/mcpbridge/`. It is a JSON-RPC 2.0 loop on stdin/stdout that handles:
  - `initialize`: echo `protocolVersion`, `capabilities:{tools:{}}`, `serverInfo:{name:"c"}`.
  - `tools/list`: returns the tools from `--tools-file` (`[{name, description, inputSchema}]`).
  - `tools/call`: should never happen (see `dontAsk`). If it does, return `isError:true` with the text "This tool runs in the user's IDE, not here." It must **never** execute anything.
  - `ping`, and notifications, which are ignored.
- The server is named **`c`** so the model sees `mcp__c__Read`. That keeps the prefix to 7 chars, well inside the API's 64-char tool-name limit. Tools whose name would exceed 64 chars get renamed to `t_<hash>`, and a reverse map is written into `tools.json`.
- Schema normalization when writing `tools.json`:
  - `parameters` missing or not an object becomes `{"type":"object","properties":{}}`.
  - A custom/freeform tool becomes `{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}` (the existing `input` convention).
  - Strip `$schema`.
  - Anthropic allows `anyOf`/`oneOf` at nested levels only. If they appear at the top level, wrap them.
- We use stdio rather than an in-gateway HTTP MCP endpoint because nothing gets exposed on the network and no per-request auth token is needed. It reuses the gateway binary that is already in the image.

### 2. Conversation builder (`internal/claude/conversation.go`)

Input is the OpenAI `messages` (after context management). Output:

- `System string`: all `system`/`developer` messages joined, then the **bridge preamble**:
  > You are operating inside the user's IDE (Cursor). The tools prefixed `mcp__c__` are the user's IDE tools; the IDE executes them and returns results. Use them for all workspace access. Ignore any working directory, platform, or scratchpad path mentioned by the runtime environment; the only workspace is the user's, reached through these tools.
  - Plus a `tool_choice` directive when one is set (below).
  - Plus "Call at most one tool per turn." when `parallel_tool_calls:false`.
- `History []entry`: every message before the tail, converted into Anthropic content blocks.
- `Tail []entry`: the stdin lines.
  - When the last message is `user`, it is that one user message.
  - When the last message is `tool`, it is the last assistant message (with its `tool_calls`) plus **one** user message holding all trailing `tool_result` blocks.

Conversion rules:

| OpenAI | Anthropic block |
|---|---|
| `user` text / parts | `text` blocks. Image parts become `image` (base64 or url). |
| `assistant.content` | `text` |
| `assistant.tool_calls[i]` (function) | `tool_use{id: normID(id), name: "mcp__c__"+name, input: json(arguments) or {} }` |
| `assistant.tool_calls[i]` (custom) | `tool_use{…, input:{input: custom.input}}` |
| `tool` (tool_call_id, content) | `tool_result{tool_use_id: normID(id), content: text}` merged into one user message per run of consecutive tool messages |
| Anthropic-shaped blocks already in `content` (Cursor sometimes sends `tool_use`/`tool_result` blocks; seen in ccproxy) | Passed through, with `name` prefixed and IDs normalized |

Invariants we enforce (the API rejects violations):
1. Every `tool_use` has a matching `tool_result` in the next user message.
   - An orphan `tool_use` (the history was compacted or truncated) gets a synthetic `tool_result{is_error:true, content:"(result unavailable)"}`.
   - An orphan `tool_result` is turned into text.
2. Messages alternate user/assistant: consecutive same-role messages are merged.
3. `normID(id)`: IDs that match `^[A-Za-z0-9_-]{1,64}$` are kept. Anything else becomes `call_<sha256[:24]>`. This mirrors `codex.normalizeCallID` but with the Anthropic charset, and applies to both sides of a pair.
4. Context management must keep call/result pairs together. `manageContext` already keeps them; `dropOldestToFit` must drop them as pairs (add a test).

### 3. Transcript writer (`internal/claude/transcript.go`)

- Writes `History` as JSONL to `$CLAUDE_CONFIG_DIR/projects/<sanitize(cwd)>/<uuid>.jsonl`.
  - Each row is `{type, uuid, parentUuid, sessionId, isSidechain:false, userType:"external", cwd, version, timestamp, message}`.
  - `sanitize` replaces each non-`[A-Za-z0-9]` character with `-`.
  - Assistant rows carry `message:{id,type:"message",role,model,content,stop_reason,stop_sequence:null,usage:{input_tokens:0,output_tokens:0}}`.
- The transcript holds everything before the tail. If it is empty, skip `--resume`.
  - When the tail is `assistant(tool_use)` + `user(tool_result)`, the transcript ends on a user row. The CLI then inserts a "No response requested." filler assistant turn. That turn exists only inside this one request, because the next request rebuilds from Cursor's messages. It is harmless in testing, but measure whether it affects cache hits.
  - Whether some other layout avoids the filler is **untested**. Candidates: send the preceding user message as a content block inside the same stdin line, or try `--resume-session-at`.
- Everything lives under a gateway-owned `CLAUDE_CONFIG_DIR` (default `/var/lib/open-agent-api/claude`), **not** the host's `~/.claude`. The file is deleted when the request ends.
- `version` is taken from `claude --version`, probed once at startup.
- This is the most version-sensitive part (it uses the CLI's internal transcript format), so it is guarded by:
  - a startup self-test (below);
  - `CLAUDE_HISTORY_MODE=native|text` (default `native`). `text` keeps a single user message: a fixed flattened transcript in the **same** `tool_use`/`tool_result` wording, deduplicated, with no duplicated results.

### 4. Invocation

```
claude -p --verbose
  --input-format stream-json --output-format stream-json --include-partial-messages
  --model <upstream> [--effort low|medium|high|xhigh|max]
  --system-prompt <System>
  --tools ""                      # no built-in tools
  --setting-sources= --strict-mcp-config
  --mcp-config <run>/mcp.json     # {"mcpServers":{"c":{"type":"stdio","command":"/usr/local/bin/open-agent-api","args":["claude-mcp","--tools-file","<run>/tools.json"]}}}
  --permission-mode dontAsk --max-turns 1
  [--resume <sid>]
  --no-session-persistence        # only when there is no --resume. With --resume the CLI appends to our temp file, which we delete.
```

- `cmd.Dir = <run dir>`: an empty directory, so no project `CLAUDE.md` is picked up.
- Env: `CLAUDE_CONFIG_DIR`, the OAuth token, `MCP_TIMEOUT=10000`, `DISABLE_AUTOUPDATER=1`, `DISABLE_TELEMETRY=1`.
- With no tools, or `tool_choice:"none"`, drop the `--mcp-config` flags entirely.

### 5. Stream parsing changes (`events.go`)

- Add `content_block_start{type:tool_use}` (with id, name), `input_json_delta` (accumulated per block index), and `content_block_stop`. Emit **one** `ToolCallDelta{Index: n, ID: toolu_…, Name: strip("mcp__c__"), Arguments: full, Final:true}` per block. Tools renamed to `t_<hash>` are mapped back.
  - This matches the Codex accumulator: `stream_processor` already emits a single complete frame.
- The complete `assistant` event is a fallback source for the input if the partial deltas were missing.
- `message_delta.stop_reason`: `tool_use` becomes finish `tool_calls`, `end_turn` becomes `stop`, and `max_tokens` becomes `length`.
- On `message_stop` with `stop_reason==tool_use`, emit `Done`, then `cancel()` the process. Don't wait for the CLI's permission-denied turn.
- Ignore events that mention the CLI's own denial (`system/permission_denied`, `user` rows with a denial `tool_result`). Never forward them.
- `rate_limit_event` with a limited status, and `result{is_error}` carrying `api_error_status`, become `codex.NewError(ErrorKindRateLimit|Auth|Upstream, status)`. This replaces the substring matching on stderr.
- Delete the fence bridge (`tool_bridge.go`, most of `tools.go`), or keep it only behind `CLAUDE_HISTORY_MODE=text` for one release.

### 6. `tool_choice`

| Value | Behaviour |
|---|---|
| absent / `auto` | Nothing added. |
| `none` | No MCP bridge; history tool blocks become text. |
| `required` (forced by `applyAgentTurnToolChoice` on user turns) | System directive: "You must call at least one tool before replying." The existing degenerate-turn retry (`completeWithDegenerateRetry`, and the streaming retry in `deliverToolStream`) already covers failures. **Decision:** keep forcing on Claude user turns, matching Codex, or skip it for Claude so plain questions ("what does X mean?") can be answered directly. Recommendation: skip it for Claude, because the CLI can't hard-enforce it and the prompt directive makes greetings awkward. |
| forced function `{name}` | Directive: "Call `mcp__c__<name>` now." |

### 7. Models and effort (`internal/openai/models.go`)

- Add a `claudeEffortLadder(id, upstream)` helper that generates `api/<id>`, `api/<id>-{low,medium,high,xhigh,max}` and `anthropic/<id>` variants.
- Apply it to:
  - `claude-opus-5-5`
  - `claude-fable-5-1`
  - `claude-sonnet-5`
  - `claude-opus-4-8`, `claude-fable-5` (existing, extended)
- **Recommended names for Cursor:** `api/claude-opus-5-5-high`, `api/claude-fable-5-1`, `api/claude-sonnet-5`. Never bare `claude-*`, because Cursor sends those to the Anthropic key.
- `claudeEffort()` accepts `xhigh` and `max`.
- `client.model()` already converts dots to dashes (`claude-opus-5.5` → `claude-opus-5-5`). Strip effort suffixes that Cursor-style names carry in the model string (`…-high`) only when they are not resolved by an alias.
- Short names `opus`/`fable`/`sonnet` keep floating with the CLI (documented).

### 8. Docker, compose and config

- **Dockerfile:** `ARG CLAUDE_CODE_VERSION=2.1.283`, then `npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}`. Also `mkdir /var/lib/open-agent-api/claude` owned by 1000.
- **docker-compose.yml:**
  - Drop the `~/.claude` bind mount (host hooks and plugins leak in through it). Mount a named volume at `/var/lib/open-agent-api/claude` and set `CLAUDE_CONFIG_DIR` to that path. Auth stays on `CLAUDE_CODE_OAUTH_TOKEN`.
  - Add `CLAUDE_HISTORY_MODE`.
- **docker-compose.cursor.yml:** set `CLAUDE_TIMEOUT=45m`, `CLAUDE_AGENT_MAX_ACTIVE`, and `CLAUDE_AGENT_QUEUE_LIMIT` to match the Codex profile.
- **docker-compose.ngrok.yml:** pin the `ngrok/ngrok` image to a tag and digest instead of `:latest` (and the same in `docker-compose.capture.yml`). Keep the domain in `.env` as `NGROK_DOMAIN`.
- **`/health`:** report `claude_code_version` and `claude_history_mode`.
- **Startup self-test** (when `claude` is enabled):
  - Run `claude --version`.
  - Optionally (`CLAUDE_SELFTEST=true`), run a replay round-trip with a synthetic transcript and a canned tool result on haiku, and assert one turn with no `tool_use`.
  - On failure, fall back to `CLAUDE_HISTORY_MODE=text` and log it loudly.
- **Delete `.docker/pin/`.** It is a stale July snapshot of the old codex-chat-api layout and nothing references it.

## Files

| File | Change |
|---|---|
| `internal/claude/mcpbridge/server.go` (+test) | new: stdio MCP server |
| `cmd/open-agent-api/main.go` | `claude-mcp` subcommand dispatch |
| `internal/claude/conversation.go` (+test) | new: messages → system/history/tail, invariants, ID normalization |
| `internal/claude/transcript.go` (+test) | new: JSONL writer, cleanup |
| `internal/claude/client.go` | new args, run dir, stdin stream-json, cancel on tool_use, effort ladder, typed errors |
| `internal/claude/events.go` (+test) | tool_use / input_json_delta / stop_reason / rate_limit parsing |
| `internal/claude/tool_bridge.go`, `tools.go` | remove the fence protocol (or keep it for `text` mode for one release) |
| `internal/config/config.go` | `CLAUDE_HISTORY_MODE`, `CLAUDE_CONFIG_DIR`, `CLAUDE_RUN_DIR`, `CLAUDE_SELFTEST` |
| `internal/openai/models.go`, `openai_test.go` | new slugs and ladders |
| `internal/server/context_manager.go` | pair-safe `dropOldestToFit` (+test) |
| `internal/server/server_test.go` | Claude cases in `assertExactCursorToolSSE` (fake `claude` script emitting recorded stream-json) |
| `Dockerfile`, `docker-compose*.yml` | version arg, config volume, ngrok pin |
| `website/docs/cursor/tool-conventions.md`, `models/catalog.md`, `README.md`, `AGENTS.md` | Claude section rewritten; slug guidance (`api/claude-*`) |

## Tests

- **Unit tests:**
  - Conversation builder: table tests covering parallel calls, orphans on both sides, consecutive tool messages, custom tools, Anthropic-shaped input blocks, long and odd-charset IDs, images, `tool_choice` variants.
  - Transcript writer: golden JSONL.
  - Events: recorded stream-json fixtures from 2.1.283 (text only, single tool, parallel tools, freeform tool, rate limit, auth error).
  - MCP bridge: JSON-RPC conformance.
- **Contract:** a fake `claude` executable (a shell script under `testdata/`, set through `CLAUDE_EXECUTABLE`) replays the fixtures, and `assertExactCursorToolSSE` runs against the Claude provider.
- **Live (build tag `live`, like `structured_live_test.go`):** one tool round-trip and one replay on haiku.

## Validation (before merge)

1. `go build/vet/gofmt/test -race` (CI gate).
2. `docker compose -f docker-compose.yml -f docker-compose.cursor.yml -f docker-compose.ngrok.yml up --build`, then check that `/health` shows `claude_code_version=2.1.283`.
3. `mitmdump -s tools/cursor_header_capture.py` capture of a real Cursor Agent chat on `api/claude-opus-5-5-high`:
   - 5+ tool rounds including parallel Read/Grep, StrReplace edit, Shell.
   - Evidence lines: `empty_tool_frames=0 finish=tool_calls tool_args_json_valid=True`.
   - Confirm the tools Cursor actually sends to an `api/claude-*` name (this confirms the bundle findings above).
4. Prompt-cache check: on consecutive turns of one chat, `usage.cache_read_input_tokens` should grow and uncached input stays small. Record this in `docs/issue-XXX-validation.md`.
5. Latency: p50 time-to-first-tool-call for each model, compared with the current fence bridge.

## Risks

- **Anthropic terms.** The current published policy (code.claude.com/docs/en/legal-and-compliance) limits subscription OAuth to "ordinary use of Claude Code". It bars third parties from intermediating Claude.ai credentials, and 2026-02 press coverage read it as banning subscription OAuth in third-party tools. This design keeps the **unmodified `claude` binary** doing all auth: no token extraction and no direct Messages API calls. It is the lower-risk shape of this idea, not a risk-free one. Serving the Claude provider from a **shared** gateway (the scarlett-network agent gateways, k8s dev/prod) to other people is clearly outside personal use. Keep `claude` out of `GATEWAY_PROVIDERS` on shared deployments, or use an API key there (`ANTHROPIC_API_KEY` works with the same CLI path).
- **CLI internals.** The transcript format and the `--resume` repair behaviour are not a public contract. Mitigations:
  - the pinned version;
  - the self-test with fallback to `text` mode;
  - fixtures re-recorded on each version bump (add a `scripts/record-claude-fixtures.sh`).
- **Per-request process cost.** A whole haiku tool turn took about 4s end to end. The overhead from Node startup and the MCP handshake has not been measured separately (Validation step 5). If needed later, keep a warm pool of idle processes waiting on stdin.
- **The `.env` token.** A test run using `CLAUDE_CODE_OAUTH_TOKEN` from `.env` with an isolated config dir got `403 oauth_org_not_allowed` ("Your organization has disabled Claude subscription access for Claude Code"). Check which account that token belongs to before validating in Docker.

## Appendix A: Cursor "latest" tool schemas (Claude vendor)

`Read{path, offset?, limit?}` · `Write{path, contents}` · `StrReplace{path, old_string, new_string, replace_all?}` · `Delete{path}` · `LS{target_directory, ignore_globs?}` · `Glob{target_directory?, glob_pattern}` · `Grep{pattern, path?, glob?, output_mode?, -A?, -B?, -C?, -i?, type?, head_limit?, offset?, multiline?}` · `Shell{command, working_directory?, timeout?, description?, is_background?}` · `TodoWrite{todos[{id,content,status}], merge}` · `ReadLints` · `SemanticSearch{query, target_directories[]}` · `WebSearch` · `WebFetch{url}` · `Task` · `EditNotebook` · plus the user's MCP tools. Older prompt versions use snake_case names (`read_file`, `run_terminal_cmd`, `search_replace`, …). The bridge is name-agnostic.

Note: `Grep` has keys like `-A` and `-i`. They are valid JSON-Schema property names and passed through MCP unchanged. Covered by a test.

## Appendix B: prior art

- schmarta/claude-code-openai-server: MCP bridge plus a live blocking process per conversation (no licence, reference only).
- MooseGooseConsulting/claude-sub-proxy (MIT): Agent SDK `createSdkMcpServer`, text replay.
- mergd/ccproxy: Cursor-specific notes (Anthropic-shaped blocks, model-name normalization). It uses direct OAuth API calls, which is out of scope.
- router-for-me/CLIProxyAPI (MIT): a complete OpenAI↔Anthropic tool translation table (`internal/translator/claude/openai/chat-completions`), useful as a checklist for the conversation builder.
- RichardAtCT/claude-code-openai-wrapper, wende/claude-max-api-proxy: ignore client tools.
