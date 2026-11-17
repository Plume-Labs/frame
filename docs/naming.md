# Hearthrack and the `frame` names

The project is named **Hearthrack**. It was called **Frame** until November 2026,
and the old name still appears in the places below. They stay on purpose: renaming
them would break clusters that are already running, and none of them is shown to a
person as the product name.

## What changed

- README, documentation, `SECURITY.md`, and the product name in the texts the console shows to people (a few comments and API-side labels still say Frame).
- The container images are published under their new names,
  `ghcr.io/plume-labs/hearthrack-{controller,ui,authd,uiproxy,provisiond,agent}`.
  Each image is **also** pushed under its former name, `ghcr.io/plume-labs/frame-<name>`
  (same tags, same digest), so a manifest that still pulls the old name keeps working.
  The old names are kept for the transition; when they stop being pushed is not decided yet.

## What stays `frame` (compatibility aliases)

| Name | Why it stays |
|---|---|
| API group `frame.plume-labs.io`, versions `v1alpha1` / `v1beta1`, and the `Frame*` kinds (`FrameJob`, `FrameNode`, `FrameUser`, …) | Stored objects, RBAC, Terraform and SDK users all reference them. Changing a group means a new CRD set and a data migration, not a rename. |
| Namespaces `cluster-control` and `frame-system`, and resource names such as `frame-uiproxy`, `frame-auth-tls` | Renaming a namespace or a Secret recreates it. |
| Helm chart name `frame` (`charts/frame`, `oci://ghcr.io/plume-labs/charts/frame`) | The chart name is part of every resource name it renders; a different name is a different Helm release, not an upgrade. |
| Go module `github.com/Plume-Labs/frame`, the `frame-sdk` module in `src/lib` and the `FrameClient` class | Import paths. |
| Request header `X-Frame-Action`, labels and annotations under `frame.plume-labs.io/` | Part of the wire format. |
| The `Frame CRs` label in Settings | It counts the `Frame*` objects. |

A future major version may introduce a new API group alongside the old one; that
needs its own migration plan and is not part of this change.

## Not done in the repository

Renaming the GitHub repository and the organisation, and reserving domains, are
settings an owner makes by hand. Once the repository is renamed, GitHub redirects the old URL for as long as no new repository takes the old name.
