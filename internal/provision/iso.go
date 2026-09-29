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

// bootArgsFixed is what makes the installer unattended. Virtual media offers
// no way to pass kernel arguments, so they are written into the image's own
// GRUB configuration.
//
//   - autoinstall: without it on the kernel command line subiquity finds the
//     seed and then stops to ask whether to proceed.
//   - ip=dhcp: the seed is fetched over the network BEFORE its own `network:`
//     section applies, so the live environment needs an address first. The
//     install network must offer DHCP. That the initramfs finds the one cabled
//     port among four is UNPROVEN ON HARDWARE (recette step 1).
//   - noprompt: the medium is still attached at reboot (ejected only at
//     cleanup); without it casper can stop on "remove the installation medium,
//     then press ENTER".
//   - frame=1 is our own marker. It is what addBootArgs checks to avoid
//     rewriting a line twice.
const bootArgsFixed = "frame=1 autoinstall ip=dhcp noprompt"

const alreadyRewritten = "frame=1"

// bootArgs adds the NoCloud datasource. The `;` is escaped: unescaped, GRUB
// ends the `linux` command there and the seed URL never reaches the kernel.
func bootArgs(seedURL string) string {
	return bootArgsFixed + ` ds=nocloud\;s=` + seedURL
}

// grubConfigs are the two files on the Ubuntu server ISO that carry kernel
// lines (read off ubuntu-24.04.5-live-server-amd64.iso, see
// testdata/ubuntu-24.04.5-boot/PROVENANCE.md). grub.cfg is required;
// loopback.cfg is rewritten when present.
var grubConfigs = []string{"/boot/grub/grub.cfg", "/boot/grub/loopback.cfg"}

// Remaster writes a per-machine installer image whose boot arguments fetch
// its autoinstall seed from seedURL.
//
// Only the GRUB files are replaced; the rest of the image, its volume label
// and both of its boot records are carried over by xorriso's native
// `-boot_image any replay`, without extracting the ~4 GB tree. The output is
// then read back and refused unless it still carries a BIOS and a UEFI El
// Torito entry: an image that lost one boots in one firmware mode and hangs in
// the other, on a machine reachable only by its BMC.
func Remaster(ctx context.Context, baseISO string, s Spec, seedURL, out string) error {
	if err := ValidateSpec(s); err != nil {
		return err
	}
	if strings.TrimSpace(seedURL) == "" {
		return fmt.Errorf("seedURL is empty: the image has nothing to fetch its autoinstall seed from")
	}
	// The URL lands on a GRUB command line, where whitespace splits it, a
	// quote or backslash changes the parsing, ; ends the command, and $name
	// or ${name} is expanded. A list of what is allowed, not of what is
	// forbidden: the list of forbidden characters this replaced had missed
	// $, { and }.
	if !seedURLShape.MatchString(seedURL) {
		return fmt.Errorf("seedURL %q: only letters, digits and : / . _ - are allowed; it is written onto a GRUB kernel line", seedURL)
	}

	work, err := os.MkdirTemp("", "frame-remaster-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	args := []string{"-indev", baseISO, "-outdev", out}
	for i, isoPath := range grubConfigs {
		local := filepath.Join(work, fmt.Sprintf("%d-%s", i, filepath.Base(isoPath)))
		if err := run(ctx, "xorriso", "-osirrox", "on", "-indev", baseISO, "-extract", isoPath, local); err != nil {
			if i == 0 {
				return fmt.Errorf("extracting %s from %s: %w", isoPath, baseISO, err)
			}
			continue // loopback.cfg is optional
		}
		if err := os.Chmod(local, 0o644); err != nil { // xorriso extracts read-only
			return err
		}
		found, _, err := addBootArgs(local, seedURL)
		if err != nil {
			return err
		}
		// grub.cfg is the file the firmware boots; if it has no kernel line
		// the image boots to a menu and waits for a keypress nobody gives.
		if i == 0 && !found {
			return fmt.Errorf("no kernel line found to make unattended in %s", isoPath)
		}
		args = append(args, "-map", local, isoPath)
	}
	args = append(args, "-boot_image", "any", "replay")

	_ = os.Remove(out) // -outdev refuses to overwrite a non-empty file it did not load
	if err := run(ctx, "xorriso", args...); err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}
	if err := checkBootRecords(ctx, out); err != nil {
		_ = os.Remove(out)
		return err
	}
	return nil
}

// checkBootRecords refuses an image that does not carry both a BIOS and a
// UEFI El Torito entry.
func checkBootRecords(ctx context.Context, iso string) error {
	cmd := exec.CommandContext(ctx, "xorriso", "-indev", iso, "-report_el_torito", "plain")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("reading boot records of %s: %w\n%s", iso, err, b)
	}
	var bios, uefi bool
	for line := range strings.SplitSeq(string(b), "\n") {
		if !strings.HasPrefix(line, "El Torito boot img") {
			continue
		}
		f := strings.Fields(strings.SplitN(line, ":", 2)[1])
		if len(f) >= 2 {
			bios = bios || f[1] == "BIOS"
			uefi = uefi || f[1] == "UEFI"
		}
	}
	if !bios || !uefi {
		return fmt.Errorf("built image %s lacks a boot record (BIOS %v, UEFI %v); refusing to serve an image that boots in only one firmware mode", iso, bios, uefi)
	}
	return nil
}

// seedURLShape is every character a seed URL may hold: enough for a scheme,
// a host name or IPv4 address, a port and a path of tokens. No percent
// encoding, no query, no IPv6 brackets: nothing Frame builds needs them.
var seedURLShape = regexp.MustCompile(`^[A-Za-z0-9:/._-]+$`)

var kernelLine = regexp.MustCompile(`(?m)^(\s*linux\s+.*)$`)

// menuTimeout is the menu wait on the stock image (`set timeout=30`): cut to
// one second, since nobody is at the console to choose.
var menuTimeout = regexp.MustCompile(`(?m)^set timeout=\d+$`)

// addBootArgs puts the unattended arguments on every kernel line in path that
// doesn't already carry them, and reports separately whether it found a kernel
// line at all (found) and whether it changed the file (changed). found and
// changed diverge exactly when every kernel line already carries the marker:
// a legitimate idempotent no-op, not a file with nothing to rewrite.
func addBootArgs(path, seedURL string) (found bool, changed bool, err error) {
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
		return withBootArgs(line, seedURL)
	})
	out = menuTimeout.ReplaceAllString(out, "set timeout=1")
	if out == string(b) {
		return true, false, nil
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return true, false, err
	}
	return true, true, nil
}

// tripleDash matches the kernel-argument separator as a standalone token.
// Every kernel line on the server ISO carries one: "linux /casper/vmlinuz  ---".
var tripleDash = regexp.MustCompile(`(?:^|\s)---(?:\s|$)`)

// withBootArgs puts the unattended arguments BEFORE "---". What follows "---"
// is copied onto the installed system's own kernel command line, where the
// seed URL would persist across every boot and be readable in /proc/cmdline.
func withBootArgs(line, seedURL string) string {
	args := bootArgs(seedURL)
	if m := tripleDash.FindStringIndex(line); m != nil {
		return line[:m[0]] + " " + args + line[m[0]:]
	}
	return line + " " + args
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w\n%s", name, err, b)
	}
	return nil
}
