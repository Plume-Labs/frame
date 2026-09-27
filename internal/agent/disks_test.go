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
    {"name":"sda","path":"/dev/sda","serial":"SSDTEST0000000000001","size":300000000000,"type":"disk","fstype":null,"mountpoint":null,
     "children":[{"name":"sda1","path":"/dev/sda1","serial":null,"size":299000000000,"type":"part","fstype":"ext4","mountpoint":"/"}]},
    {"name":"sdb","path":"/dev/sdb","serial":"SSDTEST0000000000002","size":300000000000,"type":"disk","fstype":null,"mountpoint":null},
    {"name":"sdc","path":"/dev/sdc","serial":"HDDTST01","size":1200000000000,"type":"disk","fstype":"ceph_bluestore","mountpoint":null}
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

	sda := disks[by["SSDTEST0000000000001"]]
	if sda.Occupancy != "mounted" {
		t.Errorf("sda Occupancy = %q, want mounted (a child is mounted at /)", sda.Occupancy)
	}
	if sda.SizeGB != 300 {
		t.Errorf("sda SizeGB = %d, want 300", sda.SizeGB)
	}

	sdb := disks[by["SSDTEST0000000000002"]]
	if sdb.Occupancy != "free" {
		t.Errorf("sdb Occupancy = %q, want free", sdb.Occupancy)
	}

	sdc := disks[by["HDDTST01"]]
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
	  {"name":"sdb","path":"/dev/sdb","serial":"HDDTST03","size":1200000000000,"type":"disk","fstype":null,"mountpoint":null}
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
	if disks[0].SerialNumber != "HDDTST03" {
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
	for _, want := range []string{"-J", "SERIAL", "PATH", "FSTYPE", "MOUNTPOINT", "SIZE", "TYPE", "WWN", "MODEL", "ROTA", "LABEL"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q does not request %s, but the parser reads it", joined, want)
		}
	}
}

// lsblkStorageLot2 exercises the columns and occupancies added for the
// storage diagnostic. Every serial and WWN here is invented.
//
//   - sda: partition sda1 is a linux_raid_member under md0, nothing mounted.
//   - sdb: whole-disk zfs_member of pool "tank", SSD (ROTA false).
//   - sdc: partition carries zfs_member, rota as the old string "1".
//   - sdd: blank disk, no WWN, rota null, model padded with spaces.
//   - sde: raid1 member whose md0 is mounted: the mount wins.
const lsblkStorageLot2 = `{"blockdevices":[
  {"name":"sda","path":"/dev/sda","serial":"SER0001","size":1000000000000,"type":"disk","fstype":null,"mountpoint":null,
   "wwn":"0x5000c500deadbeef","model":"FAKEDISK 1000","rota":true,"label":null,
   "children":[{"name":"sda1","path":"/dev/sda1","serial":null,"size":999000000000,"type":"part","fstype":"linux_raid_member","mountpoint":null,"wwn":"0x5000c500deadbeef","model":null,"rota":true,"label":"host:0",
     "children":[{"name":"md0","path":"/dev/md0","serial":null,"size":999000000000,"type":"raid1","fstype":null,"mountpoint":null,"wwn":null,"model":null,"rota":true,"label":null}]}]},
  {"name":"sdb","path":"/dev/sdb","serial":"SER0002","size":480000000000,"type":"disk","fstype":"zfs_member","mountpoint":null,
   "wwn":"0x5000c500deadbe02","model":"FAKESSD","rota":false,"label":"tank"},
  {"name":"sdc","path":"/dev/sdc","serial":"SER0003","size":1000000000000,"type":"disk","fstype":null,"mountpoint":null,
   "wwn":"0x5000c500deadbe03","model":"FAKEDISK","rota":"1","label":null,
   "children":[{"name":"sdc1","path":"/dev/sdc1","serial":null,"size":999000000000,"type":"part","fstype":"zfs_member","mountpoint":null,"wwn":null,"model":null,"rota":"1","label":"backup"}]},
  {"name":"sdd","path":"/dev/sdd","serial":"SER0004","size":300000000000,"type":"disk","fstype":null,"mountpoint":null,
   "wwn":null,"model":"  FAKE MODEL  ","rota":null,"label":null},
  {"name":"sde","path":"/dev/sde","serial":"SER0005","size":1000000000000,"type":"disk","fstype":null,"mountpoint":null,
   "wwn":"","model":"FAKEDISK","rota":"0","label":null,
   "children":[{"name":"sde1","path":"/dev/sde1","serial":null,"size":999000000000,"type":"part","fstype":"linux_raid_member","mountpoint":null,"wwn":null,"model":null,"rota":"0","label":null,
     "children":[{"name":"md1","path":"/dev/md1","serial":null,"size":999000000000,"type":"raid1","fstype":"ext4","mountpoint":"/srv","wwn":null,"model":null,"rota":"0","label":null}]}]}
]}`

func TestObserveDisksStorageLot2Fields(t *testing.T) {
	disks, err := ObserveDisks(&fakeRunner{out: lsblkStorageLot2})
	if err != nil {
		t.Fatalf("ObserveDisks: %v", err)
	}
	by := map[string]int{}
	for i, d := range disks {
		by[d.SerialNumber] = i
	}
	if len(disks) != 5 {
		t.Fatalf("got %d disks, want 5", len(disks))
	}

	tests := []struct {
		name       string
		serial     string
		occupancy  string
		wwn        string
		byIDPath   string
		model      string
		rotational *bool
		mountPoint string
		fsType     string
		members    []string
	}{
		{
			name: "md member via child partition", serial: "SER0001", occupancy: "md-member",
			wwn: "0x5000c500deadbeef", byIDPath: "/dev/disk/by-id/wwn-0x5000c500deadbeef",
			model: "FAKEDISK 1000", rotational: new(true), members: []string{"md0"},
		},
		{
			name: "whole-disk zfs member", serial: "SER0002", occupancy: "zfs-member",
			wwn: "0x5000c500deadbe02", byIDPath: "/dev/disk/by-id/wwn-0x5000c500deadbe02",
			model: "FAKESSD", rotational: new(false), fsType: "zfs_member", members: []string{"tank"},
		},
		{
			name: "zfs member via child partition, legacy string rota", serial: "SER0003", occupancy: "zfs-member",
			wwn: "0x5000c500deadbe03", byIDPath: "/dev/disk/by-id/wwn-0x5000c500deadbe03",
			model: "FAKEDISK", rotational: new(true), members: []string{"backup"},
		},
		{
			name: "no WWN, null rota, padded model", serial: "SER0004", occupancy: "free",
			model: "FAKE MODEL",
		},
		{
			name: "mount on md device wins over md membership", serial: "SER0005", occupancy: "mounted",
			model: "FAKEDISK", rotational: new(false), mountPoint: "/srv", members: []string{"md1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i, ok := by[tt.serial]
			if !ok {
				t.Fatalf("no disk %s", tt.serial)
			}
			d := disks[i]
			if d.Occupancy != tt.occupancy {
				t.Errorf("Occupancy = %q, want %q", d.Occupancy, tt.occupancy)
			}
			if d.WWN != tt.wwn {
				t.Errorf("WWN = %q, want %q", d.WWN, tt.wwn)
			}
			if d.ByIDPath != tt.byIDPath {
				t.Errorf("ByIDPath = %q, want %q", d.ByIDPath, tt.byIDPath)
			}
			if d.Model != tt.model {
				t.Errorf("Model = %q, want %q", d.Model, tt.model)
			}
			switch {
			case tt.rotational == nil && d.Rotational != nil:
				t.Errorf("Rotational = %v, want nil (lsblk said null)", *d.Rotational)
			case tt.rotational != nil && d.Rotational == nil:
				t.Errorf("Rotational = nil, want %v", *tt.rotational)
			case tt.rotational != nil && *d.Rotational != *tt.rotational:
				t.Errorf("Rotational = %v, want %v", *d.Rotational, *tt.rotational)
			}
			if d.MountPoint != tt.mountPoint {
				t.Errorf("MountPoint = %q, want %q", d.MountPoint, tt.mountPoint)
			}
			if d.FSType != tt.fsType {
				t.Errorf("FSType = %q, want %q", d.FSType, tt.fsType)
			}
			if strings.Join(d.Members, ",") != strings.Join(tt.members, ",") {
				t.Errorf("Members = %v, want %v", d.Members, tt.members)
			}
			// Path semantics are unchanged: still what lsblk calls PATH.
			if d.Path != "/dev/"+map[string]string{
				"SER0001": "sda", "SER0002": "sdb", "SER0003": "sdc", "SER0004": "sdd", "SER0005": "sde",
			}[tt.serial] {
				t.Errorf("Path = %q changed meaning", d.Path)
			}
		})
	}
}

// TestOccupancyNeverFreeForAMember is the fail-closed guard for the two new
// occupancies: a raid or zfs signature anywhere on a disk must never read
// as "free" (which would authorise a FrameDiskClaim wipe).
func TestOccupancyNeverFreeForAMember(t *testing.T) {
	sig := func(s string) *string { return &s }
	tests := []struct {
		name string
		dev  lsblkDevice
		want string
	}{
		{"md on the whole disk", lsblkDevice{Type: "disk", FSType: sig("linux_raid_member")}, "md-member"},
		{"md on a partition", lsblkDevice{Type: "disk", Children: []lsblkDevice{{Type: "part", FSType: sig("linux_raid_member")}}}, "md-member"},
		{"zfs on the whole disk", lsblkDevice{Type: "disk", FSType: sig("zfs_member")}, "zfs-member"},
		{"zfs on a partition", lsblkDevice{Type: "disk", Children: []lsblkDevice{{Type: "part", FSType: sig("zfs_member")}}}, "zfs-member"},
		{"mounted partition beats md", lsblkDevice{Type: "disk", Children: []lsblkDevice{
			{Type: "part", FSType: sig("linux_raid_member")},
			{Type: "part", FSType: sig("ext4"), MountPoint: sig("/boot")},
		}}, "mounted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := occupancyOf(tt.dev); got != tt.want {
				t.Errorf("occupancyOf = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLsblkBoolDecoding(t *testing.T) {
	tests := []struct {
		in   string
		want *bool
	}{
		{`null`, nil},
		{`true`, new(true)},
		{`false`, new(false)},
		{`"1"`, new(true)},
		{`"0"`, new(false)},
		{`""`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			var b lsblkBool
			if err := b.UnmarshalJSON([]byte(tt.in)); err != nil {
				t.Fatalf("UnmarshalJSON(%s): %v", tt.in, err)
			}
			if (b.Value == nil) != (tt.want == nil) || (b.Value != nil && *b.Value != *tt.want) {
				t.Errorf("UnmarshalJSON(%s) = %v, want %v", tt.in, b.Value, tt.want)
			}
		})
	}
}

func TestByIDPathFromWWN(t *testing.T) {
	if got := byIDPathFromWWN(""); got != "" {
		t.Errorf("empty WWN gave %q, want empty (a bare wwn- link names nothing)", got)
	}
	if got := byIDPathFromWWN("0x5000c500deadbeef"); got != "/dev/disk/by-id/wwn-0x5000c500deadbeef" {
		t.Errorf("got %q", got)
	}
}
