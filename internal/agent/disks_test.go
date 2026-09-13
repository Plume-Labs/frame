package agent

import (
	"errors"
	"strings"
	"testing"
)

// fakeRunner returns a canned lsblk payload and records the argv it was
// given, so the test can assert both the parsing and the invocation.
type fakeRunner struct {
	out  string
	err  error
	name string
	args []string
}

func (f *fakeRunner) Run(name string, args ...string) (string, error) {
	f.name = name
	f.args = args
	return f.out, f.err
}

// lsblkG9 is trimmed from the ML350 G9: three 300 GB disks, four 1200 GB
// disks, and no 1000 GB disk at all — the MM1000GFJTE the BMC reports is
// masked by residual logical-unit metadata and never reaches the kernel.
// sdc carries a Ceph OSD, sda is mounted, sdb is untouched.
const lsblkG9 = `{
  "blockdevices": [
    {"name":"sda","path":"/dev/sda","serial":"S420YJWS0000K6319L3R","size":300000000000,"type":"disk","fstype":null,"mountpoint":null,
     "children":[{"name":"sda1","path":"/dev/sda1","serial":null,"size":299000000000,"type":"part","fstype":"ext4","mountpoint":"/"}]},
    {"name":"sdb","path":"/dev/sdb","serial":"S421NQ0S0000K645A8C8","size":300000000000,"type":"disk","fstype":null,"mountpoint":null},
    {"name":"sdc","path":"/dev/sdc","serial":"KZK245ZG","size":1200000000000,"type":"disk","fstype":"ceph_bluestore","mountpoint":null}
  ]
}`

func TestObserveDisksJoinsOnSerialAndReportsOccupancy(t *testing.T) {
	r := &fakeRunner{out: lsblkG9}
	disks, err := ObserveDisks(r)
	if err != nil {
		t.Fatalf("ObserveDisks: %v", err)
	}
	if len(disks) != 3 {
		t.Fatalf("got %d disks, want 3 (partitions are not disks)", len(disks))
	}

	by := map[string]int{}
	for i, d := range disks {
		by[d.SerialNumber] = i
	}

	sda := disks[by["S420YJWS0000K6319L3R"]]
	if sda.Occupancy != "mounted" {
		t.Errorf("sda Occupancy = %q, want mounted (a child is mounted at /)", sda.Occupancy)
	}
	if sda.SizeGB != 300 {
		t.Errorf("sda SizeGB = %d, want 300", sda.SizeGB)
	}

	sdb := disks[by["S421NQ0S0000K645A8C8"]]
	if sdb.Occupancy != "free" {
		t.Errorf("sdb Occupancy = %q, want free", sdb.Occupancy)
	}

	sdc := disks[by["KZK245ZG"]]
	if sdc.Occupancy != "ceph-osd" {
		t.Errorf("sdc Occupancy = %q, want ceph-osd", sdc.Occupancy)
	}
}

// TestObserveDisksNeverReturnsAnEmptySerial is the guard behind guard 1 of
// the spec: FrameDiskClaim compares a hand-typed serial against these
// entries, and two empty strings are equal. A disk whose serial lsblk could
// not read must not enter the list wearing "" — it would match any claim
// that also left the field blank, and that is how a disk gets wiped with no
// confirmation.
func TestObserveDisksNeverReturnsAnEmptySerial(t *testing.T) {
	r := &fakeRunner{out: `{"blockdevices":[
	  {"name":"sda","path":"/dev/sda","serial":null,"size":300000000000,"type":"disk","fstype":null,"mountpoint":null},
	  {"name":"sdb","path":"/dev/sdb","serial":"KZK39VSH","size":1200000000000,"type":"disk","fstype":null,"mountpoint":null}
	]}`}
	disks, err := ObserveDisks(r)
	if err != nil {
		t.Fatalf("ObserveDisks: %v", err)
	}
	for _, d := range disks {
		if d.SerialNumber == "" {
			t.Fatalf("disk %s entered the inventory with an empty serial", d.Path)
		}
	}
	if len(disks) != 1 {
		t.Fatalf("got %d disks, want 1 (the serial-less disk is dropped)", len(disks))
	}
	if disks[0].SerialNumber != "KZK39VSH" {
		t.Fatalf("kept the wrong disk: %q", disks[0].SerialNumber)
	}
}

// TestObserveDisksUnrecognisedFilesystemFallsBackToInUse guards the
// occupancy fallback: FrameDiskClaim's third guard fails closed on
// anything that is not "free", so a filesystem signature occupancyOf does
// not recognise must never be reported as "free". xfs is a real,
// unremarkable filesystem deliberately not named in the switch, so it
// exercises the default branch.
func TestObserveDisksUnrecognisedFilesystemFallsBackToInUse(t *testing.T) {
	r := &fakeRunner{out: `{"blockdevices":[
	  {"name":"sdd","path":"/dev/sdd","serial":"XFS00001","size":1200000000000,"type":"disk","fstype":"xfs","mountpoint":null}
	]}`}
	disks, err := ObserveDisks(r)
	if err != nil {
		t.Fatalf("ObserveDisks: %v", err)
	}
	if len(disks) != 1 {
		t.Fatalf("got %d disks, want 1", len(disks))
	}
	if disks[0].Occupancy != "in-use" {
		t.Errorf("sdd Occupancy = %q, want in-use (an unrecognised filesystem must err towards occupied)", disks[0].Occupancy)
	}
}

func TestObserveDisksSurfacesRunnerFailure(t *testing.T) {
	r := &fakeRunner{err: errors.New("nsenter: permission denied")}
	if _, err := ObserveDisks(r); err == nil {
		t.Fatal("want an error when lsblk cannot run; a failed read must not look like a node with no disks")
	}
}

func TestObserveDisksAsksForTheFieldsItParses(t *testing.T) {
	r := &fakeRunner{out: `{"blockdevices":[]}`}
	if _, err := ObserveDisks(r); err != nil {
		t.Fatalf("ObserveDisks: %v", err)
	}
	if r.name != "lsblk" {
		t.Fatalf("ran %q, want lsblk", r.name)
	}
	joined := strings.Join(r.args, " ")
	for _, want := range []string{"-J", "SERIAL", "PATH", "FSTYPE", "MOUNTPOINT", "SIZE", "TYPE"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q does not request %s, but the parser reads it", joined, want)
		}
	}
}
