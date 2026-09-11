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

// An isolinux config with a label and no kernel line at all. Remaster must
// fail rather than build an image that stops at a menu on a machine nobody
// is standing in front of.
func TestRemasterRefusesAnImageWhoseBootConfigItCannotRewrite(t *testing.T) {
	base := buildISO(t, map[string]string{
		"isolinux/txt.cfg": "default install\nlabel install\n  ui gtk\n",
	})
	out := filepath.Join(t.TempDir(), "g9.iso")
	err := Remaster(context.Background(), base, goodSpec(), out)
	if err == nil {
		t.Fatal("want an error for a boot config with no kernel line, got nil")
	}
	if !strings.Contains(err.Error(), "isolinux/txt.cfg") {
		t.Errorf("error = %q; it must name the file that could not be made unattended", err)
	}
}

// A base image carrying only the isolinux config, with no boot/grub/grub.cfg
// at all, must still build -- a netinst is not guaranteed to carry both boot
// records -- and the one config it does carry must still be unattended. This
// is what proves the missing-file skip in addBootArgs is a deliberate
// tolerance, not an accident that happens to let a broken image through.
func TestRemasterAcceptsABaseImageMissingOneBootConfig(t *testing.T) {
	base := buildISO(t, map[string]string{
		"isolinux/txt.cfg": "default install\nlabel install\n  kernel /install.amd/vmlinuz\n  append vga=788 initrd=/install.amd/initrd.gz --- quiet\n",
	})
	out := filepath.Join(t.TempDir(), "g9.iso")
	if err := Remaster(context.Background(), base, goodSpec(), out); err != nil {
		t.Fatal(err)
	}
	got := isoContains(t, out, "/isolinux/txt.cfg")
	for _, want := range []string{"auto=true", "priority=critical", "file=/cdrom/preseed.cfg"} {
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
	err := Remaster(context.Background(), "/nonexistent/base.iso", s, filepath.Join(t.TempDir(), "out.iso"))
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
