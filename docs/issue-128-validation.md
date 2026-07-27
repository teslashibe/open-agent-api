# Issue 128 Validation

Final-validation follow-up from the structured inference stack
([issue-120](./issue-120-validation.md) … [issue-126](./issue-126-validation.md)).
Two things could mislead before this change and cannot now:

1. **Nothing ran the tests before an image shipped.** The repo had exactly two
   workflows — `docker.yml` (build → push → pin-bump k8s-control) and `docs.yml`.
   A push to `main` that did not compile was still built, still pushed to
   `ghcr.io`, and still rolled out by Flux. Pull requests ran nothing at all.
2. **Shipped images carried no provenance.** `docker.yml` passed no build args,
   so `BUILD_VERSION` / `BUILD_COMMIT` / `BUILD_DATE` were empty and the binary
   fell through to the Go toolchain's VCS stamps — which the Docker build does
   not have, because `.dockerignore` excludes `.git`. Every published image
   reported `{"version":"devel","commit":"unknown","build_date":"unknown"}`, while
   `website/docs/api.md` described a provenance story the pipeline never
   implemented.

The pipeline is now four jobs, each blocking the next:

```
checks (go-checks.yml) -> build-and-push -> verify-provenance -> deploy
```

## Why one reusable workflow, and why same-workflow `needs:`

The gate lives in `.github/workflows/go-checks.yml` (`on: workflow_call`) and is
called by both `ci.yml` and `docker.yml`. A second copy of the step list inside
`docker.yml` would be free to drift from the one PRs run, which is the failure
mode this issue exists to close.

`docker.yml` gates with a `checks` job and `needs: [checks]` **in the same
workflow**. The alternative — a separate workflow keyed on `workflow_run:
completed` — does not block anything: `docker.yml` would have already built and
pushed by the time the observer ran. Only `needs:` inside the pushing workflow
actually prevents the push.

## Why the gofmt check is scoped, and why five files moved first

`gofmt -l .` fails permanently in this repo: `.docker/pin/` is a **separate Go
module** with its own `go.mod`, is not part of this module's build, and is not
gofmt-clean. The check therefore scopes to this module's package directories:

```bash
gofmt -l $(go list -f '{{.Dir}}' ./...)
```

Five tracked files in the main module were already unformatted, so the gate
would have been red on arrival. Commit `563178d` normalizes exactly those five —
CRLF → LF in `internal/server/cursor_wire.go`,
`internal/server/cursor_wire_test.go`, `internal/gemini/thought_signature.go`;
struct-tag alignment in `internal/gemini/builder.go` and
`internal/codex/router_provider_test.go`. It is byte-level only:
`git diff -w --ignore-cr-at-eol` on that commit is empty. It is a separate
commit so the whole-file CRLF diffs can be reviewed as the no-op they are.

## Why `verify-provenance` pins by digest and boots codex-only

- **By digest, not by tag.** `docker run … @${{ needs.build-and-push.outputs.digest }}`
  asserts against the artifact *this run* produced. A tag can be moved between
  the push and the check; a digest cannot.
- **`GATEWAY_PROVIDERS=codex`.** A GitHub runner has no `~/.codex/auth.json`, no
  Antigravity OAuth file, and no Claude CLI login. The codex client only reads
  `codex_profile.json` / `codex_scaffold.json`, both baked into the image at
  `/app`, so it constructs and serves `/health` with no credentials. Gemini and
  Claude client construction can fail at startup without their auth, which would
  make the job red for a reason that has nothing to do with provenance.
- **`/health` is unauthenticated** (it is the k8s probe endpoint), so the check
  needs no bearer secret.

The assertions are deliberately both positive and negative: `build.commit` must
equal `github.sha` *and* must not be `unknown`; `build.version` must not be
`devel`; `build.build_date` must not be `unknown` *and* must match
`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`. The equality check
alone would be satisfiable by a future change that stamps something plausible but
wrong; the shape check pins the UTC format the `date -u` step emits.

`deploy` takes `needs: [build-and-push, verify-provenance]`, so a mis-stamped or
non-booting image cannot reach the k8s-control pin bump.

## Criterion → proof

| AC | Proof |
| --- | --- |
| 1 — PR/main CI runs build, vet, gofmt, `go test -race` | `.github/workflows/go-checks.yml` (`checks` job: gofmt step scoped via `go list -f '{{.Dir}}' ./...`, `go build ./...`, `go vet ./...`, `go test -race ./...`), called by `.github/workflows/ci.yml` on `pull_request` (all branches), `push` to `main` and `v*` tags, and `workflow_dispatch`. Per-ref `concurrency` cancels superseded PR pushes. |
| 2 — image build/deploy cannot proceed unless checks pass | `.github/workflows/docker.yml`: `checks` job `uses: ./.github/workflows/go-checks.yml`; `build-and-push` has `needs: [checks]`; `deploy` has `needs: [build-and-push, verify-provenance]`. Same-workflow `needs:` — see the section above for why `workflow_run` was rejected. |
| 3 — `BUILD_VERSION`, `BUILD_COMMIT=github.sha`, UTC `BUILD_DATE` into build-push-action | `docker.yml` `build-and-push`: `provenance` step emits `date=$(date -u +%Y-%m-%dT%H:%M:%SZ)`; `docker/build-push-action@v7` gets `build-args:` with `BUILD_VERSION=${{ steps.meta.outputs.version }}` (the tag name on `v*`, branch/short-sha on `main`), `BUILD_COMMIT=${{ github.sha }}`, `BUILD_DATE=${{ steps.provenance.outputs.date }}`. `Dockerfile:14-19` already declared all three ARGs and wired them to `internal/buildinfo` ldflags — no Dockerfile change. |
| 4 — a CI-built image reports a non-unknown commit matching its source | `docker.yml` `verify-provenance`: runs the pushed image **by digest**, polls `/health` (30 × 2s), then asserts with `jq` that `.status == "ok"`, `.build.commit == github.sha`, `.build.commit != "unknown"`, `.build.version != "devel"`, `.build.build_date != "unknown"`, and the date matches the UTC RFC3339 shape. `docker logs` on failure, `docker rm -f` in `if: always()`. Exercised locally against a real image — see **Commands run**. |
| 5 — documentation matches the real provenance | `website/docs/contributing.md`: the four-job pipeline, the shared gate, the pre-PR command block (gofmt + `-race`), and the note that CI never sets `STRUCTURED_LIVE_REQUIRED`. `website/docs/api.md`: CI images always carry a real full-SHA commit / version / UTC date and cannot ship otherwise; only local `docker compose` builds fall back to VCS stamps and then to `devel`/`unknown`. `website/docs/install/kubernetes.md`: a `curl … /health \| jq .build` snippet plus "`commit`:`unknown` means this is not a CI-built image". `AGENTS.md:85`: the Do list now names all four commands. |

## Commands run

| Command | Result |
| --- | --- |
| `gofmt -l $(go list -f '{{.Dir}}' ./...)` | pass (empty) — was five files before commit `563178d` |
| `git diff -w --ignore-cr-at-eol` on the gofmt commit | empty — proves the normalization is byte-level only |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test -race ./...` | pass (all 12 packages) |
| `actionlint .github/workflows/{ci,go-checks,docker}.yml` | clean on the new content; one pre-existing `SC2129` *style* hit remains in the untouched `deploy` step |
| `docker build --build-arg BUILD_VERSION=… --build-arg BUILD_COMMIT=$(git rev-parse HEAD) --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) .` | pass |
| `docker run -e GATEWAY_PROVIDERS=codex …` + the exact `verify-provenance` jq assertions | pass — `/health` returned `{"version":"main-abc1234","commit":"563178d1d1b2af1af23819e219902210f0f5cdad","build_date":"2026-07-27T04:06:57Z",…}`; all six assertions green, container booted with no credentials on disk |

The credential-free boot was also checked directly against a host binary with
`GATEWAY_PROVIDERS=codex` and `HOME`/`CODEX_HOME` pointed at an empty directory:
`/health` still returns `200 {"status":"ok"}`.

## Residual limits

- **The local provenance run was `linux/arm64`; CI builds `linux/amd64`.** The
  build is pure Go with `CGO_ENABLED=0` and `ubuntu-latest` runners are amd64, so
  no emulation is involved in CI — but the verify job assumes the single-platform
  build. Adding a second platform to `build-push-action` would make
  `steps.build.outputs.digest` a manifest-list digest, and `docker run` would
  then resolve a per-arch child; the assertions still hold, but the job would no
  longer be pinning one specific binary.
- **The gate only covers this module.** `.docker/pin/` is a separate module and
  is neither formatted, built, vetted, nor tested by CI. It is not part of the
  shipped image.
- **`go test -race` on a 2-core runner is the new release-blocking risk.** The
  suite has concurrency, streaming-idle, and queue tests; a timing flake now
  blocks a release. The job carries `timeout-minutes: 20`. The fix for a flake is
  to stabilize the test, not to drop the gate.
- **The live upstream test is not in CI and must stay that way.**
  `TestStructuredInferenceLiveUpstream` skips without credentials and only
  hard-fails under `STRUCTURED_LIVE_REQUIRED=1`. CI never sets it; every run
  would otherwise spend real Codex quota. Release-cutting still means running it
  by hand.
- **`verify-provenance` proves the image boots and is honestly stamped, not that
  it works.** It asserts `/health` only — no model call, no upstream. A gateway
  that answers `/health` and fails every completion passes this job.
- **Build args bust the layer cache** for the compile layer on every commit (it
  already busted on `COPY . .`), and the two added jobs lengthen merge → deploy
  by several minutes. That is the price of the gate.
- **Provenance is self-reported, not attested.** `/health` reports what was
  linked into the binary; nothing cryptographically binds the digest to the
  source. Anyone who can push to `ghcr.io` can publish an image that lies. Build
  attestations / signing are a separate change.
- **Everything in issues 120–126 still holds** — see
  [issue-126-validation.md](./issue-126-validation.md).
