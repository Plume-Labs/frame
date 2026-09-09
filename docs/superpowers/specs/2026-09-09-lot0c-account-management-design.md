# Lot 0c — inviting a second human

**Status:** design approved 2026-09-09. Implementation not started.

## Why

Lot 0a shipped per-user identity and was proven on the test cluster on
2026-09-09. Proving it exposed the hole: **only the first admin can ever sign
in.** `/auth/bootstrap` creates that account with `passwordAuth: disabled` and
no credential, hands back a 12h session, and closes itself forever
(`AdminCount() > 0`). authd has eleven routes — login, token, logout, the four
WebAuthn ceremonies, bootstrap — and **none of them sets a password**. An
account an admin creates therefore cannot acquire any credential: no password,
and no passkey either, because enrolment needs a session it cannot obtain.

The consequences are not cosmetic. The lot was scoped on "several humans"; the
RBAC tiers are real but nobody can be bound to them; and the viewer's 403 —
the control that discriminates the whole design — is provable only in envtest
and by `SubjectAccessReview`, never end to end.

## Decisions

**A disabled account is a flag, not an absence.** `FrameUser` gains
`spec.state: enabled|disabled`. Revoking every passkey would have avoided
touching a frozen kind, but it makes deactivation destructive and reversible
only by re-enrolment. The cost is stated plainly: this adds a field to a kind
the roadmap declared frozen, which is the objection raised against NodeTuning.
It arrives with those four debts paid — RBAC tiers already exist for the kind,
no controller is added, the docs move with it, and it ships with a test that
proves the *effect*, not the field's presence.

**One place decides whether an identity may be issued.** Four paths mint or
re-mint identity: `/auth/token`, password login, passkey login, and invitation
acceptance. A check missing from one of them makes deactivation a lie. They
all call the same function.

The load-bearing case is `/auth/token`: the UI calls it every 15 minutes, so
disabling an account cuts a **session already open** within one token
lifetime, without touching the cookie.

**The invitation link is single-use by construction.** It carries a sealed
`PurposeInvite` token (the codec already domain-separates and expires sealed
values) and is refused once the account holds any credential. No table, no
cleanup, no revocation list — the link dies at the first enrolment.

## Design

### FrameUser

```go
// State decides whether authd may issue this account an identity at all.
// +kubebuilder:validation:Enum=enabled;disabled
// +kubebuilder:default=enabled
State string `json:"state,omitempty"`
```

The webhook refuses disabling the last admin, for the same reason it refuses
demoting them, and refuses a non-admin changing `state` — the guard added in
lot 0a covers `role`, and extends here.

### authd, new routes

| Route | Caller | Behaviour |
|---|---|---|
| `POST /auth/invite` | admin session | Body `{email, role}`, `role` restricted to `operator` or `viewer`. Creates the FrameUser (`state: enabled`, `passwordAuth: disabled`, no credential). Returns `{url}` carrying a `PurposeInvite` token sealed over the email, TTL 24h. |
| `POST /auth/invite/accept` | invitation token | Opens the seal, loads the account, **410 if it already holds a credential**, else sets a 15-minute session so the holder can enrol. |
| `GET /auth/credentials` | any session | The caller's enrolled credentials. `?user=<email>` for an admin reading someone else's. |
| `DELETE /auth/credentials/{id}` | own, or admin | Removes one credential. Refuses if it would leave the last admin with none. |

`Store` already has `Create`, `ByEmail`, `AdminCount`, `RemoveCredential` — no
new persistence.

**An invitation cannot mint an admin, and that is not a policy choice.** authd
creates the FrameUser under its own ServiceAccount, and lot 0a's webhook
refuses a `spec.role: admin` create from a non-admin requester once any admin
exists. So `/auth/invite` refuses `admin` with a 400 that names the way
through: invite as operator, then promote from the Accounts screen, where the
write carries the admin's own impersonated identity and the webhook allows it.

**Revocation is already guarded, and more tightly than this document first
said.** `Store.RemoveCredential` refuses to leave *any* passkey-only account
with no credential, not merely the last admin. The route surfaces that as a
409 rather than restating the rule — a second copy would be a second thing to
keep true.

**`spec.state` goes into both served versions.** `TestHubRoundTripIsLossless`
fuzzes v1beta1 → v1alpha1 → v1beta1 and demands equality, so a field added to
v1beta1 alone fails it — correctly: it would be silently dropped by any client
writing at the older version.

### UI

An admin-only Accounts screen: email, role, state, credential count; invite
(dialog → the link to copy); change role; enable/disable; revoke a passkey.
Plus an acceptance page that reads the token from the URL, calls accept, and
goes straight to enrolment.

`PasskeysDialog` switches from its local "enrolled in this session" list to
`GET /auth/credentials`, which is the gap that made that caveat necessary.

## Testing

Go: one test per identity-issuing path, each red if its check is removed —
that is four tests, not one, because the defect this design guards against is
exactly "the check exists in three places". Invitation accepted once then 410.
The webhook refusing to disable the last admin. Revocation refused when it
would strand the last admin.

envtest: the CRD accepts `enabled`/`disabled` and rejects anything else.

Frontend: the transformations in `src/lib` — vitest never runs `.tsx`.

**On the cluster, and this is the point:** invite a viewer, accept it in a
separate browser profile, enrol a key, attempt a cordon, and get a 403 with
its FrameTask. That path has never once been executed end to end.

## Out of scope

Password login stays unavailable — no route sets one, and passkeys make it
unnecessary. Email delivery: the invite returns a link the admin copies; there
is no mail path in this cluster and inventing one would be a second project.
