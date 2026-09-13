package storage

import (
	"testing"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
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
		[]framev1beta1.DriveInfo{bmc("KZK245ZG", "1I:6:4"), bmc("L0JG1VJJ", "2I:6:5"), bmc("W4722RRA", "2I:6:8")},
		[]framev1beta1.ObservedDisk{os("KZK245ZG", "/dev/sdc"), os("L0JG1VJJ", "/dev/sdd")},
	)
	if len(got) != 1 {
		t.Fatalf("got %d divergences, want 1: %+v", len(got), got)
	}
	if got[0].SerialNumber != "W4722RRA" {
		t.Errorf("SerialNumber = %q, want W4722RRA", got[0].SerialNumber)
	}
	if got[0].Reason != "bmc-only" {
		t.Errorf("Reason = %q, want bmc-only", got[0].Reason)
	}
}

func TestJoinReportsADiskTheBMCDoesNotKnow(t *testing.T) {
	got := Join(
		[]framev1beta1.DriveInfo{bmc("KZK245ZG", "1I:6:4")},
		[]framev1beta1.ObservedDisk{os("KZK245ZG", "/dev/sdc"), os("NVME123", "/dev/nvme0n1")},
	)
	if len(got) != 1 || got[0].SerialNumber != "NVME123" || got[0].Reason != "os-only" {
		t.Fatalf("got %+v, want one os-only divergence for NVME123", got)
	}
}

func TestJoinReportsASizeMismatch(t *testing.T) {
	b := bmc("KZK245ZG", "1I:6:4")
	b.SizeGB = 1200
	o := os("KZK245ZG", "/dev/sdc")
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
		[]framev1beta1.DriveInfo{bmc("KZK245ZG", "1I:6:4"), bmc("L0JG1VJJ", "2I:6:5")},
		[]framev1beta1.ObservedDisk{os("L0JG1VJJ", "/dev/sdd"), os("KZK245ZG", "/dev/sdc")},
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
	got := Join([]framev1beta1.DriveInfo{bmc("KZK245ZG", "1I:6:4"), bmc("L0JG1VJJ", "2I:6:5")}, nil)
	if len(got) != 2 {
		t.Fatalf("got %d divergences, want 2", len(got))
	}
}
