# Telling a stuck installer from a dead one — design

**Status:** draft 2026-09-14. Amends `2026-09-11-provisioning-debian-design.md`
§10 (Failure), which is merged and running. It changes no phase, no transition
and no decision that design makes; it adds a diagnosis to the one phase that
has none.

## 1. The hole

`provision.Install`'s `Installing` phase (`internal/provision/install.go:214`)
runs for twenty minutes with one way out: `WaitForOurSystem` dialing port 22
and reading an install marker that carries this installation's UID. Between
the `Reset("ForceRestart")` that boots the installer and that first answer,
nothing is consulted. Lot 1 established why nothing can be: this BMC replays
cached sensor readings as live ones in exactly that window.

So four different outcomes produce one message, word for word:

```
waiting for <ip> to come up as our system: context deadline exceeded,
and no attempt had completed before it
```

1. `debian-installer` is sitting on a question the preseed did not answer.
   The boot arguments carry `priority=critical`
   (`internal/provision/iso.go:36`), so a question that reaches the console is
   by construction one no preseeded answer covered. It will wait forever.
2. The installer died — kernel panic, a controller that dropped its disks.
3. The machine refused, deliberately. `preseed/early_command`
   (`internal/provision/preseed.go:104`) powers the machine off when a named
   disk is not the size Frame was told it is. `install.go` already documents
   that this surfaces as an `Installing` timeout and nothing more.
4. The machine never got that far: it did not boot the media, or `netcfg`
   never brought the network up, so nothing was ever reachable at that
   address.

Case 3 is the sharpest: a guard doing exactly its job is indistinguishable
from a crash, and the operator learns nothing for twenty minutes.

## 2. The decision: two signals, because one cannot do it

Progress and liveness are different questions, and the failure modes above
separate only when both are answered.

- **Checkpoints alone** say where it stopped, never whether it still breathes.
  An installer parked on a debconf question and one that panicked immediately
  after the same checkpoint are identical to a checkpoint stream.
- **A heartbeat alone** says it breathes, never whether it is getting
  anywhere. A `main-menu` that fell back after a component crashed still ticks.

Crossed, they name all four:

| heartbeat | last checkpoint | what happened | §1 case |
|---|---|---|---|
| alive | not advancing | **a debconf question** — a human must answer it | 1 |
| stopped | `partman` or `late` | died during partitioning, `pkgsel`, or base install | 2 |
| stopped | `early` | died between the disk guard and partitioning | 2 |
| stopped | `late` | **the install succeeded** — d-i rebooted, which kills the heartbeat; `Installing` ends minutes later on SSH | — |
| never started | `netcfg-done` | **the size guard's `poweroff -f`** — a refusal, not a fault | 3 |
| never started | `netcfg` only | the run script ran but the network never came up on the static address | 4 |
| never seen | none | media never booted at all | 4 |

The `netcfg-done` row is the one this design exists for, and it took a
correction to make true. An earlier draft claimed `netcfg` alone identified
the refusal and that "no other outcome produces" it. That was wrong: `netcfg`
is emitted at the *top* of the run script, before `kill-all-dhcp`, so
anything that stops the machine between there and the size assertion — above
all a netcfg re-run that fails to take the static address, the step §10 marks
unproven — gives the identical signature. A fifth checkpoint, emitted after
`netcfg` returns, is what separates them: a machine that never reached the
static address cannot send it, and a machine the guard refuses will have.

The `late` row matters for a different reason: it is the **normal** end of a
successful install, not a failure at all. The heartbeat dies when d-i reboots
after `late_command`, and `WaitForOurSystem` then waits out a POST and boot —
routinely longer than the 60-second threshold. Reported as an ordinary
`HeartbeatLost` it would tell an operator that every successful install died
during `pkgsel`.

The heartbeat is the signal the machine cannot fake in the dangerous
direction. It runs from `debian-installer`'s own shell: it cannot outlive the
kernel that hosts it. It can only ever *under*-report liveness — a lost
heartbeat on a live machine is a false alarm, never a false success.

## 3. The boundary, enforced structurally

**A beacon must never end, advance, or fail a phase.** Only
`WaitForOurSystem` may end `Installing`, because only it proves identity: SSH
to the address plus a marker file carrying this installation's UID. A beacon
arrives over unauthenticated HTTP from the management network; if it could
move a phase, anything on that network could declare a machine installed.

This is not left to convention. The read side lives in the **controller**, and
`provision.Install` is never given a way to see it:

- `internal/provision` only *writes*: it renders the beacon URL into the
  preseed and the `preseed/run` script, and it *tells* the caller which token
  it is using, through one new write-only callback on `Deps`
  (`ReportToken func(string)`, alongside the existing `Report func(Phase)`).
  It is given no way to *ask* what has arrived — no reader appears in `Deps`,
  and `Install` gains no branch that reads beacon state.

  This is weaker than "`Deps` gains no field", which an earlier draft of this
  section claimed. The controller cannot learn the token any other way: the
  token is produced inside `Install` by `Images.Build` and is absent from
  `Result`, which the controller only sees once the install is over. The
  boundary that matters survives intact — telling is not asking — but it is
  worth being exact about which one it is.
- `frame-provisiond` records what arrives and serves it back **on the build
  listener** — the in-cluster one — never on the LAN-facing media listener it
  was collected on.
- `FrameInstallReconciler` polls that read endpoint while `status.phase` is
  `Installing` and writes a condition. It never returns an error because of
  one, never shortens a timeout, never sets `Failed`.

Delete the whole mechanism and every install behaves exactly as it does
today, twenty-minute timeout included. That is the test of whether the
boundary held.

## 4. Keyed by token, not by UID

Beacons are keyed by the 32-hex image token `ImageStore.Build` already
returns — the same token that names the ISO and the preseed on the media
listener (`internal/provision/server.go:87`).

Not by `Spec.UID`. The UID has exactly one job — proving over SSH that the
running system came out of this image — and giving it a second job as a
transport handle is how a proof turns into a label. The token is already the
transport handle, already per-install, already 32 bytes of `crypto/rand`, and
already published on that listener. Nothing new is exposed.

## 5. What the machine sends

Four checkpoints, at points d-i already gives Frame a shell:

| checkpoint | where | means |
|---|---|---|
| `netcfg` | the top of the `preseed/run` script (already used for the netcfg re-run, `preseed.go:17`) | preseed fetched, script running |
| `netcfg-done` | the end of that same script, after `netcfg` returns | the machine is on its static address |
| `early` | `preseed/early_command`, **after** the size assertion | the disk guard passed |
| `partman` | `partman/early_command` | about to partition |
| `late` | `preseed/late_command` | base system installed, about to reboot |

Plus a heartbeat: a background loop started at `early`, hitting the same
endpoint every 15 seconds with the checkpoint it last passed.

**The store keeps the furthest-progressed checkpoint, not the latest one.**
This is a correction to an earlier draft of this section, and it is
load-bearing rather than a detail. The heartbeat re-sends the checkpoint it
started at — `early` — every fifteen seconds. Against a last-write-wins
store, that means `LastCheckpoint` reverts to `early` within fifteen seconds
of `partman` firing and stays there for the rest of the install, and §2's
table collapses: every stopped-mid-install row reads `early`, whatever
actually happened. Only the `netcfg` refusal survives, because there the
heartbeat never started.

So `Record` ranks the four checkpoints in their fixed d-i order and keeps the
higher one. `LastSeen` and `Count` still move on every beacon including a
heartbeat that advances nothing — **liveness comes from `LastSeen`, progress
from `LastCheckpoint`**, and keeping them independent is the whole reason two
signals are worth collecting.

**A heartbeat is called lost after 60 seconds** — four missed sends. Not one
or two: the machine is mid-installation on a network Frame just reconfigured
under it, and the cost of calling a live installer dead is an operator sent to
a machine that needed nothing. Sixty seconds is a guess with a reason, and it
is the first number to revisit once real installs have run.

Transport is `wget` from busybox — d-i's initrd has it, and `curl` does not
arrive until `pkgsel` (`preseed.go:108`). Request shape:

```
GET /beacon/{token}/{checkpoint}
```

No body, no query string, response ignored. A beacon that fails to send must
never break the install: every invocation is `wget -q -O /dev/null ... || true`.

`early` is emitted *after* the size assertion on purpose. It is what makes
case 3 legible: the guard powering the machine off leaves `netcfg` as the last
checkpoint and no heartbeat, which is a different row of §2's table from a
panic during partitioning.

## 6. What provisiond keeps

In memory, one entry per token:

```go
type beacon struct {
    LastCheckpoint string
    LastSeen       time.Time
    Count          int
}
```

Bounded and boring on purpose:

- The checkpoint is matched against the closed set of §5. Anything else is a
  404 and is not stored. No attacker-chosen string ever reaches memory.
- Entries are capped and evicted oldest-first; an entry is dropped when its
  image is (`DELETE /iso/{name}` already removes image and preseed together).
- Writes are on the media listener, reads on the build listener, and the two
  are different `http.Server`s on different addresses — the split this design
  already relies on so the LAN never reaches anything that writes 700 MB
  files.

**In-memory state pins `frame-provisiond` to one replica.** It is already
`replicas: 1` in both the chart and `config/provisiond`
(`charts/frame/templates/provisiond-deployment.yaml:17`). This design makes
that a requirement rather than a default, and says so in a comment at both
sites. A provisiond restart loses beacon history. An empty store answers 404, which
is indistinguishable *at the wire* from an installation that never reported —
so the controller remembers, per installation, that it has once seen beacons,
and a later 404 against that memory is state loss rather than silence. Without
that, a restart would make Frame report a live machine as never having booted.
The condition then reports `Unknown` and the install is unaffected — which is the correct behaviour for
a diagnostic that is not allowed to decide anything.

## 7. What the operator sees

One condition on `FrameInstall`, no new status struct:

```
type: InstallerResponding
status: True | False | Unknown
reason: Heartbeat | Rebooting | HeartbeatLost | NeverSeen | Unavailable
message: "last checkpoint partman, 4m12s ago; heartbeat lost 3m01s ago"
```

- `True`/`Heartbeat` — beacons arriving. The message's checkpoint and age are
  what distinguish "installing normally" from "alive and not advancing".
- `False`/`HeartbeatLost` — beacons arrived and none has for 60 seconds. This
  covers both a heartbeat that stopped mid-install and one that never started
  at all (the size-guard refusal, whose only beacon is `netcfg`). The two are
  told apart by the checkpoint in the message, not by a fifth reason — the
  reason says whether Frame is being spoken to, the message says how far it
  got.
- `False`/`NeverSeen` — the phase is `Installing` and nothing ever arrived.
- `False`/`Rebooting` — the last checkpoint is `late`, so the heartbeat
  stopped because d-i rebooted. The status stays `False` — the installer
  genuinely is not answering — but the reason says why, and it is good news.
- `Unknown`/`Unavailable` — provisiond could not be reached, or lost its
  memory, or this manager does not hold the token.
  Never conflated with `NeverSeen`: one says the machine is silent, the other
  says Frame cannot hear.

The condition is refreshed by the requeue the reconciler already performs
every 15 seconds while an install is in flight
(`frameinstall_controller.go:199`). No new requeue path — but note that this
branch currently returns *immediately*, before touching status. The beacon
poll and the condition write go on that path, ahead of its return; that is the
whole of the controller change.

<!-- ponytail: condition only, no status.installer struct. A condition
     carries reason + message, which is everything a human reading
     `kubectl describe` needs. Add the struct when the console needs to sort
     or filter on checkpoint, not before. -->

## 8. Remote installer syslog — staged second, and gated

`debian-installer` supports `log_host` / `log_port` boot parameters, sending
its syslog to a remote collector over UDP. Frame already rewrites every kernel
line of the isolinux and grub trees (`iso.go:36`, `iso.go:104`), so adding
them is one string.

It would carry the thing the beacon cannot: the **text of the question** that
blocked. It is the diagnosis where the beacon is the discriminator, and it
does not replace it — UDP silence proves nothing, so liveness still needs the
heartbeat.

**Stage 1 — §§3-7 — is what the implementation plan covers.** Stage 2 is this
section, and it is staged second, gated on measurement rather than argument,
because:

- Correlation is unproven. Syslog carries no token. It would be keyed by
  source address — and during early d-i the machine is on a DHCP lease, not
  yet the static address Frame configured (`preseed.go:20-24` is the same
  ordering problem in a different guise). Which address the first lines
  arrive from has to be *observed* on the ML350 Gen9.
- It adds a UDP listener on the LAN-facing side, parsing input from an
  unauthenticated source. That is a larger surface than a path-matched GET,
  and it should not be added on the same day as the mechanism that makes it
  diagnosable.

Stage 2 begins by booting one machine with `log_host` set and reading what
arrives. If nothing does, the stage ends there and costs one boot.

## 9. Testing

The discriminator is the thing under test, so the tests assert the *four rows*
of §2, not the plumbing:

- `provisiond`: a beacon with an unknown checkpoint is a 404 and stores
  nothing; a beacon on the build listener's path is not the write path; the
  read endpoint is absent from the media listener entirely.
- Preseed rendering: every emitted beacon URL goes through the existing
  `checkPreseedValue` guards — a beacon URL is interpolated into a shell
  command in the preseed, which is the exact boundary `checkPreseedValue` (`preseed.go:302`) exists
  for. A newline or a quote in the media URL must be refused, and its test
  must assert the refusal, not the rendering.
- Controller: four table cases mapping (beacon state, elapsed) to (reason,
  message), plus **one test that proves the boundary**: with every beacon
  state including "never seen", `Installing` still ends only on
  `WaitForOurSystem`, and a `Failed` is never produced by beacon state alone.
  That last one is the test that fails if someone later makes the beacon
  load-bearing.
- The size-guard case (§1 case 3) gets its own named test: last checkpoint
  `netcfg`, no heartbeat, and the message must distinguish it from a panic.

## 10. Assumptions to verify, not assume

Each of these is currently belief, and each is cheap to falsify on the ML350
Gen9. None of them may be written down as fact until it has been.

0. **The backgrounded heartbeat does not hold `early_command` open.** d-i runs
   preseed hooks under `log-output`, which reads the hook's output; a reader
   waiting for EOF rather than for the child's exit would block on the loop's
   inherited copy of the pipe. The loop redirects all three descriptors to
   `/dev/null` so it cannot, but that the redirect is sufficient on this d-i
   version is still an observation to make, not a fact.
1. **A backgrounded loop started in `preseed/early_command` survives** for the
   rest of the installation. This is the single load-bearing assumption; if it
   is false, the heartbeat becomes per-checkpoint only and the "alive and not
   advancing" row of §2 goes away with it. Falsifiable in one install.
2. **busybox `wget` is present and can reach the media listener** from the d-i
   environment at `early`. Highly likely — d-i fetches its own preseed over
   the same network — but the preseed fetch happens through d-i's own
   machinery, not necessarily through a `wget` on `PATH`.
3. **`partman/early_command` runs and can reach the network.** If not, the
   `partman` checkpoint is dropped; §2's table still works with three.
4. **d-i emits to `log_host` at all on this hardware**, and from which source
   address (§8).

Nothing in stage 1 depends on 3 or 4. Assumption 1 is the one to test first,
because a negative answer changes the design rather than trimming it.
