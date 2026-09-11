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
//
// The bare /dev/sda case cannot tell a prefix check from a real one --
// /dev/disk/by-idX would satisfy a prefix check too. The path-traversal case
// is the one that only an exact match closes: it carries the right prefix and
// names a different device entirely.
func TestPartmanRecipeRejectsKernelNames(t *testing.T) {
	for _, byID := range []string{"/dev/sda", "/dev/disk/by-id/../../sda"} {
		l := Layout{Kind: LayoutSingleDisk, Disks: []Disk{{ByID: byID, SizeBytes: 300 << 30}}}
		if _, err := PartmanRecipe(l); err == nil {
			t.Errorf("want error for %q, got nil", byID)
		}
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
	for _, want := range []string{
		"scsi-aaa", "scsi-bbb",
		"partman-auto/method string raid",
		"partman-auto/expert_recipe string",
		"/dev/disk/by-id/scsi-aaa-part2#/dev/disk/by-id/scsi-bbb-part2",
		"grub-installer/bootdev string /dev/disk/by-id/scsi-aaa /dev/disk/by-id/scsi-bbb",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("recipe missing %q:\n%s", want, got)
		}
	}
}

// A mirror is only redundant if its two halves are different hardware. The
// same disk named twice would render a "raid" recipe over one physical disk.
func TestPartmanRecipeRejectsTheSameDiskTwice(t *testing.T) {
	l := Layout{Kind: LayoutMirror, Disks: []Disk{
		{ByID: "/dev/disk/by-id/scsi-aaa", SizeBytes: 300 << 30},
		{ByID: "/dev/disk/by-id/scsi-aaa", SizeBytes: 300 << 30},
	}}
	if _, err := PartmanRecipe(l); err == nil {
		t.Fatal("want error for the same disk named twice, got nil")
	}
}

func TestPartmanRecipeRejectsAnUnknownLayoutKind(t *testing.T) {
	l := Layout{Kind: "raid5", Disks: []Disk{{ByID: "/dev/disk/by-id/scsi-x", SizeBytes: 300 << 30}}}
	if _, err := PartmanRecipe(l); err == nil {
		t.Fatal("want error for an unknown layout kind, got nil")
	}
}

func TestPartmanRecipeRejectsAnEmptyRawRecipe(t *testing.T) {
	l := Layout{Kind: LayoutRaw, Raw: "   ", Disks: []Disk{{ByID: "/dev/disk/by-id/scsi-x", SizeBytes: 300 << 30}}}
	if _, err := PartmanRecipe(l); err == nil {
		t.Fatal("want error for an empty raw recipe, got nil")
	}
}

func TestPartmanRecipeRejectsADiskWithUnknownSize(t *testing.T) {
	l := Layout{Kind: LayoutSingleDisk, Disks: []Disk{{ByID: "/dev/disk/by-id/scsi-x", SizeBytes: 0}}}
	if _, err := PartmanRecipe(l); err == nil {
		t.Fatal("want error for a disk with SizeBytes <= 0, got nil")
	}
}

// A raw layout used to render `early_command string true || {...}` -- no
// assertion at all, because diskSizeAssertion special-cased LayoutRaw. A raw
// layout must still name at least one disk, and that disk's size must still
// be asserted before partman runs.
func TestPartmanRecipeRawRequiresAtLeastOneDisk(t *testing.T) {
	l := Layout{Kind: LayoutRaw, Raw: "some-recipe-string"}
	if _, err := PartmanRecipe(l); err == nil {
		t.Fatal("want error for a raw layout with no disks named, got nil")
	}
}

func TestDiskSizeAssertionAssertsEvenForRawLayouts(t *testing.T) {
	l := Layout{Kind: LayoutRaw, Raw: "some-recipe-string", Disks: []Disk{
		{ByID: "/dev/disk/by-id/scsi-aaa", SizeBytes: 300 << 30},
	}}
	got := diskSizeAssertion(l)
	if !strings.Contains(got, "blockdev --getsize64 /dev/disk/by-id/scsi-aaa") {
		t.Errorf("a raw layout's named disk size is not asserted: %q", got)
	}
}
