# Frame UI — per-user authentication with WebAuthn

**Date:** 2026-07-30
**Status:** approved, not implemented

## Problem

The Cluster Control UI has no notion of a user. All three pods share one
ServiceAccount, and an nginx sidecar injects its token into every request to
the Kubernetes API. Anyone who reaches the service has every permission the
ServiceAccount holds, including node drain and SchedulingPolicy deletion. The
apiserver audit log records `cluster-control-ui` for every action, never the
person who took it. A read-only `cluster-control-viewer` ClusterRole exists but
nothing is bound to it.

This was a deliberate, accepted trade-off for a private cluster, documented in
`deploy/kubernetes/base/rbac.yaml`. It is what this design replaces.

## Goals

- A YubiKey (WebAuthn) is the primary credential; a password is available
  per-account as an alternative.
- Kubernetes evaluates RBAC against the real person, and the audit log names
  them.
- Accounts are managed from the UI: create, delete, assign role, revoke a key.
- No credential is reachable by JavaScript, and no cluster misconfiguration can
  lock an operator out of the cluster itself.

## Non-goals

- Federating to an external identity provider. Frame is the platform layer; it
  should not depend on one.
- Per-namespace or per-resource authorization beyond the three roles below.
  Kubernetes RBAC already expresses that if it is ever needed.

## Decisions

| Decision | Choice | Why |
|---|---|---|
| Identity reaches Kubernetes via | Native OIDC | The apiserver validates the token itself. No component holds an impersonation privilege. |
| Apiserver configuration applied by | Privileged DaemonSet | No SSH access to the nodes exists. |
| Account storage | `FrameUser` CRD | Idiomatic for an operator project, inspectable with kubectl, backed up by Velero. No database. |
| Password | Per-account toggle | The real security level of each account stays visible in its CR. |
| Account management | Full CRUD in the UI | Requested. Brings an admin role and a lockout guard with it. |

## Architecture

The `kubectl proxy` sidecar is removed. It is what carried the shared
ServiceAccount; deleting it is what makes this change real rather than
cosmetic.

```
browser
  ├─ /auth/*     ──► authd            WebAuthn, password, OIDC issuer,
  │                                   credential writes
  └─ /api,/apis  ──► nginx ──► k3s apiserver (HTTPS)
                               Authorization: Bearer <ID token>
```

### Components

**`cmd/authd`** — new Go binary, two replicas, no shared state. Serves
`/.well-known/openid-configuration` and JWKS, runs WebAuthn registration and
assertion, verifies passwords, mints ID tokens, and writes credentials into
`FrameUser` resources with its own ServiceAccount.

WebAuthn challenges live in a short-lived signed cookie rather than a shared
store, and sessions are JWTs, so the replicas need no Redis and no database.

Its own RBAC stays narrow: get and list on `FrameUser`, patch on their status,
create for bootstrap only, and delete on the single bootstrap Secret. It never
needs the permissions it hands out — the ID token it mints is what carries
those, and the apiserver checks them against the user, not against `authd`.

**`FrameUser` CRD** — one resource per account.

```yaml
spec:
  email: alice@example.com
  role: admin | operator | viewer
  passwordAuth: enabled | disabled
  passwordHash: <argon2id>          # written only by authd
status:
  credentials:
    - id: <credential id>
      publicKey: <cose key>
      signCount: 42
      addedAt: <timestamp>
      label: "YubiKey 5C"
```

**Validating webhook** (`internal/webhook/`, which already exists) — refuses
deleting or demoting the last admin. This is load-bearing, not decorative: see
the identity/secret split below, where admins write these resources directly.

**`k3s-oidc-configurator` DaemonSet** — writes the apiserver arguments and the
issuer CA onto the k3s server node, then restarts k3s.

### Token handling

`authd` sets an `HttpOnly` session cookie, unreadable from JavaScript. The ID
token the apiserver consumes is short-lived (15 minutes), fetched on demand
from `/auth/token`, and held in memory only — never `localStorage`. An XSS flaw
therefore cannot steal the session, only a token that expires within the
quarter hour.

## Flows

### Bootstrapping the first admin

A `frame-auth-bootstrap` Secret holds a one-time token. While zero `FrameUser`
resources exist, `/auth/bootstrap` accepts it, creates the first admin account,
enrols its passkey, and deletes the Secret. Once any `FrameUser` exists the
endpoint returns 404 unconditionally — there is no open registration window.

### Signing in with a passkey

Usernameless flow, matching what Neura already ships: no `allowCredentials`, so
the browser offers whichever resident key matches the RP ID, and the assertion
carries the `userHandle` identifying the account. `authd` verifies the
signature, advances the stored counter, and sets the session cookie.

A counter that fails to advance is logged for investigation and rejects the
login, but never revokes the credential. The library verifies the assertion's
signature first and only afterwards compares the reported counter against the
stored one, so by the time this check can even fire the caller has already
proven possession of the private key — this is a genuine clone/replay signal,
or an authenticator restored from a backup, not something an unauthenticated
caller can trigger by guessing a `credentialId`. Revoking automatically would
instead punish the legitimate case: a real owner whose authenticator glitched
or was restored must not lose their only way in over an automatic action.
This reproduces a decision already validated in Neura.

### Signing in with a password

Accepted only when `spec.passwordAuth: enabled` on that account. Argon2id.
Yields the same session cookie.

### Mapping identity to RBAC

Apiserver arguments:

```
oidc-issuer-url=https://cluster-control-auth.cluster-control.svc
oidc-client-id=frame-ui
oidc-username-claim=email      oidc-username-prefix=frame:
oidc-groups-claim=groups       oidc-groups-prefix=frame:
oidc-ca-file=/etc/rancher/k3s/frame-oidc-ca.crt
```

The prefix is set explicitly rather than left to default, so an identity minted
by Frame can never collide with a cluster user of the same name.

| Role | Group | Grants |
|---|---|---|
| viewer | `frame:viewers` | `cluster-control-viewer` |
| operator | `frame:operators` | plus `cluster-control-operator` |
| admin | `frame:admins` | plus create/delete/patch on `FrameUser` |

Changing someone's role means editing their resource. The bindings target
groups, and membership comes from the token, so no RBAC YAML changes.

The issuer's TLS certificate comes from cert-manager: a dedicated CA issued by
the existing `neura-selfsigned` ClusterIssuer, then a serving certificate for
`cluster-control-auth.cluster-control.svc`. The DaemonSet only has to place the
CA on the node.

### Identity writes versus secret writes

Account lifecycle — create, delete, change role — goes **directly to the
apiserver** under the browser's OIDC identity. RBAC governs it and the audit
log names the person, exactly like every other action in the UI.

Only secrets go through `authd`: setting a password, enrolling a key, revoking
a key. A hash computed in the browser would be a client-controlled hash, so
that cannot live there.

Credentials sit in `status`, not `spec`, so `authd` owns them and an admin
editing an account cannot corrupt a key by hand. Revocation is therefore an
`authd` endpoint rather than a direct patch: the caller may revoke their own
keys, and an admin may revoke anyone's. `authd` refuses to remove the last
credential of an account whose password is disabled, since that would leave it
with no way to sign in — the same class of guard as the webhook's, applied
where `authd` is the only writer.

This is precisely why the anti-lockout guard must be an admission webhook. A
check inside `authd` would be bypassed by `kubectl delete`.

## Failure modes

The one scenario that can break the cluster: the DaemonSet writes an invalid
`config.yaml` and k3s fails to restart. There is then no API, and only the
Proxmox console recovers it. Mitigations: back up the original before writing,
validate the YAML *before* triggering the restart, write atomically, and
restart only when the content actually changed. Idempotency is required for a
second reason — the restart kills the DaemonSet's own pod, so a pod that
re-applied unconditionally would restart k3s in a loop.

| Failure | Effect | Recovery |
|---|---|---|
| `authd` unavailable | No new sign-ins; issued tokens last 15 minutes | Restart. No cluster impact. |
| Issuer unreachable from apiserver | OIDC auth fails, UI unusable | Apiserver retries; certificate auth unaffected |
| Clock skew | JWT validation fails | NTP — already watched by the PTP screen |
| Counter regression | Sign-in refused, logged | Manual, no automatic revocation |
| Lost key, password disabled | That account cannot sign in | `kubectl edit frameuser` re-enables the password |

**The kubeconfig is the universal break-glass.** Client-certificate
authentication is never in the OIDC path, so no authentication
misconfiguration can lock an operator out of the cluster. That property is what
makes the rest of this acceptable.

## Rollout

Ordered so the current path keeps working until the new one is proven:

1. Deploy `authd`, the CRD and the webhook alongside the existing setup,
   touching nothing. Verify discovery, JWKS and token shape.
2. Configure the apiserver via the DaemonSet. End-to-end proof:
   `kubectl --token=<ID token> auth whoami` returns `frame:<email>` and the
   expected groups.
3. Only then switch the UI: remove the sidecar, bind RBAC to the groups, drop
   the ServiceAccount bindings.

Nothing is broken until step 3, and stopping after step 1 or 2 is safe.
Rollback is a `git revert` plus an Argo resync, plus removing the apiserver
arguments from the backup the DaemonSet kept.

## Testing

`authd` is testable without a cluster: WebAuthn assertion verification against
fixed vectors (valid signature, forged signature, regressed counter, expired
challenge), ID token signature and claims, password refusal when
`passwordAuth: disabled`, and the bootstrap endpoint closing permanently once a
`FrameUser` exists. The webhook is covered by envtest, already used in this
project.

The one link no unit test covers is the apiserver's own OIDC validation. It is
verified live at step 2 with `auth whoami`, followed by an `auth can-i` per
role confirming that a viewer cannot patch a node.
