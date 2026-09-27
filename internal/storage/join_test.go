package storage

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	framev1beta1 "github.com/gothshoot/frame/api/frame/v1beta1"
)

func bmc(serial, location string) framev1beta1.DriveInfo {
	return framev1beta1.DriveInfo{SerialNumber: serial, Location: location, Health: "OK", SizeGB: 1000}
}

func os(serial, path string) framev1beta1.ObservedDisk {
	return framev1beta1.ObservedDisk{SerialNumber: serial, Path: path, SizeGB: 1000, Occupancy: "free"}
}

// TestJoinFindsTheMaskedDisk is the ML350 G9, reduced to three disks: the
// BMC reports all three, the kernel sees two. The BMC says Health OK about
// all three — nothing in its own document admits the third is masked — so
// the only way this fact exists at all is the join.
func TestJoinFindsTheMaskedDisk(t *testing.T) {
	got := Join(
		[]framev1beta1.DriveInfo{bmc("HDDTST01", "1I:6:4"), bmc("HDDTST04", "2I:6:5"), bmc("HDDTST05", "2I:6:8")},
		[]framev1beta1.ObservedDisk{os("HDDTST01", "/dev/sdc"), os("HDDTST04", "/dev/sdd")},
	)
	if len(got) != 1 {
		t.Fatalf("got %d divergences, want 1: %+v", len(got), got)
	}
	if got[0].SerialNumber != "HDDTST05" {
		t.Errorf("SerialNumber = %q, want HDDTST05", got[0].SerialNumber)
	}
	if got[0].Reason != "bmc-only" {
		t.Errorf("Reason = %q, want bmc-only", got[0].Reason)
	}
}

func TestJoinReportsADiskTheBMCDoesNotKnow(t *testing.T) {
	got := Join(
		[]framev1beta1.DriveInfo{bmc("HDDTST01", "1I:6:4")},
		[]framev1beta1.ObservedDisk{os("HDDTST01", "/dev/sdc"), os("NVME123", "/dev/nvme0n1")},
	)
	if len(got) != 1 || got[0].SerialNumber != "NVME123" || got[0].Reason != "os-only" {
		t.Fatalf("got %+v, want one os-only divergence for NVME123", got)
	}
}

func TestJoinReportsASizeMismatch(t *testing.T) {
	b := bmc("HDDTST01", "1I:6:4")
	b.SizeGB = 1200
	o := os("HDDTST01", "/dev/sdc")
	o.SizeGB = 1000

	got := Join([]framev1beta1.DriveInfo{b}, []framev1beta1.ObservedDisk{o})
	if len(got) != 1 || got[0].Reason != "mismatch" {
		t.Fatalf("got %+v, want one mismatch", got)
	}
	if got[0].Detail == "" {
		t.Error("a mismatch with no detail says two numbers differ without saying which")
	}
}

func TestJoinIsSilentWhenBothSourcesAgree(t *testing.T) {
	got := Join(
		[]framev1beta1.DriveInfo{bmc("HDDTST01", "1I:6:4"), bmc("HDDTST04", "2I:6:5")},
		[]framev1beta1.ObservedDisk{os("HDDTST04", "/dev/sdd"), os("HDDTST01", "/dev/sdc")},
	)
	if len(got) != 0 {
		t.Fatalf("got %+v, want none — order is not a divergence", got)
	}
}

// TestJoinNeverMatchesTwoBlanks is the whole family of defects this lot
// exists to not repeat: a check that passes for a reason other than the one
// it names. Two empty serials are equal in Go, so a naive map join pairs a
// BMC drive whose serial the controller failed to read with an OS disk
// whose serial lsblk failed to read, and reports agreement between two
// disks that were never identified at all.
func TestJoinNeverMatchesTwoBlanks(t *testing.T) {
	got := Join(
		[]framev1beta1.DriveInfo{bmc("", "1I:6:4")},
		[]framev1beta1.ObservedDisk{os("", "/dev/sdc")},
	)
	if len(got) != 2 {
		t.Fatalf("got %d divergences, want 2 (one per unidentified disk): %+v", len(got), got)
	}
	reasons := map[string]bool{}
	for _, d := range got {
		reasons[d.Reason] = true
	}
	if !reasons["bmc-only"] || !reasons["os-only"] {
		t.Fatalf("got reasons %v, want both bmc-only and os-only", reasons)
	}
}

// TestJoinReportsAnEmptyOSListAsTotalDivergence: an agent that has never
// reported is not a machine with no disks. Every BMC drive comes back
// bmc-only, which is what makes FrameDiskClaim's third guard refuse.
func TestJoinReportsAnEmptyOSListAsTotalDivergence(t *testing.T) {
	got := Join([]framev1beta1.DriveInfo{bmc("HDDTST01", "1I:6:4"), bmc("HDDTST04", "2I:6:5")}, nil)
	if len(got) != 2 {
		t.Fatalf("got %d divergences, want 2", len(got))
	}
}

// 32 BMC drives and 64 node disks, none shared: 96 divergences, where the CRD
// admits 64. The list must fit, and what it keeps must be the disks the node
// cannot see.
func TestJoinStaysWithinMaxItemsKeepingBMCOnlyFirst(t *testing.T) {
	var bmc []framev1beta1.DriveInfo
	var observed []framev1beta1.ObservedDisk
	for i := 0; i < 32; i++ {
		bmc = append(bmc, framev1beta1.DriveInfo{SerialNumber: fmt.Sprintf("Z-BMC%02d", i), Location: fmt.Sprintf("1I:1:%d", i)})
	}
	for i := 0; i < 64; i++ {
		observed = append(observed, framev1beta1.ObservedDisk{SerialNumber: fmt.Sprintf("A-OS%02d", i), Path: fmt.Sprintf("/dev/sd%d", i)})
	}
	got := Join(bmc, observed)
	if len(got) != MaxDivergences {
		t.Fatalf("got %d divergences, want %d", len(got), MaxDivergences)
	}
	bmcOnly := 0
	for _, d := range got {
		if d.Reason == "bmc-only" {
			bmcOnly++
		}
	}
	if bmcOnly != 32 {
		t.Fatalf("kept %d bmc-only divergences, want all 32 (they sort after the os-only ones, so a plain cut would drop them)", bmcOnly)
	}
}

func TestJoinClipsDetailToTheCRDLength(t *testing.T) {
	long := "/dev/" + strings.Repeat("p", 500)
	got := Join(nil, []framev1beta1.ObservedDisk{{SerialNumber: "S1", Path: long}})
	if n := utf8.RuneCountInString(got[0].Detail); n > 256 {
		t.Fatalf("detail is %d characters, the CRD admits 256", n)
	}
}
