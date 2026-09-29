package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The tests in this file run rendered commands under sh instead of reading
// their text. A command whose text is right but whose logic is inverted (an
// `=` turned into `!=`) passes every string assertion and refuses, or lets
// through, the wrong machine. Only running it tells the two apart.

// runSh runs one rendered command under sh with dir prepended to PATH, so the
// fake tools written there shadow the real ones. It returns the exit code.
func runSh(t *testing.T, command, fakeBin string) (int, string) {
	t.Helper()
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found")
	}
	cmd := exec.Command(shPath, "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(out)
	}
	t.Fatalf("running %q: %v", command, err)
	return -1, ""
}

// fakeTool writes an executable shell script named name into dir.
func fakeTool(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The first early-command, run with a fake blockdev that answers a size per
// disk. The by-id paths do not exist on this host; the fake never opens them.
func TestDiskSizeGuardRefusesTheWrongSizeWhenRun(t *testing.T) {
	const want = "322122547200" // 300 << 30, goodSpec's size
	for _, c := range []struct {
		name   string
		layout Layout
		a, b   string
		code   int
	}{
		{"mirror, both the named size", goodSpec().Layout, want, want, 0},
		{"mirror, second disk one byte short", goodSpec().Layout, want, "322122547199", 1},
		{"mirror, first disk larger", goodSpec().Layout, "322122547201", want, 1},
		{"mirror, second disk unreadable", goodSpec().Layout, want, "", 1},
		{"mirror, first disk unreadable", goodSpec().Layout, "", want, 1},
		{"single disk, the named size", Layout{Kind: LayoutSingleDisk, Disks: goodSpec().Layout.Disks[:1]}, want, "", 0},
		{"single disk, wrong size", Layout{Kind: LayoutSingleDisk, Disks: goodSpec().Layout.Disks[:1]}, "1000", "", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := goodSpec()
			s.Layout = c.layout
			early := commands(t, parsed(t, s, ""), "early-commands")
			bin := t.TempDir()
			fakeTool(t, bin, "blockdev", `case "$2" in
*/scsi-aaa) [ -n "`+c.a+`" ] && echo "`+c.a+`" ;;
*/scsi-bbb) [ -n "`+c.b+`" ] && echo "`+c.b+`" ;;
esac
[ "$1" = "--getsize64" ] || exit 2`)
			if code, out := runSh(t, early[0], bin); code != c.code {
				t.Errorf("guard exited %d, want %d\n%s\n%s", code, c.code, early[0], out)
			}
		})
	}
}

// blockDevices returns up to n block devices present on this host, for the
// resolution test's `[ -b ]`, which is a shell builtin and cannot be faked.
func blockDevices(t *testing.T, n int) []string {
	t.Helper()
	var out []string
	for _, pattern := range []string{"/dev/loop[0-9]*", "/dev/sd[a-z]", "/dev/vd[a-z]", "/dev/nvme[0-9]n[0-9]", "/dev/zram[0-9]"} {
		m, _ := filepath.Glob(pattern)
		for _, p := range m {
			if fi, err := os.Stat(p); err == nil && fi.Mode()&os.ModeDevice != 0 && fi.Mode()&os.ModeCharDevice == 0 {
				out = append(out, p)
			}
			if len(out) == n {
				return out
			}
		}
	}
	t.Skipf("need %d block devices on this host, found %d", n, len(out))
	return nil
}

// Two by-id names for one disk -- a disk seen through two paths, or a typo
// that happens to name the same wwn twice under different prefixes -- would
// build a mirror of one disk with itself. validateDisks only sees names; the
// resolved kernel path is what says whether the halves are different hardware.
func TestDiskResolutionRefusesOneDiskUnderTwoNamesWhenRun(t *testing.T) {
	devs := blockDevices(t, 2)
	for _, c := range []struct {
		name string
		a, b string
		code int
	}{
		{"two different disks", devs[0], devs[1], 0},
		{"the same disk under both names", devs[0], devs[0], 1},
		{"second name resolves to nothing", devs[0], "/nonexistent/frame-test", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin := t.TempDir()
			log := filepath.Join(bin, "sed.log")
			fakeTool(t, bin, "readlink", `case "$2" in
*/scsi-aaa) echo "`+c.a+`" ;;
*/scsi-bbb) echo "`+c.b+`" ;;
esac`)
			// Records instead of rewriting /autoinstall.yaml, which does
			// not exist here and must never be touched on this host.
			fakeTool(t, bin, "sed", `echo "$*" >> '`+log+`'`)
			code, out := runSh(t, diskResolution(goodSpec().Layout), bin)
			if code != c.code {
				t.Fatalf("resolution exited %d, want %d\n%s", code, c.code, out)
			}
			rewrites, _ := os.ReadFile(log)
			if c.code != 0 && len(rewrites) != 0 {
				t.Errorf("refused, but still rewrote the document:\n%s", rewrites)
			}
			if c.code == 0 {
				for i, p := range []string{c.a, c.b} {
					if !strings.Contains(string(rewrites), "s#"+diskPlaceholders[i]+"#"+p+"#g") {
						t.Errorf("placeholder %s not rewritten to %s:\n%s", diskPlaceholders[i], p, rewrites)
					}
				}
			}
		})
	}
}

// subiquity writes `lock_passwd: false` for the identity user into
// 99-installer.cfg, and cloud-init's users_groups runs `passwd -u frame` on
// first boot -- undoing the late-command's lock (seen in the QEMU rehearsal:
// `passwd -S frame` read P). runcmd runs after users_groups, so a runcmd
// that locks again is the last word.
func TestFirstBootRelocksTheAccountWhenRun(t *testing.T) {
	late := commands(t, parsed(t, goodSpec(), ""), "late-commands")
	if !strings.Contains(strings.Join(late, "\n"), relockCommand(targetRoot)) {
		t.Fatalf("no late-command writes the first-boot relock:\n%s", strings.Join(late, "\n"))
	}
	if !strings.Contains(strings.Join(late, "\n"), "curtin in-target --target=/target -- passwd -l frame") {
		t.Error("the install-time lock is gone; the relock is in addition to it, not instead")
	}

	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "etc/cloud/cloud.cfg.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, out := runSh(t, relockCommand(target), t.TempDir()); code != 0 {
		t.Fatalf("relock command exited %d:\n%s", code, out)
	}
	path := filepath.Join(target, "etc/cloud/cloud.cfg.d/99-zz-frame-lock.cfg")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		RunCmd [][]string `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("%s is not YAML: %v\n%s", path, err, b)
	}
	if want := [][]string{{"passwd", "-l", "frame"}}; !reflect.DeepEqual(cfg.RunCmd, want) {
		t.Errorf("runcmd = %v, want %v\n%s", cfg.RunCmd, want, b)
	}
}

// pinTarget is a fake /target holding the files subiquity leaves behind: both
// netplan names it has used, and the cloud-init network config 24.04 writes
// instead when cloud-init owns /etc/netplan.
func pinTarget(t *testing.T) string {
	t.Helper()
	target := t.TempDir()
	for _, f := range []string{"etc/netplan/00-installer-config.yaml", "etc/netplan/50-cloud-init.yaml", "etc/cloud/cloud.cfg.d/90-installer-network.cfg"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(target, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, f), []byte("network: {version: 2, ethernets: {frame0: {match: {name: 'en*'}}}}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return target
}

// The installed system gets the address on one port -- the one the live
// system reached the gateway through -- named by MAC, not on every en* port.
func TestNetplanPinWritesOnePortByMACWhenRun(t *testing.T) {
	s := goodSpec()
	late := strings.Join(commands(t, parsed(t, s, ""), "late-commands"), "\n")
	v, err := validate(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(late, netplanPin(v, s, targetRoot, "/sys/class/net")) {
		t.Fatalf("no late-command pins the installed netplan:\n%s", late)
	}

	target, sys, bin := pinTarget(t), t.TempDir(), t.TempDir()
	// A name neither subiquity version is known to write: netplan merges
	// every *.yaml under /etc/netplan, so whatever its origin, an en*
	// stanza in it would put the address back on every port.
	if err := os.WriteFile(filepath.Join(target, "etc/netplan/01-other.yaml"), []byte("network: {version: 2, ethernets: {x: {match: {name: 'en*'}, dhcp4: true}}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sys, "enp3s0f1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sys, "enp3s0f1", "address"), []byte("52:54:00:00:00:02\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	fakeTool(t, bin, "ip", `[ "$1 $2 $3 $4" = "-o route get `+s.Network.Gateway+`" ] || exit 2
echo "`+s.Network.Gateway+` dev enp3s0f1 src 192.0.2.210 uid 0 \    cache "`)
	if code, out := runSh(t, netplanPin(v, s, target, sys), bin); code != 0 {
		t.Fatalf("pin exited %d:\n%s", code, out)
	}

	path := filepath.Join(target, "etc/netplan/60-frame.yaml")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s mode %v; netplan warns on, and later refuses, a world-readable file", path, fi.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	var np struct {
		Network struct {
			Version   int `yaml:"version"`
			Ethernets map[string]struct {
				Match       map[string]string   `yaml:"match"`
				SetName     string              `yaml:"set-name"`
				DHCP4       *bool               `yaml:"dhcp4"`
				Addresses   []string            `yaml:"addresses"`
				Routes      []map[string]string `yaml:"routes"`
				Nameservers map[string][]string `yaml:"nameservers"`
			} `yaml:"ethernets"`
		} `yaml:"network"`
	}
	if err := yaml.Unmarshal(b, &np); err != nil {
		t.Fatalf("%s is not YAML: %v\n%s", path, err, b)
	}
	if len(np.Network.Ethernets) != 1 {
		t.Fatalf("want exactly one stanza:\n%s", b)
	}
	for _, e := range np.Network.Ethernets {
		if !reflect.DeepEqual(e.Match, map[string]string{"macaddress": "52:54:00:00:00:02"}) || e.SetName != "enp3s0f1" {
			t.Errorf("stanza matches %v, set-name %q; want the gateway port's MAC only:\n%s", e.Match, e.SetName, b)
		}
		if e.DHCP4 == nil || *e.DHCP4 || !reflect.DeepEqual(e.Addresses, []string{"192.0.2.210/24"}) ||
			!reflect.DeepEqual(e.Routes, []map[string]string{{"to": "default", "via": s.Network.Gateway}}) ||
			!reflect.DeepEqual(e.Nameservers["addresses"], s.Network.DNS) {
			t.Errorf("stanza does not carry the spec's network:\n%s", b)
		}
	}
	for _, gone := range []string{"etc/netplan/00-installer-config.yaml", "etc/netplan/50-cloud-init.yaml", "etc/netplan/01-other.yaml", "etc/cloud/cloud.cfg.d/90-installer-network.cfg"} {
		if _, err := os.Stat(filepath.Join(target, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived; its en* stanza would put the address back on every port", gone)
		}
	}
	// Without this, cloud-init's first boot finds no network config and
	// falls back to DHCP on the first port it sees, writing 50-cloud-init.yaml.
	cb, err := os.ReadFile(filepath.Join(target, "etc/cloud/cloud.cfg.d/99-zz-frame-network.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	var cc struct {
		Network map[string]string `yaml:"network"`
	}
	if err := yaml.Unmarshal(cb, &cc); err != nil || cc.Network["config"] != "disabled" {
		t.Errorf("cloud-init network not disabled (%v):\n%s", err, cb)
	}
}

func TestNetplanPinRefusesWhenItCannotNameThePortWhenRun(t *testing.T) {
	s := goodSpec()
	v, err := validate(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, route, port, mac string
	}{
		{"no route to the gateway", "", "enp3s0f1", "52:54:00:00:00:02"},
		{"no MAC for the port", "dev enp3s0f1 src 192.0.2.210", "enp3s0f1", ""},
		{"not a MAC", "dev enp3s0f1 src 192.0.2.210", "enp3s0f1", "52:54:00:00:00:02/x"},
		{"a port name that is not a name", "dev ../x src 192.0.2.210", "enp3s0f1", "52:54:00:00:00:02"},
		// Made of allowed characters only, so the character class lets them
		// through and only the leading-dot rule stands in the way. The MAC
		// file is planted where "$sys/./address" and "$sys/../address"
		// resolve, so a guard that let them pass would find a valid MAC.
		{"the port is .", "dev . src 192.0.2.210", ".", "52:54:00:00:00:02"},
		{"the port is ..", "dev .. src 192.0.2.210", "..", "52:54:00:00:00:02"},
		// A gateway equal to the machine's own address routes over lo:
		// pinning lo would leave the installed system with no network.
		{"the route goes over loopback", "dev lo src 192.0.2.210", "lo", "52:54:00:00:00:02"},
		// lo's MAC, and what a virtual port with no hardware address reads.
		{"the MAC is all zeroes", "dev enp3s0f1 src 192.0.2.210", "enp3s0f1", "00:00:00:00:00:00"},
	} {
		t.Run(c.name, func(t *testing.T) {
			target, bin := pinTarget(t), t.TempDir()
			// One level down, so ".." still lands inside this test's own
			// temporary directory.
			sys := filepath.Join(t.TempDir(), "net")
			if err := os.MkdirAll(sys, 0o755); err != nil {
				t.Fatal(err)
			}
			if c.mac != "" {
				if err := os.MkdirAll(filepath.Join(sys, c.port), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(sys, c.port, "address"), []byte(c.mac+"\n"), 0o444); err != nil {
					t.Fatal(err)
				}
			}
			route := ""
			if c.route != "" {
				route = `echo "` + s.Network.Gateway + ` ` + c.route + `"`
			}
			fakeTool(t, bin, "ip", route)
			if code, out := runSh(t, netplanPin(v, s, target, sys), bin); code != 1 {
				t.Errorf("pin exited %d, want 1:\n%s", code, out)
			}
			if _, err := os.Stat(filepath.Join(target, "etc/netplan/60-frame.yaml")); !os.IsNotExist(err) {
				t.Error("refused, but wrote a netplan anyway")
			}
			if _, err := os.Stat(filepath.Join(target, "etc/netplan/50-cloud-init.yaml")); err != nil {
				t.Error("refused, but removed the installer's netplan anyway")
			}
		})
	}
}
