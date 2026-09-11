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
