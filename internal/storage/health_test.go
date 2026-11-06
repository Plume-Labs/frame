package storage

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

var (
	now      = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	defaults = ThresholdsFrom(nil)
)

// healthyStatus is a node with a clean md mirror and one ZFS pool: md0
// a clean raid1, the pool ONLINE and scrubbed, one SSD reporting endurance.
func healthyStatus() *framev1beta1.FrameMachineStatus {
	seen := metav1.NewTime(now.Add(-time.Minute))
	zero, capacity, wear := int32(0), int32(50), int32(10)
	return &framev1beta1.FrameMachineStatus{
		Storage: &framev1beta1.MachineStorage{
			ObservedAt: &seen,
			Local: &framev1beta1.LocalStorage{
				MD: framev1beta1.MDStatus{State: framev1beta1.StackPresent, Arrays: []framev1beta1.MDArray{{
					Name: "md0", Level: "raid1", State: "active", RaidDisks: 2, ActiveDisks: 2, Slots: "UU",
					Members: []framev1beta1.MDMember{{Device: "sdb2", Serial: "S-B"}, {Device: "sda2", Serial: "S-A"}},
				}}},
				ZFS: framev1beta1.ZFSStatus{State: framev1beta1.StackPresent, Pools: []framev1beta1.ZPool{{
					Name: "tank", Health: "ONLINE", CapacityPercent: &capacity, ScrubErrors: &zero,
					Scan: "scrub repaired 0B in 01:00:00 with 0 errors on Sun Nov  1",
					Vdevs: []framev1beta1.ZVdev{
						{Name: "mirror-0", Depth: 0, State: "ONLINE"},
						{Name: "/dev/disk/by-id/ata-A", Depth: 1, State: "ONLINE"},
						{Name: "/dev/disk/by-id/ata-B", Depth: 1, State: "ONLINE"},
						{Name: "spares", Depth: 0},
						{Name: "/dev/disk/by-id/ata-C", Depth: 1, State: "AVAIL"},
					},
				}}},
				LVM: framev1beta1.LVMStatus{State: framev1beta1.StackAbsent},
			},
		},
		Inventory: &framev1beta1.MachineInventory{Drives: []framev1beta1.DriveInfo{
			{SerialNumber: "S-A", Location: "1I:6:1", MediaType: "SSD", SSDEnduranceUsedPercent: &wear},
			{SerialNumber: "H-1", Location: "2I:6:5", MediaType: "HDD"},
		}},
	}
}

func degradeMD(st *framev1beta1.FrameMachineStatus) {
	arr := &st.Storage.Local.MD.Arrays[0]
	arr.Degraded, arr.ActiveDisks, arr.Slots = true, 1, "U_"
	arr.Members[1].Failed = true
}

func keys(a Assessment) map[Key]bool {
	out := map[Key]bool{}
	for _, f := range a.Findings {
		out[f.Key] = true
	}
	return out
}

func TestHealthyMachineHasNoFindingAndEveryConditionFalse(t *testing.T) {
	a := Assess(healthyStatus(), defaults, true, nil, now)
	if len(a.Findings) != 0 {
		t.Fatalf("findings: %+v", a.Findings)
	}
	for _, c := range Checks {
		if v := a.Verdict(c, nil); v.Status != "False" {
			t.Errorf("%s = %+v", c, v)
		}
	}
}

func TestDegradedStorageIsFound(t *testing.T) {
	v := func(st *framev1beta1.FrameMachineStatus) *framev1beta1.ZVdev {
		return &st.Storage.Local.ZFS.Pools[0].Vdevs[2]
	}
	tests := []struct {
		name   string
		mutate func(*framev1beta1.FrameMachineStatus)
		want   Key
	}{
		{"md array with a failed member", degradeMD, Key{CheckDegraded, "md0"}},
		{"pool not ONLINE", func(st *framev1beta1.FrameMachineStatus) { st.Storage.Local.ZFS.Pools[0].Health = "DEGRADED" }, Key{CheckDegraded, "tank"}},
		{"mirror leaf faulted, pool still says ONLINE", func(st *framev1beta1.FrameMachineStatus) { v(st).State = "FAULTED" }, Key{CheckDegraded, "tank"}},
		{"leaf with checksum errors", func(st *framev1beta1.FrameMachineStatus) { v(st).ChecksumErrors = 3 }, Key{CheckDegraded, "tank"}},
		{"scrub errors", func(st *framev1beta1.FrameMachineStatus) {
			n := int32(2)
			st.Storage.Local.ZFS.Pools[0].ScrubErrors = &n
		}, Key{CheckScrubErrors, "tank"}},
		{"pool at the threshold", func(st *framev1beta1.FrameMachineStatus) {
			n := defaults.ZFSCapacityPercent
			st.Storage.Local.ZFS.Pools[0].CapacityPercent = &n
		}, Key{CheckCapacityHigh, "tank"}},
		{"SSD at the threshold", func(st *framev1beta1.FrameMachineStatus) {
			n := defaults.SSDWearPercent
			st.Inventory.Drives[0].SSDEnduranceUsedPercent = &n
		}, Key{CheckWearHigh, "S-A"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := healthyStatus()
			tt.mutate(st)
			a := Assess(st, defaults, true, nil, now)
			if got := keys(a); len(got) != 1 || !got[tt.want] {
				t.Fatalf("findings %v, want only %v", got, tt.want)
			}
			if v := a.Verdict(tt.want.Check, nil); v.Status != "True" || v.Reason != "Detected" || v.Message == "" {
				t.Errorf("verdict %+v", v)
			}
		})
	}
}

func TestFailedMemberIsNamedWithItsSerial(t *testing.T) {
	st := healthyStatus()
	degradeMD(st)
	msg := Assess(st, defaults, true, nil, now).Findings[0].Message
	if !strings.Contains(msg, "sda2") || !strings.Contains(msg, "S-A") {
		t.Fatalf("message %q does not name the failed disk", msg)
	}
}

func TestRebuildProgressDoesNotMoveTheAlertMessage(t *testing.T) {
	st := healthyStatus()
	degradeMD(st)
	st.Storage.Local.MD.Arrays[0].Sync = "recovery 8.5%"
	before := Assess(st, defaults, true, nil, now).Findings[0].Message
	st.Storage.Local.MD.Arrays[0].Sync = "recovery 9.1%"
	if after := Assess(st, defaults, true, nil, now).Findings[0].Message; after != before {
		t.Fatalf("%q != %q", after, before)
	}
}

// An unreadable stack neither finds nor clears, whatever its stale lists say.
func TestUnreadableStackNeitherRaisesNorClears(t *testing.T) {
	seenLongAgo := metav1.NewTime(now.Add(-AgentSilentAfter - time.Minute))
	tests := []struct {
		name   string
		mutate func(*framev1beta1.FrameMachineStatus)
	}{
		{"error", func(st *framev1beta1.FrameMachineStatus) {
			st.Storage.Local.MD.State, st.Storage.Local.MD.Error = framev1beta1.StackError, "open /proc/mdstat: EIO"
		}},
		{"busy", func(st *framev1beta1.FrameMachineStatus) { st.Storage.Local.MD.State = framev1beta1.StackBusy }},
		{"agent silent", func(st *framev1beta1.FrameMachineStatus) { st.Storage.ObservedAt = &seenLongAgo }},
		{"no local report", func(st *framev1beta1.FrameMachineStatus) { st.Storage.Local = nil }},
	}
	k := Key{CheckDegraded, "md0"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Lists saying degraded, nothing open: no false alert.
			st := healthyStatus()
			degradeMD(st)
			tt.mutate(st)
			a := Assess(st, defaults, true, nil, now)
			if keys(a)[k] {
				t.Fatal("raised from an unread stack")
			}
			if v := a.Verdict(CheckDegraded, nil); v.Status != "Unknown" {
				t.Fatalf("verdict %+v, want Unknown", v)
			}
			// Lists saying healthy, alert open: it stays.
			st = healthyStatus()
			tt.mutate(st)
			open := Open{k: ScopeMD}
			a = Assess(st, defaults, true, open, now)
			if !a.Keeps(k, ScopeMD) {
				t.Fatal("open alert not kept")
			}
			if v := a.Verdict(CheckDegraded, open); v.Status != "True" || v.Reason != "LastReadHeld" {
				t.Fatalf("verdict %+v", v)
			}
		})
	}
}

func TestOneUnreadableStackDoesNotHoldTheOther(t *testing.T) {
	st := healthyStatus()
	st.Storage.Local.ZFS.State = framev1beta1.StackError
	k := Key{CheckDegraded, "md0"}
	if Assess(st, defaults, true, Open{k: ScopeMD}, now).Keeps(k, ScopeMD) {
		t.Fatal("a ZFS read error held an md alert")
	}
}

func TestAbsentStackIsARead(t *testing.T) {
	st := healthyStatus()
	st.Storage.Local.ZFS = framev1beta1.ZFSStatus{State: framev1beta1.StackAbsent}
	a := Assess(st, defaults, true, nil, now)
	if v := a.Verdict(CheckScrubErrors, nil); v.Status != "False" {
		t.Fatalf("%+v", v)
	}
}

// A pool a reboot did not import reads like a pool destroyed: neither may
// tell subscribers the incident is over.
func TestADeviceGoneFromAReadKeepsItsAlert(t *testing.T) {
	tank := Key{CheckDegraded, "tank"}
	tests := []struct {
		name   string
		mutate func(*framev1beta1.FrameMachineStatus)
		key    Key
		scope  Scope
	}{
		{"zfs module not loaded", func(st *framev1beta1.FrameMachineStatus) {
			st.Storage.Local.ZFS = framev1beta1.ZFSStatus{State: framev1beta1.StackAbsent}
		}, tank, ScopeZFS},
		{"pool not imported", func(st *framev1beta1.FrameMachineStatus) { st.Storage.Local.ZFS.Pools = nil }, tank, ScopeZFS},
		{"scrub alert of a pool not imported", func(st *framev1beta1.FrameMachineStatus) { st.Storage.Local.ZFS.Pools = nil },
			Key{CheckScrubErrors, "tank"}, ScopeZFS},
		{"array stopped", func(st *framev1beta1.FrameMachineStatus) { st.Storage.Local.MD.Arrays = nil },
			Key{CheckDegraded, "md0"}, ScopeMD},
		{"disk not returned by the BMC", func(*framev1beta1.FrameMachineStatus) {}, Key{CheckWearHigh, "gone"}, ScopeDrives},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := healthyStatus()
			tt.mutate(st)
			open := Open{tt.key: tt.scope}
			a := Assess(st, defaults, true, open, now)
			if !a.Keeps(tt.key, tt.scope) {
				t.Fatal("cleared by a read that does not mention it")
			}
			if v := a.Verdict(tt.key.Check, open); v.Status != "True" || v.Reason != "LastReadHeld" {
				t.Fatalf("%+v", v)
			}
		})
	}
}

func TestAnUnlistedDeviceOfAnotherScopeIsNotHeld(t *testing.T) {
	st := healthyStatus()
	k := Key{CheckDegraded, "md0"} // listed by md, not by zfs
	if Assess(st, defaults, true, Open{k: ScopeMD}, now).Keeps(k, ScopeMD) {
		t.Fatal("held by another stack's list")
	}
}

// A field the read left empty says nothing, so it cannot clear anything.
func TestAReadThatSaysNothingKeepsTheAlert(t *testing.T) {
	pool := func(st *framev1beta1.FrameMachineStatus) *framev1beta1.ZPool { return &st.Storage.Local.ZFS.Pools[0] }
	tests := []struct {
		name   string
		mutate func(*framev1beta1.FrameMachineStatus)
		key    Key
		scope  Scope
	}{
		{"pool with no capacity", func(st *framev1beta1.FrameMachineStatus) { pool(st).CapacityPercent = nil }, Key{CheckCapacityHigh, "tank"}, ScopeZFS},
		{"pool with no health", func(st *framev1beta1.FrameMachineStatus) { pool(st).Health = "" }, Key{CheckDegraded, "tank"}, ScopeZFS},
		{"pool with no scan", func(st *framev1beta1.FrameMachineStatus) { pool(st).ScrubErrors, pool(st).Scan = nil, "" }, Key{CheckScrubErrors, "tank"}, ScopeZFS},
		{"vdev tree cut", func(st *framev1beta1.FrameMachineStatus) { pool(st).VdevsOmitted = 1 }, Key{CheckDegraded, "tank"}, ScopeZFS},
		{"pool past the cut", func(st *framev1beta1.FrameMachineStatus) { st.Storage.Local.ZFS.PoolsOmitted = 1 }, Key{CheckCapacityHigh, "cut"}, ScopeZFS},
		{"drive with no endurance and no media type", func(st *framev1beta1.FrameMachineStatus) {
			st.Inventory.Drives[0].SSDEnduranceUsedPercent, st.Inventory.Drives[0].MediaType = nil, ""
		}, Key{CheckWearHigh, "S-A"}, ScopeDrives},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := healthyStatus()
			tt.mutate(st)
			if !Assess(st, defaults, true, Open{tt.key: tt.scope}, now).Keeps(tt.key, tt.scope) {
				t.Fatal("cleared")
			}
		})
	}
}

// With nothing open, a list known to be partial is not a False.
func TestAPartialReadIsNeverHealthy(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*framev1beta1.FrameMachineStatus)
		check  string
	}{
		{"arrays cut", func(st *framev1beta1.FrameMachineStatus) { st.Storage.Local.MD.ArraysOmitted = 1 }, CheckDegraded},
		{"pools cut", func(st *framev1beta1.FrameMachineStatus) { st.Storage.Local.ZFS.PoolsOmitted = 1 }, CheckCapacityHigh},
		{"vdevs cut", func(st *framev1beta1.FrameMachineStatus) { st.Storage.Local.ZFS.Pools[0].VdevsOmitted = 1 }, CheckDegraded},
		{"inventory at its limit", func(st *framev1beta1.FrameMachineStatus) {
			st.Inventory.Drives = make([]framev1beta1.DriveInfo, MaxInventoryDrives)
		}, CheckWearHigh},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := healthyStatus()
			tt.mutate(st)
			a := Assess(st, defaults, true, nil, now)
			if v := a.Verdict(tt.check, nil); v.Status != "Unknown" {
				t.Fatalf("%+v", v)
			}
		})
	}
}

func TestListCutByTheAgentHoldsOnlyWhatIsCut(t *testing.T) {
	st := healthyStatus()
	st.Storage.Local.MD.ArraysOmitted = 1
	cut, listed := Key{CheckDegraded, "md9"}, Key{CheckDegraded, "md0"}
	a := Assess(st, defaults, true, Open{cut: ScopeMD, listed: ScopeMD}, now)
	if !a.Keeps(cut, ScopeMD) {
		t.Error("an array past the cut was resolved")
	}
	if a.Keeps(listed, ScopeMD) {
		t.Error("a listed healthy array was held")
	}
}

func TestScrubErrorsStayOpenWhileTheNextScrubRuns(t *testing.T) {
	st := healthyStatus()
	p := &st.Storage.Local.ZFS.Pools[0]
	p.ScrubErrors, p.Scan = nil, "scrub in progress since Fri Nov  6 10:00:00 2026"
	k := Key{CheckScrubErrors, "tank"}
	open := Open{k: ScopeZFS}
	a := Assess(st, defaults, true, open, now)
	if !a.Keeps(k, ScopeZFS) {
		t.Fatal("alert dropped by a scrub in progress")
	}
	if v := a.Verdict(CheckScrubErrors, open); v.Status != "True" {
		t.Fatalf("%+v", v)
	}
	p.Scan = "none requested"
	if Assess(st, defaults, true, open, now).Keeps(k, ScopeZFS) {
		t.Fatal("a pool with no scan at all held the alert")
	}
}

func TestCapacityClearsBelowTheThresholdNotOnIt(t *testing.T) {
	st := healthyStatus()
	k := Key{CheckCapacityHigh, "tank"}
	open := Open{k: ScopeZFS}
	belowByOne := defaults.ZFSCapacityPercent - 1
	st.Storage.Local.ZFS.Pools[0].CapacityPercent = &belowByOne
	if !keys(Assess(st, defaults, true, open, now))[k] {
		t.Error("open alert cleared one point under the threshold")
	}
	if keys(Assess(st, defaults, true, nil, now))[k] {
		t.Error("alert raised under the threshold")
	}
	clear := defaults.ZFSCapacityPercent - capacityHysteresis - 1
	st.Storage.Local.ZFS.Pools[0].CapacityPercent = &clear
	if keys(Assess(st, defaults, true, open, now))[k] {
		t.Error("open alert did not clear past the margin")
	}
}

func TestThresholdsComeFromThePolicy(t *testing.T) {
	st := healthyStatus()
	th := ThresholdsFrom(&framev1beta1.StorageHealthPolicy{Spec: framev1beta1.StorageHealthPolicySpec{ZFSCapacityPercent: 40, SSDWearPercent: 5}})
	got := keys(Assess(st, th, true, nil, now))
	if !got[Key{CheckCapacityHigh, "tank"}] || !got[Key{CheckWearHigh, "S-A"}] {
		t.Fatalf("findings %v", got)
	}
}

func TestPolicyWithAFieldUnsetKeepsTheDefault(t *testing.T) {
	th := ThresholdsFrom(&framev1beta1.StorageHealthPolicy{Spec: framev1beta1.StorageHealthPolicySpec{SSDWearPercent: 5}})
	if th.ZFSCapacityPercent != framev1beta1.DefaultZFSCapacityPercent || th.SSDWearPercent != 5 {
		t.Fatalf("%+v", th)
	}
}

func TestStaleInventoryNeitherRaisesNorClearsWear(t *testing.T) {
	st := healthyStatus()
	used := int32(95)
	st.Inventory.Drives[0].SSDEnduranceUsedPercent = &used
	a := Assess(st, defaults, false, nil, now)
	if len(a.Findings) != 0 {
		t.Fatalf("raised from a stale inventory: %+v", a.Findings)
	}
	k := Key{CheckWearHigh, "S-A"}
	if !Assess(healthyStatus(), defaults, false, Open{k: ScopeDrives}, now).Keeps(k, ScopeDrives) {
		t.Fatal("cleared on a stale inventory")
	}
	if v := a.Verdict(CheckWearHigh, nil); v.Status != "Unknown" {
		t.Fatalf("%+v", v)
	}
}

// The G9's own SSDs: the iLO returns null, so False would claim a read that
// never happened.
func TestSSDWithoutEnduranceFigureIsUnknownNotHealthy(t *testing.T) {
	st := healthyStatus()
	st.Inventory.Drives[0].SSDEnduranceUsedPercent = nil
	k := Key{CheckWearHigh, "S-A"}
	a := Assess(st, defaults, true, nil, now)
	if v := a.Verdict(CheckWearHigh, nil); v.Status != "Unknown" || v.Reason != "WearNotReported" {
		t.Fatalf("%+v", v)
	}
	if !Assess(st, defaults, true, Open{k: ScopeDrives}, now).Keeps(k, ScopeDrives) {
		t.Fatal("alert cleared by a drive that stopped reporting")
	}
}

func smartDisk(serial string, wear *int32, passed *bool) framev1beta1.ObservedDisk {
	return framev1beta1.ObservedDisk{SerialNumber: serial, SMART: &framev1beta1.DiskSMART{WearUsedPercent: wear, Passed: passed}}
}

func TestSMARTFillsInWhereTheBMCIsSilent(t *testing.T) {
	st := healthyStatus()
	st.Inventory.Drives[0].SSDEnduranceUsedPercent = nil
	st.Storage.Observed = []framev1beta1.ObservedDisk{smartDisk("S-A", new(defaults.SSDWearPercent), new(true))}
	a := Assess(st, defaults, true, nil, now)
	if !keys(a)[Key{CheckWearHigh, "S-A"}] {
		t.Fatalf("%+v", a.Findings)
	}

	st.Storage.Observed = []framev1beta1.ObservedDisk{smartDisk("S-A", new(int32(10)), new(true))}
	a = Assess(st, defaults, true, nil, now)
	if v := a.Verdict(CheckWearHigh, nil); v.Status != "False" {
		t.Fatalf("a disk reporting its own wear is not unreported: %+v", v)
	}
}

func TestTheBMCFigureComesFirst(t *testing.T) {
	st := healthyStatus() // BMC says 10
	st.Storage.Observed = []framev1beta1.ObservedDisk{smartDisk("S-A", new(int32(99)), new(true))}
	if a := Assess(st, defaults, true, nil, now); len(a.Findings) != 0 {
		t.Fatalf("%+v", a.Findings)
	}
}

func TestAFailingDiskIsDegraded(t *testing.T) {
	st := healthyStatus()
	st.Storage.Observed = []framev1beta1.ObservedDisk{smartDisk("S-A", nil, new(false))}
	a := Assess(st, defaults, true, nil, now)
	if !keys(a)[Key{CheckDegraded, "S-A"}] {
		t.Fatalf("%+v", a.Findings)
	}
}

func TestSMARTThatSaysNothingKeepsTheAlert(t *testing.T) {
	wear, health := Key{CheckWearHigh, "X"}, Key{CheckDegraded, "X"}
	for name, disks := range map[string][]framev1beta1.ObservedDisk{
		"failed read": {{SerialNumber: "X", SMART: &framev1beta1.DiskSMART{Error: "timed out"}}},
		"no read yet": {{SerialNumber: "X"}},
		"disk gone":   nil,
	} {
		t.Run(name, func(t *testing.T) {
			st := healthyStatus()
			st.Storage.Observed = disks
			open := Open{wear: ScopeSMART, health: ScopeSMART}
			a := Assess(st, defaults, true, open, now)
			if !a.Keeps(wear, ScopeSMART) || !a.Keeps(health, ScopeSMART) {
				t.Fatal("cleared")
			}
		})
	}
}

func TestSMARTAlertClearsOnAGoodRead(t *testing.T) {
	st := healthyStatus()
	st.Storage.Observed = []framev1beta1.ObservedDisk{smartDisk("X", new(int32(5)), new(true))}
	wear, health := Key{CheckWearHigh, "X"}, Key{CheckDegraded, "X"}
	a := Assess(st, defaults, true, Open{wear: ScopeSMART, health: ScopeSMART}, now)
	if a.Keeps(wear, ScopeSMART) || a.Keeps(health, ScopeSMART) || len(a.Findings) != 0 {
		t.Fatalf("%+v", a)
	}
}

// The BMC stays at null for good on these disks: the disk's figure must settle
// an open alert, whichever reading raised it.
func TestWearAlertClearsOnTheDiskFigureWhenTheBMCIsSilent(t *testing.T) {
	k := Key{CheckWearHigh, "S-A"}
	for _, scope := range []Scope{ScopeSMART, ScopeDrives} {
		st := healthyStatus()
		st.Inventory.Drives[0].SSDEnduranceUsedPercent = nil
		st.Storage.Observed = []framev1beta1.ObservedDisk{smartDisk("S-A", new(int32(10)), new(true))}
		open := Open{k: scope}
		a := Assess(st, defaults, true, open, now)
		if a.Keeps(k, scope) {
			t.Errorf("%s: held on a read of 10 %%", scope)
		}
		if v := a.Verdict(CheckWearHigh, open); v.Status != "False" {
			t.Errorf("%s: %+v", scope, v)
		}
	}
}

func TestWearAlertStaysHeldWhileTheDiskGivesNoFigureEither(t *testing.T) {
	k := Key{CheckWearHigh, "S-A"}
	st := healthyStatus()
	st.Inventory.Drives[0].SSDEnduranceUsedPercent = nil
	st.Storage.Observed = []framev1beta1.ObservedDisk{{SerialNumber: "S-A", SMART: &framev1beta1.DiskSMART{Error: "timed out"}}}
	if !Assess(st, defaults, true, Open{k: ScopeDrives}, now).Keeps(k, ScopeDrives) {
		t.Fatal("cleared")
	}
}

// A report that has gone stale cannot close what the BMC does not speak of.
func TestASilentAgentsFigureClearsNoBMCAlert(t *testing.T) {
	k := Key{CheckWearHigh, "S-A"}
	st := healthyStatus()
	st.Inventory.Drives[0].SSDEnduranceUsedPercent = nil
	old := metav1.NewTime(now.Add(-AgentSilentAfter - time.Minute))
	st.Storage.ObservedAt = &old
	st.Storage.Observed = []framev1beta1.ObservedDisk{smartDisk("S-A", new(int32(10)), new(true))}
	if !Assess(st, defaults, true, Open{k: ScopeDrives}, now).Keeps(k, ScopeDrives) {
		t.Fatal("cleared")
	}
}

// False takes a read. An agent gone silent leaves only the BMC to speak.
func TestASilentAgentLeavesADiskOffTheBMCUnknown(t *testing.T) {
	old := metav1.NewTime(now.Add(-AgentSilentAfter - time.Minute))
	offBMC := framev1beta1.ObservedDisk{SerialNumber: "OFF-BMC", Rotational: new(false)}
	for name, disk := range map[string]framev1beta1.ObservedDisk{
		"last figure": {SerialNumber: "OFF-BMC", Rotational: new(false), SMART: &framev1beta1.DiskSMART{WearUsedPercent: new(int32(79))}},
		"no figure":   offBMC,
	} {
		st := healthyStatus()
		st.Storage.ObservedAt = &old
		st.Storage.Observed = []framev1beta1.ObservedDisk{disk}
		if v := Assess(st, defaults, true, nil, now).Verdict(CheckWearHigh, nil); v.Status != "Unknown" {
			t.Errorf("%s: %+v", name, v)
		}
	}
	// The BMC's own disk keeps its figure, and a spinning one owes none.
	st := healthyStatus()
	st.Storage.ObservedAt = &old
	st.Storage.Observed = []framev1beta1.ObservedDisk{
		{SerialNumber: "S-A", Rotational: new(false)},
		{SerialNumber: "HDD", Rotational: new(true)},
	}
	if v := Assess(st, defaults, true, nil, now).Verdict(CheckWearHigh, nil); v.Status != "False" {
		t.Fatalf("%+v", v)
	}
}

// A solid-state disk not read yet, or that cannot be, is not a healthy one.
func TestASolidStateDiskWithoutAReadIsNeverHealthy(t *testing.T) {
	st := healthyStatus()
	st.Storage.Observed = []framev1beta1.ObservedDisk{{SerialNumber: "EMMC", Rotational: new(false)}}
	a := Assess(st, defaults, true, nil, now)
	for _, check := range []string{CheckDegraded, CheckWearHigh} {
		if v := a.Verdict(check, nil); v.Status != "Unknown" {
			t.Errorf("%s: %+v", check, v)
		}
	}
	st.Storage.Observed = []framev1beta1.ObservedDisk{{SerialNumber: "HDD", Rotational: new(true)}}
	a = Assess(st, defaults, true, nil, now)
	for _, check := range []string{CheckDegraded, CheckWearHigh} {
		if v := a.Verdict(check, nil); v.Status != "False" {
			t.Errorf("%s: %+v", check, v)
		}
	}
}

// A disk that answered nothing on its health is no better than one unread.
func TestAnEmptySMARTReportIsNeverHealthy(t *testing.T) {
	st := healthyStatus()
	st.Storage.Observed = []framev1beta1.ObservedDisk{{SerialNumber: "SILENT", SMART: &framev1beta1.DiskSMART{}}}
	v := Assess(st, defaults, true, nil, now).Verdict(CheckDegraded, nil)
	if v.Status != "Unknown" || !strings.Contains(v.Message, "SILENT") {
		t.Fatalf("%+v", v)
	}
}

// One disk's figure says nothing of another's.
func TestAnotherDisksFigureClearsNothing(t *testing.T) {
	k := Key{CheckWearHigh, "S-A"}
	st := healthyStatus()
	st.Inventory.Drives[0].SSDEnduranceUsedPercent = nil
	st.Storage.Observed = []framev1beta1.ObservedDisk{smartDisk("OTHER", new(int32(10)), new(true))}
	if !Assess(st, defaults, true, Open{k: ScopeDrives}, now).Keeps(k, ScopeDrives) {
		t.Fatal("cleared")
	}
}

// A spinning disk off the BMC's list has no SMART block and owes no figure.
func TestADiskWithoutAReadOwesNoWear(t *testing.T) {
	st := healthyStatus()
	st.Storage.Observed = []framev1beta1.ObservedDisk{{SerialNumber: "HDD"}}
	if v := Assess(st, defaults, true, nil, now).Verdict(CheckWearHigh, nil); v.Status != "False" {
		t.Fatalf("%+v", v)
	}
}

// The BMC's figures stand on their own: wear does not need the agent.
func TestBMCWearNeedsNoAgent(t *testing.T) {
	st := healthyStatus()
	st.Storage = nil
	if v := Assess(st, defaults, true, nil, now).Verdict(CheckWearHigh, nil); v.Status != "False" {
		t.Fatalf("%+v", v)
	}
}

func TestAFailedSMARTReadIsNeverHealthy(t *testing.T) {
	failed := framev1beta1.ObservedDisk{SerialNumber: "OFF-BMC", SMART: &framev1beta1.DiskSMART{Error: "timed out"}}

	st := healthyStatus()
	st.Storage.Observed = []framev1beta1.ObservedDisk{failed}
	a := Assess(st, defaults, true, nil, now)
	for _, check := range []string{CheckDegraded, CheckWearHigh} {
		if v := a.Verdict(check, nil); v.Status != "Unknown" {
			t.Errorf("%s: %+v", check, v)
		}
	}
	// Wear is known from the BMC: a failed read takes only the health away.
	st.Storage.Observed = []framev1beta1.ObservedDisk{{SerialNumber: "S-A", SMART: &framev1beta1.DiskSMART{Error: "timed out"}}}
	a = Assess(st, defaults, true, nil, now)
	if v := a.Verdict(CheckWearHigh, nil); v.Status != "False" {
		t.Errorf("%+v", v)
	}
	if v := a.Verdict(CheckDegraded, nil); v.Status != "Unknown" {
		t.Errorf("%+v", v)
	}
	// What was read still counts.
	st.Storage.Observed = []framev1beta1.ObservedDisk{failed, smartDisk("BAD", nil, new(false))}
	a = Assess(st, defaults, true, nil, now)
	if v := a.Verdict(CheckDegraded, nil); v.Status != "True" {
		t.Errorf("%+v", v)
	}
}

// A disk off the BMC's list that reads fine but gives no figure: what docs say.
func TestAReadWithoutAFigureIsWearNotReported(t *testing.T) {
	st := healthyStatus()
	st.Storage.Observed = []framev1beta1.ObservedDisk{smartDisk("OFF-BMC", nil, new(true))}
	a := Assess(st, defaults, true, nil, now)
	if v := a.Verdict(CheckWearHigh, nil); v.Status != "Unknown" || v.Reason != "WearNotReported" {
		t.Fatalf("%+v", v)
	}
	if v := a.Verdict(CheckDegraded, nil); v.Status != "False" {
		t.Fatalf("%+v", v)
	}
}

func TestAnSSDIsCountedOnceAsUnreported(t *testing.T) {
	st := healthyStatus()
	st.Inventory.Drives[0].SSDEnduranceUsedPercent = nil
	st.Storage.Observed = []framev1beta1.ObservedDisk{smartDisk("S-A", nil, new(true))}
	if a := Assess(st, defaults, true, nil, now); a.WearUnreported != 1 {
		t.Fatalf("%d", a.WearUnreported)
	}
}

func TestSilentAgentMakesSMARTUnread(t *testing.T) {
	st := healthyStatus()
	old := metav1.NewTime(now.Add(-AgentSilentAfter - time.Minute))
	st.Storage.ObservedAt = &old
	if _, unread := Assess(st, defaults, true, nil, now).Unread[ScopeSMART]; !unread {
		t.Fatal("read")
	}
	st.Storage = nil
	if _, unread := Assess(st, defaults, true, nil, now).Unread[ScopeSMART]; !unread {
		t.Fatal("read")
	}
}

func TestNoSMARTReadIsNotAnUnknownStorageVerdict(t *testing.T) {
	// A node without SMART data must not turn every StorageDegraded Unknown.
	if v := func() Verdict {
		a := Assess(healthyStatus(), defaults, true, nil, now)
		return a.Verdict(CheckDegraded, nil)
	}(); v.Status != "False" {
		t.Fatalf("%+v", v)
	}
}

func TestSilentAgentHoldsSMARTAlerts(t *testing.T) {
	st := healthyStatus()
	old := metav1.NewTime(now.Add(-AgentSilentAfter - time.Minute))
	st.Storage.ObservedAt = &old
	st.Storage.Observed = []framev1beta1.ObservedDisk{smartDisk("X", new(int32(5)), new(true))}
	k := Key{CheckWearHigh, "X"}
	if !Assess(st, defaults, true, Open{k: ScopeSMART}, now).Keeps(k, ScopeSMART) {
		t.Fatal("cleared")
	}
}
