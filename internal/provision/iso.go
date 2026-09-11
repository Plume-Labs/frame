package provision

import (
	"context"
	"fmt"
	"io/fs"
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

// alreadyRewritten is the idempotency marker addBootArgs checks for, and it
// must be this string rather than "auto=true": Debian's own boot/grub/grub.cfg
// ships five "Automated install" submenu entries that already carry
// "auto=true priority=critical" out of the box, with no file= directive at
// all. Treating "already has auto=true" as "we already rewrote this line"
// skips all five permanently -- they never receive a preseed path. This
// string is ours; Debian never ships it.
const alreadyRewritten = "file=/cdrom/preseed.cfg"

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

	if err := rewriteBootConfigs(tree); err != nil {
		return err
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

// rewriteBootConfigs makes every kernel line in the tree unattended.
//
// It walks isolinux/*.cfg and every *.cfg under boot/grub/, rather than
// opening a fixed two-file list: a real netinst's BIOS boot graph branches
// into eighteen included files (accessible/dark-contrast variants, rescue
// mode, speech synthesis, advanced submenus), and which one the firmware
// actually boots -- gtk.cfg's graphical installer, on a stock BIOS boot, not
// txt.cfg -- is a menu-selection question Remaster has no way to answer and
// no need to: rewriting every kernel line it finds means whichever entry the
// menu graph selects is unattended. An entry nobody boots also carrying the
// arguments is harmless.
func rewriteBootConfigs(tree string) error {
	var files []string

	isolinuxCfgs, err := filepath.Glob(filepath.Join(tree, "isolinux", "*.cfg"))
	if err != nil {
		return err
	}
	files = append(files, isolinuxCfgs...)

	grubRoot := filepath.Join(tree, "boot", "grub")
	if _, err := os.Stat(grubRoot); err == nil {
		walkErr := filepath.WalkDir(grubRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(path, ".cfg") {
				files = append(files, path)
			}
			return nil
		})
		if walkErr != nil {
			return walkErr
		}
	}

	var rewritten int
	for _, f := range files {
		did, err := addBootArgs(f)
		if err != nil {
			return err
		}
		if did {
			rewritten++
		}
	}
	// The error fires once for the whole tree, not per file: most files in a
	// real isolinux tree are menu styling or submenu plumbing with no kernel
	// line at all, so "this one file has none" is not itself a problem. "none
	// of them do" is -- that is an image that boots to a menu and waits for a
	// keypress that will never come, on a machine reachable only by its BMC.
	if rewritten == 0 {
		return fmt.Errorf("no kernel line found to make unattended in %d boot config file(s) under %s", len(files), tree)
	}
	return nil
}

// addBootArgs appends the unattended arguments to every kernel line it finds
// in path, and reports whether it rewrote anything. Finding nothing to
// rewrite in one particular file is not an error here -- it is normal for
// most files in a real isolinux tree -- rewriteBootConfigs is what decides
// whether the walk as a whole found nothing.
func addBootArgs(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	out := kernelLine.ReplaceAllStringFunc(string(b), func(line string) string {
		if strings.Contains(line, alreadyRewritten) {
			return line
		}
		return line + " " + bootArgs
	})
	if out == string(b) {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w\n%s", name, err, b)
	}
	return nil
}
