package provision

import (
	"strings"
	"testing"
)

func withLayout(kind LayoutKind, disks ...Disk) Spec {
	s := goodSpec()
	s.Layout = Layout{Kind: kind, Disks: disks}
	return s
}

func disk(byID string) Disk { return Disk{ByID: byID, SizeBytes: 300 << 30} }

// mirror is exactly two disks. One is a typo that would silently install on a
// single disk with no redundancy; three is a typo that would consume a disk
// nobody meant to touch.
func TestValidateSpecMirrorRequiresExactlyTwoDisks(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		var disks []Disk
		for i := range n {
			disks = append(disks, disk("/dev/disk/by-id/scsi-"+string(rune('a'+i))))
		}
		if err := ValidateSpec(withLayout(LayoutMirror, disks...)); err == nil {
			t.Errorf("mirror with %d disks: want error, got nil", n)
		}
	}
}

func TestValidateSpecSingleDiskRequiresExactlyOneDisk(t *testing.T) {
	for _, disks := range [][]Disk{nil, {disk("/dev/disk/by-id/scsi-a"), disk("/dev/disk/by-id/scsi-b")}} {
		if err := ValidateSpec(withLayout(LayoutSingleDisk, disks...)); err == nil {
			t.Errorf("single-disk with %d disks: want error, got nil", len(disks))
		}
	}
}

// The whole point of by-id: /dev/sda reorders between boots and controllers.
// The traversal case is the one only an exact match closes -- it carries the
// right prefix and names a different device.
func TestValidateSpecRejectsKernelNames(t *testing.T) {
	for _, byID := range []string{"/dev/sda", "/dev/disk/by-id/../../sda", "/dev/disk/by-id/a'b"} {
		if err := ValidateSpec(withLayout(LayoutSingleDisk, disk(byID))); err == nil {
			t.Errorf("want error for %q, got nil", byID)
		}
	}
}

func TestValidateSpecRejectsTheSameDiskTwice(t *testing.T) {
	d := disk("/dev/disk/by-id/scsi-a")
	if err := ValidateSpec(withLayout(LayoutMirror, d, d)); err == nil {
		t.Fatal("a mirror of one disk with itself was accepted")
	}
}

// "raw" was removed from the API; it is now just another unknown kind.
func TestValidateSpecRejectsUnknownLayoutKinds(t *testing.T) {
	for _, k := range []LayoutKind{"raw", "", "raid5"} {
		if err := ValidateSpec(withLayout(k, disk("/dev/disk/by-id/scsi-a"))); err == nil {
			t.Errorf("layout kind %q accepted", k)
		}
	}
}

func TestValidateSpecRejectsADiskWithUnknownSize(t *testing.T) {
	if err := ValidateSpec(withLayout(LayoutSingleDisk, Disk{ByID: "/dev/disk/by-id/scsi-a"})); err == nil {
		t.Fatal("want error for a disk with SizeBytes <= 0, got nil")
	}
}

// Every named disk is size-asserted on the machine before anything is wiped.
func TestDiskSizeAssertionNamesEveryDiskInTheLayout(t *testing.T) {
	l := Layout{Kind: LayoutMirror, Disks: []Disk{disk("/dev/disk/by-id/scsi-aaa"), disk("/dev/disk/by-id/scsi-bbb")}}
	got := diskSizeAssertion(l)
	for _, d := range l.Disks {
		if !strings.Contains(got, "blockdev --getsize64 "+d.ByID) {
			t.Errorf("disk %s is not size-asserted: %q", d.ByID, got)
		}
	}
}
