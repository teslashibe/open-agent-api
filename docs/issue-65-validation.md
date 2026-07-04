# Issue 65 Validation

This page records validation evidence for scaling Codex API throughput across
multiple Cursor chats using a multi-client Codex pool with per-chat sticky
affinity (not single-chat round-robin).

## Automated Verification

Run from the repository root:

```bash
GOCACHE=$PWD/.gocache go test ./internal/codex ./internal/config
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go build ./...
```

Covering tests:

- `internal/codex` — `TestPooledServiceSameQueueKeyMapsToSameClient` (same
  affinity key → one stable shard),
  `TestPooledServiceDifferentQueueKeysCanMapToDifferentClients` (different keys →
  different shards), and `TestPooledServiceCursorShardSelectionLogFormat`
  (four `codex-N` shards, cursor stickiness, and the redacted
  `codex_client_pool` / `codex_client_select` log lines).
- `internal/config` — `TestLoadDefaults` (missing `CODEX_CLIENTS` still yields
  the single `default` client), `TestLoadScaleExampleSharedHome` and
  `TestLoadScaleExampleDistinctHomes` (the documented four-client examples), and
  `TestScaleComposeExampleProducesValidClients` (the committed
  `docker-compose.scale.yml` overlay parses to valid JSON/config and keeps
  `CODEX_AGENT_QUEUE_KEY_MODE: cursor`).

## Live Cursor Validation

1. Start the API with four logical Codex clients sharing the mounted `.codex`
   home:

   ```bash
   docker compose -f docker-compose.yml -f docker-compose.scale.yml up -d
   ```

2. Confirm the startup pool line reports four labels (no auth paths):

   ```bash
   docker compose logs api | grep codex_client_pool
   # codex_client_pool clients=4 policy=fail labels=codex-1,codex-2,codex-3,codex-4
   ```

3. Open several Cursor chats against the local API and send a few tool-capable
   turns in each.

4. Confirm sharding by watching selection logs:

   ```bash
   docker compose logs -f api | grep codex_client_select
   ```

   - Different chats show different `key_hash` and multiple `client_label`
     values (chats distribute across shards).
   - Repeated requests from the **same** chat keep the same `key_hash` and the
     same `client_label` (per-chat stickiness).

   > Note: with only a few chats, hash distribution may place two chats on the
   > same shard by chance — expected, not a bug. Behind a proxy where every chat
   > shares one source IP and no stable Cursor metadata is present, the cursor
   > key can fall back to that shared IP and collapse chats onto one shard.

5. Confirm health and that existing Codex requests still complete:

   ```bash
   curl -s http://127.0.0.1:8088/health
   ```

## Notes

- All four logical clients share one authenticated Codex home, so upstream
  ChatGPT/Codex account or session concurrency limits may still throttle
  parallelism regardless of local sharding. For true isolation, switch each
  client to a distinct `codex_home` (Story 4) — the config shape is unchanged.
- The scale overlay sets `CODEX_AGENT_QUEUE_LOCK_DIR=` (empty), disabling the
  cross-process per-key lock. That is fine for a single local API process; for
  multiple replicas point it at a shared writable volume so one chat cannot
  stream concurrently in two processes.
