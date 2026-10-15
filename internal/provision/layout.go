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

// validateDisks checks each disk's name and size, and refuses the same disk
// named twice -- a mirror is only redundant if its two halves are different
// hardware.
func validateDisks(disks []Disk) error {
	seen := make(map[string]bool, len(disks))
	for _, d := range disks {
		if err := checkShellValue("disk by-id", d.ByID); err != nil {
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
// the installer probes or wipes anything. Frame checked the serial remotely; this checks the
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
