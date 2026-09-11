package provision

import "strings"
import "testing"

// mirror is exactly two disks. One is a typo that would silently install on a
// single disk with no redundancy; three is a typo that would consume a disk
// nobody meant to touch.
func TestPartmanRecipeMirrorRequiresExactlyTwoDisks(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		disks := make([]Disk, n)
		for i := range disks {
			disks[i] = Disk{ByID: "/dev/disk/by-id/scsi-x", SizeBytes: 300 << 30}
		}
		if _, err := PartmanRecipe(Layout{Kind: LayoutMirror, Disks: disks}); err == nil {
			t.Errorf("mirror with %d disks: want error, got nil", n)
		}
	}
}

func TestPartmanRecipeSingleDiskRequiresExactlyOneDisk(t *testing.T) {
	for _, n := range []int{0, 2} {
		disks := make([]Disk, n)
		for i := range disks {
			disks[i] = Disk{ByID: "/dev/disk/by-id/scsi-x", SizeBytes: 300 << 30}
		}
		if _, err := PartmanRecipe(Layout{Kind: LayoutSingleDisk, Disks: disks}); err == nil {
			t.Errorf("single-disk with %d disks: want error, got nil", n)
		}
	}
}

// The whole point of by-id. /dev/sda reorders between boots and between
// controllers; naming it would install on whatever happens to be first.
func TestPartmanRecipeRejectsKernelNames(t *testing.T) {
	l := Layout{Kind: LayoutSingleDisk, Disks: []Disk{{ByID: "/dev/sda", SizeBytes: 300 << 30}}}
	if _, err := PartmanRecipe(l); err == nil {
		t.Fatal("want error for /dev/sda, got nil")
	}
}

func TestPartmanRecipeNamesEveryDiskItWasGiven(t *testing.T) {
	l := Layout{Kind: LayoutMirror, Disks: []Disk{
		{ByID: "/dev/disk/by-id/scsi-aaa", SizeBytes: 300 << 30},
		{ByID: "/dev/disk/by-id/scsi-bbb", SizeBytes: 300 << 30},
	}}
	got, err := PartmanRecipe(l)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"scsi-aaa", "scsi-bbb", "partman-auto/method string raid"} {
		if !strings.Contains(got, want) {
			t.Errorf("recipe missing %q:\n%s", want, got)
		}
	}
}
