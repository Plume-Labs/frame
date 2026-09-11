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
// to pass kernel arguments, so they have to be written into the image's own
// boot configuration.
//
// The preseed is fetched over HTTP rather than read from the image.
// Measured on an ML350 Gen9 on 2026-09-11: an image saying
// file=/cdrom/preseed.cfg read 79 MB from the virtual CD and stopped before
// reaching the network, while url= read 139-148 MB and got through — three
// trials each way, constant. The mechanism is not understood and the screen
// was never seen, so this is a correlation acted on, not an explanation.
//
// interface=auto is not optional on that machine: it has four NICs and one
// cabled. Observed, not explained, same as url= above and by the same
// session, where nobody saw the screen either: without interface=auto the
// boot does not reach the preseed; with it, DHCP proceeds and the
// installer takes its address. "d-i is asking which interface to use" is a
// plausible reading of that symptom, not a confirmed mechanism -- do not
// trust it further than that.
//
// frame=1 is our own marker. It is what addBootArgs checks to avoid
// rewriting a line twice. It cannot be auto=true, which Debian itself ships
// on five entries of the real grub.cfg.
const bootArgsFixed = "auto=true priority=critical interface=auto frame=1"

const alreadyRewritten = "frame=1"

func bootArgs(preseedURL string) string {
	return bootArgsFixed + " url=" + preseedURL
}

// Remaster writes a per-machine installer image whose boot arguments fetch
// its preseed from preseedURL.
//
// The image is built per machine rather than generic because no secret enters
// it, which removes any reason to fetch a configuration at install time and
// with it an entire MAC-based identification protocol. The image is the
// installation intent, readable in full.
//
// The preseed itself is no longer written onto the image -- see bootArgs's
// doc comment for why. preseedURL is required and refused empty, before
// baseISO is ever touched, the same as a bad Spec is.
func Remaster(ctx context.Context, baseISO string, s Spec, preseedURL, out string) error {
	if _, err := RenderPreseed(s); err != nil {
		return err
	}
	if strings.TrimSpace(preseedURL) == "" {
		return fmt.Errorf("preseedURL is empty: the image has nothing to fetch its preseed from")
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

	if err := rewriteBootConfigs(tree, preseedURL); err != nil {
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
// into many included files (accessible/dark-contrast variants, rescue mode,
// speech synthesis, advanced submenus -- see
// testdata/debian-13.6.0-boot/PROVENANCE.md for the definitive, captured
// listing), and which one the firmware actually boots -- gtk.cfg's graphical
// installer, on a stock BIOS boot, not txt.cfg -- is a menu-selection
// question Remaster has no way to answer and no need to: rewriting every
// kernel line it finds means whichever entry the menu graph selects is
// unattended. An entry nobody boots also carrying the arguments is harmless.
func rewriteBootConfigs(tree, preseedURL string) error {
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

	var anyKernelLine bool
	for _, f := range files {
		found, _, err := addBootArgs(f, preseedURL)
		if err != nil {
			return err
		}
		if found {
			anyKernelLine = true
		}
	}
	// The error fires once for the whole tree, not per file: most files in a
	// real isolinux tree are menu styling or submenu plumbing with no kernel
	// line at all, so "this one file has none" is not itself a problem. "none
	// of them do" is -- that is an image that boots to a menu and waits for a
	// keypress that will never come, on a machine reachable only by its BMC.
	//
	// This checks whether any kernel line was ever found, not whether any
	// line was changed: Remaster always works on a freshly extracted tree
	// today, so every kernel line found is unmarked and every found line is
	// also a changed line. But if that ever stops being true -- an idempotent
	// re-run against an already-remastered tree, say -- "0 changed" would
	// otherwise be reported as "no kernel line to rewrite", which is false:
	// every kernel line was found and correctly left alone because it
	// already carried the marker. Those are different failure states and
	// only one of them is actually a failure.
	if !anyKernelLine {
		return fmt.Errorf("no kernel line found to make unattended in %d boot config file(s) under %s", len(files), tree)
	}
	return nil
}

// addBootArgs appends the unattended arguments to every kernel line it finds
// in path that doesn't already carry them, and reports separately whether it
// found a kernel line at all (found) and whether it changed the file
// (changed). A file with no kernel line is not itself a problem -- most files
// in a real isolinux tree are menu styling or submenu plumbing --
// rewriteBootConfigs decides whether the walk as a whole found nothing to
// rewrite. found and changed diverge exactly when every kernel line in the
// file already carries the marker: that is a legitimate idempotent no-op, not
// the same thing as a file with nothing to rewrite in the first place.
func addBootArgs(path, preseedURL string) (found bool, changed bool, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, false, err
	}
	if !kernelLine.MatchString(string(b)) {
		return false, false, nil
	}
	out := kernelLine.ReplaceAllStringFunc(string(b), func(line string) string {
		if strings.Contains(line, alreadyRewritten) {
			return line
		}
		return line + " " + bootArgs(preseedURL)
	})
	if out == string(b) {
		return true, false, nil
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return true, false, err
	}
	return true, true, nil
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w\n%s", name, err, b)
	}
	return nil
}
