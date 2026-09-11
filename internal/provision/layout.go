package provision

import (
	"fmt"
	"strings"
)

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
		return l.Raw, nil
	}

	want := map[LayoutKind]int{LayoutSingleDisk: 1, LayoutMirror: 2}[l.Kind]
	if want == 0 {
		return "", fmt.Errorf("layout %q: unknown kind", l.Kind)
	}
	if len(l.Disks) != want {
		return "", fmt.Errorf("layout %s: needs exactly %d disks, got %d", l.Kind, want, len(l.Disks))
	}
	for _, d := range l.Disks {
		if !strings.HasPrefix(d.ByID, "/dev/disk/by-id/") {
			return "", fmt.Errorf("disk %q: must be a /dev/disk/by-id path", d.ByID)
		}
		if d.SizeBytes <= 0 {
			return "", fmt.Errorf("disk %q: size must be known, so the installer can refuse the wrong disk", d.ByID)
		}
	}

	names := make([]string, len(l.Disks))
	for i, d := range l.Disks {
		names[i] = d.ByID
	}
	disks := strings.Join(names, " ")

	var b strings.Builder
	fmt.Fprintf(&b, "d-i partman-auto/disk string %s\n", disks)
	if l.Kind == LayoutSingleDisk {
		b.WriteString("d-i partman-auto/method string regular\n")
	} else {
		b.WriteString("d-i partman-auto/method string raid\n")
		fmt.Fprintf(&b, "d-i partman-auto-raid/recipe string 1 2 0 ext4 / %s#%s .\n", names[0], names[1])
		b.WriteString("d-i partman-md/device_remove_md boolean true\n")
		b.WriteString("d-i partman-md/confirm boolean true\n")
		b.WriteString("d-i partman-md/confirm_nooverwrite boolean true\n")
	}
	b.WriteString("d-i partman-auto/choose_recipe select atomic\n")
	b.WriteString("d-i partman/confirm_write_new_label boolean true\n")
	b.WriteString("d-i partman/choose_partition select finish\n")
	b.WriteString("d-i partman/confirm boolean true\n")
	b.WriteString("d-i partman/confirm_nooverwrite boolean true\n")
	return b.String(), nil
}

// diskSizeAssertion is the last-ditch check, run on the machine itself before
// partman touches anything. Frame checked the serial remotely; this checks the
// disks locally. Both can be wrong, but not in the same way.
func diskSizeAssertion(l Layout) string {
	if l.Kind == LayoutRaw {
		return "true"
	}
	var checks []string
	for _, d := range l.Disks {
		checks = append(checks, fmt.Sprintf(
			"[ \"$(blockdev --getsize64 %s 2>/dev/null)\" = \"%d\" ]", d.ByID, d.SizeBytes))
	}
	return strings.Join(checks, " && ")
}
