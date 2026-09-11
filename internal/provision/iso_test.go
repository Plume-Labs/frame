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
