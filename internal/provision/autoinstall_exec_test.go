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
