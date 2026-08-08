# The update screen (S3)

Status: design agreed, not yet implemented
Depends on: [the release chain](2026-08-09-frame-release-chain-design.md)
Group: none — this adds no CRD

## What this is

A screen that shows which version of Frame is running, which releases exist,
what an update would disturb right now, and a button per component.

## Why there is no self-updating operator

"Auto-update" for an operator usually means the hard version: a controller that
replaces its own Deployment and must survive its own rollout without losing the
reconcile it was in the middle of. That problem exists only because the operator
is the thing doing the updating.

Here the UI does it. The control plane already talks straight to the Kubernetes
API with no server in between, so patching a Deployment's image is a call it can
already make. The trigger is a browser: nothing is mid-reconcile when the
operator restarts, because the operator was not the one pressing the button.

That removes the bootstrap problem outright rather than solving it, and it is
why this design has no new controller and no new CRD.

## Why Frame builds this and not Argo CD

Argo CD is installed on this cluster and its whole job is converging declared
versions. Fetching an image and applying it is not a gap Frame should fill.

What Argo cannot see is whether **now** is a moment the cluster can afford the
interruption. Argo reads manifests. Frame reads its own resources: a FrameJob
running, a FrameService holding the only GPU, a TalosUpgrade with a node
mid-reboot, a FrameNode still provisioning. The screen's value is that panel,
not the button.

## The screen

**Per component** — operator, UI, authd — show the running version, read from
the tag of the live Deployment's image, and the releases available, fetched
anonymously from `api.github.com/repos/Plume-Labs/frame/releases` by the
browser. GitHub's API allows CORS and the repository is public, so this needs
no token, no proxy and no secret.

Show the release notes for the version being offered. An operator deciding
whether to interrupt work deserves to know what they get for it.

**The readiness panel** is per component, because the components do not cost the
same thing to restart:

- **UI** — disturbs nothing. A browser reloads.
- **authd** — drops every open session. Anyone signed in signs in again.
- **operator** — reconciliation pauses for the rollout. Running FrameJobs keep
  running; their Argo Workflows never notice. The exception worth naming: a
  `TalosUpgrade` in flight means a node is rebooting, and while the
  generation-based idempotency guard makes a restart survivable, this is the one
  case where waiting is obviously right.
- **inference instances** — not a Frame component, but the panel must say it:
  restarting a `FrameService` of type inference takes the only GPU away from
  whatever is using it, and on this cluster that is usually an agent mid-mission.
  Updating the operator does *not* restart them — owner references are untouched
  by an image change — and the panel should say that too, because the instinct
  is to assume otherwise.

Each signal names what it found, not a colour: "2 FrameJobs running", "1
TalosUpgrade in progress on neura-k3s-w2", "llama-8b is Ready and serving".

**The button** patches the Deployment's image through the SDK. Kubernetes'
own rollout does the rest, and its revision history is the record of what
changed and when — which is why this design records nothing of its own.

## What it does not do

No rollback button. Kubernetes has `kubectl rollout undo`, the revision history
is already there, and a rollback offered without also offering to reverse a
schema migration is a button that lies in exactly the cases it matters.

No scheduling, no maintenance windows, no automatic updates. The screen is a
decision a human makes with better information. Making it autonomous means
deciding what to do when the readiness panel says no, and that question is worth
its own design rather than a default.

No CRD updates. If a release changes a CRD schema, applying it is `make install`
and outside this screen — a screen that silently applied schema changes would be
the most dangerous button in the product.

## Where this leaves the API freeze

This was expected to touch `frame.plume-labs.io` and therefore to block Phase B.
It does not: no CRD, no controller, no field. The freeze and this screen are
independent, and can proceed in either order.

## Exit criteria

From a released version, the screen lists it, shows what it would disturb, and
pressing the button rolls the component onto it — verified against the test
cluster, with the readiness panel showing something true while a FrameJob is
actually running.

## Open question

Where the operator's own version is read from. The image tag is the obvious
source and needs nothing new, but it is a deploy detail rather than a fact about
the binary: an image retagged by hand reports whatever the tag says. Building
the version into the binary at link time and exposing it — on the metrics
endpoint, or in a condition — is more honest and is a small change to the
release chain. Worth deciding when the release chain is built, not now.
