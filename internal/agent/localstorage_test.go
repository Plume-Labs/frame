/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	framev1beta1 "github.com/gothshoot/frame/api/frame/v1beta1"
)

// Fixtures under testdata/localstorage/. mdstat-g9.txt and pvs-empty-g9.json
// are the real outputs of the hardware node (a raid1 md0 on two partitions,
// LVM tools installed with no PV); the ZFS and LVM-with-volumes fixtures are
// invented in the exact layout of OpenZFS 2.x and lvm2 2.03, with invented
// serials. No real serial, WWN or hostname.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "localstorage", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ── the command list ───────────────────────────────────────────────────────

// The local-storage reader's whole reach, written out a second time. Adding
// an argv to storageCommands without adding it here fails this test — a
// new command is a reviewed decision, not a side effect.
var reviewedStorageCommands = [][]string{
	{"zpool", "list", "-Hp", "-o", "name,size,alloc,free,frag,cap,health"},
	{"zpool", "status", "-P"},
	{"zfs", "list", "-Hp", "-o", "name,used,avail,refer,mountpoint,type"},
	{"pvs", "--reportformat", "json", "--units", "b", "--nosuffix", "-o", "pv_name,vg_name,pv_size,pv_free,pv_attr"},
	{"vgs", "--reportformat", "json", "--units", "b", "--nosuffix", "-o", "vg_name,vg_size,vg_free,pv_count,lv_count,vg_attr"},
	{"lvs", "--reportformat", "json", "--units", "b", "--nosuffix", "-o", "lv_name,vg_name,lv_size,lv_attr,segtype,data_percent"},
}

func TestStorageCommandsAreExactlyTheReviewedList(t *testing.T) {
	if len(storageCommands) != len(reviewedStorageCommands) {
		t.Fatalf("storageCommands has %d entries, the reviewed list %d", len(storageCommands), len(reviewedStorageCommands))
	}
	for i := range storageCommands {
		if !slices.Equal(storageCommands[i], reviewedStorageCommands[i]) {
			t.Errorf("entry %d is %q, reviewed %q", i, storageCommands[i], reviewedStorageCommands[i])
		}
	}
}

// Belt and braces on top of the reviewed list: no argv may carry a verb or
// flag that changes anything, whatever else is decided later.
func TestStorageCommandsCarryNoWritingVerb(t *testing.T) {
	allowedTools := map[string][]string{
		"zpool": {"list", "status"},
		"zfs":   {"list"},
		"pvs":   nil, "vgs": nil, "lvs": nil,
	}
	writing := []string{
		"create", "destroy", "set", "import", "export", "scrub", "clear", "online", "offline",
		"attach", "detach", "replace", "remove", "add", "split", "upgrade", "reguid", "trim",
		"initialize", "resilver", "labelclear", "rollback", "snapshot", "send", "receive", "mount",
		"unmount", "rename", "promote", "inherit", "--yes", "-y", "--force", "-f",
	}
	for _, argv := range storageCommands {
		verbs, ok := allowedTools[argv[0]]
		if !ok {
			t.Errorf("%q: tool not allowed", argv)
			continue
		}
		if verbs != nil && !slices.Contains(verbs, argv[1]) {
			t.Errorf("%q: verb %q not a listing verb", argv, argv[1])
		}
		for _, a := range argv[1:] {
			if slices.Contains(writing, a) {
				t.Errorf("%q carries %q", argv, a)
			}
		}
	}
}

type recordingRunner struct {
	calls   [][]string
	answers map[string]string
	fail    map[string]error
}

func (r *recordingRunner) Run(name string, args ...string) (string, error) {
	argv := append([]string{name}, args...)
	r.calls = append(r.calls, argv)
	key := strings.Join(args[1:3], " ") // after "timeout N": tool + verb
	if err, ok := r.fail[args[1]]; ok {
		return "", err
	}
	return r.answers[key], nil
}

func TestRunStorageRefusesAnythingOffTheList(t *testing.T) {
	r := &recordingRunner{}
	for _, argv := range [][]string{
		{"zpool", "scrub", "data"},
		{"zpool", "status"}, // a prefix of a listed argv is not the listed argv
		{"zpool", "status", "-P", "data"},
		{"rm", "-rf", "/"},
	} {
		if _, err := runStorage(r, argv); err == nil {
			t.Errorf("%q was run", argv)
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("the runner was reached: %q", r.calls)
	}
}

func TestRunStorageWrapsEveryCommandInTheHostTimeout(t *testing.T) {
	r := &recordingRunner{answers: map[string]string{}}
	if _, err := runStorage(r, storageCommands[1]); err != nil {
		t.Fatal(err)
	}
	want := append([]string{"timeout", "10"}, storageCommands[1]...)
	if !slices.Equal(r.calls[0], want) {
		t.Fatalf("argv %q, want %q", r.calls[0], want)
	}
}

// exitErr fakes the exit status `timeout` returns through nsenter.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run() // constant input, no injection
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != code {
		t.Fatalf("could not fake exit %d: %v", code, err)
	}
	return err
}

func TestExecTimeoutRunnerKillsAHungCommand(t *testing.T) {
	start := time.Now()
	_, err := ExecTimeoutRunner{Timeout: 100 * time.Millisecond}.Run("sleep", "5")
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("sleep 5 was not killed (err %v after %s)", err, time.Since(start))
	}
}

// ── whole reads ────────────────────────────────────────────────────────────

func sources(r *recordingRunner, files map[string]string, paths ...string) LocalSources {
	return LocalSources{
		Runner: r,
		ReadFile: func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, os.ErrNotExist
		},
		PathExist: func(p string) bool { return slices.Contains(paths, p) },
	}
}

// The hardware node as it is: md0 mirrored, no ZFS module, LVM tools with
// no PV. Nothing about ZFS may be run, md is read without any command.
func TestObserveLocalStorageOnTheHardwareNode(t *testing.T) {
	r := &recordingRunner{answers: map[string]string{
		"pvs --reportformat": fixture(t, "pvs-empty-g9.json"),
		"vgs --reportformat": `{"report":[{"vg":[]}]}`,
		"lvs --reportformat": `{"report":[{"lv":[]}]}`,
	}}
	got := ObserveLocalStorage(
		sources(r, map[string]string{"/proc/mdstat": fixture(t, "mdstat-g9.txt")}),
		map[string]string{"sdc2": "SER-SSD1", "sdd2": "SER-SSD2", "sdc": "SER-SSD1", "sdd": "SER-SSD2"},
	)
	if got.ZFS.State != framev1beta1.StackAbsent {
		t.Errorf("ZFS state %q, want absent", got.ZFS.State)
	}
	for _, c := range r.calls {
		if c[2] == "zpool" || c[2] == "zfs" {
			t.Errorf("ran %q on a node without the zfs module", c)
		}
	}
	if got.MD.State != framev1beta1.StackPresent || len(got.MD.Arrays) != 1 {
		t.Fatalf("md = %+v", got.MD)
	}
	md0 := got.MD.Arrays[0]
	if md0.Name != "md0" || md0.Level != "raid1" || md0.Degraded || md0.Slots != "UU" || md0.RaidDisks != 2 || md0.ActiveDisks != 2 {
		t.Errorf("md0 = %+v", md0)
	}
	if md0.Members[0].Device != "sdd2" || md0.Members[0].Serial != "SER-SSD2" || md0.Members[1].Serial != "SER-SSD1" {
		t.Errorf("members = %+v", md0.Members)
	}
	if got.LVM.State != framev1beta1.StackAbsent {
		t.Errorf("LVM with no PV: state %q, want absent", got.LVM.State)
	}
}

// A VM node: no zfs module, no md, no LVM tools at all.
func TestObserveLocalStorageOnABareNodeIsAbsentNotError(t *testing.T) {
	r := &recordingRunner{fail: map[string]error{"pvs": exitErr(t, 127)}}
	got := ObserveLocalStorage(sources(r, nil), nil)
	if got.ZFS.State != framev1beta1.StackAbsent || got.MD.State != framev1beta1.StackAbsent || got.LVM.State != framev1beta1.StackAbsent {
		t.Fatalf("got %+v", got)
	}
}

func TestObserveLocalStorageReportsATimeoutAsAnError(t *testing.T) {
	r := &recordingRunner{fail: map[string]error{"zpool": exitErr(t, 124)}}
	got := ObserveLocalStorage(sources(r, nil, "/sys/module/zfs"), nil)
	if got.ZFS.State != framev1beta1.StackError || !strings.Contains(got.ZFS.Error, "timed out") {
		t.Fatalf("zfs = %+v", got.ZFS)
	}
}

func TestObserveLocalStorageZFSModuleWithoutBinaryIsAnError(t *testing.T) {
	r := &recordingRunner{fail: map[string]error{"zpool": exitErr(t, 127)}}
	got := ObserveLocalStorage(sources(r, nil, "/sys/module/zfs"), nil)
	if got.ZFS.State != framev1beta1.StackError {
		t.Fatalf("zfs module loaded but no zpool: state %q, want error", got.ZFS.State)
	}
}

func zfsNode(t *testing.T) *recordingRunner {
	return &recordingRunner{answers: map[string]string{
		"zpool list":         fixture(t, "zpool-list.txt"),
		"zpool status":       fixture(t, "zpool-status-P.txt"),
		"zfs list":           fixture(t, "zfs-list.txt"),
		"pvs --reportformat": fixture(t, "pvs.json"),
		"vgs --reportformat": fixture(t, "vgs.json"),
		"lvs --reportformat": fixture(t, "lvs.json"),
	}}
}

func TestObserveZFS(t *testing.T) {
	got := ObserveLocalStorage(sources(zfsNode(t), nil, "/sys/module/zfs"), nil).ZFS
	if got.State != framev1beta1.StackPresent || len(got.Pools) != 2 {
		t.Fatalf("zfs = %+v", got)
	}
	data := got.Pools[0]
	if data.Name != "data" || data.Health != "DEGRADED" || data.SizeBytes != 15994458210304 || *data.CapacityPercent != 31 {
		t.Errorf("data = %+v", data)
	}
	if data.ScrubErrors == nil || *data.ScrubErrors != 2 || data.LastScrub != "Sun Sep 14 00:24:01 2026" {
		t.Errorf("scrub = %v %q", data.ScrubErrors, data.LastScrub)
	}
	if data.Errors != "No known data errors" {
		t.Errorf("errors = %q", data.Errors)
	}
	type row struct {
		name  string
		depth int32
		state string
	}
	var rows []row
	for _, v := range data.Vdevs {
		rows = append(rows, row{v.Name, v.Depth, v.State})
	}
	want := []row{
		{"raidz1-0", 0, "DEGRADED"},
		{"/dev/disk/by-id/ata-HDD4T_SER-Z1-part1", 1, "ONLINE"},
		{"/dev/disk/by-id/ata-HDD4T_SER-Z2-part1", 1, "ONLINE"},
		{"/dev/disk/by-id/wwn-0x5000c500deadbe03-part1", 1, "FAULTED"},
		{"/dev/disk/by-id/ata-HDD4T_SER-Z4-part1", 1, "ONLINE"},
		{"logs", 0, ""},
		{"mirror-1", 1, "ONLINE"},
		{"/dev/disk/by-id/ata-SSD-A_SER-L1-part1", 2, "ONLINE"},
		{"/dev/disk/by-id/ata-SSD-B_SER-L2-part1", 2, "ONLINE"},
		{"cache", 0, ""},
		{"/dev/disk/by-id/nvme-NVME1T_SER-N1-part4", 1, "ONLINE"},
		{"spares", 0, ""},
		{"/dev/disk/by-id/ata-HDD4T_SER-Z5-part1", 1, "AVAIL"},
	}
	if !slices.Equal(rows, want) {
		t.Errorf("vdevs\n got %v\nwant %v", rows, want)
	}
	faulted := data.Vdevs[3]
	if faulted.ReadErrors != 12 || faulted.ChecksumErrors != 1200 || faulted.Note != "too many errors" {
		t.Errorf("faulted leaf = %+v", faulted)
	}
	rpool := got.Pools[1]
	if rpool.ScrubErrors != nil || !strings.Contains(rpool.Scan, "in progress") || !strings.Contains(rpool.Scan, "24.19% done") {
		t.Errorf("rpool scan %q, scrubErrors %v", rpool.Scan, rpool.ScrubErrors)
	}
	if len(got.Datasets) != 4 || got.Datasets[1].Type != "volume" || got.Datasets[1].MountPoint != "" || got.Datasets[3].MountPoint != "/" {
		t.Errorf("datasets = %+v", got.Datasets)
	}
}

func TestObserveMDDegradedRecoveryAndInactive(t *testing.T) {
	arrays := parseMdstat(fixture(t, "mdstat-degraded-recovery.txt"))
	if len(arrays) != 3 {
		t.Fatalf("got %d arrays", len(arrays))
	}
	md0, md1, md127 := arrays[0], arrays[1], arrays[2]
	if !md0.Degraded || md0.Slots != "U_" || md0.ActiveDisks != 1 || md0.Sync != "recovery 8.5%" {
		t.Errorf("md0 = %+v", md0)
	}
	if !md0.Members[2].Failed || md0.Members[2].Device != "sdc2" {
		t.Errorf("md0 members = %+v", md0.Members)
	}
	if md1.Degraded || md1.State != "active (auto-read-only)" || md1.Level != "raid1" || md1.Sync != "resync PENDING" {
		t.Errorf("md1 = %+v", md1)
	}
	if !md127.Degraded || md127.State != "inactive" || md127.Level != "" || !md127.Members[0].Spare {
		t.Errorf("md127 = %+v", md127)
	}
	if got := parseMdstat(fixture(t, "mdstat-empty.txt")); len(got) != 0 {
		t.Errorf("empty mdstat gave %+v", got)
	}
}

func TestObserveLVM(t *testing.T) {
	got := ObserveLocalStorage(sources(zfsNode(t), nil), map[string]string{"sdb2": "SER-A", "sdc": "SER-B"}).LVM
	if got.State != framev1beta1.StackPresent || len(got.PVs) != 2 || len(got.VGs) != 1 || len(got.LVs) != 2 {
		t.Fatalf("lvm = %+v", got)
	}
	if got.PVs[0].Serial != "SER-A" || got.PVs[1].Serial != "SER-B" || got.PVs[1].Attr != "a-m" {
		t.Errorf("pvs = %+v", got.PVs)
	}
	if got.VGs[0].Attr != "wz-pn-" || got.VGs[0].PVCount != 2 {
		t.Errorf("vg = %+v", got.VGs[0])
	}
	if got.LVs[1].SegType != "thin-pool" || got.LVs[1].DataPercent != "37.12" {
		t.Errorf("lv = %+v", got.LVs[1])
	}
}

// Past the CRD bounds, lists are cut and the cut is counted.
func TestLocalStorageListsAreCappedAndCounted(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 70; i++ {
		b.WriteString("data/ds")
		b.WriteString(strings.Repeat("x", i))
		b.WriteString("\t1\t2\t3\t-\tfilesystem\n")
	}
	r := zfsNode(t)
	r.answers["zfs list"] = b.String()
	got := ObserveLocalStorage(sources(r, nil, "/sys/module/zfs"), nil).ZFS
	if len(got.Datasets) != maxZFSDatasets || got.DatasetsOmitted != 6 {
		t.Fatalf("datasets %d, omitted %d", len(got.Datasets), got.DatasetsOmitted)
	}
}

// Each reason for "degraded", alone: a failed member, a missing slot, fewer
// active than raid disks, an inactive array. One array per reason so that
// dropping any single rule turns exactly its row red.
func TestMDDegradedReasonsOneByOne(t *testing.T) {
	cases := map[string]string{
		"failed member":  "md0 : active raid1 sda1[0] sdb1[1](F)\n      100 blocks super 1.2\n",
		"missing slot":   "md0 : active raid1 sda1[0] sdb1[1]\n      100 blocks super 1.2 [2/2] [U_]\n",
		"fewer active":   "md0 : active raid1 sda1[0] sdb1[1]\n      100 blocks super 1.2 [2/1] [UU]\n",
		"inactive array": "md0 : inactive sda1[0]\n      100 blocks super 1.2\n",
	}
	for name, mdstat := range cases {
		a := parseMdstat(mdstat)
		if len(a) != 1 || !a[0].Degraded {
			t.Errorf("%s: not degraded: %+v", name, a)
		}
	}
	healthy := parseMdstat("md0 : active raid1 sda1[0] sdb1[1]\n      100 blocks super 1.2 [2/2] [UU]\n")
	if healthy[0].Degraded {
		t.Errorf("healthy mirror read as degraded: %+v", healthy[0])
	}
}
