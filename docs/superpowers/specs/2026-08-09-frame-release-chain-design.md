# Release chain — publishing Frame's three images

Status: design agreed, not yet implemented
Prerequisite for: [the update screen](2026-08-09-frame-update-screen-design.md)

## What this is

Frame is deployed today by building images with `podman` on a laptop and pushing
them to the cluster's own registry at `192.168.2.201:30500`. Nothing records
which commit is running beyond a short SHA in an image tag, and nobody outside
that laptop can reproduce a deploy.

The update screen needs a published, addressable version to offer. That means a
real release: a git tag, three images built from it, and a GitHub release that
names them.

## The problem to fix first

`.github/workflows/build.yml` already builds and pushes an image on `v*` tags —
but it builds the **root `Dockerfile`, which is the React UI**, and publishes it
as `ghcr.io/Plume-Labs/frame`, the bare repository name. So the artifact CI
publishes under the project's own name is one of its three components, and the
other two — the operator and authd — are built by CI nowhere at all.

This is the same mistake the repository already documents at the top of
`Dockerfile.controller`: `make docker-build` once pointed at the root Dockerfile
and silently tagged the UI as the operator. It was fixed in the Makefile and not
in CI.

## Design

Three images, named for what they are:

| Image | Dockerfile | What it is |
|---|---|---|
| `ghcr.io/plume-labs/frame-controller` | `Dockerfile.controller` | the kubebuilder operator |
| `ghcr.io/plume-labs/frame-ui` | `Dockerfile` | the React control plane |
| `ghcr.io/plume-labs/frame-authd` | `Dockerfile.authd` | the auth surface |

The bare `ghcr.io/plume-labs/frame` name is retired rather than reused. A name
that has meant "the UI" for months should not quietly start meaning something
else; a reader who pulls it expecting the project gets a surprise either way.

**Tagging.** On a `v*` tag, each image is published as `vX.Y.Z`, `X.Y`, and
`sha-<commit>`. On a push to `main`, only `sha-<commit>` — main is not a release
and must not be offered as one.

**Publishing.** The GitHub release is created by the workflow from the tag,
with the release notes GitHub generates from the commits, and lists the three
image references in its body so the screen and a human read the same thing.

**Visibility.** The repository is public, so the packages are public and the
cluster pulls without an `imagePullSecret`. Anonymous pull from GHCR works
through its token exchange; the 401 on a bare `/v2/` request is the auth
challenge, not a refusal.

**The local registry keeps working.** `192.168.2.201:30500` remains how an
unreleased build reaches the cluster during development. GHCR is for releases.
Nothing forces a choice between them: both are just image references.

## What this does not do

No signing, no SBOM, no provenance attestation. They belong with Phase D's
security review, which already lists SBOM and image scanning, and adding them
here would be building the release process for a product Frame is not yet.

No automatic deployment. Publishing a release changes nothing on any cluster.
That is the update screen's job, and keeping the two apart is what makes the
screen a decision rather than a formality.

## Exit criteria

Tagging `v0.1.0` produces three images on GHCR under the names above, a GitHub
release naming them, and a cluster that can pull all three anonymously —
demonstrated by actually pulling one, not by reading the workflow.

## Prerequisite before any of it

Local `main` is well ahead of `origin` and has been for some time: the service
catalog, the lint cleanup, the pre-freeze API work and the Phase A closure all
exist only on one machine. A release built from a tag GitHub cannot see is not
a release. Pushing comes first, and it is worth doing deliberately rather than
as a side effect — the push makes public a history that has so far been private.
