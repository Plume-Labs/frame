# Debian Provisioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A machine with a BMC becomes a `Ready` Kubernetes node, driven by Frame, without anyone standing in front of it.

**Architecture:** All installation logic lives in `internal/provision`, a package that does not import Kubernetes. Two thin consumers drive it: a `FrameInstall` controller for machines added to a cluster that exists, and a `frame bootstrap` command for node zero of a cluster that does not. A third component, `frame-provisiond`, builds per-machine installer images (it needs `xorriso`, which cannot live in the distroless manager image) and serves them to the BMC over HTTP.

**Tech Stack:** Go 1.26.1, controller-runtime, `golang.org/x/crypto/ssh` (already a direct dependency), `xorriso`, Debian 13.6.0 netinst, k3s, React 19 + Vite, vitest.

**Spec:** `docs/superpowers/specs/2026-09-11-provisioning-debian-design.md`

## Global Constraints

Every task's requirements implicitly include this section.

- **Base image:** `debian-13.6.0-amd64-netinst.iso`, SHA256 `65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7`, from `https://cdimage.debian.org/cdimage/release/13.6.0/amd64/iso-cd/debian-13.6.0-amd64-netinst.iso`. Verified 2026-09-11 against the published `SHA256SUMS`. The checksum is the integrity control: a downloaded file that does not match is deleted, not used.
- **`internal/provision` must not import Kubernetes.** No `k8s.io/...`, no `sigs.k8s.io/...`. It defines the interfaces its consumers implement. A test enforces this (Task 5).
- **No new Go module dependency.** `golang.org/x/crypto` is already direct (`go.mod:14`); `golang.org/x/crypto/ssh` is therefore free. Lot 1 declined `gofish` on the same rule.
- **No secret material ever enters an installer image.** The image carries an SSH *public* key. A test searches the built image for private-key material (Task 2).
- **`v1beta1` is frozen for existing kinds.** `FrameInstall` is a new kind, so it ships `v1beta1`-only with **no conversion webhook** — the `FrameTask` and `FrameMachine` precedent.
- **RBAC tiers are `admin`, `editor`, `viewer`, selected by the label `rbac.frame.plume-labs.io/tier`.** There is no "operator" tier. `FrameInstall` aggregates to `admin` and `viewer` only; the editor role file carries **no tier label**, with a comment saying why. Lot 1 shipped the opposite once and it was a one-request escalation.
- **`make helm-parity` exits at its first red section.** After fixing one gap, run it again — a second gap may have been hidden behind the first.
- **vitest runs `environment: 'node'` with `include: ['src/**/*.test.ts']`.** No `.tsx` file is ever executed by a test. All decisions therefore live in `src/lib/`.
- **CRD `maxLength` counts characters (runes), not bytes.**
- **Never commit secrets, credentials, or `.env` files. Never add a `Co-Authored-By` trailer.**
- **Every discriminating claim must be shown failing against broken code.** When a step says "verify it fails", run it against the unfixed tree and paste the failure. A test that cannot fail proves nothing.

## File Structure

**The Kubernetes-free package** — `internal/provision/`:

| File | Responsibility |
|---|---|
| `types.go` | `Spec`, `Network`, `Layout`, `Disk`, `ClusterTarget`, `Phase`, and the interfaces consumers implement (`BMC`, `ImageStore`, `SSHClient`, `NodeChecker`) |
| `preseed.go` | renders `preseed.cfg` from a `Spec` |
| `layout.go` | partman recipes for `single-disk` and `mirror`, disks named by `by-id` |
| `base.go` | fetches and checksum-verifies the base netinst, caching it |
| `iso.go` | remasters the base into a per-machine image |
| `ssh.go` | dial, wait-for-reachable, run, read file; pins the host key |
| `join.go` | k3s install, `--cluster-init` vs join, kubeconfig extraction and rewrite |
| `install.go` | the phase machine that drives all of the above |

**The controller** — `api/frame/v1beta1/frameinstall_types.go`, `internal/controller/frame/frameinstall_controller.go`, plus `config/rbac/frameinstall_{admin,editor,viewer}_role.yaml`.

**The image builder/server** — `cmd/provisiond/main.go`, `Dockerfile.provisiond`, `config/provisiond/{deployment,service}.yaml`.

**The command** — `cmd/bootstrap/main.go`.

**The screen** — `src/lib/installs.ts` (every decision, so vitest can reach it), `src/components/hardware/InstallsTab.tsx`, `src/components/hardware/InstallDetail.tsx`.

---

### Task 1: Preseed rendering and partman layouts

The text that tells the Debian installer what to do. Pure string generation, so it is the most testable part of the lot and the place to pin the safety properties.

**Files:**
- Create: `internal/provision/types.go`
- Create: `internal/provision/layout.go`
- Create: `internal/provision/preseed.go`
- Test: `internal/provision/preseed_test.go`
- Test: `internal/provision/layout_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: the types every later task uses.

```go
type Spec struct {
	UID          string  // proves this system came out of this image
	Hostname     string
	Network      Network
	Layout       Layout
	SSHPublicKey string  // an authorized_keys line; never a private key
	Cluster      ClusterTarget
}

type Network struct {
	Address string   // CIDR, e.g. "192.168.2.210/24"
	Gateway string
	DNS     []string
	VLAN    int
	Bond    string
}

type LayoutKind string

const (
	LayoutSingleDisk LayoutKind = "single-disk"
	LayoutMirror     LayoutKind = "mirror"
	LayoutRaw        LayoutKind = "raw"
)

type Layout struct {
	Kind  LayoutKind
	Disks []Disk
	Raw   string  // a partman recipe; only when Kind == LayoutRaw
}

type Disk struct {
	ByID      string  // "/dev/disk/by-id/..."
	SizeBytes int64   // asserted on the machine before partitioning
}

type ClusterMode string

const (
	ClusterInit ClusterMode = "init"
	ClusterJoin ClusterMode = "join"
)

type ClusterTarget struct {
	Mode       ClusterMode
	ServerURL  string  // only for ClusterJoin
	JoinToken  string  // only for ClusterJoin; never enters the image
	K3sVersion string
}

// RenderPreseed turns a Spec into a preseed.cfg.
func RenderPreseed(s Spec) (string, error)

// PartmanRecipe turns a Layout into the partman-auto stanzas for it.
func PartmanRecipe(l Layout) (string, error)
```

- [ ] **Step 1: Write the failing tests for layout validation**

```go
package provision

import "strings"
import "testing"

// mirror is exactly two disks. One is a typo that would silently install on a
// single disk with no redundancy; three is a typo that would consume a disk
// nobody meant to touch.
func TestPartmanRecipeMirrorRequiresExactlyTwoDisks(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		disks := make([]Disk, n)
		for i := range disks {
			disks[i] = Disk{ByID: "/dev/disk/by-id/scsi-x", SizeBytes: 300 << 30}
		}
		if _, err := PartmanRecipe(Layout{Kind: LayoutMirror, Disks: disks}); err == nil {
			t.Errorf("mirror with %d disks: want error, got nil", n)
		}
	}
}

func TestPartmanRecipeSingleDiskRequiresExactlyOneDisk(t *testing.T) {
	for _, n := range []int{0, 2} {
		disks := make([]Disk, n)
		for i := range disks {
			disks[i] = Disk{ByID: "/dev/disk/by-id/scsi-x", SizeBytes: 300 << 30}
		}
		if _, err := PartmanRecipe(Layout{Kind: LayoutSingleDisk, Disks: disks}); err == nil {
			t.Errorf("single-disk with %d disks: want error, got nil", n)
		}
	}
}

// The whole point of by-id. /dev/sda reorders between boots and between
// controllers; naming it would install on whatever happens to be first.
func TestPartmanRecipeRejectsKernelNames(t *testing.T) {
	l := Layout{Kind: LayoutSingleDisk, Disks: []Disk{{ByID: "/dev/sda", SizeBytes: 300 << 30}}}
	if _, err := PartmanRecipe(l); err == nil {
		t.Fatal("want error for /dev/sda, got nil")
	}
}

func TestPartmanRecipeNamesEveryDiskItWasGiven(t *testing.T) {
	l := Layout{Kind: LayoutMirror, Disks: []Disk{
		{ByID: "/dev/disk/by-id/scsi-aaa", SizeBytes: 300 << 30},
		{ByID: "/dev/disk/by-id/scsi-bbb", SizeBytes: 300 << 30},
	}}
	got, err := PartmanRecipe(l)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"scsi-aaa", "scsi-bbb", "partman-auto/method string raid"} {
		if !strings.Contains(got, want) {
			t.Errorf("recipe missing %q:\n%s", want, got)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run 'TestPartman' -v`
Expected: FAIL — the package does not compile, `undefined: PartmanRecipe`.

- [ ] **Step 3: Write `types.go` and `layout.go`**

`types.go` holds exactly the type declarations quoted in the Interfaces block above, with a package comment stating the Kubernetes-free rule:

```go
// Package provision installs an operating system on a machine and joins it to
// a cluster. It imports nothing from Kubernetes: its two consumers are a
// controller, which runs inside a cluster, and a command, which runs when no
// cluster exists yet. The interfaces below are what those consumers implement.
package provision
```

`layout.go`:

```go
package provision

import (
	"fmt"
	"strings"
)

// PartmanRecipe turns a Layout into partman-auto stanzas.
//
// Disks are named by /dev/disk/by-id. A kernel name like /dev/sda reorders
// between boots and between controllers, so naming one means installing on
// whatever happens to be first that day. On the ML350 Gen9 this is not
// hypothetical: IML entry 1832 warns that residual logical-volume metadata can
// hide disks from the host, which changes the ordering.
func PartmanRecipe(l Layout) (string, error) {
	if l.Kind == LayoutRaw {
		if strings.TrimSpace(l.Raw) == "" {
			return "", fmt.Errorf("layout raw: recipe is empty")
		}
		return l.Raw, nil
	}

	want := map[LayoutKind]int{LayoutSingleDisk: 1, LayoutMirror: 2}[l.Kind]
	if want == 0 {
		return "", fmt.Errorf("layout %q: unknown kind", l.Kind)
	}
	if len(l.Disks) != want {
		return "", fmt.Errorf("layout %s: needs exactly %d disks, got %d", l.Kind, want, len(l.Disks))
	}
	for _, d := range l.Disks {
		if !strings.HasPrefix(d.ByID, "/dev/disk/by-id/") {
			return "", fmt.Errorf("disk %q: must be a /dev/disk/by-id path", d.ByID)
		}
		if d.SizeBytes <= 0 {
			return "", fmt.Errorf("disk %q: size must be known, so the installer can refuse the wrong disk", d.ByID)
		}
	}

	names := make([]string, len(l.Disks))
	for i, d := range l.Disks {
		names[i] = d.ByID
	}
	disks := strings.Join(names, " ")

	var b strings.Builder
	fmt.Fprintf(&b, "d-i partman-auto/disk string %s\n", disks)
	if l.Kind == LayoutSingleDisk {
		b.WriteString("d-i partman-auto/method string regular\n")
	} else {
		b.WriteString("d-i partman-auto/method string raid\n")
		fmt.Fprintf(&b, "d-i partman-auto-raid/recipe string 1 2 0 ext4 / %s#%s .\n", names[0], names[1])
		b.WriteString("d-i partman-md/device_remove_md boolean true\n")
		b.WriteString("d-i partman-md/confirm boolean true\n")
		b.WriteString("d-i partman-md/confirm_nooverwrite boolean true\n")
	}
	b.WriteString("d-i partman-auto/choose_recipe select atomic\n")
	b.WriteString("d-i partman/confirm_write_new_label boolean true\n")
	b.WriteString("d-i partman/choose_partition select finish\n")
	b.WriteString("d-i partman/confirm boolean true\n")
	b.WriteString("d-i partman/confirm_nooverwrite boolean true\n")
	return b.String(), nil
}

// diskSizeAssertion is the last-ditch check, run on the machine itself before
// partman touches anything. Frame checked the serial remotely; this checks the
// disks locally. Both can be wrong, but not in the same way.
func diskSizeAssertion(l Layout) string {
	if l.Kind == LayoutRaw {
		return "true"
	}
	var checks []string
	for _, d := range l.Disks {
		checks = append(checks, fmt.Sprintf(
			"[ \"$(blockdev --getsize64 %s 2>/dev/null)\" = \"%d\" ]", d.ByID, d.SizeBytes))
	}
	return strings.Join(checks, " && ")
}
```

- [ ] **Step 4: Run the layout tests**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run 'TestPartman' -v`
Expected: PASS, four tests.

- [ ] **Step 5: Write the failing preseed tests**

```go
package provision

import (
	"strings"
	"testing"
)

func goodSpec() Spec {
	return Spec{
		UID:      "b3f1c2d4-0000-4000-8000-000000000001",
		Hostname: "g9",
		Network: Network{
			Address: "192.168.2.210/24",
			Gateway: "192.168.2.254",
			DNS:     []string{"192.168.2.254"},
		},
		Layout: Layout{Kind: LayoutMirror, Disks: []Disk{
			{ByID: "/dev/disk/by-id/scsi-aaa", SizeBytes: 300 << 30},
			{ByID: "/dev/disk/by-id/scsi-bbb", SizeBytes: 300 << 30},
		}},
		SSHPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILsytToxkJ2CiWuiv8BZ3hYpu7tFXn7Rwz+kc2gjbPSy frame-test-fixture",
		Cluster:      ClusterTarget{Mode: ClusterInit, K3sVersion: "v1.33.4+k3s1"},
	}
}

// The UID marker is what separates "this system came out of this image" from
// "something answers SSH at that address". Without it the installer's success
// signal is satisfied by the machine we believed we were overwriting and in
// fact never touched.
func TestRenderPreseedWritesTheUIDMarker(t *testing.T) {
	got, err := RenderPreseed(goodSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "/etc/frame-install-uid") {
		t.Error("preseed does not write the marker file")
	}
	if !strings.Contains(got, "b3f1c2d4-0000-4000-8000-000000000001") {
		t.Error("preseed does not carry the UID")
	}
}

func TestRenderPreseedCarriesTheStaticNetwork(t *testing.T) {
	got, err := RenderPreseed(goodSpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"netcfg/disable_autoconfig boolean true",
		"netcfg/get_ipaddress string 192.168.2.210",
		"netcfg/get_netmask string 255.255.255.0",
		"netcfg/get_gateway string 192.168.2.254",
		"netcfg/get_hostname string g9",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preseed missing %q", want)
		}
	}
}

// Decision 3 of the spec, made testable. The image is served over an
// unauthenticated HTTP path that any sandbox on the platform can reach.
func TestRenderPreseedRefusesAnythingThatLooksLikeAPrivateKey(t *testing.T) {
	s := goodSpec()
	s.SSHPublicKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNz\n-----END OPENSSH PRIVATE KEY-----"
	if _, err := RenderPreseed(s); err == nil {
		t.Fatal("want error for private key material, got nil")
	}
}

func TestRenderPreseedRefusesAKeyThatIsNotAnAuthorizedKeysLine(t *testing.T) {
	s := goodSpec()
	s.SSHPublicKey = "hunter2"
	if _, err := RenderPreseed(s); err == nil {
		t.Fatal("want error for a non-key, got nil")
	}
}

// The join token belongs to the cluster, not to the image. If it ever reaches
// RenderPreseed it must not come out the other side.
func TestRenderPreseedNeverEmitsTheJoinToken(t *testing.T) {
	s := goodSpec()
	s.Cluster = ClusterTarget{Mode: ClusterJoin, ServerURL: "https://192.168.2.201:6443", JoinToken: "K10SECRETTOKEN", K3sVersion: "v1.33.4+k3s1"}
	got, err := RenderPreseed(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "K10SECRETTOKEN") {
		t.Fatal("the join token is in the preseed")
	}
}

// The installer refuses on the machine if the named disk is not the size Frame
// was told it is.
func TestRenderPreseedAssertsDiskSizeBeforePartitioning(t *testing.T) {
	got, err := RenderPreseed(goodSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "blockdev --getsize64 /dev/disk/by-id/scsi-aaa") {
		t.Error("preseed does not assert the disk size")
	}
	if !strings.Contains(got, "preseed/early_command") {
		t.Error("the assertion must run before partman, in early_command")
	}
}

func TestRenderPreseedRejectsAnEmptyUID(t *testing.T) {
	s := goodSpec()
	s.UID = ""
	if _, err := RenderPreseed(s); err == nil {
		t.Fatal("want error for empty UID, got nil")
	}
}
```

- [ ] **Step 6: Run them to verify they fail**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run 'TestRenderPreseed' -v`
Expected: FAIL — `undefined: RenderPreseed`.

- [ ] **Step 7: Write `preseed.go`**

```go
package provision

import (
	"fmt"
	"net"
	"strings"
	"text/template"

	"golang.org/x/crypto/ssh"
)

// markerPath is where the installed system records which installation produced
// it. Frame reads it over SSH before believing the machine is its own.
const markerPath = "/etc/frame-install-uid"

const preseedTemplate = `# Generated by Frame. Do not edit on the machine.
d-i debian-installer/locale string en_US.UTF-8
d-i keyboard-configuration/xkb-keymap select us

d-i netcfg/choose_interface select auto
d-i netcfg/disable_autoconfig boolean true
d-i netcfg/get_ipaddress string {{ .IP }}
d-i netcfg/get_netmask string {{ .Netmask }}
d-i netcfg/get_gateway string {{ .Gateway }}
d-i netcfg/get_nameservers string {{ .DNS }}
d-i netcfg/confirm_static boolean true
d-i netcfg/get_hostname string {{ .Hostname }}
d-i netcfg/get_domain string local
d-i netcfg/hostname string {{ .Hostname }}

d-i mirror/country string manual
d-i mirror/http/hostname string deb.debian.org
d-i mirror/http/directory string /debian
d-i mirror/http/proxy string

d-i passwd/root-login boolean false
d-i passwd/user-fullname string Frame
d-i passwd/username string frame
d-i passwd/user-password-crypted password !
d-i user-setup/allow-password-weak boolean false
d-i user-setup/encrypt-home boolean false

d-i clock-setup/utc boolean true
d-i time/zone string Etc/UTC
d-i clock-setup/ntp boolean true

# Refuse before partman touches anything if a named disk is not the size Frame
# was told it is. Frame checked the machine's serial remotely; this checks its
# disks locally. Both can be wrong, but not in the same way.
d-i preseed/early_command string {{ .SizeAssertion }} || { echo "FRAME: named disk is not the expected size, refusing" >&2; exit 1; }

{{ .Partman }}
tasksel tasksel/first multiselect standard, ssh-server
d-i pkgsel/include string openssh-server sudo ca-certificates curl
d-i pkgsel/upgrade select none
popularity-contest popularity-contest/participate boolean false

d-i grub-installer/only_debian boolean true
d-i grub-installer/with_other_os boolean false
d-i finish-install/reboot_in_progress note

d-i preseed/late_command string \
  in-target mkdir -p /home/frame/.ssh ; \
  echo '{{ .SSHPublicKey }}' > /target/home/frame/.ssh/authorized_keys ; \
  in-target chown -R frame:frame /home/frame/.ssh ; \
  in-target chmod 700 /home/frame/.ssh ; \
  in-target chmod 600 /home/frame/.ssh/authorized_keys ; \
  echo 'frame ALL=(ALL) NOPASSWD:ALL' > /target/etc/sudoers.d/frame ; \
  in-target chmod 440 /etc/sudoers.d/frame ; \
  echo '{{ .UID }}' > /target{{ .MarkerPath }} ; \
  in-target chmod 444 {{ .MarkerPath }}
`

// RenderPreseed turns a Spec into a preseed.cfg.
//
// It refuses rather than emits when something is wrong, because the two ways
// this can go badly are silent: an image carrying secret material, and an
// installation that wipes a disk nobody named.
func RenderPreseed(s Spec) (string, error) {
	if strings.TrimSpace(s.UID) == "" {
		return "", fmt.Errorf("spec: UID is empty, so the installed system could not be told apart from any other machine at that address")
	}
	if strings.TrimSpace(s.Hostname) == "" {
		return "", fmt.Errorf("spec: hostname is empty")
	}
	if err := checkPublicKeyOnly(s.SSHPublicKey); err != nil {
		return "", err
	}

	ip, ipnet, err := net.ParseCIDR(s.Network.Address)
	if err != nil {
		return "", fmt.Errorf("network address %q: %w", s.Network.Address, err)
	}
	if net.ParseIP(s.Network.Gateway) == nil {
		return "", fmt.Errorf("network gateway %q is not an IP address", s.Network.Gateway)
	}

	partman, err := PartmanRecipe(s.Layout)
	if err != nil {
		return "", err
	}

	data := struct {
		IP, Netmask, Gateway, DNS, Hostname string
		SizeAssertion, Partman              string
		SSHPublicKey, UID, MarkerPath       string
	}{
		IP:            ip.String(),
		Netmask:       net.IP(ipnet.Mask).String(),
		Gateway:       s.Network.Gateway,
		DNS:           strings.Join(s.Network.DNS, " "),
		Hostname:      s.Hostname,
		SizeAssertion: diskSizeAssertion(s.Layout),
		Partman:       partman,
		SSHPublicKey:  strings.TrimSpace(s.SSHPublicKey),
		UID:           s.UID,
		MarkerPath:    markerPath,
	}

	var b strings.Builder
	if err := template.Must(template.New("preseed").Parse(preseedTemplate)).Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// checkPublicKeyOnly is the guard that keeps decision 3 true. The image is
// served over an unauthenticated HTTP path reachable by every notebook and
// sandbox on the platform, so anything secret in it is published.
func checkPublicKeyOnly(key string) error {
	if strings.Contains(key, "PRIVATE KEY") {
		return fmt.Errorf("ssh key: this is private key material, which must never enter an installer image")
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(key))); err != nil {
		return fmt.Errorf("ssh key: not a usable authorized_keys line: %w", err)
	}
	return nil
}
```

- [ ] **Step 8: Run the preseed tests**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -v`
Expected: PASS, eleven tests.

- [ ] **Step 9: Prove the private-key guard discriminates**

Comment out the `strings.Contains(key, "PRIVATE KEY")` branch in `checkPublicKeyOnly`, run the suite, and confirm `TestRenderPreseedRefusesAnythingThatLooksLikeAPrivateKey` turns red. Restore the branch. Paste the failure into the task report — a guard whose test cannot fail is decoration.

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run 'PrivateKey' -v`

- [ ] **Step 10: Commit**

```bash
cd /home/rmocq/frame-provision
git add internal/provision/
git commit -m "feat(provision): render a preseed that names its disks and carries no secret

Disks are named by /dev/disk/by-id, never by kernel name: /dev/sda reorders
between boots and between controllers, and on the ML350 Gen9 IML entry 1832
warns that residual volume metadata can hide disks from the host, which
changes the ordering. A mirror is exactly two disks and a single-disk layout
exactly one; any other count is a typo that would either install with no
redundancy or consume a disk nobody meant to touch.

The preseed asserts each named disk's size on the machine before partman runs.
Frame checks the machine's serial remotely; this checks its disks locally.

RenderPreseed refuses private key material outright. The image it feeds is
served over an unauthenticated HTTP path that every notebook and sandbox on
the platform can reach, so anything secret in it is published."
```

---

### Task 2: Fetch the base image, and remaster it

**Files:**
- Create: `internal/provision/base.go`
- Create: `internal/provision/iso.go`
- Test: `internal/provision/base_test.go`
- Test: `internal/provision/iso_test.go`

**Interfaces:**
- Consumes: `Spec`, `RenderPreseed` (Task 1).
- Produces:

```go
// FetchBase downloads the pinned netinst into dir, verifies its SHA256, and
// returns the path. A cached file that already matches is not re-downloaded.
func FetchBase(ctx context.Context, dir string, src BaseSource) (string, error)

type BaseSource struct {
	URL    string
	SHA256 string
}

// DefaultBase is the pinned image from the plan's Global Constraints.
func DefaultBase() BaseSource

// Remaster writes a per-machine installer image to out, built from baseISO.
func Remaster(ctx context.Context, baseISO string, s Spec, out string) error
```

- [ ] **Step 1: Write the failing checksum tests**

```go
package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func serveBytes(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// A netinst that does not match its published checksum is the supply-chain
// case this pin exists for. It must be deleted, not merely reported: a file
// left on disk is a file a later run treats as cached.
func TestFetchBaseDeletesAFileThatFailsItsChecksum(t *testing.T) {
	body := []byte("not the debian installer")
	srv := serveBytes(t, body)
	dir := t.TempDir()

	_, err := FetchBase(context.Background(), dir, BaseSource{URL: srv.URL + "/x.iso", SHA256: sum([]byte("something else"))})
	if err == nil {
		t.Fatal("want a checksum error, got nil")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("a file survived a failed checksum: %s", e.Name())
	}
}

func TestFetchBaseReusesAMatchingCachedFileWithoutDownloading(t *testing.T) {
	body := []byte("pretend netinst")
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	src := BaseSource{URL: srv.URL + "/x.iso", SHA256: sum(body)}

	for i := 0; i < 2; i++ {
		p, err := FetchBase(context.Background(), dir, src)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(p) != dir {
			t.Errorf("cached outside dir: %s", p)
		}
	}
	if hits != 1 {
		t.Errorf("downloads = %d, want 1", hits)
	}
}

// The value in Global Constraints, verified against the published SHA256SUMS
// on 2026-09-11. If this ever changes, the change is deliberate.
func TestDefaultBaseIsThePinnedDebianImage(t *testing.T) {
	b := DefaultBase()
	if b.SHA256 != "65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7" {
		t.Errorf("SHA256 = %s", b.SHA256)
	}
	if got := "debian-13.6.0-amd64-netinst.iso"; filepath.Base(b.URL) != got {
		t.Errorf("URL base = %s, want %s", filepath.Base(b.URL), got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run 'TestFetchBase|TestDefaultBase' -v`
Expected: FAIL — `undefined: FetchBase`.

- [ ] **Step 3: Write `base.go`**

```go
package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

type BaseSource struct {
	URL    string
	SHA256 string
}

// DefaultBase is the image this lot is built against. The checksum is the
// integrity control, verified against Debian's published SHA256SUMS on
// 2026-09-11; changing either field is a deliberate act.
func DefaultBase() BaseSource {
	return BaseSource{
		URL:    "https://cdimage.debian.org/cdimage/release/13.6.0/amd64/iso-cd/debian-13.6.0-amd64-netinst.iso",
		SHA256: "65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7",
	}
}

// FetchBase returns the path to a verified copy of src inside dir.
//
// A file that fails its checksum is removed rather than left in place: on the
// next run a leftover is indistinguishable from a cache hit, which turns one
// bad download into a permanently poisoned cache.
func FetchBase(ctx context.Context, dir string, src BaseSource) (string, error) {
	path := filepath.Join(dir, filepath.Base(src.URL))

	if got, err := fileSHA256(path); err == nil && got == src.SHA256 {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: HTTP %d", src.URL, resp.StatusCode)
	}

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("fetch %s: %w", src.URL, firstErr(copyErr, closeErr))
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != src.SHA256 {
		_ = os.Remove(path)
		return "", fmt.Errorf("fetch %s: sha256 %s, want %s", src.URL, got, src.SHA256)
	}
	return path, nil
}

// firstErr, not cmp: cmp is a standard library package name, and shadowing it
// in a file that may later want cmp.Or is a trap for whoever writes that line.
func firstErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
```

- [ ] **Step 4: Run the checksum tests**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run 'TestFetchBase|TestDefaultBase' -v`
Expected: PASS, three tests.

- [ ] **Step 5: Write the failing remaster tests**

These build a real ISO with `xorriso` — a tiny one, not the 700 MB Debian image — and read it back. `xorriso` must be installed; if it is not, the tests fail with a message saying so rather than skipping, because a skipped test on a machine that will later build real images is a hole.

```go
package provision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireXorriso(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("xorriso"); err != nil {
		t.Fatal("xorriso is not installed; this lot cannot build installer images without it (apt install xorriso)")
	}
}

// A stand-in for the netinst: an ISO carrying the two files Remaster rewrites.
func tinyBaseISO(t *testing.T) string {
	t.Helper()
	requireXorriso(t)
	src := t.TempDir()
	for path, body := range map[string]string{
		"isolinux/txt.cfg":  "default install\nlabel install\n  kernel /install.amd/vmlinuz\n  append vga=788 initrd=/install.amd/initrd.gz --- quiet\n",
		"boot/grub/grub.cfg": "menuentry --hotkey=i 'Install' {\n  linux /install.amd/vmlinuz vga=788 --- quiet\n  initrd /install.amd/initrd.gz\n}\n",
	} {
		full := filepath.Join(src, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "base.iso")
	cmd := exec.Command("xorriso", "-as", "mkisofs", "-o", out, "-V", "TESTBASE", src)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the test base ISO failed: %v\n%s", err, b)
	}
	return out
}

func isoContains(t *testing.T, iso, path string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("xorriso", "-osirrox", "on", "-indev", iso, "-extract", path, filepath.Join(dir, "out"))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("extracting %s failed: %v\n%s", path, err, b)
	}
	b, err := os.ReadFile(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRemasterPutsThePreseedOnTheImage(t *testing.T) {
	base := tinyBaseISO(t)
	out := filepath.Join(t.TempDir(), "g9.iso")
	if err := Remaster(context.Background(), base, goodSpec(), out); err != nil {
		t.Fatal(err)
	}
	got := isoContains(t, out, "/preseed.cfg")
	if !strings.Contains(got, "b3f1c2d4-0000-4000-8000-000000000001") {
		t.Error("the preseed on the image does not carry this installation's UID")
	}
}

// Virtual media offers no way to pass kernel arguments, so an image that does
// not carry them in its own boot configuration stops at a menu and waits for a
// keypress that will never come.
func TestRemasterMakesBothBootPathsUnattended(t *testing.T) {
	base := tinyBaseISO(t)
	out := filepath.Join(t.TempDir(), "g9.iso")
	if err := Remaster(context.Background(), base, goodSpec(), out); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/isolinux/txt.cfg", "/boot/grub/grub.cfg"} {
		got := isoContains(t, out, path)
		for _, want := range []string{"auto=true", "priority=critical", "file=/cdrom/preseed.cfg"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s is missing %q:\n%s", path, want, got)
			}
		}
	}
}

// The check that matters on the artifact is not that it boots -- it is that
// nothing secret is on it.
func TestRemasterProducesAnImageWithNoPrivateKeyMaterial(t *testing.T) {
	base := tinyBaseISO(t)
	out := filepath.Join(t.TempDir(), "g9.iso")
	if err := Remaster(context.Background(), base, goodSpec(), out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"PRIVATE KEY", "BEGIN OPENSSH"} {
		if strings.Contains(string(b), forbidden) {
			t.Errorf("the built image contains %q", forbidden)
		}
	}
}
```

- [ ] **Step 6: Run them to verify they fail**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run 'TestRemaster' -v`
Expected: FAIL — `undefined: Remaster`. If instead it fails with "xorriso is not installed", install it (`sudo apt install xorriso`) and re-run; the lot cannot be built without it.

- [ ] **Step 7: Write `iso.go`**

```go
package provision

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// bootArgs is what makes the installer unattended. Virtual media offers no way
// to pass kernel arguments -- that is the whole reason this file exists -- so
// they have to be written into the image's own boot configuration.
const bootArgs = "auto=true priority=critical file=/cdrom/preseed.cfg"

// Remaster writes a per-machine installer image built from baseISO.
//
// The image is built per machine rather than generic because no secret enters
// it, which removes any reason to fetch a configuration at install time and
// with it an entire MAC-based identification protocol. The image is the
// installation intent, readable in full.
func Remaster(ctx context.Context, baseISO string, s Spec, out string) error {
	preseed, err := RenderPreseed(s)
	if err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "frame-remaster-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	tree := filepath.Join(work, "tree")
	if err := run(ctx, "xorriso", "-osirrox", "on", "-indev", baseISO, "-extract", "/", tree); err != nil {
		return fmt.Errorf("unpack %s: %w", baseISO, err)
	}
	// xorriso extracts read-only; the tree has to be writable to rewrite it.
	if err := run(ctx, "chmod", "-R", "u+w", tree); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(tree, "preseed.cfg"), []byte(preseed), 0o644); err != nil {
		return err
	}

	for _, cfg := range []string{"isolinux/txt.cfg", "boot/grub/grub.cfg"} {
		if err := addBootArgs(filepath.Join(tree, cfg)); err != nil {
			return err
		}
	}

	return run(ctx, "xorriso", "-as", "mkisofs",
		"-o", out,
		"-V", "FRAME_INSTALL",
		"-r", "-J",
		"-isohybrid-mbr", filepath.Join(tree, "isolinux", "isohdpfx.bin"),
		"-c", "isolinux/boot.cat",
		"-b", "isolinux/isolinux.bin",
		"-no-emul-boot", "-boot-load-size", "4", "-boot-info-table",
		"-eltorito-alt-boot",
		"-e", "boot/grub/efi.img",
		"-no-emul-boot", "-isohybrid-gpt-basdat",
		tree)
}

var kernelLine = regexp.MustCompile(`(?m)^(\s*(?:append|linux)\s+.*)$`)

// addBootArgs appends the unattended arguments to every kernel line it finds.
// Both paths are rewritten because the machine's boot mode decides which one
// runs, and an image that is unattended in only one of them boots to a menu
// half the time.
func addBootArgs(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // an image that has only one of the two is still usable
		}
		return err
	}
	out := kernelLine.ReplaceAllStringFunc(string(b), func(line string) string {
		if strings.Contains(line, "auto=true") {
			return line
		}
		return line + " " + bootArgs
	})
	if out == string(b) {
		return fmt.Errorf("%s: no kernel line to make unattended; the image would boot to a menu and wait", path)
	}
	return os.WriteFile(path, []byte(out), 0o644)
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w\n%s", name, err, b)
	}
	return nil
}
```

- [ ] **Step 8: Run the remaster tests**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -v`
Expected: PASS. The tiny test ISO has no `isohdpfx.bin` or `efi.img`, so `Remaster` will fail on the real `xorriso` invocation. Make the boot-record arguments conditional on those files existing — a real netinst has them, the test fixture does not — and say so in a comment:

```go
	args := []string{"-as", "mkisofs", "-o", out, "-V", "FRAME_INSTALL", "-r", "-J"}
	// A real netinst carries both boot records. The test fixture carries
	// neither, and an image with no boot record is still worth building and
	// reading back -- what those tests assert is the content, not the boot.
	if _, err := os.Stat(filepath.Join(tree, "isolinux", "isohdpfx.bin")); err == nil {
		args = append(args,
			"-isohybrid-mbr", filepath.Join(tree, "isolinux", "isohdpfx.bin"),
			"-c", "isolinux/boot.cat", "-b", "isolinux/isolinux.bin",
			"-no-emul-boot", "-boot-load-size", "4", "-boot-info-table")
	}
	if _, err := os.Stat(filepath.Join(tree, "boot", "grub", "efi.img")); err == nil {
		args = append(args, "-eltorito-alt-boot", "-e", "boot/grub/efi.img",
			"-no-emul-boot", "-isohybrid-gpt-basdat")
	}
	args = append(args, tree)
	return run(ctx, "xorriso", args...)
```

- [ ] **Step 9: Prove the unattended rewrite discriminates**

Change `bootArgs` to the empty string, run `TestRemasterMakesBothBootPathsUnattended`, and confirm it turns red. Restore it. Paste the failure into the report.

- [ ] **Step 10: Commit**

```bash
cd /home/rmocq/frame-provision
git add internal/provision/
git commit -m "feat(provision): fetch the pinned netinst and remaster it per machine

The checksum is the integrity control, and a file that fails it is deleted
rather than left in place: on the next run a leftover is indistinguishable
from a cache hit, which turns one bad download into a poisoned cache.

Remaster writes the unattended boot arguments into both the BIOS and the UEFI
boot configuration. Virtual media offers no way to pass kernel arguments --
that is the reason this code exists at all -- and an image unattended in only
one of the two paths boots to a menu half the time, depending on a mode set
somewhere else entirely.

The test that matters on the artifact is not that it boots. It is that no
private key material is anywhere in it."
```

---

### Task 3: SSH — reach the installed system, and prove it is ours

**Files:**
- Create: `internal/provision/ssh.go`
- Test: `internal/provision/ssh_test.go`

**Interfaces:**
- Consumes: `markerPath` (Task 1).
- Produces:

```go
type SSHClient interface {
	// Dial opens a session to addr ("host:22") as user, with key.
	Dial(ctx context.Context, addr, user string, key []byte) (Session, error)
}

type Session interface {
	HostKey() string                                        // "ssh-ed25519 AAAA..." as seen on connect
	Run(ctx context.Context, cmd string) (string, error)     // combined output
	ReadFile(ctx context.Context, path string) ([]byte, error)
	Close() error
}

// NewSSHClient returns the real client, over golang.org/x/crypto/ssh.
func NewSSHClient() SSHClient

// WaitForOurSystem blocks until addr answers SSH, our key is accepted, and the
// marker file carries uid. It returns the host key it saw, for pinning.
func WaitForOurSystem(ctx context.Context, c SSHClient, addr, user string, key []byte, uid string, every time.Duration) (hostKey string, err error)
```

- [ ] **Step 1: Write the failing tests**

```go
package provision

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeSession struct {
	hostKey string
	marker  string
	closed  bool
}

func (f *fakeSession) HostKey() string                                  { return f.hostKey }
func (f *fakeSession) Run(context.Context, string) (string, error)      { return "", nil }
func (f *fakeSession) Close() error                                     { f.closed = true; return nil }
func (f *fakeSession) ReadFile(_ context.Context, path string) ([]byte, error) {
	if path != markerPath {
		return nil, errors.New("no such file")
	}
	return []byte(f.marker + "\n"), nil
}

type fakeSSH struct {
	attempts  int
	failUntil int
	session   Session // an interface, so a recordingSession keeps its own Run/ReadFile
}

func (f *fakeSSH) Dial(context.Context, string, string, []byte) (Session, error) {
	f.attempts++
	if f.attempts <= f.failUntil {
		return nil, errors.New("connection refused")
	}
	return f.session, nil
}

// The installer takes many minutes and refuses connections the whole time.
func TestWaitForOurSystemKeepsTryingUntilTheMachineAnswers(t *testing.T) {
	f := &fakeSSH{failUntil: 3, session: &fakeSession{hostKey: "ssh-ed25519 AAAAhost", marker: "the-uid"}}
	hk, err := WaitForOurSystem(context.Background(), f, "192.168.2.210:22", "frame", nil, "the-uid", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if f.attempts != 4 {
		t.Errorf("attempts = %d, want 4", f.attempts)
	}
	if hk != "ssh-ed25519 AAAAhost" {
		t.Errorf("host key = %q", hk)
	}
}

// THE discriminating test of this lot. Without the marker check, a machine
// that was never touched -- the one we believed we were overwriting -- answers
// SSH at that address and satisfies "installed".
func TestWaitForOurSystemRefusesAMachineThatIsNotOurs(t *testing.T) {
	f := &fakeSSH{session: &fakeSession{hostKey: "ssh-ed25519 AAAAhost", marker: "a-different-installation"}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := WaitForOurSystem(ctx, f, "192.168.2.210:22", "frame", nil, "the-uid", time.Millisecond); err == nil {
		t.Fatal("a machine carrying someone else's UID was accepted as ours")
	}
}

// A system with no marker at all is not ours either -- that is a pre-existing
// machine at the address, not a failed write of our own marker.
func TestWaitForOurSystemRefusesASystemWithNoMarker(t *testing.T) {
	f := &fakeSSH{session: &fakeSession{hostKey: "ssh-ed25519 AAAAhost", marker: ""}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := WaitForOurSystem(ctx, f, "192.168.2.210:22", "frame", nil, "the-uid", time.Millisecond); err == nil {
		t.Fatal("a system with no marker was accepted as ours")
	}
}

func TestWaitForOurSystemStopsWhenTheContextExpires(t *testing.T) {
	f := &fakeSSH{failUntil: 1 << 30, session: &fakeSession{}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := WaitForOurSystem(ctx, f, "a:22", "frame", nil, "u", time.Millisecond); err == nil {
		t.Fatal("want an error when the deadline passes")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("it kept trying past the deadline")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run 'TestWaitForOurSystem' -v`
Expected: FAIL — `undefined: WaitForOurSystem`.

- [ ] **Step 3: Write `ssh.go`**

The real client, over `golang.org/x/crypto/ssh` (already a direct dependency — do not add a module):

```go
// WaitForOurSystem blocks until the machine at addr is reachable over SSH,
// accepts our key, and carries this installation's UID in its marker file.
//
// The marker is what makes this a proof rather than a coincidence. "Something
// answers SSH at 192.168.2.210" is satisfied by any machine already at that
// address -- including the one we believed we were overwriting and in fact
// never touched. The UID existed nowhere but inside the image we built.
func WaitForOurSystem(ctx context.Context, c SSHClient, addr, user string, key []byte, uid string, every time.Duration) (string, error) {
	var last error
	for {
		select {
		case <-ctx.Done():
			if last == nil {
				last = ctx.Err()
			}
			return "", fmt.Errorf("waiting for %s to come up as our system: %w", addr, last)
		default:
		}

		sess, err := c.Dial(ctx, addr, user, key)
		if err != nil {
			last = err
		} else {
			hostKey := sess.HostKey()
			b, readErr := sess.ReadFile(ctx, markerPath)
			_ = sess.Close()
			switch {
			case readErr != nil:
				last = fmt.Errorf("%s has no %s: this is a system we did not install", addr, markerPath)
			case strings.TrimSpace(string(b)) != uid:
				last = fmt.Errorf("%s carries install UID %q, not %q: this is a different system at that address",
					strings.TrimSpace(string(b)), uid, addr)
			default:
				return hostKey, nil
			}
		}

		select {
		case <-ctx.Done():
		case <-time.After(every):
		}
	}
}
```

The `sshClient` implementation dials with `ssh.ClientConfig`, capturing the host key in a `HostKeyCallback` rather than verifying it (Frame cannot know it in advance — see the spec, §7), storing it on the session so `HostKey()` can return it for pinning. `ReadFile` runs `cat <path>`; `Run` returns combined output. `Timeout` on the config is set to `5 * time.Second` so an unreachable address fails fast instead of holding the phase.

- [ ] **Step 4: Run the fake-driven tests**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run 'TestWaitForOurSystem' -v`
Expected: PASS, four tests.

- [ ] **Step 5: Add one test against a real in-process SSH server**

The fakes prove the waiting logic. This proves the dialer captures a host key at all, which the fakes cannot reach:

```go
func TestNewSSHClientCapturesTheHostKeyItConnectedTo(t *testing.T) {
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	_, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPriv)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(hostSigner)

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		for ch := range chans {
			_ = ch.Reject(ssh.Prohibited, "no sessions needed for this test")
		}
		_ = sc.Close()
	}()

	pem, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := NewSSHClient().Dial(context.Background(), ln.Addr().String(), "frame", pem.Bytes)
	if err != nil {
		t.Fatalf("the handshake did not complete: %v", err)
	}
	defer func() { _ = sess.Close() }()

	want := string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))
	if strings.TrimSpace(sess.HostKey()) != strings.TrimSpace(want) {
		t.Errorf("HostKey() = %q, want %q", sess.HostKey(), want)
	}
}
```

It fails rather than skips if the handshake does not complete. A skipped test here is the one place a silent regression in the dialer could hide.

- [ ] **Step 6: Run the whole package**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -v`
Expected: PASS.

- [ ] **Step 7: Prove the marker check discriminates**

Delete the `strings.TrimSpace(string(b)) != uid` case from the switch, run the suite, and confirm both `TestWaitForOurSystemRefusesAMachineThatIsNotOurs` and `TestWaitForOurSystemRefusesASystemWithNoMarker` turn red. Restore it. Paste the failure into the report.

- [ ] **Step 8: Commit**

```bash
cd /home/rmocq/frame-provision
git add internal/provision/ssh.go internal/provision/ssh_test.go
git commit -m "feat(provision): wait for the machine to come up as our system, not just as a machine

'Something answers SSH at 192.168.2.210' is a coincidence, not a proof. It is
satisfied by any machine already at that address -- including the one we
believed we were overwriting and in fact never touched, which is the worst
outcome this lot can produce and the one hardest to notice.

So the phase ends on the marker file instead: the installed system carries the
UID of the installation that produced it, and that UID existed nowhere but
inside the image Frame built. A system with a different UID, or with none, is
refused.

Frame cannot know the host key in advance -- baking it into the image would
make the image a secret carrier, which the design forbids -- so the dialer
captures it for pinning rather than verifying it."
```

---

### Task 4: Join a cluster, or start one

**Files:**
- Create: `internal/provision/join.go`
- Test: `internal/provision/join_test.go`

**Interfaces:**
- Consumes: `Session` (Task 3), `ClusterTarget` (Task 1).
- Produces:

```go
// Join installs k3s on the session's machine. When Mode is ClusterInit it
// starts a new cluster and returns its kubeconfig, rewritten to point at
// nodeAddress. When Mode is ClusterJoin it joins the existing one and returns
// nil.
func Join(ctx context.Context, sess Session, t ClusterTarget, nodeAddress string) (kubeconfig []byte, err error)

// RewriteKubeconfigServer replaces the server address in a k3s kubeconfig.
func RewriteKubeconfigServer(in []byte, address string) ([]byte, error)
```

- [ ] **Step 1: Write the failing tests**

```go
package provision

import (
	"context"
	"strings"
	"testing"
)

type recordingSession struct {
	fakeSession
	cmds  []string
	files map[string]string
}

func (r *recordingSession) Run(_ context.Context, cmd string) (string, error) {
	r.cmds = append(r.cmds, cmd)
	return "", nil
}

func (r *recordingSession) ReadFile(_ context.Context, path string) ([]byte, error) {
	if b, ok := r.files[path]; ok {
		return []byte(b), nil
	}
	return nil, errNotFound
}

const k3sKubeconfig = `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: QQ==
    server: https://127.0.0.1:6443
  name: default
contexts:
- context: {cluster: default, user: default}
  name: default
current-context: default
kind: Config
users:
- name: default
  user: {client-certificate-data: QQ==, client-key-data: QQ==}
`

// Creating a cluster needs no token. That is the fact that makes the G9
// installable today, and it is the opposite of what one expects.
func TestJoinInitNeedsNoToken(t *testing.T) {
	s := &recordingSession{files: map[string]string{"/etc/rancher/k3s/k3s.yaml": k3sKubeconfig}}
	kc, err := Join(context.Background(), s, ClusterTarget{Mode: ClusterInit, K3sVersion: "v1.33.4+k3s1"}, "192.168.2.210")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(s.cmds, "\n")
	if !strings.Contains(joined, "--cluster-init") {
		t.Errorf("no --cluster-init in:\n%s", joined)
	}
	if !strings.Contains(joined, "INSTALL_K3S_VERSION=v1.33.4+k3s1") {
		t.Error("the k3s version is not pinned")
	}
	if !strings.Contains(string(kc), "https://192.168.2.210:6443") {
		t.Errorf("kubeconfig still points at localhost:\n%s", kc)
	}
}

// A cluster that exists and nobody can talk to is not a delivered cluster.
func TestJoinInitFailsLoudlyIfTheKubeconfigNeverAppears(t *testing.T) {
	s := &recordingSession{files: map[string]string{}}
	if _, err := Join(context.Background(), s, ClusterTarget{Mode: ClusterInit, K3sVersion: "v1.33.4+k3s1"}, "192.168.2.210"); err == nil {
		t.Fatal("want an error when k3s.yaml is not there, got nil")
	}
}

func TestJoinRequiresATokenAndAServerURL(t *testing.T) {
	for name, target := range map[string]ClusterTarget{
		"no token":  {Mode: ClusterJoin, ServerURL: "https://192.168.2.201:6443", K3sVersion: "v1.33.4+k3s1"},
		"no server": {Mode: ClusterJoin, JoinToken: "K10x", K3sVersion: "v1.33.4+k3s1"},
	} {
		s := &recordingSession{files: map[string]string{}}
		if _, err := Join(context.Background(), s, target, "192.168.2.210"); err == nil {
			t.Errorf("%s: want an error, got nil", name)
		}
	}
}

func TestJoinRefusesAnUnpinnedK3sVersion(t *testing.T) {
	s := &recordingSession{files: map[string]string{}}
	if _, err := Join(context.Background(), s, ClusterTarget{Mode: ClusterInit}, "192.168.2.210"); err == nil {
		t.Fatal("want an error for an empty k3s version, got nil")
	}
}

func TestRewriteKubeconfigServerReplacesOnlyTheServer(t *testing.T) {
	out, err := RewriteKubeconfigServer([]byte(k3sKubeconfig), "192.168.2.210")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "127.0.0.1") {
		t.Error("localhost survived the rewrite")
	}
	if !strings.Contains(string(out), "certificate-authority-data") {
		t.Error("the rewrite dropped the CA data")
	}
}
```

Declare `errNotFound = errors.New("no such file")` beside the fakes.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run 'TestJoin|TestRewrite' -v`
Expected: FAIL — `undefined: Join`.

- [ ] **Step 3: Write `join.go`**

Use the YAML package already in `go.mod` (`gopkg.in/yaml.v3`) — promote it from indirect to direct with `go mod tidy`, do not add a new module. The k3s installer is fetched from `https://get.k3s.io` at a pinned `INSTALL_K3S_VERSION`; that internet dependency is stated in the spec, §7, and must appear as a comment here too.

```go
// Join installs k3s and either starts a cluster or joins one.
//
// Starting a cluster is the simpler of the two, which is the opposite of what
// one expects: --cluster-init needs no token, while joining needs the one that
// lives at /var/lib/rancher/k3s/server/node-token on a server node, which a
// pod does not read. That asymmetry is why a machine with no cluster at all is
// installable today and a machine joining one needs an administrator first.
func Join(ctx context.Context, sess Session, t ClusterTarget, nodeAddress string) ([]byte, error) {
	if strings.TrimSpace(t.K3sVersion) == "" {
		return nil, fmt.Errorf("cluster: k3s version must be pinned; an unpinned install makes two machines built a week apart different machines")
	}

	switch t.Mode {
	case ClusterInit:
		cmd := fmt.Sprintf(
			"curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION=%s sh -s - server --cluster-init --node-ip %s",
			shellQuote(t.K3sVersion), shellQuote(nodeAddress))
		if _, err := sess.Run(ctx, cmd); err != nil {
			return nil, fmt.Errorf("k3s server --cluster-init: %w", err)
		}
		raw, err := sess.ReadFile(ctx, "/etc/rancher/k3s/k3s.yaml")
		if err != nil {
			return nil, fmt.Errorf("the cluster started but its kubeconfig is not readable, so nobody can talk to it: %w", err)
		}
		return RewriteKubeconfigServer(raw, nodeAddress)

	case ClusterJoin:
		if strings.TrimSpace(t.JoinToken) == "" {
			return nil, fmt.Errorf("cluster join: no token. It lives at /var/lib/rancher/k3s/server/node-token on a server node and must be placed in a Secret first")
		}
		if strings.TrimSpace(t.ServerURL) == "" {
			return nil, fmt.Errorf("cluster join: no server URL")
		}
		cmd := fmt.Sprintf(
			"curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION=%s K3S_URL=%s K3S_TOKEN=%s sh -s - agent --node-ip %s",
			shellQuote(t.K3sVersion), shellQuote(t.ServerURL), shellQuote(t.JoinToken), shellQuote(nodeAddress))
		if _, err := sess.Run(ctx, cmd); err != nil {
			return nil, fmt.Errorf("k3s agent: %w", err)
		}
		return nil, nil

	default:
		return nil, fmt.Errorf("cluster: unknown mode %q", t.Mode)
	}
}
```

`RewriteKubeconfigServer` unmarshals into `map[string]any`, walks `clusters[].cluster.server`, replaces each with `https://<address>:6443`, and marshals back. It must not rebuild the document from a typed struct: that silently drops fields it does not know, including the CA data, which is what the test above pins.

`shellQuote` wraps a value in single quotes and escapes embedded ones. The join token reaches a shell command line, so it is quoted rather than interpolated.

- [ ] **Step 4: Run the tests**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/rmocq/frame-provision
git add internal/provision/join.go internal/provision/join_test.go go.mod go.sum
git commit -m "feat(provision): start a cluster, or join one, over the session we already hold

Starting a cluster turns out to be the simpler of the two, which is the
opposite of what one expects. --cluster-init needs no token; joining needs the
one at /var/lib/rancher/k3s/server/node-token on a server node, which a pod
does not read, so joining requires an administrator to have placed it in a
Secret and starting requires nothing. That asymmetry is why the machine with
no cluster at all is the one installable today.

The kubeconfig is rewritten by walking the parsed document rather than
rebuilding it from a struct: a typed round-trip silently drops the fields it
does not know, the certificate authority data among them, and a cluster whose
kubeconfig has no CA is a cluster nobody can talk to."
```

---

### Task 5: The phase machine

The piece that decides what happens and in what order, and the one a broken version passes most easily.

**Files:**
- Create: `internal/provision/install.go`
- Modify: `internal/provision/types.go` (add `Phase`, `BMC`, `ImageStore`, `NodeChecker`)
- Test: `internal/provision/install_test.go`
- Test: `internal/provision/imports_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1-4.
- Produces:

```go
type Phase string

const (
	PhasePending       Phase = "Pending"
	PhasePreparing     Phase = "Preparing"
	PhaseMediaAttached Phase = "MediaAttached"
	PhaseInstalling    Phase = "Installing"
	PhaseInstalled     Phase = "Installed"
	PhaseJoining       Phase = "Joining"
	PhaseReady         Phase = "Ready"
	PhaseFailed        Phase = "Failed"
)

type BMC interface {
	Serial(ctx context.Context) (string, error)
	InsertMedia(ctx context.Context, url string) error
	MediaInserted(ctx context.Context) (bool, error)
	EjectMedia(ctx context.Context) error
	SetBootOnce(ctx context.Context, target, mode string) error // "Cd", "UEFI"|"Legacy"
	ClearBootOverride(ctx context.Context) error
	Reset(ctx context.Context, resetType string) error
}

type ImageStore interface {
	Build(ctx context.Context, s Spec) (url, token string, err error)
	Remove(ctx context.Context, token string) error
}

type NodeChecker interface {
	NodeReady(ctx context.Context, kubeconfig []byte, name string) (bool, error)
}

type Deps struct {
	BMC    BMC
	Images ImageStore
	SSH    SSHClient
	Nodes  NodeChecker
	Report func(Phase)  // called on entering each phase; may be nil
}

type Options struct {
	BootMode      string        // "UEFI" or "Legacy"; never inherited
	SSHUser       string        // "frame"
	SSHKey        []byte        // the private half; never enters an image
	NodeAddress   string        // the address the installed system answers on
	ConfirmSerial string        // must equal BMC.Serial()
	PhaseTimeout  map[Phase]time.Duration
	Poll          time.Duration
}

type Result struct {
	Phase       Phase
	FailedPhase Phase
	HostKey     string
	Kubeconfig  []byte
	NodeName    string
}

// Install runs the whole sequence. It never retries: replaying a destructive
// operation without a human asking is a fault.
func Install(ctx context.Context, d Deps, s Spec, o Options) (Result, error)
```

- [ ] **Step 1: Write the failing tests**

```go
package provision

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeBMC struct {
	serial      string
	calls       []string
	inserted    bool
	insertedLie bool // report not-inserted even after a successful insert
	failInsert  error
}

func (b *fakeBMC) Serial(context.Context) (string, error) { return b.serial, nil }
func (b *fakeBMC) InsertMedia(_ context.Context, url string) error {
	b.calls = append(b.calls, "insert:"+url)
	if b.failInsert != nil {
		return b.failInsert
	}
	b.inserted = true
	return nil
}
func (b *fakeBMC) MediaInserted(context.Context) (bool, error) {
	if b.insertedLie {
		return false, nil
	}
	return b.inserted, nil
}
func (b *fakeBMC) EjectMedia(context.Context) error {
	b.calls = append(b.calls, "eject")
	b.inserted = false
	return nil
}
func (b *fakeBMC) SetBootOnce(_ context.Context, target, mode string) error {
	b.calls = append(b.calls, "boot:"+target+":"+mode)
	return nil
}
func (b *fakeBMC) ClearBootOverride(context.Context) error {
	b.calls = append(b.calls, "clearboot")
	return nil
}
func (b *fakeBMC) Reset(_ context.Context, t string) error {
	b.calls = append(b.calls, "reset:"+t)
	return nil
}

type fakeImages struct {
	built, removed int
	failBuild      error
}

func (i *fakeImages) Build(context.Context, Spec) (string, string, error) {
	if i.failBuild != nil {
		return "", "", i.failBuild
	}
	i.built++
	return "http://provisiond/iso/tok.iso", "tok", nil
}
func (i *fakeImages) Remove(context.Context, string) error { i.removed++; return nil }

type fakeNodes struct{ ready bool }

func (n *fakeNodes) NodeReady(context.Context, []byte, string) (bool, error) { return n.ready, nil }

func happyDeps() (Deps, *fakeBMC, *fakeImages) {
	b := &fakeBMC{serial: "CZ3xxxxxxx"}
	i := &fakeImages{}
	sess := &recordingSession{files: map[string]string{
		markerPath:                     "b3f1c2d4-0000-4000-8000-000000000001",
		"/etc/rancher/k3s/k3s.yaml":    k3sKubeconfig,
	}}
	sess.hostKey = "ssh-ed25519 AAAAhost"
	return Deps{
		BMC:    b,
		Images: i,
		SSH:    &fakeSSH{session: sess},
		Nodes:  &fakeNodes{ready: true},
	}, b, i
}

func opts() Options {
	return Options{
		BootMode: "UEFI", SSHUser: "frame", NodeAddress: "192.168.2.210",
		ConfirmSerial: "CZ3xxxxxxx", Poll: time.Millisecond,
		PhaseTimeout: map[Phase]time.Duration{
			PhasePreparing: time.Second, PhaseMediaAttached: time.Second,
			PhaseInstalling: time.Second, PhaseJoining: time.Second, PhaseReady: time.Second,
		},
	}
}

// The serial is retyped by hand. This is what catches "I pointed at the wrong
// machine", and it must refuse before anything is built or attached.
func TestInstallRefusesWhenTheConfirmedSerialDoesNotMatch(t *testing.T) {
	d, b, i := happyDeps()
	o := opts()
	o.ConfirmSerial = "CZ3typo"
	res, err := Install(context.Background(), d, goodSpec(), o)
	if err == nil {
		t.Fatal("want a refusal, got nil")
	}
	if res.FailedPhase != PhasePending {
		t.Errorf("failed in %s, want Pending", res.FailedPhase)
	}
	if i.built != 0 {
		t.Error("an image was built for a machine whose serial did not match")
	}
	if len(b.calls) != 0 {
		t.Errorf("the BMC was touched: %v", b.calls)
	}
}

// InsertVirtualMedia returning 200 is not the same as media being attached.
// Booting a machine whose media is not there wastes twenty minutes and ends in
// a timeout that says nothing useful.
func TestInstallRereadsThatTheMediaIsActuallyAttached(t *testing.T) {
	d, b, _ := happyDeps()
	b.insertedLie = true
	res, err := Install(context.Background(), d, goodSpec(), opts())
	if err == nil {
		t.Fatal("want an error when the media did not attach, got nil")
	}
	if res.FailedPhase != PhaseMediaAttached {
		t.Errorf("failed in %s, want MediaAttached", res.FailedPhase)
	}
	for _, c := range b.calls {
		if c == "reset:ForceRestart" || c == "reset:On" {
			t.Error("the machine was booted with no media attached")
		}
	}
}

// A failed install that leaves the boot override and the media in place
// reboots into the installer forever.
func TestInstallEjectsAndClearsTheOverrideOnFailure(t *testing.T) {
	d, b, _ := happyDeps()
	d.Nodes = &fakeNodes{ready: false} // never becomes Ready
	if _, err := Install(context.Background(), d, goodSpec(), opts()); err == nil {
		t.Fatal("want a timeout error, got nil")
	}
	if !contains(b.calls, "eject") {
		t.Errorf("media was not ejected after failure: %v", b.calls)
	}
	if !contains(b.calls, "clearboot") {
		t.Errorf("boot override was not cleared after failure: %v", b.calls)
	}
}

func TestInstallEjectsAndClearsTheOverrideOnSuccess(t *testing.T) {
	d, b, i := happyDeps()
	res, err := Install(context.Background(), d, goodSpec(), opts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Phase != PhaseReady {
		t.Errorf("phase = %s, want Ready", res.Phase)
	}
	if !contains(b.calls, "eject") || !contains(b.calls, "clearboot") {
		t.Errorf("cleanup did not run on success: %v", b.calls)
	}
	if i.removed != 1 {
		t.Errorf("the built image was not removed (removed=%d)", i.removed)
	}
	if res.HostKey == "" {
		t.Error("the host key was not pinned")
	}
	if len(res.Kubeconfig) == 0 {
		t.Error("cluster-init produced no kubeconfig, so nobody can talk to the new cluster")
	}
}

// The boot mode is set explicitly. An image that boots in the other mode stays
// on a black screen and says nothing.
func TestInstallSetsTheBootModeExplicitly(t *testing.T) {
	d, b, _ := happyDeps()
	if _, err := Install(context.Background(), d, goodSpec(), opts()); err != nil {
		t.Fatal(err)
	}
	if !contains(b.calls, "boot:Cd:UEFI") {
		t.Errorf("boot mode was not set explicitly: %v", b.calls)
	}
}

// Replaying a destructive operation without a human asking is a fault.
func TestInstallNeverRetries(t *testing.T) {
	d, _, i := happyDeps()
	i.failBuild = errors.New("builder is down")
	if _, err := Install(context.Background(), d, goodSpec(), opts()); err == nil {
		t.Fatal("want an error, got nil")
	}
	if i.built != 0 {
		t.Errorf("builds = %d; a failed build must not be retried", i.built)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run 'TestInstall' -v`
Expected: FAIL — `undefined: Install`.

- [ ] **Step 3: Write `install.go`**

The sequence, with the cleanup on a `defer` so both exits take it:

```go
// Install runs one installation from end to end.
//
// It never retries. A failure leaves a machine in a state a human should look
// at, and replaying a destructive operation on their behalf is a fault; a
// retry is a new installation, created deliberately.
func Install(ctx context.Context, d Deps, s Spec, o Options) (Result, error) {
	res := Result{Phase: PhasePending}
	report := func(p Phase) {
		res.Phase = p
		if d.Report != nil {
			d.Report(p)
		}
	}
	fail := func(p Phase, err error) (Result, error) {
		res.FailedPhase, res.Phase = p, PhaseFailed
		return res, err
	}

	// Pending: the guard, before anything is built or attached.
	serial, err := d.BMC.Serial(ctx)
	if err != nil {
		return fail(PhasePending, fmt.Errorf("reading the machine's serial: %w", err))
	}
	if serial != o.ConfirmSerial {
		return fail(PhasePending, fmt.Errorf(
			"this machine reports serial %q and the request confirmed %q: refusing, because the cost of being wrong here is a wiped disk on the wrong machine",
			serial, o.ConfirmSerial))
	}

	// Preparing. Every phase gets its own deadline: the spec says each phase
	// carries one, and a build that hangs must not consume the install's whole
	// budget before the machine is ever touched.
	prepCtx, cancelPrep := context.WithTimeout(ctx, o.PhaseTimeout[PhasePreparing])
	defer cancelPrep()
	report(PhasePreparing)
	url, token, err := d.Images.Build(prepCtx, s)
	if err != nil {
		return fail(PhasePreparing, fmt.Errorf("building the installer image: %w", err))
	}

	// From here on the machine has been touched, so every exit cleans up.
	// A failed install that leaves the media attached and the boot override
	// set reboots into the installer forever.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		_ = d.BMC.EjectMedia(cleanup)
		_ = d.BMC.ClearBootOverride(cleanup)
		_ = d.Images.Remove(cleanup, token)
	}()

	// MediaAttached: insert, then re-read. A 200 from InsertVirtualMedia is
	// not the same as media being attached, and booting with none wastes
	// twenty minutes to reach a timeout that explains nothing.
	report(PhaseMediaAttached)
	mediaCtx, cancelMedia := context.WithTimeout(ctx, o.PhaseTimeout[PhaseMediaAttached])
	defer cancelMedia()
	if err := d.BMC.InsertMedia(mediaCtx, url); err != nil {
		return fail(PhaseMediaAttached, err)
	}
	if ok, err := d.BMC.MediaInserted(mediaCtx); err != nil || !ok {
		return fail(PhaseMediaAttached, fmt.Errorf("the BMC accepted the insert but reports no media attached"))
	}
	if err := d.BMC.SetBootOnce(mediaCtx, "Cd", o.BootMode); err != nil {
		return fail(PhaseMediaAttached, err)
	}
	if err := d.BMC.Reset(mediaCtx, "ForceRestart"); err != nil {
		return fail(PhaseMediaAttached, err)
	}

	// Installing: long and nearly blind. Redfish offers only PowerState and
	// PostState here, and lot 1 established this BMC replays cached sensor
	// readings as live ones in exactly this window, so nothing there is
	// trusted. The phase ends on the machine proving it is ours.
	report(PhaseInstalling)
	instCtx, cancel := context.WithTimeout(ctx, o.PhaseTimeout[PhaseInstalling])
	hostKey, err := WaitForOurSystem(instCtx, d.SSH, net.JoinHostPort(o.NodeAddress, "22"), o.SSHUser, o.SSHKey, s.UID, o.Poll)
	cancel()
	if err != nil {
		return fail(PhaseInstalling, err)
	}
	res.HostKey = hostKey
	report(PhaseInstalled)

	// Joining.
	report(PhaseJoining)
	sess, err := d.SSH.Dial(ctx, net.JoinHostPort(o.NodeAddress, "22"), o.SSHUser, o.SSHKey)
	if err != nil {
		return fail(PhaseJoining, err)
	}
	kubeconfig, err := Join(ctx, sess, s.Cluster, o.NodeAddress)
	_ = sess.Close()
	if err != nil {
		return fail(PhaseJoining, err)
	}
	res.Kubeconfig = kubeconfig

	// Ready, checked against the target cluster -- which for a new cluster is
	// not the one Frame is running in.
	readyCtx, cancel2 := context.WithTimeout(ctx, o.PhaseTimeout[PhaseReady])
	defer cancel2()
	res.NodeName = s.Hostname
	for {
		ok, err := d.Nodes.NodeReady(readyCtx, kubeconfig, s.Hostname)
		if err == nil && ok {
			report(PhaseReady)
			return res, nil
		}
		select {
		case <-readyCtx.Done():
			return fail(PhaseReady, fmt.Errorf("node %s never became Ready", s.Hostname))
		case <-time.After(o.Poll):
		}
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -v`
Expected: PASS.

- [ ] **Step 5: Add the test that keeps the package boundary honest**

`internal/provision/imports_test.go`:

```go
package provision

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The package has two consumers: a controller, which runs inside a cluster,
// and a command, which runs when no cluster exists. If Kubernetes gets in
// here, the command stops being buildable and the cold start stops being
// answerable -- and nothing else in the tree would turn red the day it
// happens.
func TestProvisionImportsNothingFromKubernetes(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			for _, imp := range f.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				if strings.HasPrefix(p, "k8s.io/") || strings.HasPrefix(p, "sigs.k8s.io/") ||
					strings.HasPrefix(p, "github.com/rmocq/frame/api/") ||
					strings.HasPrefix(p, "github.com/rmocq/frame/internal/controller") {
					t.Errorf("%s imports %s", name, p)
				}
			}
		}
	}
}
```

- [ ] **Step 6: Prove that test discriminates**

Add `_ "k8s.io/api/core/v1"` to `install.go`, run it, confirm red, remove it. Paste the failure into the report.

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run 'TestProvisionImports' -v`

- [ ] **Step 7: Commit**

```bash
cd /home/rmocq/frame-provision
git add internal/provision/
git commit -m "feat(provision): the phase machine, with cleanup on every exit

The guard runs before anything is built or attached: a machine whose serial
does not match the one retyped in the request is refused while it is still
untouched. The cost of being wrong at that point is a wiped disk on a machine
nobody meant to reinstall.

InsertVirtualMedia returning success is not the same as media being attached,
so it is re-read before the machine is booted. Booting with no media wastes
twenty minutes and ends at a timeout that explains nothing.

Cleanup is on a defer, so both exits take it. A failure that leaves the media
attached and the boot override set reboots into the installer forever, which
is worse than the failure it followed.

The Installing phase ends on the marker file, not on the BMC. Lot 1
established that this BMC replays cached sensor readings as live ones in
exactly that window, so PowerState and PostState are the only things it says
there and neither of them means 'the installer finished'.

A test parses the package's own imports and fails if Kubernetes appears.
Without it nothing turns red the day the boundary breaks, and the boundary is
the only reason the cold start has an answer."
```

---

### Task 6: The `FrameInstall` kind

**Files:**
- Create: `api/frame/v1beta1/frameinstall_types.go`
- Test: `internal/controller/frame/frameinstall_v1beta1_schema_test.go`
- Modify: `cmd/scheme_test.go` (register the new kind where the others are)

**Interfaces:**
- Consumes: nothing from earlier tasks; the CRD mirrors `provision.Spec` but is its own type. They are deliberately not the same type: the CRD is an API with compatibility obligations, `provision.Spec` is an argument.
- Produces: `FrameInstall`, `FrameInstallSpec`, `FrameInstallStatus` for Task 9.

Read `api/frame/v1beta1/framemachine_types.go` first and follow it exactly: the same copyright header, the same marker style, `v1beta1`-only, **no conversion webhook**, `shortName` `fi`, and printer columns for phase and machine.

- [ ] **Step 1: Write `frameinstall_types.go`**

```go
// FrameInstallSpec is one installation's intent. Every destructive input is
// named explicitly: there is no field here that means "figure it out".
type FrameInstallSpec struct {
	// MachineRef names the FrameMachine whose BMC drives this installation.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	MachineRef string `json:"machineRef"`

	// ConfirmSerial is the machine's serial, retyped by hand. The controller
	// refuses unless it equals what Redfish reports at
	// Systems/1.SerialNumber. A typo is a refusal, not an installation
	// somewhere else; this is what catches pointing at the wrong machine.
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:MinLength=1
	ConfirmSerial string `json:"confirmSerial"`

	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Hostname string `json:"hostname"`

	Network InstallNetwork `json:"network"`
	Layout  InstallLayout  `json:"layout"`
	Cluster InstallCluster `json:"cluster"`

	// BootMode is set on the machine rather than inherited. An image that
	// boots in the other mode stays on a black screen and reports nothing.
	// +kubebuilder:validation:Enum=UEFI;Legacy
	// +kubebuilder:default=UEFI
	BootMode string `json:"bootMode,omitempty"`

	// SSHKeyRef names a Secret holding "id" (private) and "id.pub" (public).
	// Only the public half ever reaches an installer image.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	SSHKeyRef string `json:"sshKeyRef"`
}

type InstallNetwork struct {
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:XValidation:rule="isCIDR(self)",message="address must be a CIDR, e.g. 192.168.2.210/24"
	Address string `json:"address"`
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="isIP(self)",message="gateway must be an IP address"
	Gateway string `json:"gateway"`
	// +kubebuilder:validation:MaxItems=3
	DNS []string `json:"dns,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=4094
	VLAN int32 `json:"vlan,omitempty"`
	// +kubebuilder:validation:MaxLength=15
	Bond string `json:"bond,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.kind != 'mirror' || size(self.disks) == 2",message="a mirror is exactly two disks"
// +kubebuilder:validation:XValidation:rule="self.kind != 'single-disk' || size(self.disks) == 1",message="single-disk is exactly one disk"
// +kubebuilder:validation:XValidation:rule="self.kind != 'raw' || has(self.raw)",message="layout raw needs a recipe"
type InstallLayout struct {
	// +kubebuilder:validation:Enum=single-disk;mirror;raw
	Kind string `json:"kind"`
	// +kubebuilder:validation:MaxItems=8
	Disks []InstallDisk `json:"disks,omitempty"`
	// +kubebuilder:validation:MaxLength=4096
	Raw string `json:"raw,omitempty"`
}

type InstallDisk struct {
	// ByID is a /dev/disk/by-id path. Kernel names reorder between boots and
	// between controllers; on the ML350 Gen9, IML entry 1832 warns that
	// residual volume metadata can hide disks from the host, which changes
	// that ordering. Naming /dev/sda means installing on whatever is first
	// that day.
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:XValidation:rule="self.startsWith('/dev/disk/by-id/')",message="disks must be named by /dev/disk/by-id, never by kernel name"
	ByID string `json:"byID"`
	// SizeBytes is asserted on the machine before partitioning.
	// +kubebuilder:validation:Minimum=1
	SizeBytes int64 `json:"sizeBytes"`
}

// +kubebuilder:validation:XValidation:rule="self.mode != 'join' || (has(self.serverURL) && has(self.joinTokenRef))",message="joining an existing cluster needs both serverURL and joinTokenRef; the token lives at /var/lib/rancher/k3s/server/node-token on a server node and must be placed in a Secret first"
type InstallCluster struct {
	// +kubebuilder:validation:Enum=init;join
	Mode string `json:"mode"`
	// +kubebuilder:validation:MaxLength=255
	ServerURL string `json:"serverURL,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	JoinTokenRef string `json:"joinTokenRef,omitempty"`
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:MinLength=1
	K3sVersion string `json:"k3sVersion"`
}

type FrameInstallStatus struct {
	// +kubebuilder:validation:Enum=Pending;Preparing;MediaAttached;Installing;Installed;Joining;Ready;Failed
	Phase string `json:"phase,omitempty"`
	PhaseSince *metav1.Time `json:"phaseSince,omitempty"`
	// FailedPhase names where it stopped, which is the first thing anyone
	// reading a failure needs.
	// +kubebuilder:validation:MaxLength=32
	FailedPhase string `json:"failedPhase,omitempty"`
	// +kubebuilder:validation:MaxLength=512
	Message string `json:"message,omitempty"`
	// HostKey is pinned on first contact. Frame cannot know it in advance:
	// baking it into the image would make the image a secret carrier.
	// +kubebuilder:validation:MaxLength=512
	HostKey string `json:"hostKey,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	NodeName string `json:"nodeName,omitempty"`
	// KubeconfigSecret names the Secret holding the new cluster's kubeconfig,
	// set only when this installation created a cluster.
	// +kubebuilder:validation:MaxLength=253
	KubeconfigSecret string `json:"kubeconfigSecret,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}
```

Markers on the root type: `+kubebuilder:object:root=true`, `+kubebuilder:subresource:status`, `+kubebuilder:resource:shortName=fi`, and printer columns `Machine` (`.spec.machineRef`), `Phase` (`.status.phase`), `Node` (`.status.nodeName`), `Age`.

- [ ] **Step 2: Generate and build**

Run: `cd /home/rmocq/frame-provision && make manifests generate && go build ./...`
Expected: a new `config/crd/bases/frame.plume-labs.io_frameinstalls.yaml`, and a clean build.

- [ ] **Step 3: Copy the CRD into the chart**

The chart carries its own copy at `charts/frame/files/crds/`. Copy the generated file there; `make helm-parity` compares them and will say so if they drift.

Run: `cp config/crd/bases/frame.plume-labs.io_frameinstalls.yaml charts/frame/files/crds/`

- [ ] **Step 4: Write the schema test**

Follow `internal/controller/frame/framemachine_v1beta1_schema_test.go`. **Use fixture names unique to this file** — lot 1 lost time to a fixture named `fm-ok` colliding with another test's object and failing with `AlreadyExists` depending on seed order. Prefix every object here `fi-schema-`.

One case written out, and the rest driven from a table in the same shape:

```go
func TestFrameInstallV1Beta1Schema(t *testing.T) {
	// ... envtest setup, as in framemachine_v1beta1_schema_test.go ...
	for name, tc := range map[string]struct {
		mutate  func(*framev1beta1.FrameInstall)
		wantErr string
	}{
		"mirror with one disk": {
			mutate:  func(fi *framev1beta1.FrameInstall) { fi.Spec.Layout.Disks = fi.Spec.Layout.Disks[:1] },
			wantErr: "a mirror is exactly two disks",
		},
		// ... one entry per row of the list below ...
	} {
		t.Run(name, func(t *testing.T) {
			fi := validFrameInstall("fi-schema-" + strings.ReplaceAll(name, " ", "-"))
			tc.mutate(fi)
			err := k8sClient.Create(ctx, fi)
			if err == nil {
				t.Fatalf("the apiserver accepted it")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}
```

`validFrameInstall(name)` returns a complete, accepted object; every case mutates one field of a fresh copy. The name is derived from the case so no two objects collide.

The apiserver must **reject**:

- a mirror with one disk, and a mirror with three
- a single-disk layout with two disks
- a disk named `/dev/sda`
- `mode: join` with no `joinTokenRef`
- `mode: join` with no `serverURL`
- an `address` that is not CIDR, and a `gateway` that is not an IP
- an empty `confirmSerial`
- a `bootMode` of `Bios`

and **accepts** a complete `mirror`/`init` object, and a complete
`single-disk`/`join` object.

- [ ] **Step 5: Run it**

Run: `cd /home/rmocq/frame-provision && make test ARGS='-run TestFrameInstallV1Beta1Schema'`
Expected: PASS. If envtest cannot start, run `make crd-render setup-envtest` first. **Never apply `bin/crd-render/`'s output to the cluster** — it switches CRD conversion to a webhook pointing at a service that does not exist.

- [ ] **Step 6: Prove one rule discriminates**

Delete the `mirror is exactly two disks` CEL rule, regenerate, and confirm that subtest turns red. Restore it. Paste the failure into the report — CEL rules that are never seen refusing anything are the easiest thing in this file to get subtly wrong.

- [ ] **Step 7: Commit**

```bash
cd /home/rmocq/frame-provision
git add api/ config/crd/ charts/frame/files/crds/ internal/controller/frame/frameinstall_v1beta1_schema_test.go cmd/scheme_test.go
git commit -m "feat(api): add FrameInstall, an installation as an object

A separate kind rather than a field on FrameMachine. That controller is a
polling loop rewriting sensor status every interval; an installation is a
twenty-minute sequence of independently failing phases, and sharing an object
makes the two contend on status writes. Lot 1 modelled power actions as
timestamps precisely because they are instantaneous, and an installation is
not. Making it an object also means a reinstallation is a second object and
the outcome of the first stays readable.

v1beta1-only with no conversion webhook, following FrameTask and FrameMachine.

The schema refuses what a typo would produce: a mirror that is not exactly two
disks, a disk named by kernel name rather than by-id, and a join with no token
reference. Each of those, accepted, is a wiped disk on a machine nobody meant
to touch."
```

---

### Task 7: RBAC, and the parity the chart needs

**Files:**
- Create: `config/rbac/frameinstall_admin_role.yaml`
- Create: `config/rbac/frameinstall_editor_role.yaml`
- Create: `config/rbac/frameinstall_viewer_role.yaml`
- Modify: `charts/frame/templates/_helpers.tpl` (the `frame.tierRoleCRDs` list)
- Modify: `config/rbac/kustomization.yaml`

**Interfaces:**
- Consumes: the `frameinstalls` resource name from Task 6.
- Produces: nothing code-level.

- [ ] **Step 1: Write the three role files**

Copy `config/rbac/framemachine_{admin,editor,viewer}_role.yaml` and change the resource. Then make the one change that matters:

- `frameinstall_admin_role.yaml` carries `rbac.frame.plume-labs.io/tier: admin`, and its verbs include `deletecollection` — without it, admin and editor are byte-identical on this kind and the tier means nothing.
- `frameinstall_viewer_role.yaml` carries `rbac.frame.plume-labs.io/tier: viewer`.
- `frameinstall_editor_role.yaml` carries **no tier label at all**, with this comment at the top:

```yaml
# This role deliberately carries no rbac.frame.plume-labs.io/tier label.
#
# Creating a FrameInstall wipes a machine's disks. Labelling this file would
# make frame-editor aggregate its create and patch verbs, which is a
# one-request promotion from "can edit workloads" to "can reinstall the
# cluster's hardware". Lot 1 shipped exactly that mistake on
# framemachine_editor_role.yaml -- it had tier: editor for one commit, and
# that was a one-request escalation.
#
# The precedent is frameuser_editor_role.yaml, unlabelled for the same reason.
```

- [ ] **Step 2: Add the chart entry**

In `charts/frame/templates/_helpers.tpl`, inside `frame.tierRoleCRDs`, in alphabetical position:

```yaml
- roleBase: frameinstall
  apiGroup: frame.plume-labs.io
  resource: frameinstalls
  aggregate: [admin, viewer]
```

`aggregate: [admin, viewer]` is what leaves the editor out. The helper's own comment says this list and `config/rbac/*_role.yaml` are two hand-maintained copies of one thing.

- [ ] **Step 3: Run parity, and run it again**

Run: `cd /home/rmocq/frame-provision && make helm-parity`
Expected: green.

**It exits at its first red section.** When it goes green on the first fix, run it a second time: in lot 1 the first fix uncovered two further gaps that had been hidden behind it. Record both runs in the report.

- [ ] **Step 4: Prove the editor is actually excluded**

Render the chart and confirm `frame-editor`'s aggregation rule does not select the FrameInstall editor role:

```bash
cd /home/rmocq/frame-provision
helm template charts/frame | grep -A5 'name: frameinstall-editor-role' | grep -c 'rbac.frame.plume-labs.io/tier' || echo "unlabelled, as intended"
```

Expected: `unlabelled, as intended`. Then add `rbac.frame.plume-labs.io/tier: editor` to the file, re-render, confirm the label appears, and remove it again. Paste both outputs into the report — this is the check that would have caught lot 1's escalation.

- [ ] **Step 5: Commit**

```bash
cd /home/rmocq/frame-provision
git add config/rbac/ charts/frame/templates/_helpers.tpl
git commit -m "feat(rbac): admin and viewer roles for FrameInstall, and deliberately not editor

Creating a FrameInstall wipes a machine's disks, so the editor tier does not
get it. frameinstall_editor_role.yaml therefore carries no tier label and says
why in a comment: labelling it would make frame-editor aggregate create and
patch, turning 'can edit workloads' into 'can reinstall the cluster's
hardware' in one request. Lot 1 shipped that exact mistake on
framemachine_editor_role.yaml and it lived for one commit.

admin gets deletecollection; without it admin and editor are byte-identical on
this kind and the tier distinction is decorative."
```

---

### Task 8: `frame-provisiond` — build the image, and serve it to the BMC

The manager image is `gcr.io/distroless/static:nonroot` (`Dockerfile.controller:20`), so `xorriso` cannot live in it. This component exists for that reason, and it also solves two other problems at once: where a 700 MB artifact is stored, and how the BMC — which is on the management network, not the pod network — fetches it.

**Files:**
- Create: `cmd/provisiond/main.go`
- Create: `internal/provision/server.go`
- Create: `Dockerfile.provisiond`
- Create: `config/provisiond/deployment.yaml`
- Create: `config/provisiond/service.yaml`
- Test: `internal/provision/server_test.go`
- Modify: `Makefile` (`docker-build-provisiond`, `docker-push-provisiond`, mirroring the uiproxy targets)

**Interfaces:**
- Consumes: `Remaster`, `FetchBase`, `Spec` (Tasks 1-2).
- Produces:

```go
// BuildHandler serves the build API. It must never be exposed outside the
// cluster.
func BuildHandler(dir string, base BaseSource) http.Handler

// MediaHandler serves built images read-only. This is the listener the BMC
// reaches, on the management network.
func MediaHandler(dir string) http.Handler

// HTTPImageStore is the ImageStore the controller uses, over the build API.
type HTTPImageStore struct {
	BuildURL string // the in-cluster build API
	MediaURL string // the base URL the BMC will fetch from
	Client   *http.Client
}

// LocalImageStore is the ImageStore `frame bootstrap` uses: it calls
// FetchBase and Remaster directly, because when no cluster exists there is
// nothing to host a build API.
type LocalImageStore struct {
	Dir      string
	Base     BaseSource
	MediaURL string // how the BMC reaches this machine, e.g. http://192.168.2.50:8081
}
```

**Two listeners, on purpose.** The build API accepts a `Spec` and writes a 700 MB file; it stays on a ClusterIP Service reachable only by the manager. The media listener is read-only, carries no secret by construction (Task 1's guard), and is the only thing a NodePort exposes to `192.168.2.0/24`. Collapsing them into one port would hand the LAN an endpoint that writes files.

- [ ] **Step 1: Write the failing tests**

```go
package provision

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The listener the BMC reaches must not be able to make anything.
func TestMediaHandlerRefusesEverythingButReadingAnImage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tok.iso"), []byte("ISO"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := MediaHandler(dir)

	for _, tc := range []struct{ method, path string; want int }{
		{http.MethodGet, "/iso/tok.iso", http.StatusOK},
		{http.MethodPost, "/build", http.StatusNotFound},
		{http.MethodPost, "/iso/tok.iso", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/iso/tok.iso", http.StatusMethodNotAllowed},
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
		if rr.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rr.Code, tc.want)
		}
	}
}

// Serving files by a name the caller supplies is how a media server becomes a
// way to read /etc/shadow.
func TestMediaHandlerRefusesPathsThatClimbOut(t *testing.T) {
	dir := t.TempDir()
	h := MediaHandler(dir)
	for _, p := range []string{"/iso/../../etc/passwd", "/iso/..%2f..%2fetc%2fpasswd", "/iso/sub/dir.iso"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		if rr.Code == http.StatusOK {
			t.Errorf("%s was served", p)
		}
	}
}

func TestBuildHandlerRejectsASpecItWouldRefuseToRender(t *testing.T) {
	h := BuildHandler(t.TempDir(), DefaultBase())
	body := `{"uid":"","hostname":"g9"}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/build", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rr.Code)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run 'TestMediaHandler|TestBuildHandler' -v`
Expected: FAIL — `undefined: MediaHandler`.

- [ ] **Step 3: Write `server.go`**

```go
var imageName = regexp.MustCompile(`^[a-f0-9]{32}\.iso$`)

// MediaHandler is the only listener a BMC reaches. It reads, and that is all
// it can do: the build API lives on a different port, on a Service nothing
// outside the cluster can reach.
func MediaHandler(dir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /iso/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		// Matched before it touches the filesystem. A name that cannot be an
		// image name is refused outright, rather than cleaned and hoped about.
		if !imageName.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, name))
	})
	return mux
}
```

`GET /iso/{name}` is a single-segment wildcard: `/iso/sub/dir.iso` does not match it, and other methods on the same path get 405 from `ServeMux` itself.

> Never register a bare trailing slash here. In `http.ServeMux` a trailing-slash pattern is a **subtree** match that absorbs every deeper path. Lot 1 lost a task to exactly that: a harness registered `/redfish/v1/`, every deeper path returned 200, and the 404 tolerance it claimed to prove could not fail.

> A trailing-slash pattern in `http.ServeMux` is a **subtree** match: `/iso/` absorbs every deeper path. Lot 1 lost a task to exactly this — a test harness registered `/redfish/v1/` and could never produce a 404, so the tolerance it claimed to prove was untested. Use `{name}` and `{$}`, never a bare trailing slash.

`BuildHandler` decodes a `Spec`, calls `RenderPreseed` first (so a bad spec is a 400 before anything is fetched or built), then `FetchBase` and `Remaster` into `dir/<token>.iso` where the token is 32 hex characters from `crypto/rand`, and answers `{"url": "...", "token": "..."}`. `DELETE /iso/{name}` removes it.

`HTTPImageStore.Build` posts the spec to `BuildURL` and returns `MediaURL + "/iso/" + token + ".iso"` — the URL the *BMC* will use, which is not the one the manager talked to.

- [ ] **Step 4: Run the tests**

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -v`
Expected: PASS.

- [ ] **Step 5: Write `Dockerfile.provisiond`**

Two stages: `golang:1.26` builds `cmd/provisiond`, then `debian:13-slim` with `xorriso` and `ca-certificates` installed, a non-root user, and the binary. Distroless is not an option here — the whole point of this component is that it runs `xorriso`.

- [ ] **Step 6: Write the manifests**

`config/provisiond/deployment.yaml`: one replica; an `emptyDir` with `sizeLimit: 8Gi` mounted at `/var/lib/frame/images`; both listeners on `8080` (build) and `8081` (media); a `readinessProbe` on the media port.

`config/provisiond/service.yaml`: **two** Services.

```yaml
# The build API. ClusterIP, reachable by the manager and nothing else.
apiVersion: v1
kind: Service
metadata:
  name: frame-provisiond
spec:
  type: ClusterIP
  ports: [{name: build, port: 8080, targetPort: 8080}]
---
# The media listener. This is the one the BMC reaches, so it is the one
# exposed on the machine network -- and it is read-only by construction,
# carrying no secret because no secret ever enters an installer image.
apiVersion: v1
kind: Service
metadata:
  name: frame-provisiond-media
spec:
  type: NodePort
  ports: [{name: media, port: 8081, targetPort: 8081, nodePort: 30581}]
```

- [ ] **Step 7: Build the image and confirm xorriso is in it**

```bash
cd /home/rmocq/frame-provision
podman build -f Dockerfile.provisiond -t frame-provisiond:dev .
podman run --rm frame-provisiond:dev xorriso -version
```

Expected: a version banner. **Podman, never Docker.**

- [ ] **Step 8: Commit**

```bash
cd /home/rmocq/frame-provision
git add cmd/provisiond/ internal/provision/server.go internal/provision/server_test.go Dockerfile.provisiond config/provisiond/ Makefile
git commit -m "feat(provisiond): build installer images, and serve them to the BMC

The manager image is distroless, so xorriso cannot live in it. This component
exists for that reason and solves two more problems on the way: where a 700 MB
artifact is stored, and how a BMC on the management network -- which cannot
reach the pod network -- fetches it.

Two listeners, on purpose. The build API writes 700 MB files and stays on a
ClusterIP nothing outside the cluster can reach. The media listener is
read-only, carries no secret by construction, and is the only one a NodePort
exposes to the machine network. One port serving both would hand the LAN an
endpoint that writes files.

Image names are matched against a pattern before they reach the filesystem, so
a name that climbs out of the directory is refused rather than cleaned. The
routes are exact rather than subtree: a bare trailing slash in http.ServeMux
absorbs every deeper path, which in lot 1 meant a test harness that could
never return 404 and a tolerance that was never actually proven."
```

---

### Task 9: The controller

**Files:**
- Create: `internal/controller/frame/frameinstall_controller.go`
- Create: `internal/provision/redfishbmc.go`
- Test: `internal/controller/frame/frameinstall_controller_test.go`
- Modify: `cmd/main.go` (register the reconciler beside the others)

**Interfaces:**
- Consumes: `provision.Install`, `provision.Deps`, `provision.Options`, `provision.Spec` (Task 5); `FrameInstall` (Task 6); `BuildRedfishClient` (`internal/controller/frame/redfish_client.go`).
- Produces: nothing further.

The controller is thin on purpose: it translates a CRD into a `provision.Spec`, supplies the four interfaces, runs the install in a goroutine keyed by object UID, and writes phases back to status. All judgment lives in `internal/provision`.

`internal/provision/redfishbmc.go` adapts lot 1's `redfish.Client` to `provision.BMC`. It lives in `internal/provision`, **not** beside the controller: Task 10's command cannot import a package that imports Kubernetes, and `internal/redfish` is already Kubernetes-free, so this is its only possible home. The Task 5 import test covers this file. Lot 1's client has `Reset`, `ClearLog`, `SetIndicatorLED` and `Probe`; the virtual-media and boot-override calls are new and go here, using the HP OEM actions:

```go
// iLO 4 exposes virtual media under Oem.Hp.Actions, not the standard
// VirtualMedia actions. VM2 is the CD/DVD device; VM1 is Floppy/USBStick.
//   POST /redfish/v1/Managers/1/VirtualMedia/2/
//        Action: #HpiLOVirtualMedia.InsertVirtualMedia, Image: <url>
//
// GracefulShutdown is NOT in this machine's
// ResetType@Redfish.AllowableValues -- lot 1 already resolves that to
// PushPowerButton in internal/redfish/client.go. Reset("ForceRestart") is
// used here deliberately: the machine being installed has nothing to lose.
```

- [ ] **Step 1: Write the failing tests**

Use the fake-client pattern from `framemachine_controller_test.go` and `framemachine_fake_test.go`. Prefix every fixture `fi-ctrl-` — unique to this file. Assert:

- A `FrameInstall` whose `machineRef` names no `FrameMachine` goes `Failed` with a message naming the missing machine, and **never touches a BMC**.
- A `FrameInstall` whose `hostname` matches an existing Node that is `Ready` is refused in phase `Pending`, with a message saying the node must leave the cluster first. Same for one whose `network.address` matches a Ready node's address. This is the spec's third guard layer, and it is the one that stops someone reinstalling a machine that is currently carrying workloads:

```go
func TestFrameInstallRefusesToReinstallALiveClusterMember(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "fi-ctrl-live"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.168.2.210"}},
		},
	}
	// ... build the reconciler with node in the fake client, and a
	// FrameInstall whose hostname is "fi-ctrl-live".
	// Assert: status.phase == "Failed", status.failedPhase == "Pending",
	// the message mentions removing it from the cluster, and the fake BMC
	// recorded no calls at all.
}
```

Write that test out in full, following the fixture style of `framemachine_controller_test.go`. Then add the same shape for the address match.
- Creating a `FrameInstall`, and reaching a terminal phase, each append a `FrameTask` audit entry — the kind lot 0a introduced. The action string is bounded at 200 runes, which is `FrameTaskSpec.Action`'s CRD cap. `boundAction` lives in `internal/uiproxy` and is unexported, so it is not reachable here — use `truncateString` from `framemachine_controller.go:233`, which is in this package and already counts runes rather than bytes. Assert the entry exists after a successful run and after a failed one, and that its action names the machine.
- A `FrameInstall` whose `confirmSerial` does not match goes `Failed` in phase `Pending`, and no image is built.
- A successful run writes `status.phase: Ready`, `status.nodeName`, `status.hostKey`, and — for `mode: init` — `status.kubeconfigSecret`, with the Secret actually created.
- The Secret holding the new cluster's kubeconfig is created in the operator's namespace with `type: Opaque` and a single `kubeconfig` key.
- Deleting an object mid-install runs the finalizer, and the finalizer ejects the media.
- A second reconcile of an object already in a terminal phase does **not** start a second install.

That last one matters: without it, every resync re-runs a destructive operation.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd /home/rmocq/frame-provision && make test ARGS='-run TestFrameInstallReconciler'`
Expected: FAIL — the reconciler does not exist.

- [ ] **Step 3: Write the controller**

RBAC markers it needs:

```go
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameinstalls,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameinstalls/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameinstalls/finalizers,verbs=update
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framemachines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
```

Structure:

```go
func (r *FrameInstallReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fi framev1beta1.FrameInstall
	if err := r.Get(ctx, req.NamespacedName, &fi); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !fi.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &fi)
	}

	// An installation runs once. Without this, every resync replays a
	// destructive operation.
	if fi.Status.Phase == string(provision.PhaseReady) || fi.Status.Phase == string(provision.PhaseFailed) {
		return ctrl.Result{}, nil
	}
	if r.running(fi.UID) {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	...
}
```

The running install writes each phase through `Deps.Report`, which patches `status.phase` and `status.phaseSince`. Errors are truncated with the existing `truncateError` helper in `framemachine_controller.go` to the 512-rune status cap — **runes, not bytes**.

Register in `cmd/main.go` beside `FrameMachineReconciler`.

**Do not write `FrameMachine.spec.nodeRef`.** It lives on `spec`, which makes it a fact an operator declares rather than one a controller writes. A controller that edits another object's spec fights the person who wrote it. The screen may offer to fill it — that is a human action — but this reconciler leaves it alone.

- [ ] **Step 4: Run the tests**

Run: `cd /home/rmocq/frame-provision && make test ARGS='-run TestFrameInstallReconciler'`
Expected: PASS.

- [ ] **Step 5: Prove the run-once guard discriminates**

Remove the terminal-phase early return, run the "second reconcile does not start a second install" test, confirm it turns red, restore it. Paste the failure into the report. This guard is the difference between one installation and one installation every resync interval.

- [ ] **Step 6: Commit**

```bash
cd /home/rmocq/frame-provision
git add internal/controller/frame/frameinstall_controller.go internal/provision/redfishbmc.go internal/controller/frame/frameinstall_controller_test.go cmd/main.go config/rbac/role.yaml
git commit -m "feat(controller): drive an installation from a FrameInstall

The controller is thin on purpose. It turns a CRD into a provision.Spec,
supplies the four interfaces the package declares, and writes phases back;
every judgment stays in internal/provision, where the command can reach it
too.

An object in a terminal phase is not reconciled again. Without that, every
resync replays a destructive operation -- the resync interval would become the
reinstall interval.

iLO 4 exposes virtual media under Oem.Hp.Actions rather than the standard
VirtualMedia actions, and VM2 is the CD device. ForceRestart is used
deliberately rather than a graceful shutdown: this machine's whole purpose at
that moment is to be overwritten, and lot 1 already established that this
firmware does not list GracefulShutdown among its allowable reset types."
```

---

### Task 10: `frame bootstrap` — node zero, when there is no cluster

The second consumer, and the reason the package boundary is a requirement rather than a preference. It is also the recovery path: on a single-node cluster, the day that cluster dies is a day that exists.

**Files:**
- Create: `cmd/bootstrap/main.go`
- Create: `cmd/bootstrap/config.go`
- Test: `cmd/bootstrap/config_test.go`
- Modify: `Makefile` (a `bootstrap` target building `bin/frame-bootstrap`)

**Interfaces:**
- Consumes: `provision.Install`, `provision.RedfishBMC`, `provision.LocalImageStore`, `provision.Spec` (Tasks 1-9).
- Produces: nothing further.

```go
// Config is the flat file that stands in for a FrameInstall when there is no
// apiserver to hold one.
type Config struct {
	BMC struct {
		Address, Username, Password string
		InsecureSkipVerify          bool
	}
	ConfirmSerial string
	Hostname      string
	Network       provision.Network
	Layout        provision.Layout
	Cluster       provision.ClusterTarget
	BootMode      string
	SSHKeyPath    string // the private key; its .pub is what enters the image
	MediaBaseURL  string // how the BMC reaches this laptop, e.g. http://192.168.2.50:8081
	Out           string // where to write the new cluster's kubeconfig
}

func LoadConfig(path string) (Config, error)
```

- [ ] **Step 1: Write the failing config tests**

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The laptop's address is not discoverable from inside this process, and a
// wrong one produces a twenty-minute silence ending in a timeout. Refuse
// early instead.
func TestLoadConfigRequiresAMediaBaseURLTheBMCCanReach(t *testing.T) {
	for _, url := range []string{"", "http://localhost:8081", "http://127.0.0.1:8081"} {
		p := writeConfig(t, validConfig(func(c *Config) { c.MediaBaseURL = url }))
		if _, err := LoadConfig(p); err == nil {
			t.Errorf("MediaBaseURL %q was accepted; a BMC cannot reach it", url)
		}
	}
}

func TestLoadConfigRequiresTheConfirmedSerial(t *testing.T) {
	p := writeConfig(t, validConfig(func(c *Config) { c.ConfirmSerial = "" }))
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("a config with no confirmed serial was accepted")
	}
}

// A password in a config file is a password on a laptop's disk. It is allowed
// -- there is nowhere else to put it when no cluster exists -- but the file
// must not be world-readable.
func TestLoadConfigRefusesAWorldReadableFile(t *testing.T) {
	p := writeConfig(t, validConfig(nil))
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("a world-readable config carrying a BMC password was accepted")
	}
}

func TestLoadConfigReadsThePublicHalfOfTheKey(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	// write a valid ed25519 private key and its .pub beside it
	cfg := validConfig(func(c *Config) { c.SSHKeyPath = key })
	got, err := LoadConfig(writeConfig(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if got.SSHKeyPath != key {
		t.Errorf("key path = %s", got.SSHKeyPath)
	}
}

func TestLoadConfigFailsWhenThePublicHalfIsMissing(t *testing.T) {
	cfg := validConfig(func(c *Config) { c.SSHKeyPath = filepath.Join(t.TempDir(), "absent") })
	if _, err := LoadConfig(writeConfig(t, cfg)); err == nil {
		t.Fatal("a config naming a key with no .pub was accepted")
	}
}
```

Write `validConfig` and `writeConfig` helpers alongside; `writeConfig` marshals to YAML and chmods `0600`.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd /home/rmocq/frame-provision && go test ./cmd/bootstrap/ -v`
Expected: FAIL — `undefined: LoadConfig`.

- [ ] **Step 3: Write `config.go` and `main.go`**

`main.go`:

1. Checks `xorriso` is on `PATH` and exits with `frame bootstrap needs xorriso (apt install xorriso)` if not. A missing tool must fail in the first second, not twenty minutes in.
2. Loads the config.
3. Serves `provision.MediaHandler(dir)` on `:8081`, and uses a `LocalImageStore` that calls `FetchBase` and `Remaster` directly — no build API, because there is no cluster to host one.
4. Builds a `provision.BMC` from `internal/redfish` via `provision.RedfishBMC`.
5. Supplies a `NodeChecker` built on `client-go` against the kubeconfig the install produced. **This is in `cmd/bootstrap`, not in `internal/provision`** — the command may import Kubernetes; the package may not.
6. Calls `provision.Install` and prints each phase as it arrives.
7. Writes the kubeconfig to `Out` with mode `0600`.

- [ ] **Step 4: Confirm the Redfish adapter is reachable from here**

`provision.RedfishBMC` was created in Task 9, in `internal/provision/redfishbmc.go`. This command uses it directly. Confirm it carries no Kubernetes import, which is the only reason this command can exist:

Run: `cd /home/rmocq/frame-provision && go test ./internal/provision/ -run TestProvisionImports -v`

- [ ] **Step 5: Build it**

Run: `cd /home/rmocq/frame-provision && go build -o bin/frame-bootstrap ./cmd/bootstrap && ./bin/frame-bootstrap -help`
Expected: usage text, and an exit that names `xorriso` if it is absent.

- [ ] **Step 6: Commit**

```bash
cd /home/rmocq/frame-provision
git add cmd/bootstrap/ internal/provision/redfishbmc.go Makefile
git commit -m "feat(bootstrap): install node zero, when there is no cluster to run an operator in

An operator inside the cluster it is creating cannot create it. No tool solves
that -- kubeadm, talosctl and k3sup all have a binary run from elsewhere for
node zero, after which the tool moves into the cluster it just made. This is
that binary.

It is also the second consumer that keeps the package boundary honest. A
package with one consumer drifts, and nothing turns red the day Kubernetes
gets into it; with two, the command stops building.

And it is the recovery path. On a single-node cluster the day that cluster
dies is a day that exists.

xorriso is checked in the first second rather than twenty minutes in, and a
media base URL of localhost is refused outright: a BMC cannot reach it, and
the failure it produces is a long silence rather than an error."
```

---

### Task 11: The screen

**Files:**
- Create: `src/lib/installs.ts`
- Test: `src/lib/installs.test.ts`
- Create: `src/components/hardware/InstallsTab.tsx`
- Create: `src/components/hardware/InstallDialog.tsx`
- Modify: `src/components/hardware/HardwareView.tsx` (a second tab)
- Modify: `src/App.tsx:238` (add the tab to the `hardware` section)
- Modify: `src/lib/frame-sdk.ts` (an `InstallClient`, mirroring `MachineClient`)

**Interfaces:**
- Consumes: the `FrameInstall` shape from Task 6.
- Produces:

```ts
export type InstallPhase =
  | 'Pending' | 'Preparing' | 'MediaAttached' | 'Installing'
  | 'Installed' | 'Joining' | 'Ready' | 'Failed'

export const PHASE_ORDER: InstallPhase[]
export function phaseIndex(p: InstallPhase): number
export function isTerminal(p: InstallPhase): boolean
export function phaseTone(p: InstallPhase): 'pending' | 'running' | 'good' | 'bad'
export function elapsedLabel(since: string | undefined, now: Date): string
export function isStalledInPhase(p: InstallPhase, since: string | undefined, now: Date): boolean
export function confirmationMatches(typed: string, serial: string): boolean
export function canCreateInstall(tier: string): boolean
```

**vitest runs `environment: 'node'` with `include: ['src/**/*.test.ts']`, so no `.tsx` file is ever executed by a test.** Every decision therefore lives in `installs.ts` and the components only render its output. That is the same split lot 1 used for `src/lib/machines.ts`.

- [ ] **Step 1: Write the failing tests**

```ts
import { describe, expect, it } from 'vitest'
import {
  canCreateInstall, confirmationMatches, elapsedLabel, isStalledInPhase,
  isTerminal, phaseIndex, phaseTone,
} from './installs'

describe('phases', () => {
  it('orders every phase the controller can write', () => {
    expect(phaseIndex('Pending')).toBe(0)
    expect(phaseIndex('Ready')).toBeGreaterThan(phaseIndex('Joining'))
  })

  it('treats Failed as terminal but not as progress', () => {
    expect(isTerminal('Failed')).toBe(true)
    expect(isTerminal('Ready')).toBe(true)
    expect(isTerminal('Installing')).toBe(false)
    expect(phaseTone('Failed')).toBe('bad')
  })
})

describe('elapsed time', () => {
  // Lot 1 shipped a freshness marker computed with `new Date()` at render
  // time, under a watch with no poll: it froze exactly when the operator
  // stopped writing, which is exactly when it mattered. `now` is a parameter
  // here so a test can move it and a component must pass a ticking one.
  it('grows as now moves, with the timestamp fixed', () => {
    const since = '2026-09-11T10:00:00Z'
    expect(elapsedLabel(since, new Date('2026-09-11T10:00:30Z'))).toBe('30 s')
    expect(elapsedLabel(since, new Date('2026-09-11T10:04:00Z'))).toBe('4 min')
    expect(elapsedLabel(since, new Date('2026-09-11T11:30:00Z'))).toBe('1 h 30')
  })

  it('says so rather than guessing when there is no timestamp', () => {
    expect(elapsedLabel(undefined, new Date())).toBe('—')
  })

  // Installing legitimately takes twenty minutes; Preparing does not.
  it('calls a phase stalled against that phase, not against one number', () => {
    const since = '2026-09-11T10:00:00Z'
    const at = (m: number) => new Date(Date.parse(since) + m * 60_000)
    expect(isStalledInPhase('Preparing', since, at(9))).toBe(true)
    expect(isStalledInPhase('Installing', since, at(9))).toBe(false)
    expect(isStalledInPhase('Installing', since, at(65))).toBe(true)
  })

  it('never calls a terminal phase stalled', () => {
    expect(isStalledInPhase('Ready', '2020-01-01T00:00:00Z', new Date())).toBe(false)
    expect(isStalledInPhase('Failed', '2020-01-01T00:00:00Z', new Date())).toBe(false)
  })
})

describe('the confirmation', () => {
  // Typing the serial is the guard. A comparison that ignores case or
  // whitespace is a guard that a paste of the wrong serial also passes.
  it('requires the serial exactly', () => {
    expect(confirmationMatches('CZ3xxxxxxx', 'CZ3xxxxxxx')).toBe(true)
    expect(confirmationMatches(' CZ3xxxxxxx ', 'CZ3xxxxxxx')).toBe(true)
    expect(confirmationMatches('cz3xxxxxxx', 'CZ3xxxxxxx')).toBe(false)
    expect(confirmationMatches('', '')).toBe(false)
    expect(confirmationMatches('CZ3', 'CZ3xxxxxxx')).toBe(false)
  })
})

describe('who may create one', () => {
  // Creating a FrameInstall wipes disks. The editor tier does not get it, and
  // the screen must agree with the RBAC rather than offer a button the
  // apiserver will refuse.
  it('is admin and nobody else', () => {
    expect(canCreateInstall('admin')).toBe(true)
    expect(canCreateInstall('editor')).toBe(false)
    expect(canCreateInstall('viewer')).toBe(false)
    expect(canCreateInstall('')).toBe(false)
  })
})
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd /home/rmocq/frame-provision && npx vitest run src/lib/installs.test.ts`
Expected: FAIL — the module does not exist.

- [ ] **Step 3: Write `src/lib/installs.ts`**

```ts
// One threshold for every phase either cries wolf on Installing, which
// legitimately takes twenty minutes, or never fires on Preparing, which takes
// seconds. So the budget is per phase.
const PHASE_BUDGET_MS: Partial<Record<InstallPhase, number>> = {
  Pending: 60_000,
  Preparing: 5 * 60_000,
  MediaAttached: 2 * 60_000,
  Installing: 60 * 60_000,
  Installed: 2 * 60_000,
  Joining: 10 * 60_000,
}

export function isStalledInPhase(p: InstallPhase, since: string | undefined, now: Date): boolean {
  if (isTerminal(p) || !since) return false
  const budget = PHASE_BUDGET_MS[p]
  if (budget === undefined) return false
  const started = Date.parse(since)
  if (Number.isNaN(started)) return false
  return now.getTime() - started > budget
}
```

`confirmationMatches` trims and compares exactly. It does not lowercase: a guard that accepts a near-miss is a guard that a paste of the wrong serial passes.

- [ ] **Step 4: Run the tests**

Run: `cd /home/rmocq/frame-provision && npx vitest run src/lib/installs.test.ts`
Expected: PASS.

- [ ] **Step 5: Write the components**

`InstallsTab.tsx` lists installations with machine, phase, node and elapsed time. **It holds `now` in state and ticks it every second**, passing it into `elapsedLabel` and `isStalledInPhase` — a `new Date()` evaluated at render freezes the moment nothing re-renders, which is the failure lot 1 shipped.

`InstallDialog.tsx` creates one: pick an inventoried `FrameMachine`, fill hostname, address, layout and cluster mode, and type the serial. The create button stays disabled until `confirmationMatches` is true. The dialog states in plain words that this erases the named disks.

The tab is added to `src/App.tsx:238`, beside `{ id: 'hardware', label: 'Machines' }`.

- [ ] **Step 6: Build and typecheck**

Run: `cd /home/rmocq/frame-provision && npx tsc --noEmit && npm run build`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
cd /home/rmocq/frame-provision
git add src/lib/installs.ts src/lib/installs.test.ts src/components/hardware/ src/App.tsx src/lib/frame-sdk.ts
git commit -m "feat(ui): show installations, and let an admin start one

Every decision is in src/lib/installs.ts because vitest runs with
environment: node and include: src/**/*.test.ts, so no .tsx file is ever
executed by a test. Logic left in a component is logic with no coverage.

Elapsed time takes now as a parameter and the tab ticks it. Lot 1 shipped a
freshness marker computed with new Date() at render under a watch with no
poll: it froze exactly when the operator stopped writing, which is the moment
it existed for.

Staleness is judged per phase. Installing legitimately takes twenty minutes
and Preparing does not, so one threshold for both either cries wolf or never
fires.

The create button stays disabled until the machine's serial is typed exactly.
The comparison trims but does not lowercase: a guard that accepts a near-miss
is a guard a paste of the wrong serial gets through, and what is behind it is
an erased disk."
```

---

### Task 12: Retire the dead provisioning wizard

`src/components/NodeProvisionWizard.tsx` is wired at `src/App.tsx:796` and drives `FrameNode`'s provisioning half — the half `docs/provisioning.md` established has never produced a single byte of status on this cluster. Leaving a working-looking wizard beside a real one is how someone uses the wrong one.

**Files:**
- Delete: `src/components/NodeProvisionWizard.tsx`
- Modify: `src/App.tsx` (its import and its use — **locate them by the symbol name, not by line number**: Task 11 edits this file first and shifts every line below its change)

- [ ] **Step 1: Verify it drives the dead path before removing anything**

```bash
cd /home/rmocq/frame-provision
grep -n "framenodes\|spec.disk\|talos\|Talos" src/components/NodeProvisionWizard.tsx | head -20
```

Expected: references to `FrameNode` provisioning fields and/or Talos. Record the output in the report. If it turns out to drive something still live, **stop and say so** rather than deleting it — this task assumes a finding, and the finding is checkable in one command.

- [ ] **Step 2: Remove it and its wiring**

- [ ] **Step 3: Confirm nothing else referenced it**

```bash
cd /home/rmocq/frame-provision
grep -rn "NodeProvisionWizard" src/ || echo "no references remain"
npx tsc --noEmit && npm run build
```

Expected: `no references remain`, and a clean build.

- [ ] **Step 4: Commit**

```bash
cd /home/rmocq/frame-provision
git add -u src/
git commit -m "refactor(ui): remove the wizard that provisioned through the dead FrameNode path

docs/provisioning.md established that FrameNode's provisioning half has never
produced a single byte of status on this cluster: three objects, 48 days old,
all stopped at Discovered, and zero TalosMachineConfig. This wizard drove that
path.

It is removed now rather than with the Talos CRDs, which are deliberately
last, because a CRD nobody can see costs only the reading while a wizard on
screen beside a working one is a way to use the wrong one."
```

---

### Task 13: Documentation

**Files:**
- Modify: `docs/provisioning.md` (mark work item 2 answered, and point at the spec)
- Modify: `docs/deployment.md` (a runbook section, and the unexecuted checks)
- Modify: `docs/README.md` (a pointer)

- [ ] **Step 1: Update `docs/provisioning.md`**

Under "Three pieces of work", mark item 2 as designed and implemented, naming
the spec and this plan. Leave items 1 and 3 exactly as they are: decoupling
classification is still the urgent one, and retiring the Talos CRDs is still
deliberately last.

- [ ] **Step 2: Write the runbook in `docs/deployment.md`**

Cover, with real commands: creating the SSH key Secret; deploying
`frame-provisiond` and confirming the media listener answers from the
management network; creating a `FrameInstall` for the ML350 Gen9 with
`layout: mirror` over the two 300 GB SAS disks; and reading the resulting
kubeconfig out of its Secret.

Include the ML350's own reservations, because they are facts about the machine
that will carry the cluster:

> IML entry `1832` warns that residual logical-volume metadata can hide disks
> from the host. The check that settles it is `lsblk -d -o NAME,SIZE,MODEL` on
> the first booted system: it must list **8** disks. Fewer means the metadata
> must be cleared before trusting any `by-id` name, and Redfish's SmartStorage
> tree exposes no action that can do it.

- [ ] **Step 3: Record what is still unproven**

Three claims in this lot cannot be proven without hardware, and they are
written as unexecuted rather than assumed — the same treatment as lot 1's
browser check, which still carries that label:

```markdown
### Not yet executed

- [ ] The remastered image boots the ML350 Gen9 unattended, in the boot mode
      Frame set, without a keypress.
- [ ] partman partitions the two named 300 GB disks as a mirror, and refuses
      when a named disk is absent.
- [ ] `k3s server --cluster-init` produces a cluster whose kubeconfig, as
      rewritten by Frame, reaches it from another machine.

Until these three run, this lot is proven only against fakes and captures.
```

- [ ] **Step 4: Commit**

```bash
cd /home/rmocq/frame-provision
git add docs/
git commit -m "docs(provisioning): record what this lot answers, and what it has not yet proven

docs/provisioning.md's work item 2 is answered; items 1 and 3 are left exactly
as they were, because decoupling classification is still the urgent one and
retiring the Talos CRDs is still deliberately last.

Three claims cannot be proven without hardware -- that the image boots, that
partman partitions, and that a new cluster's kubeconfig reaches it -- and they
are written as unexecuted rather than assumed. Until they run, this lot is
proven against fakes and captures only, which is worth knowing before anyone
points it at a machine they care about."
```
