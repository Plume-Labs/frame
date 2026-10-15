# Provisioning on Ubuntu 24.04 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** replace the Debian d-i preseed installer in `internal/provision` with Ubuntu 24.04 autoinstall, keeping the install pipeline, its guards and its contract with the controller.

**Architecture:** a new `autoinstall.go` marshals a Go struct into a `#cloud-config` document served per token over NoCloud; `iso.go` rewrites only the two GRUB files of the Ubuntu ISO and replays its boot records with native xorriso; the beacon keeps its store and route with a reduced checkpoint set. The GPU Operator release changes separately.

**Tech Stack:** Go 1.2x, `sigs.k8s.io/yaml` (already in go.mod via controller-runtime) or `gopkg.in/yaml.v3`, xorriso, subiquity autoinstall v1, cloud-init NoCloud.

**Spec:** `docs/superpowers/specs/2026-09-29-provisioning-ubuntu-design.md`

## Global Constraints

- Base image: `ubuntu-24.04.5-live-server-amd64.iso`, SHA256 `97f3d7ffb032c3eb3b23d2c8be9cc76e60c2c1f2c0146ba5ba9fe01cafae0fd8`, cap 6 GiB.
- Boot args, inserted before `---`: `frame=1 autoinstall ds=nocloud\;s=<media>/autoinstall/<token>/ ip=dhcp noprompt`.
- Served names: `user-data`, `meta-data`, `vendor-data` only.
- Checkpoints: `early`, `late`, `failed`. Heartbeat 15 s, lost after 60 s, backgrounded with all three streams redirected.
- Static networking only; DNS ≥ 1; IPv4 address as CIDR.
- No new dependency without an upper bound.
- Package comments keep the repo's style: say why, name what is unproven on hardware.

## Review Focus

1. A value containing YAML-significant text (`: `, ` #`, `{`, `&`, `*`, leading `-`) in hostname, DNS or key comment → refused or marshalled inert, never a structural change. Test in Task 1.
2. A Legacy-boot spec must not get an ESP-only layout (and UEFI must not get bios_grub-only) → `Spec.BootMode` reaches the renderer. Test in Task 1.
3. A base ISO whose remaster loses the UEFI or BIOS El Torito entry → build refused, not served. Test in Task 3.
4. A heartbeat line that is not fully backgrounded would hang subiquity at early-commands → rendered line asserted exactly. Test in Task 2.
5. `DELETE /iso/{token}.iso` must remove the three autoinstall files and the directory, or a later build with a colliding token serves stale data. Test in Task 4.

---

### Task 1: autoinstall renderer

**Files:**
- Create: `internal/provision/autoinstall.go`, `internal/provision/autoinstall_test.go`
- Modify: `internal/provision/types.go` (add `BootMode string \`yaml:"bootMode" json:"bootMode"\`` to `Spec`), `internal/provision/install.go` (set `s.BootMode = o.BootMode` before `Images.Build`), `internal/provision/layout.go` (drop `PartmanRecipe`, keep `validateDisks`, `diskSizeAssertion`, `byIDName`)

**Interfaces:**
- Produces: `func ValidateSpec(s Spec) error` (same name, same callers), `func RenderAutoinstall(s Spec, beaconBase, token string) (string, error)` returning the `user-data` text.

- [ ] Tests first: golden-shape tests unmarshalling the output back (`yaml.Unmarshal` into `map[string]any`) for single-disk×UEFI, single-disk×Legacy, mirror×UEFI, mirror×Legacy; assert: `#cloud-config` first line; `autoinstall.version == 1`; `interactive-sections == []`; `updates == security`; network stanza `frame0` with `match.name == "en*"`, `optional == true`, addresses, default route, nameservers; `identity.username == frame`, password starts with `$6$` or is `!`-locked per chosen value; `ssh.allow-pw == false`; early-commands order = size assertion, by-id rewrite, beacon early, heartbeat (with beacon) and = size assertion, by-id rewrite (without); late-commands contain sudoers, marker with UID, hostname + 127.0.1.1 line; error-commands contain `failed` beacon only with beacon; storage: UEFI → ESP per disk with `grub_device: true` on the ESP partitions, Legacy → `bios_grub` partition per disk and `grub_device: true` on disks; mirror → `raid` raid1 over the two root partitions, `wipe: superblock-recursive` on both disks.
- [ ] Refusal table carried from `preseed_test.go` (empty UID, bad UID, key material, options prefix, two keys, bad CIDR, IPv6, bad gateway, no DNS, non-IP DNS, bad by-id, duplicate disk, wrong disk count, unknown kind) + new: hostname not RFC 1123 (`a_b`, `-a`, 64+ chars, `a: b`), UID not 32 hex, BootMode not UEFI/Legacy.
- [ ] Implement: struct types mirroring the document; storage placeholders `path: /dev/frame-disk-0` / `-1`; early-command 2 is a `sed -i` on `/autoinstall.yaml` replacing each placeholder with `$(readlink -f <byID>)`, refusing (exit 1) if readlink yields nothing; size assertion reused from `diskSizeAssertion` wrapped as `if ! <assertion>; then echo ... >/dev/console; exit 1; fi`.
- [ ] `go test ./internal/provision/ -run Autoinstall -count=1` green; commit `feat(provision): render Ubuntu autoinstall user-data`.

### Task 2: beacon checkpoints

**Files:** `internal/provision/beacon.go`, `beacon_test.go`, `beaconstore_test.go`, `internal/controller/frame/frameinstall_controller.go` (+ its test)

- [ ] Tests: `ValidCheckpoint` accepts exactly early/late/failed; rank early<late<failed; a heartbeat `early` after `late` does not regress; `BeaconHeartbeat` renders `(while true; do …; sleep 15; done) </dev/null >/dev/null 2>&1 &`; controller: `failed` → condition False, Reason `InstallerFailed` regardless of LastSeen.
- [ ] Implement; remove Netcfg/NetcfgDone/Partman constants; update comments (subiquity reads command output, same reason as before).
- [ ] Tests green; commit `feat(provision): beacon checkpoints for autoinstall`.

### Task 3: ISO remaster

**Files:** `internal/provision/iso.go`, `iso_test.go`, `testdata/ubuntu-24.04.5-boot/{boot/grub/grub.cfg,boot/grub/loopback.cfg,PROVENANCE.md}`; delete `testdata/debian-13.6.0-boot/`

**Interfaces:** `func Remaster(ctx, baseISO string, s Spec, seedURL, out string) error` (seedURL = `<media>/autoinstall/<token>/`).

- [ ] Capture the two real files from the ISO (`xorriso -osirrox on -indev … -extract`), PROVENANCE with SHA256 of the ISO.
- [ ] Tests: `withBootArgs` puts the args before `---` and escapes `;`; idempotent via `frame=1`; timeout rewritten to 1; both captured files rewritten on every `linux` line; a fixture ISO built in the test with BIOS+UEFI El Torito entries (`xorriso -as mkisofs -b boot/grub/i386-pc/eltorito.img -no-emul-boot -eltorito-alt-boot -e efi.img -no-emul-boot`) remasters with both entries and label kept, and the rewritten grub.cfg is read back from the output; a fixture with only the BIOS entry is refused.
- [ ] Implement: extract the two files to a temp dir, rewrite, `xorriso -indev base -outdev out -map … -boot_image any replay`, then `-report_el_torito plain` on `out` must list one BIOS and one UEFI entry, else remove `out` and error.
- [ ] Tests green (skip-if-no-xorriso guard kept as today); commit `feat(provision): remaster the Ubuntu server ISO`.

### Task 4: served files, stores, bootstrap

**Files:** `internal/provision/server.go`, `server_test.go`, `cmd/bootstrap/*` if they name `.cfg`/`.sh`

- [ ] Tests: `GET /autoinstall/{token}/{file}` serves the three names, 404 for anything else, for a non-hex token, for an extra segment; `POST /build` writes `<dir>/<token>/{user-data,meta-data,vendor-data}` and the ISO; `DELETE /iso/<token>.iso` removes the ISO and the directory; `LocalImageStore.Build/Remove` same.
- [ ] Implement; remove `/preseed/{name}`, `preseedName`, `runScriptName`.
- [ ] Commit `feat(provision): serve autoinstall seed over NoCloud`.

### Task 5: base image, budgets, packaging, cleanup, docs

**Files:** `base.go`(+test), `internal/controller/frame/frameinstall_controller.go` (Preparing 30m, Installing 45m, commented as provisional until the recette), `Dockerfile.provisiond`, `charts/frame/values.yaml` (images ≥ 16Gi), `charts/frame/templates/provisiond-deployment.yaml` (comments), delete `preseed.go`/`preseed_test.go`, `api/frame/v1beta1/frameinstall_types.go` comments + `make manifests` (CRD copies in `config/crd/bases` and `charts/frame/files/crds`), `docs/provisioning.md`, `docs/deployment.md`, `docs/crd-reference.md`.

- [ ] `go build ./... && go vet ./... && go test ./internal/... ./api/... -count=1` green; `make manifests` diff limited to comments; commit.

### Task 6: GPU Operator release

**Files:** `deploy/scripts/acme-bootstrap.sh`, new `deploy/gpu/nvidiadriver-580.yaml`

- [ ] `driver.enabled=true`, `driver.nvidiaDriverCRD.enabled=true`, `driver.nvidiaDriverCRD.deployDefaultCR=false`; NVIDIADriver `version: 580.178.04`, nodeSelector gpu.present + `feature.node.kubernetes.io/system-os_release.VERSION_ID: "24.04"`. `helm template` renders; commit.

### Final

- [ ] Whole-branch audit by a fresh agent; fixes; delivery line in Acme's `docs/livraisons.md`.
