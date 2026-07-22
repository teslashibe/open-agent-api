---
title: Tagged release and production promotion
sidebar_label: Tagged release
---

# Tagged release and production promotion

A release tag promotes the exact tagged commit to production. Do not create a
tag merely to repair a failed dev rollout.

## Preconditions

1. The candidate commit is on `main` and its required checks passed.
2. The image pipeline published `sha-<short>` for that exact commit.
3. The dev manifest reconciled to that immutable tag.
4. Liveness, readiness, models, completion, drain, and metrics checks passed in
   dev with redacted evidence.
5. The chosen version is new and follows the repository's `vX.Y.Z` convention.

Run the local release checks from the repository root:

```bash
GOCACHE=$PWD/.gocache go test ./...
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go build ./...
(cd website && npm ci && npm run build)
```

## Promote

Create an annotated tag on the already-validated `main` commit and push that
exact tag. Substitute the intended version; never reuse or move a published tag.

```bash
git switch main
git pull --ff-only
git tag -a vX.Y.Z -m 'vX.Y.Z'
git push origin vX.Y.Z
```

The tag triggers the same image build first. Only after that job succeeds does
the deploy job update `manifests/prod/kustomization.yaml` to the exact `vX.Y.Z`
tag. It must not update the dev pin as part of tag promotion.

## Verify production

1. Confirm the registry contains the tag and record its digest.
2. Confirm the production manifest changed only the intended image pin.
3. Wait for GitOps reconciliation and verify the running digest matches.
4. Repeat the safe health, readiness, models, completion, drain, and metrics
   checks from [dev validation](./dev-validation) against the approved
   production access path.
5. Record the release tag, commit, digest, and redacted results.

For rollback, change production through the deployment repository's reviewed
process to a previously verified immutable `v*` tag. Do not force-move a release
tag or point production at `latest`.
