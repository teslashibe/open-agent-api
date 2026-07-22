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
| AC1 | `TestAlertRulesUseBoundedMetricsAndActionableAnnotations`, `TestCoolingAlertPreservesScrapeTargetLabels`; `codex-chat-api.rules.test.yml` firing, realistic target-label, cross-target isolation, single-client false-positive, and healthy fixtures |
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

The exact-candidate evidence is the successful `prom-rules` and `image-build`
checks on the pushed commit. The PR quality workflow runs both Prometheus rule
checks in the official Prometheus container and runs `docker build .` on every
pull request; its check results are the authoritative record. Do not promote
unless both jobs pass for the exact candidate commit.


## Exact-candidate local evidence (2026-07-21)

Recorded for commit `cd622c28e67cb9df694961ec17c0be8d272ddaad` (`cd622c2`):

```text
$ docker run --rm --entrypoint promtool -v "$PWD:/work" -w /work prom/prometheus:v2.55.1 \
    check rules examples/prometheus/codex-chat-api.rules.yml
Checking examples/prometheus/codex-chat-api.rules.yml
  SUCCESS: 8 rules found

$ docker run --rm --entrypoint promtool -v "$PWD:/work" -w /work prom/prometheus:v2.55.1 \
    test rules examples/prometheus/codex-chat-api.rules.test.yml
SUCCESS

$ docker build -t codex-chat-api:preflight-7b14d38 .
# succeeded; image docker.io/library/codex-chat-api:preflight-7b14d38
# digest sha256:3c34c6738f5873c47d3ea223102001c78eaafdd237555a25e18c3faecbd16638
```

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
