---
title: Pipeline and DAG operation
sidebar_label: Pipeline and DAG
---

# Pipeline and DAG operation

The image and GitOps pipeline lives in `.github/workflows/docker.yml`. Its
promotion contract is explicit:

```text
pull request -> docs build only
main         -> build/push image -> dev pin  = sha-<short>
v* tag       -> build/push image -> prod pin = vX.Y.Z
```

Production is never updated by an ordinary `main` push. A version tag is the
only production promotion trigger.

## Job dependency graph

```text
build-and-push
      |
      v
    deploy
      |
      +-- main: manifests/dev/kustomization.yaml  -> sha-<short>
      |
      +-- v*:   manifests/prod/kustomization.yaml -> exact tag
```

The `deploy` job has `needs: [build-and-push]`, so it cannot update a manifest
until the image build and registry push succeed. The pin-update step verifies
that the target file already contains the expected public image name, replaces
only its tag, verifies the new pin, then commits the single environment update
to the GitOps repository.

## Required repository settings

1. Permit GitHub Actions to publish packages for this repository. The image job
   uses the workflow `GITHUB_TOKEN` with `contents: read` and `packages: write`.
2. Store the GitOps credential as the Actions secret `GITOPS_TOKEN`. Give it only
   the access required to check out and update the deployment repository's
   `main` branch. Never include its value in logs or documentation.
3. Keep the source workflow triggers limited to pushes on `main`, tags matching
   `v*`, and intentional manual dispatches.
4. Configure repository Pages source as **GitHub Actions**. The docs build runs
   on every pull request and on `main`; only the `main` deploy job receives
   `pages: write` and `id-token: write`.
5. Preserve the environment mapping. Dev tracks immutable commit tags; prod
   tracks an explicit release tag. Do not point either environment at `latest`.

## Operate a main deployment

1. Merge the reviewed change to `main`.
2. Confirm the image job published `sha-<short>` for that commit.
3. Confirm the deploy job updated only the dev kustomization pin.
4. Wait for the GitOps controller to reconcile dev.
5. Run the [dev validation](./dev-validation) suite before considering a tag.

If the image succeeds but the manifest update fails, do not tag. Resolve the
credential, target-file, or branch-protection failure and rerun the failed job.
An image existing in the registry does not prove that dev is running it.
