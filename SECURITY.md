# Security Policy

Frame is a Kubernetes operator and control plane. A vulnerability in it is a
vulnerability in whatever cluster runs it, so reports are welcome and taken
seriously.

## Supported versions

Frame has not cut a tagged release yet. Until it does, **only the current
`main` branch is supported**, and fixes land there rather than on a backport
branch. Once releases begin, this table will name the supported minor series.

| Version  | Supported |
| -------- | --------- |
| `main`   | ✅        |
| Anything built from an older commit | ❌ — rebuild from `main` |

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues,
discussions, or pull requests.** A public issue tells everyone about the hole
before there is a fix.

Report privately through GitHub's Private Vulnerability Reporting:

> **<https://github.com/rmocq/frame/security/advisories/new>**

That form is the only supported reporting channel. It opens a draft advisory
visible to the maintainers and to you, and it is where the fix, the CVE
request, and the disclosure are coordinated. If you cannot use it — for
example because the repository's advisory form is unavailable to you — open a
public issue containing **no technical detail**, asking a maintainer to make
contact, and wait to be reached.

### What to include

Please include as much of the following as you can. The first three are what
make a report triageable at all:

- The type of issue (e.g. privilege escalation, RBAC over-grant, SSRF,
  container escape, credential exposure)
- Full paths of the source file(s) involved
- The location of the affected code (branch, commit, or a direct URL)
- Any special configuration required to reproduce it — in particular, whether
  it needs a specific Kubernetes distribution, CNI, or admission configuration
- Step-by-step instructions to reproduce
- Proof-of-concept or exploit code, if you have it
- The impact: what an attacker gains, and what they need already to get there

Frame runs with real cluster authority, so it is especially useful to say
**which principal** the attack starts from — an unauthenticated network
position, a namespaced user, a compromised workload — and what it reaches.

## What to expect

| Stage | Target |
| ----- | ------ |
| Acknowledgement of your report | 5 working days |
| Initial assessment and severity | 10 working days |
| Fix or documented mitigation for Critical/High | 30 days where practical |

Frame is maintained by a small team; these are honest targets, not a
contractual SLA. If a report goes unacknowledged past the first target, please
comment on the draft advisory to nudge it.

## Disclosure

Fixes are disclosed through a GitHub Security Advisory on this repository once
a fix is available, or after 90 days from the report, whichever comes first.
Reporters are credited by name or handle unless they ask not to be.

## Scope

In scope: anything in this repository — the operator (`Dockerfile.controller`),
the control plane UI (`Dockerfile`), `authd` (`Dockerfile.authd`), the
generated RBAC in `config/rbac/`, the admission webhooks, the Helm chart, and
the deployment manifests under `deploy/`.

Out of scope, because they are not Frame's to fix — report these upstream:

- Vulnerabilities in Kubernetes itself, in Talos, or in Argo Workflows
- Vulnerabilities in base images (`nginx`, `golang`, `distroless`,
  `ghcr.io/ggml-org/llama.cpp`) that Frame merely consumes, unless Frame's use
  of them is what makes the issue exploitable
- Findings that require cluster-admin to begin with, since cluster-admin can
  already do anything Frame can

There is no bug bounty. Frame is not part of any bounty programme, and reports
here are not eligible for rewards from GitHub or anyone else.
