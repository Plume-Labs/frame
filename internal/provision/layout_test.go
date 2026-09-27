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

// preseedValue returns the value of one `d-i <question> <type> <value>` line,
// with its backslash continuations joined the way debconf-set-selections
// joins them.
func preseedValue(t *testing.T, preseed, question string) string {
	t.Helper()
	joined := strings.ReplaceAll(preseed, "\\\n", " ")
	for line := range strings.SplitSeq(joined, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "d-i" && f[1] == question {
			return strings.Join(f[3:], " ")
		}
	}
	t.Fatalf("no %s line in:\n%s", question, preseed)
	return ""
}

// partman-auto reads an expert recipe as `<name> ::` followed by partition
// stanzas, each closed by a lone ".". A "." with nothing before it closes an
// empty stanza, which partman turns into one more partition with no method
// and no mount point. On the ML350 Gen9, installing in QEMU, that stopped d-i
// at "Identical mount points". Every stanza must therefore carry something,
// and the recipe must declare exactly the two partitions it means: the ESP
// and the RAID member.
func TestPartmanExpertRecipeHasNoEmptyStanza(t *testing.T) {
	l := Layout{Kind: LayoutMirror, Disks: []Disk{
		{ByID: "/dev/disk/by-id/scsi-aaa", SizeBytes: 300 << 30},
		{ByID: "/dev/disk/by-id/scsi-bbb", SizeBytes: 300 << 30},
	}}
	got, err := PartmanRecipe(l)
	if err != nil {
		t.Fatal(err)
	}
	recipe := preseedValue(t, got, "partman-auto/expert_recipe")
	_, body, ok := strings.Cut(recipe, "::")
	if !ok {
		t.Fatalf("expert_recipe has no `::` header: %q", recipe)
	}
	fields := strings.Fields(body)
	if len(fields) == 0 || fields[len(fields)-1] != "." {
		t.Fatalf("expert_recipe must end by closing its last stanza with \".\": %q", recipe)
	}
	var stanzas [][]string
	var cur []string
	for _, f := range fields {
		if f == "." {
			if len(cur) == 0 {
				t.Fatalf("expert_recipe closes an empty stanza (a \".\" with nothing before it): %q", recipe)
			}
			stanzas = append(stanzas, cur)
			cur = nil
			continue
		}
		cur = append(cur, f)
	}
	if len(stanzas) != 2 {
		t.Fatalf("expert_recipe declares %d partitions, want 2 (ESP + RAID member): %q", len(stanzas), recipe)
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

// The raw escape hatch is gone (see InstallLayout's comment in
// api/frame/v1beta1/frameinstall_types.go). What replaces its tests is this:
// "raw" is now just another unknown kind, refused by the same branch that
// refuses any other, rather than a shape the apiserver accepts and this
// function then rejects a phase later.
func TestPartmanRecipeRejectsTheRemovedRawKind(t *testing.T) {
	l := Layout{Kind: "raw", Disks: []Disk{{ByID: "/dev/disk/by-id/scsi-x", SizeBytes: 300 << 30}}}
	if _, err := PartmanRecipe(l); err == nil {
		t.Fatal("want error for the removed raw layout kind, got nil")
	}
}

func TestPartmanRecipeRejectsADiskWithUnknownSize(t *testing.T) {
	l := Layout{Kind: LayoutSingleDisk, Disks: []Disk{{ByID: "/dev/disk/by-id/scsi-x", SizeBytes: 0}}}
	if _, err := PartmanRecipe(l); err == nil {
		t.Fatal("want error for a disk with SizeBytes <= 0, got nil")
	}
}

// Every layout that renders at all names its disks, and every named disk is
// size-asserted on the machine before partman runs. This used to have to be
// said specially for the raw kind, which had special-cased itself out of the
// assertion entirely; with raw gone there is exactly one path and this is
// the check that it still fires.
func TestDiskSizeAssertionNamesEveryDiskInTheLayout(t *testing.T) {
	l := Layout{Kind: LayoutMirror, Disks: []Disk{
		{ByID: "/dev/disk/by-id/scsi-aaa", SizeBytes: 300 << 30},
		{ByID: "/dev/disk/by-id/scsi-bbb", SizeBytes: 300 << 30},
	}}
	got := diskSizeAssertion(l)
	for _, d := range l.Disks {
		if !strings.Contains(got, "blockdev --getsize64 "+d.ByID) {
			t.Errorf("disk %s is not size-asserted: %q", d.ByID, got)
		}
	}
}
