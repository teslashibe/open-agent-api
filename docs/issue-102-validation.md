# Issue 102 Validation

This page records the production-alert and release-runbook evidence for issue
102. The portable rules stay in this repository; cluster-specific rule
selectors, probes, routes, replica settings, and image pins stay in
`teslashibe/k8s-control`.

## Acceptance Criteria

- **AC1:** Alerts use existing bounded metrics and include actionable annotations/runbook links.
- **AC2:** Runbook includes preflight, dev soak, canary, credential rotation, rollback, and `v*` promotion gates.
- **AC3:** It states process-local cooldown/pin limitations and one-replica recommendation.
- **AC4:** Full tests, image build, docs build, and soak command are documented and pass.

## Automated Evidence

Run from the repository root:

```bash
GOCACHE=$PWD/.gocache go test -count=1 ./...
GOCACHE=$PWD/.gocache go test -race -count=1 ./internal/codex ./internal/codextest ./internal/server
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go build ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOCACHE=$PWD/.gocache \
  go build -o /tmp/codex-chat-api-linux-amd64 ./cmd/codex-chat-api
(cd website && npm ci && npm run build)
```

Result recorded on 2026-07-21: the full suite, race suite, vet, native build,
Linux static build, and Docusaurus production build passed. The full suite
includes `TestDevSoakExercisesHealthCompletionAndStreaming`, which runs the
documented soak command for two deterministic iterations and verifies health,
completion, stream termination, bearer handling, aggregate counts, and output
redaction.

The following regression tests map directly to the criteria:

| Criterion | Evidence |
| --- | --- |
| AC1 | `TestAlertRulesUseBoundedMetricsAndActionableAnnotations`; `codex-chat-api.rules.test.yml` firing and healthy fixtures |
| AC2 | `website/docs/production-readiness.md`; successful Docusaurus production build |
| AC3 | Production runbook operating constraint and `website/docs/multi-credential-pool.md` |
| AC4 | Full/race/vet/build/docs results above; `TestDevSoakExercisesHealthCompletionAndStreaming`; existing PR image-build job |

## Release-environment gates

Run these where `promtool` and a Docker daemon are available:

```bash
promtool check rules examples/prometheus/codex-chat-api.rules.yml
promtool test rules examples/prometheus/codex-chat-api.rules.test.yml
docker build -t codex-chat-api:preflight .
```

The 2026-07-21 managed worktree sandbox parsed both YAML files and passed the
bounded-metric/annotation contract tests, but it did not provide `promtool`,
blocked network installation, and denied both Docker daemon sockets. The Docker
command was attempted and failed before reading the build context with
`operation not permitted`; this is a runner capability limitation. The existing
PR quality `image-build` job runs `docker build .`; do not promote until that job
and both promtool commands pass in a release-capable environment.

## Live dev evidence

After `main` deploys the candidate `sha-<short>` to the single dev replica, run:

```bash
API_URL=https://dev.example.invalid \
SOAK_LABEL=dev \
SOAK_DURATION=30m \
GATEWAY_BEARER_SECRET_FILE=/run/secrets/codex-chat-api-gateway \
./scripts/dev-soak.sh
```

Record only the commit, immutable tag and digest, timestamps, aggregate pass
counts, and alert state. Do not record the real URL, bearer value, credential
identity, prompt/response content, or request identifiers. A production `v*`
tag remains blocked until this live dev soak and the canary checklist pass.
