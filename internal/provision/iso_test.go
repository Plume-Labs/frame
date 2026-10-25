package provision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testSeedURL is the seed address every test below hands to Remaster that
// doesn't itself exist to test seed-URL handling. Nothing here fetches it --
// these tests only assert what lands in the image's own boot arguments.
const testSeedURL = "http://192.0.2.50:8081/autoinstall/0123456789abcdef0123456789abcdef/"

func requireXorriso(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("xorriso"); err != nil {
		t.Fatal("xorriso is not installed; this lot cannot build installer images without it (apt install xorriso)")
	}
}

// bootable says which El Torito entries a fixture ISO carries.
type bootable struct{ bios, uefi bool }

// buildISO packages files into an ISO with the requested El Torito entries.
// The boot images are placeholders -- what the tests assert is that Remaster
// carries the entries over and refuses when one is missing, not that they
// boot.
func buildISO(t *testing.T, files map[string]string, b bootable) string {
	t.Helper()
	requireXorriso(t)
	src := t.TempDir()
	if b.bios {
		files["boot/grub/i386-pc/eltorito.img"] = strings.Repeat("B", 2048)
	}
	if b.uefi {
		files["efi.img"] = strings.Repeat("E", 2048)
	}
	for path, body := range files {
		full := filepath.Join(src, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "base.iso")
	args := []string{"-as", "mkisofs", "-o", out, "-V", "Ubuntu-Server 24.04.5 LTS amd64", "-r", "-J"}
	if b.bios {
		args = append(args, "-b", "boot/grub/i386-pc/eltorito.img", "-no-emul-boot", "-boot-load-size", "4")
	}
	if b.uefi {
		args = append(args, "-eltorito-alt-boot", "-e", "efi.img", "-no-emul-boot")
	}
	args = append(args, src)
	if b, err := exec.Command("xorriso", args...).CombinedOutput(); err != nil {
		t.Fatalf("building the test base ISO failed: %v\n%s", err, b)
	}
	return out
}

// realBaseISO carries the two GRUB files captured byte for byte from the
// pinned server ISO (testdata/ubuntu-24.04.5-boot/PROVENANCE.md), with both
// boot entries.
func realBaseISO(t *testing.T) string {
	t.Helper()
	files := map[string]string{}
	for _, f := range []string{"grub.cfg", "loopback.cfg"} {
		b, err := os.ReadFile(filepath.Join("testdata/ubuntu-24.04.5-boot/boot/grub", f))
		if err != nil {
			t.Fatal(err)
		}
		files["boot/grub/"+f] = string(b)
	}
	return buildISO(t, files, bootable{bios: true, uefi: true})
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

func remasterReal(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.iso")
	if err := Remaster(context.Background(), realBaseISO(t), goodSpec(), testSeedURL, out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Every kernel line in both files -- grub.cfg is what the firmware boots,
// loopback.cfg is what an ISO-loopback boot reads -- carries the arguments,
// before "---", with the NoCloud separator escaped for GRUB.
func TestRemasterMakesEveryKernelLineUnattended(t *testing.T) {
	out := remasterReal(t)
	for _, f := range []string{"/boot/grub/grub.cfg", "/boot/grub/loopback.cfg"} {
		cfg := isoContains(t, out, f)
		var n int
		for line := range strings.SplitSeq(cfg, "\n") {
			if !kernelLine.MatchString(line) {
				continue
			}
			n++
			args := bootArgs(testSeedURL)
			i, sep := strings.Index(line, args), strings.Index(line, " ---")
			if i < 0 {
				t.Errorf("%s: kernel line lacks the unattended arguments:\n%s", f, line)
				continue
			}
			if sep < 0 || i > sep {
				t.Errorf("%s: arguments must come before \"---\", or the installed system inherits them:\n%s", f, line)
			}
		}
		if n != 2 {
			t.Errorf("%s: found %d kernel lines, the captured file has 2", f, n)
		}
	}
}

// Unescaped, GRUB ends the linux command at ";" and the seed URL never
// reaches the kernel: the installer boots, finds no seed, and waits.
func TestBootArgsEscapeTheNoCloudSeparatorForGrub(t *testing.T) {
	got := bootArgs(testSeedURL)
	if !strings.Contains(got, `ds=nocloud\;s=`+testSeedURL) {
		t.Errorf("seed argument not escaped for GRUB: %q", got)
	}
	if strings.Contains(strings.ReplaceAll(got, `\;`, ""), ";") {
		t.Errorf("an unescaped ; remains: %q", got)
	}
}

// The loopback.cfg kernel lines carry iso-scan/filename after the kernel
// path; it must survive, and stay before "---" alongside ours.
func TestRemasterKeepsWhatWasAlreadyOnTheKernelLine(t *testing.T) {
	cfg := isoContains(t, remasterReal(t), "/boot/grub/loopback.cfg")
	if !strings.Contains(cfg, "iso-scan/filename=${iso_path}") {
		t.Errorf("iso-scan argument lost:\n%s", cfg)
	}
}

func TestRemasterCutsTheMenuTimeout(t *testing.T) {
	cfg := isoContains(t, remasterReal(t), "/boot/grub/grub.cfg")
	if !strings.Contains(cfg, "\nset timeout=1\n") && !strings.HasPrefix(cfg, "set timeout=1\n") {
		t.Errorf("menu timeout not cut to 1:\n%s", cfg)
	}
	if strings.Contains(cfg, "timeout=30") {
		t.Errorf("stock 30-second wait still present:\n%s", cfg)
	}
}

// The output keeps both boot entries and the base's volume label: casper
// looks for its medium, and an image that lost a boot entry hangs in one
// firmware mode.
func TestRemasterKeepsBothBootEntriesAndTheLabel(t *testing.T) {
	out := remasterReal(t)
	if err := checkBootRecords(context.Background(), out); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command("xorriso", "-indev", out, "-pvd_info").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Ubuntu-Server 24.04.5 LTS amd64") {
		t.Errorf("volume label not carried over:\n%s", b)
	}
}

// A base lacking the UEFI entry (or the BIOS one) produces an image that
// boots in only one mode. Refused, and nothing left behind to be served.
func TestRemasterRefusesAnImageMissingABootEntry(t *testing.T) {
	cfg, err := os.ReadFile("testdata/ubuntu-24.04.5-boot/boot/grub/grub.cfg")
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]bootable{"no UEFI": {bios: true}, "no BIOS": {uefi: true}} {
		t.Run(name, func(t *testing.T) {
			base := buildISO(t, map[string]string{"boot/grub/grub.cfg": string(cfg)}, b)
			out := filepath.Join(t.TempDir(), "out.iso")
			if err := Remaster(context.Background(), base, goodSpec(), testSeedURL, out); err == nil {
				t.Fatal("an image missing a boot entry was accepted")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("the refused image was left at %s", out)
			}
		})
	}
}

// grub.cfg is required: without it there is nothing to make unattended.
// loopback.cfg is optional: an image without it still boots from grub.cfg.
func TestRemasterRefusesAnImageWithoutGrubCfg(t *testing.T) {
	base := buildISO(t, map[string]string{"README": "x"}, bootable{bios: true, uefi: true})
	if err := Remaster(context.Background(), base, goodSpec(), testSeedURL, filepath.Join(t.TempDir(), "o.iso")); err == nil {
		t.Fatal("an image with no grub.cfg was accepted")
	}
}

func TestRemasterAcceptsAnImageWithoutLoopbackCfg(t *testing.T) {
	cfg, err := os.ReadFile("testdata/ubuntu-24.04.5-boot/boot/grub/grub.cfg")
	if err != nil {
		t.Fatal(err)
	}
	base := buildISO(t, map[string]string{"boot/grub/grub.cfg": string(cfg)}, bootable{bios: true, uefi: true})
	if err := Remaster(context.Background(), base, goodSpec(), testSeedURL, filepath.Join(t.TempDir(), "o.iso")); err != nil {
		t.Fatal(err)
	}
}

// The Debian netinst this package installed before 2026-09-29 boots
// /install.amd/vmlinuz, which never reads autoinstall: the machine would wait
// on the installer's first question. Its kernel line is a real one, so the
// "no kernel line" refusal does not catch it.
func TestRemasterRefusesADebianInstallerImage(t *testing.T) {
	debian := "set timeout=30\nmenuentry --hotkey=i 'Install' {\n\tlinux\t/install.amd/vmlinuz vga=788 --- quiet\n\tinitrd\t/install.amd/initrd.gz\n}\n"
	base := buildISO(t, map[string]string{"boot/grub/grub.cfg": debian}, bootable{bios: true, uefi: true})
	out := filepath.Join(t.TempDir(), "o.iso")
	err := Remaster(context.Background(), base, goodSpec(), testSeedURL, out)
	if err == nil || !strings.Contains(err.Error(), "not an Ubuntu live-server image") {
		t.Fatalf("err = %v, want the Debian installer refused as not live-server", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("the refused image was left at %s", out)
	}
}

// A grub.cfg with no kernel line boots to a menu and waits for a keypress.
func TestRemasterRefusesAGrubCfgWithNoKernelLine(t *testing.T) {
	base := buildISO(t, map[string]string{"boot/grub/grub.cfg": "set timeout=30\n"}, bootable{bios: true, uefi: true})
	err := Remaster(context.Background(), base, goodSpec(), testSeedURL, filepath.Join(t.TempDir(), "o.iso"))
	if err == nil || !strings.Contains(err.Error(), "no kernel line") {
		t.Fatalf("err = %v, want a grub.cfg with no kernel line refused as such", err)
	}
}

// Refusals that need no image: a bad spec, an empty or GRUB-unsafe seed URL.
// The base path does not exist, so reaching xorriso would fail differently.
func TestRemasterRefusesBeforeTouchingTheBaseImage(t *testing.T) {
	bad := goodSpec()
	bad.Hostname = "Not_A_Label"
	for name, c := range map[string]struct {
		spec Spec
		url  string
	}{
		"bad spec":       {bad, testSeedURL},
		"empty seed URL": {goodSpec(), ""},
		"semicolon":      {goodSpec(), "http://x/;reboot"},
		"space":          {goodSpec(), "http://x/ a"},
		"quote":          {goodSpec(), "http://x/'"},
		// GRUB expands $name and ${name} on the kernel line, and { } group:
		// none of them is refused by a list of forbidden characters that
		// did not think of them, which is why the check is a list of
		// allowed ones.
		"dollar":      {goodSpec(), "http://x/$root"},
		"braces":      {goodSpec(), "http://x/${root}"},
		"open brace":  {goodSpec(), "http://x/{"},
		"close brace": {goodSpec(), "http://x/}"},
		"percent":     {goodSpec(), "http://x/%24root"},
	} {
		t.Run(name, func(t *testing.T) {
			err := Remaster(context.Background(), "/nonexistent/base.iso", c.spec, c.url, filepath.Join(t.TempDir(), "o.iso"))
			if err == nil || strings.Contains(err.Error(), "/nonexistent") {
				t.Errorf("want a refusal before the base image is read, got %v", err)
			}
		})
	}
}

// What every real seed URL looks like passes: scheme, host or address, port,
// path of hex and slashes.
func TestRemasterAcceptsAnOrdinarySeedURL(t *testing.T) {
	for _, u := range []string{testSeedURL, "http://10.0.2.2:18081/autoinstall/0123456789abcdef0123456789abcdef/", "http://frame-provisiond.frame-system.svc:8081/autoinstall/x_y-z/"} {
		err := Remaster(context.Background(), "/nonexistent/base.iso", goodSpec(), u, filepath.Join(t.TempDir(), "o.iso"))
		if err == nil || !strings.Contains(err.Error(), "/nonexistent") {
			t.Errorf("%s: want it to reach the base image, got %v", u, err)
		}
	}
}

// Rewriting an already-rewritten file changes nothing: the frame=1 marker.
func TestAddBootArgsIsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "grub.cfg")
	if err := os.WriteFile(p, []byte("set timeout=30\nmenuentry x {\n\tlinux\t/casper/vmlinuz  ---\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found, changed, err := addBootArgs(p, testSeedURL); err != nil || !found || !changed {
		t.Fatalf("first pass: found=%v changed=%v err=%v", found, changed, err)
	}
	if found, changed, err := addBootArgs(p, testSeedURL); err != nil || !found || changed {
		t.Fatalf("second pass: found=%v changed=%v err=%v -- want found and unchanged", found, changed, err)
	}
}
