package provision

import (
	"fmt"
	"regexp"
	"strings"
)

// byIDName matches a /dev/disk/by-id name with no subdirectories. by-id names
// never nest, so an exact match costs nothing and closes a shape a prefix
// check does not: /dev/disk/by-id/../../sda has the right prefix and names a
// different device entirely.
var byIDName = regexp.MustCompile(`^/dev/disk/by-id/[A-Za-z0-9_.:+-]+$`)

// PartmanRecipe turns a Layout into partman-auto stanzas.
//
// Disks are named by /dev/disk/by-id. A kernel name like /dev/sda reorders
// between boots and between controllers, so naming one means installing on
// whatever happens to be first that day. On the ML350 Gen9 this is not
// hypothetical: IML entry 1832 warns that residual logical-volume metadata can
// hide disks from the host, which changes the ordering.
func PartmanRecipe(l Layout) (string, error) {
	if l.Kind == LayoutRaw {
		if strings.TrimSpace(l.Raw) == "" {
			return "", fmt.Errorf("layout raw: recipe is empty")
		}
		if err := checkPreseedValue("layout raw recipe", l.Raw); err != nil {
			return "", err
		}
		// A raw recipe still names disks so their size can be asserted on the
		// machine before partman runs. Without this, early_command had nothing
		// to check and the size assertion silently became `true`.
		if len(l.Disks) == 0 {
			return "", fmt.Errorf("layout raw: at least one disk must be named, so its size can be asserted before partman runs")
		}
		if err := validateDisks(l.Disks); err != nil {
			return "", err
		}
		return l.Raw, nil
	}

	want := map[LayoutKind]int{LayoutSingleDisk: 1, LayoutMirror: 2}[l.Kind]
	if want == 0 {
		return "", fmt.Errorf("layout %q: unknown kind", l.Kind)
	}
	if len(l.Disks) != want {
		return "", fmt.Errorf("layout %s: needs exactly %d disks, got %d", l.Kind, want, len(l.Disks))
	}
	if err := validateDisks(l.Disks); err != nil {
		return "", err
	}

	names := make([]string, len(l.Disks))
	for i, d := range l.Disks {
		names[i] = d.ByID
	}

	var b strings.Builder
	if l.Kind == LayoutSingleDisk {
		fmt.Fprintf(&b, "d-i partman-auto/disk string %s\n", names[0])
		b.WriteString("d-i partman-auto/method string regular\n")
		b.WriteString("d-i partman-auto/choose_recipe select atomic\n")
	} else {
		a, bb := names[0], names[1]
		b.WriteString("d-i partman-auto/method string raid\n")
		fmt.Fprintf(&b, "d-i partman-auto/disk string %s %s\n", a, bb)
		b.WriteString("d-i partman-auto/expert_recipe string \\\n")
		b.WriteString("      frameraid :: \\\n")
		b.WriteString("              538 538 1075 free $iflabel{ gpt } $reusemethod{ } method{ efi } format{ } . \\\n")
		b.WriteString("              1000 5000 -1 raid $lvmignore{ } method{ raid } . \\\n")
		b.WriteString("      .\n")
		// The recipe is selected by the label used above (frameraid), not by a
		// built-in name like atomic: choose_recipe has no other way to find an
		// expert_recipe stanza.
		b.WriteString("d-i partman-auto/choose_recipe select frameraid\n")
		fmt.Fprintf(&b, "d-i partman-auto-raid/recipe string \\\n      1 2 0 ext4 / %s-part2#%s-part2 \\\n      .\n", a, bb)
		b.WriteString("d-i partman-md/device_remove_md boolean true\n")
		b.WriteString("d-i partman-md/confirm boolean true\n")
		b.WriteString("d-i partman-md/confirm_nooverwrite boolean true\n")
		b.WriteString("d-i partman-basicfilesystems/no_swap boolean false\n")
		// The ESP is not mirrored: d-i will not place an EFI System Partition on
		// software RAID. If the first disk is lost, the machine needs a manual
		// boot repair to point at the ESP on the surviving disk. That is a
		// limitation of d-i, not an oversight -- written down rather than
		// hidden.
		fmt.Fprintf(&b, "d-i grub-installer/bootdev string %s %s\n", a, bb)
	}
	b.WriteString("d-i partman/confirm_write_new_label boolean true\n")
	b.WriteString("d-i partman/choose_partition select finish\n")
	b.WriteString("d-i partman/confirm boolean true\n")
	b.WriteString("d-i partman/confirm_nooverwrite boolean true\n")
	return b.String(), nil
}

// validateDisks checks each disk's name and size, and refuses the same disk
// named twice -- a mirror is only redundant if its two halves are different
// hardware.
func validateDisks(disks []Disk) error {
	seen := make(map[string]bool, len(disks))
	for _, d := range disks {
		if err := checkPreseedValue("disk by-id", d.ByID); err != nil {
			return err
		}
		if !byIDName.MatchString(d.ByID) {
			return fmt.Errorf("disk %q: must be a /dev/disk/by-id path with no subdirectories", d.ByID)
		}
		if d.SizeBytes <= 0 {
			return fmt.Errorf("disk %q: size must be known, so the installer can refuse the wrong disk", d.ByID)
		}
		if seen[d.ByID] {
			return fmt.Errorf("disk %q: named twice in the same layout; a mirror needs two different disks", d.ByID)
		}
		seen[d.ByID] = true
	}
	return nil
}

// diskSizeAssertion is the last-ditch check, run on the machine itself before
// partman touches anything. Frame checked the serial remotely; this checks the
// disks locally. Both can be wrong, but not in the same way.
func diskSizeAssertion(l Layout) string {
	if len(l.Disks) == 0 {
		return "true"
	}
	var checks []string
	for _, d := range l.Disks {
		checks = append(checks, fmt.Sprintf(
			"[ \"$(blockdev --getsize64 %s 2>/dev/null)\" = \"%d\" ]", d.ByID, d.SizeBytes))
	}
	return strings.Join(checks, " && ")
}
