package provision

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testPreseedURL is the preseedURL every test below hands to Remaster that
// doesn't itself exist to test preseedURL handling. It looks like a real
// address MediaHandler's /preseed/{name} route would serve, but nothing
// here fetches it -- these tests only assert what lands in the image's own
// boot arguments.
const testPreseedURL = "http://192.168.2.50:8081/preseed/0123456789abcdef0123456789abcdef.cfg"

func requireXorriso(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("xorriso"); err != nil {
		t.Fatal("xorriso is not installed; this lot cannot build installer images without it (apt install xorriso)")
	}
}

// A stand-in for the netinst, for tests that need a deliberately degenerate
// image: no kernel line anywhere, or only one of the two boot config trees
// present. Anything asserting what a real remaster produces uses
// realBaseISO instead -- this fixture's two-file, two-line shape does not
// match the real image (see testdata/debian-13.6.0-boot/PROVENANCE.md for
// what that gap hid).
func tinyBaseISO(t *testing.T) string {
	t.Helper()
	requireXorriso(t)
	src := t.TempDir()
	for path, body := range map[string]string{
		"isolinux/txt.cfg":   "default install\nlabel install\n  kernel /install.amd/vmlinuz\n  append vga=788 initrd=/install.amd/initrd.gz --- quiet\n",
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

// buildISO packages an arbitrary set of files into an ISO, for tests that
// need a base image shaped differently from tinyBaseISO's two-config default.
func buildISO(t *testing.T, files map[string]string) string {
	t.Helper()
	requireXorriso(t)
	src := t.TempDir()
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
	cmd := exec.Command("xorriso", "-as", "mkisofs", "-o", out, "-V", "TESTBASE", src)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the test base ISO failed: %v\n%s", err, b)
	}
	return out
}

// realBaseISO packages the real, checksum-verified boot configuration
// extracted from the pinned netinst (testdata/debian-13.6.0-boot; see the
// PROVENANCE.md beside it) into an ISO. This is the fixture for every test
// that asserts what a real remaster produces -- unlike tinyBaseISO, it
// carries the real eighteen-file isolinux menu graph, the real gtk.cfg
// default entry, and Debian's own five pre-existing "Automated install"
// grub.cfg entries.
func realBaseISO(t *testing.T) string {
	t.Helper()
	requireXorriso(t)
	out := filepath.Join(t.TempDir(), "base.iso")
	cmd := exec.Command("xorriso", "-as", "mkisofs", "-o", out, "-V", "TESTBASE", "testdata/debian-13.6.0-boot")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the real-boot-config test ISO failed: %v\n%s", err, b)
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

// The preseed is fetched over HTTP now, so the image itself must not carry
// it: a second copy nothing reads is exactly the dead-code-behind-a-passing-
// test shape this lot keeps finding, and here it would also be a leftover
// place a stale preseed could be read from by mistake.
func TestRemasterDoesNotPutThePreseedOnTheImage(t *testing.T) {
	base := realBaseISO(t)
	out := filepath.Join(t.TempDir(), "g9.iso")
	if err := Remaster(context.Background(), base, goodSpec(), testPreseedURL, out); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cmd := exec.Command("xorriso", "-osirrox", "on", "-indev", out, "-extract", "/preseed.cfg", filepath.Join(dir, "out"))
	if b, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("preseed.cfg was extracted from the image; it must not be there:\n%s", b)
	}
}

// Virtual media offers no way to pass kernel arguments, so an image that does
// not carry them in its own boot configuration stops at a menu and waits for a
// keypress that will never come.
func TestRemasterMakesBothBootPathsUnattended(t *testing.T) {
	base := realBaseISO(t)
	out := filepath.Join(t.TempDir(), "g9.iso")
	if err := Remaster(context.Background(), base, goodSpec(), testPreseedURL, out); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/isolinux/txt.cfg", "/boot/grub/grub.cfg"} {
		got := isoContains(t, out, path)
		for _, want := range []string{"auto=true", "priority=critical", "interface=auto", "frame=1", "url=" + testPreseedURL} {
			if !strings.Contains(got, want) {
				t.Errorf("%s is missing %q:\n%s", path, want, got)
			}
		}
	}
}

// The entry that actually boots on real BIOS hardware is not txt.cfg: an
// unattended prompt=0/timeout=0 boot picks whichever isolinux entry carries
// "menu default", and on the real image that is gtk.cfg's graphical
// installer, not the text one. A rewrite that only ever opened txt.cfg
// leaves the machine sitting at a language prompt forever, reachable only by
// its BMC -- this is the exact gap the original brief's fabricated txt.cfg
// fixture hid. See testdata/debian-13.6.0-boot/PROVENANCE.md.
func TestRemasterMakesTheActualDefaultEntryUnattended(t *testing.T) {
	base := realBaseISO(t)
	out := filepath.Join(t.TempDir(), "g9.iso")
	if err := Remaster(context.Background(), base, goodSpec(), testPreseedURL, out); err != nil {
		t.Fatal(err)
	}
	got := isoContains(t, out, "/isolinux/gtk.cfg")
	if !strings.Contains(got, "menu default") {
		t.Fatal("test fixture regression: gtk.cfg no longer carries menu default")
	}
	for _, want := range []string{"auto=true", "priority=critical", "interface=auto", "frame=1", "url=" + testPreseedURL} {
		if !strings.Contains(got, want) {
			t.Errorf("gtk.cfg (the entry actually marked menu default) is missing %q:\n%s", want, got)
		}
	}
}

// Debian's real grub.cfg ships five "Automated install" submenu entries that
// already carry auto=true priority=critical out of the box, with no file=
// directive -- a rewrite that treats "already has auto=true" as "already
// rewritten by us" skips all five permanently. All five must receive the
// preseed path.
//
// "vmlinuz auto=true priority=critical" (kernel path immediately followed by
// those two tokens) identifies exactly Debian's five pre-existing entries,
// and only them: bootArgs, when appended, reads "... auto=true
// priority=critical interface=auto frame=1 url=..." with no "vmlinuz"
// immediately before it, so this substring can't appear as a side effect of
// the rewrite itself -- it only ever comes from the original image content.
// Counting "frame=1" across the whole file, by contrast, cannot tell a
// rewritten pre-existing entry from any of grub.cfg's other menu entries
// that never had auto=true to begin with -- measured: that looser version of
// this test passed against the unfixed code, because those other entries
// still got the marker even though the five pre-existing ones didn't.
func TestRemasterRewritesDebiansOwnAutomatedInstallEntries(t *testing.T) {
	base := realBaseISO(t)
	out := filepath.Join(t.TempDir(), "g9.iso")
	if err := Remaster(context.Background(), base, goodSpec(), testPreseedURL, out); err != nil {
		t.Fatal(err)
	}
	got := isoContains(t, out, "/boot/grub/grub.cfg")

	var preExisting int
	for _, line := range strings.Split(got, "\n") {
		if !strings.Contains(line, "vmlinuz auto=true priority=critical") {
			continue
		}
		preExisting++
		if !strings.Contains(line, "frame=1") {
			t.Errorf("a pre-existing Debian auto=true entry did not receive our boot arguments:\n%s", line)
		}
	}
	if preExisting != 5 {
		t.Fatalf("test fixture regression: expected exactly 5 pre-existing auto=true entries in the real grub.cfg, found %d", preExisting)
	}
}

// This used to search the built image for "PRIVATE KEY" and "BEGIN
// OPENSSH", and it could no longer fail: once the preseed moved off the
// image, no field of Spec reaches the ISO at all, so the naive version of
// Remaster passes it too -- reproduced by the reviewer. The search for
// private key material moved to where the spec-derived content now is (the
// served preseed; see server_test.go).
//
// What is asserted on the image instead is the stronger property the move
// created and nothing was checking: **no value from the Spec reaches the
// image, at all.** A change that put the preseed back on the ISO, or
// baked a hostname or a disk name into a boot argument, turns this red --
// which the old assertion, scoped to two PEM strings, would not have.
func TestRemasterPutsNoSpecContentOnTheImage(t *testing.T) {
	base := realBaseISO(t)
	out := filepath.Join(t.TempDir(), "g9.iso")
	spec := goodSpec()
	if err := Remaster(context.Background(), base, spec, testPreseedURL, out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	image := string(b)

	// The positive control, and it is not optional: an ISO this test cannot
	// read at all would satisfy every "does not contain" below. The preseed
	// URL is the one thing Remaster is supposed to write into the image, so
	// finding it proves the search reaches the bytes that were written.
	if !strings.Contains(image, testPreseedURL) {
		t.Fatalf("the preseed URL is not in the built image; this search is not looking at what Remaster wrote")
	}

	// Fields, then the two PEM strings the old test looked for -- kept so
	// this is strictly stronger than what it replaces, not different.
	forbidden := map[string]string{
		"the SSH public key":  spec.SSHPublicKey,
		"the install UID":     spec.UID,
		"the hostname":        spec.Hostname,
		"the network address": spec.Network.Address,
		"the gateway":         spec.Network.Gateway,
		"the first disk":      spec.Layout.Disks[0].ByID,
		"the second disk":     spec.Layout.Disks[1].ByID,
		"PEM armour":          "PRIVATE KEY",
		"an OpenSSH key file": "BEGIN OPENSSH",
	}
	for what, v := range forbidden {
		if v == "" {
			t.Fatalf("%s is empty in the fixture, so looking for it proves nothing", what)
		}
		if strings.Contains(image, v) {
			t.Errorf("the built image carries %s (%q); nothing from the Spec belongs on it", what, v)
		}
	}
}

// An isolinux config with a label and no kernel line at all, and nothing
// under boot/grub/. Remaster must fail rather than build an image that stops
// at a menu on a machine nobody is standing in front of.
func TestRemasterRefusesAnImageWhoseBootConfigItCannotRewrite(t *testing.T) {
	base := buildISO(t, map[string]string{
		"isolinux/txt.cfg": "default install\nlabel install\n  ui gtk\n",
	})
	out := filepath.Join(t.TempDir(), "g9.iso")
	err := Remaster(context.Background(), base, goodSpec(), testPreseedURL, out)
	if err == nil {
		t.Fatal("want an error for a boot config with no kernel line, got nil")
	}
	if !strings.Contains(err.Error(), "no kernel line") {
		t.Errorf("error = %q; it must say no kernel line was found", err)
	}
	if !strings.Contains(err.Error(), "1 boot config file") {
		t.Errorf("error = %q; it must name how many files were examined", err)
	}
}

// A base image carrying only the isolinux config, with no boot/grub/grub.cfg
// at all, must still build -- a netinst is not guaranteed to carry both boot
// records -- and the one config it does carry must still be unattended. This
// is what proves the missing-tree skip in rewriteBootConfigs is a deliberate
// tolerance, not an accident that happens to let a broken image through.
func TestRemasterAcceptsABaseImageMissingOneBootConfig(t *testing.T) {
	base := buildISO(t, map[string]string{
		"isolinux/txt.cfg": "default install\nlabel install\n  kernel /install.amd/vmlinuz\n  append vga=788 initrd=/install.amd/initrd.gz --- quiet\n",
	})
	out := filepath.Join(t.TempDir(), "g9.iso")
	if err := Remaster(context.Background(), base, goodSpec(), testPreseedURL, out); err != nil {
		t.Fatal(err)
	}
	got := isoContains(t, out, "/isolinux/txt.cfg")
	for _, want := range []string{"auto=true", "priority=critical", "interface=auto", "frame=1", "url=" + testPreseedURL} {
		if !strings.Contains(got, want) {
			t.Errorf("/isolinux/txt.cfg is missing %q:\n%s", want, got)
		}
	}
}

// RenderPreseed is called before anything touches baseISO, so a bad spec is
// refused before a 700 MB unpack that was always going to be thrown away. The
// error must name the spec problem, not the filesystem: if it names the
// (nonexistent) base image instead, RenderPreseed is being called too late.
func TestRemasterRefusesABadSpecBeforeTouchingTheBaseImage(t *testing.T) {
	s := goodSpec()
	s.UID = ""
	err := Remaster(context.Background(), "/nonexistent/base.iso", s, testPreseedURL, filepath.Join(t.TempDir(), "out.iso"))
	if err == nil {
		t.Fatal("want an error for an empty UID, got nil")
	}
	if !strings.Contains(err.Error(), "UID") {
		t.Errorf("error = %q; want it to name the spec problem (UID), not a filesystem error", err)
	}
	if strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error = %q; it names the missing base image, which means RenderPreseed ran too late", err)
	}
}

// The same guard as above, for the other required-before-anything-is-
// touched value: an empty preseedURL means the image would boot to a
// machine with nothing to fetch its preseed from, so it must be refused
// before the base image is ever touched, not discovered on real hardware.
func TestRemasterRefusesAnEmptyPreseedURLBeforeTouchingTheBaseImage(t *testing.T) {
	err := Remaster(context.Background(), "/nonexistent/base.iso", goodSpec(), "", filepath.Join(t.TempDir(), "out.iso"))
	if err == nil {
		t.Fatal("want an error for an empty preseedURL, got nil")
	}
	if !strings.Contains(err.Error(), "preseedURL") {
		t.Errorf("error = %q; want it to name the empty preseedURL, not a filesystem error", err)
	}
	if strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error = %q; it names the missing base image, which means the preseedURL check ran too late", err)
	}
}

// Everything after "---" on a d-i kernel line is not for the installer: it
// is copied onto the installed system's own kernel command line. Appended at
// the end of the line, our arguments landed there -- meaningless on the
// installed node, permanent across every future boot, and publishing the
// preseed URL to anyone who can read /proc/cmdline on it.
func TestRemasterPutsTheBootArgsBeforeTheInstallerSeparator(t *testing.T) {
	base := realBaseISO(t)
	out := filepath.Join(t.TempDir(), "g9.iso")
	if err := Remaster(context.Background(), base, goodSpec(), testPreseedURL, out); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/isolinux/txt.cfg", "/boot/grub/grub.cfg"} {
		body := isoContains(t, out, path)
		var checked int
		for _, line := range strings.Split(body, "\n") {
			if !strings.Contains(line, "url="+testPreseedURL) {
				continue
			}
			checked++
			sep := strings.Index(line, "---")
			if sep < 0 {
				// A kernel line with no separator at all: appending is the
				// only thing to do and there is nothing to assert.
				continue
			}
			if strings.Index(line, "url=") > sep {
				t.Errorf("%s: the boot arguments are after --- and will be copied to the installed system:\n%s", path, line)
			}
			if strings.Index(line, "interface=auto") > sep {
				t.Errorf("%s: interface=auto is after ---:\n%s", path, line)
			}
		}
		// Positive control: this file really does carry rewritten kernel
		// lines, so "none of them are wrong" is a statement about something.
		if checked == 0 {
			t.Errorf("%s: no kernel line carries the preseed URL at all; nothing was checked", path)
		}
	}
}

// And what was already after "---" is still after it: this must not
// reorder the installer's own tail.
func TestRemasterKeepsWhatWasAlreadyAfterTheSeparator(t *testing.T) {
	base := realBaseISO(t)
	out := filepath.Join(t.TempDir(), "g9.iso")
	if err := Remaster(context.Background(), base, goodSpec(), testPreseedURL, out); err != nil {
		t.Fatal(err)
	}
	body := isoContains(t, out, "/isolinux/txt.cfg")
	if !strings.Contains(body, "--- quiet") {
		t.Errorf("the installer's own trailing arguments were disturbed:\n%s", body)
	}
}
