package agent

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// reviewedSMARTPrefix is a copy on purpose: changing what the agent runs takes
// changing it in two places, the second being a line a reviewer reads.
var reviewedSMARTPrefix = []string{"smartctl", "--json=c", "--nocheck=standby,0", "--all", "--log=devstat"}

func TestSMARTCommandIsExactlyTheReviewedOne(t *testing.T) {
	if !slices.Equal(smartArgvPrefix, reviewedSMARTPrefix) {
		t.Fatalf("%q", smartArgvPrefix)
	}
}

// Every option of smartctl that writes to a disk or its settings, in both
// spellings. None may appear, joined to a value or not.
func TestSMARTCommandCarriesNoWritingOption(t *testing.T) {
	forbidden := []string{"-t", "--test", "-X", "--abort", "-s", "--smart", "-S", "--saveauto", "-o", "--offlineauto", "--set", "-F", "-v", "--vendorattribute", "-d", "--device"}
	for _, arg := range smartArgvPrefix {
		for _, f := range forbidden {
			if arg == f || strings.HasPrefix(arg, f+"=") {
				t.Errorf("%q", arg)
			}
		}
	}
}

func TestSMARTArgvTakesOnlyAWholeDiskByKernelName(t *testing.T) {
	for _, ok := range []string{"/dev/sda", "/dev/sdab", "/dev/nvme0n1", "/dev/nvme12n3"} {
		argv, err := smartArgv(ok)
		if err != nil || argv[len(argv)-1] != ok || len(argv) != len(smartArgvPrefix)+1 {
			t.Errorf("%s: %v %v", ok, argv, err)
		}
	}
	for _, bad := range []string{
		"", "sda", "/dev/sda1", "/dev/nvme0n1p1", "/dev/sda ", " /dev/sda", "/dev/sda\n", "/dev/sda -t short",
		"-t", "--test=short", "/etc/passwd", "/dev/../etc/passwd", "/dev/sd/../sda", "/dev/disk/by-id/x", "/dev/sda;id",
		"/dev/sda\x00", "/dev/SDA", "/dev/mem", "/dev/nvme0",
	} {
		if argv, err := smartArgv(bad); err == nil {
			t.Errorf("%q accepted: %q", bad, argv)
		}
	}
}

type recordingExec struct {
	mu     sync.Mutex
	calls  [][]string
	stdout string
	err    error
}

func (x *recordingExec) Exec(argv []string) (string, string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.calls = append(x.calls, argv)
	return x.stdout, "", x.err
}

func TestExecSMARTRefusesEveryOtherInvocation(t *testing.T) {
	good := append(slices.Clone(smartArgvPrefix), "/dev/sda")
	withTest := slices.Clone(good)
	withTest[3] = "--test=short"
	for name, argv := range map[string][]string{
		"empty":            nil,
		"no device":        smartArgvPrefix,
		"self-test":        withTest,
		"extra argument":   append(slices.Clone(good), "--test=short"),
		"option as device": append(slices.Clone(smartArgvPrefix), "-t"),
		"other path":       append(slices.Clone(smartArgvPrefix), "/etc/shadow"),
		"other tool":       {"sh", "-c", "id"},
		"storage command":  storageCommands[0],
	} {
		x := &recordingExec{}
		if _, _, err := execSMART(x, argv); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if len(x.calls) != 0 {
			t.Errorf("%s: reached the executor: %q", name, x.calls)
		}
	}
	x := &recordingExec{}
	if _, _, err := execSMART(x, good); err != nil {
		t.Fatal(err)
	}
	if want := append(slices.Clone(timeoutPrefix), good...); !slices.Equal(x.calls[0], want) {
		t.Fatalf("%q", x.calls[0])
	}
}

func TestWearReadsTheStandardFieldFirst(t *testing.T) {
	tests := []struct {
		name, json string
		want       *int32
	}{
		{"nvme", `{"nvme_smart_health_information_log":{"percentage_used":7}}`, new(int32(7))},
		{"nvme past its rating", `{"nvme_smart_health_information_log":{"percentage_used":130}}`, new(int32(100))},
		{"ata statistics beat attributes", `{"ata_device_statistics":{"pages":[{"table":[{"name":"Percentage Used Endurance Indicator","value":12}]}]},
			"ata_smart_attributes":{"table":[{"id":233,"value":10}]}}`, new(int32(12))},
		{"Media_Wearout_Indicator counts down", `{"ata_smart_attributes":{"table":[{"id":233,"value":97}]}}`, new(int32(3))},
		{"attribute order", `{"ata_smart_attributes":{"table":[{"id":177,"value":50},{"id":231,"value":90}]}}`, new(int32(10))},
		{"a value past 100 is not a percentage", `{"ata_smart_attributes":{"table":[{"id":177,"value":253}]}}`, nil},
		{"nothing known", `{"ata_smart_attributes":{"table":[{"id":9,"value":99}]}}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSMART(tt.json)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tt.want == nil && got.WearUsedPercent != nil:
				t.Fatalf("%d", *got.WearUsedPercent)
			case tt.want != nil && (got.WearUsedPercent == nil || *got.WearUsedPercent != *tt.want):
				t.Fatalf("%v", got.WearUsedPercent)
			}
		})
	}
}

// Written sectors pass 2^31 after 1 TiB: a failing, worn disk must not be lost
// to the width of a number in the same table.
func TestAHugeStatisticDoesNotLoseTheReport(t *testing.T) {
	got, err := parseSMART(`{"smart_status":{"passed":false},"ata_device_statistics":{"pages":[{"table":[
		{"name":"Logical Sectors Written","value":12884901888},
		{"name":"Percentage Used Endurance Indicator","value":91}]}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Passed == nil || *got.Passed || got.WearUsedPercent == nil || *got.WearUsedPercent != 91 {
		t.Fatalf("%+v", got)
	}
}

func TestParseSMARTSaysWhenItDidNotRead(t *testing.T) {
	if _, err := parseSMART(`{"smartctl":{"exit_status":2}}`); err == nil {
		t.Error("device not opened read as a report")
	}
	if _, err := parseSMART(`not json`); err == nil {
		t.Error("garbage read as a report")
	}
	// Bit 3: disk failing. That is a read, and the one that matters most.
	got, err := parseSMART(`{"smartctl":{"exit_status":8},"smart_status":{"passed":false}}`)
	if err != nil || got.Passed == nil || *got.Passed {
		t.Fatalf("%+v %v", got, err)
	}
}

type exitError int

func (e exitError) Error() string { return "exit status" }
func (e exitError) ExitCode() int { return int(e) }

func waitFor(t *testing.T, r *SMARTReader, disks []framev1beta1.ObservedDisk) {
	t.Helper()
	for range 200 {
		r.Annotate(disks)
		r.mu.Lock()
		done := !r.running
		r.mu.Unlock()
		if done {
			r.Annotate(disks)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("refresh never ended")
}

func ssd(path string) framev1beta1.ObservedDisk {
	no := false
	return framev1beta1.ObservedDisk{Path: path, SerialNumber: "S-" + path, Rotational: &no}
}

func TestReaderSkipsSpinningAndUnknownDisks(t *testing.T) {
	yes := true
	x := &recordingExec{stdout: `{"smart_status":{"passed":true}}`}
	r := NewSMARTReader(x)
	disks := []framev1beta1.ObservedDisk{
		{Path: "/dev/sda", SerialNumber: "HDD", Rotational: &yes},
		{Path: "/dev/sdb", SerialNumber: "UNKNOWN"},
		ssd("/dev/sdc"),
	}
	r.Annotate(disks)
	waitFor(t, r, disks)
	if len(x.calls) != 1 || x.calls[0][len(x.calls[0])-1] != "/dev/sdc" {
		t.Fatalf("%q", x.calls)
	}
	if disks[2].SMART == nil || disks[0].SMART != nil {
		t.Fatalf("%+v", disks)
	}
}

func TestReaderIsNotCalledAgainBeforeItsInterval(t *testing.T) {
	x := &recordingExec{stdout: `{}`}
	r := NewSMARTReader(x)
	clock := time.Now()
	r.now = func() time.Time { return clock }
	disks := []framev1beta1.ObservedDisk{ssd("/dev/sda")}
	r.Annotate(disks)
	waitFor(t, r, disks)
	r.Annotate(disks)
	waitFor(t, r, disks)
	if len(x.calls) != 1 {
		t.Fatalf("%d calls", len(x.calls))
	}
	clock = clock.Add(smartInterval)
	r.Annotate(disks)
	waitFor(t, r, disks)
	if len(x.calls) != 2 {
		t.Fatalf("%d calls", len(x.calls))
	}
}

func TestAPathOffThePatternIsNeverRun(t *testing.T) {
	x := &recordingExec{}
	r := NewSMARTReader(x)
	disks := []framev1beta1.ObservedDisk{ssd("/dev/sda --test=short")}
	r.Annotate(disks)
	waitFor(t, r, disks)
	if len(x.calls) != 0 {
		t.Fatalf("%q", x.calls)
	}
	if disks[0].SMART == nil || disks[0].SMART.Error == "" {
		t.Fatalf("%+v", disks[0].SMART)
	}
}

func TestFailuresAreStatesAndLeaveNoFigure(t *testing.T) {
	for name, err := range map[string]error{
		"absent":  exitError(127),
		"timeout": exitError(124),
		"busy":    errBusy,
		"other":   errors.New("boom"),
	} {
		x := &recordingExec{err: err}
		r := NewSMARTReader(x)
		disks := []framev1beta1.ObservedDisk{ssd("/dev/sda")}
		r.Annotate(disks)
		waitFor(t, r, disks)
		s := disks[0].SMART
		if s == nil || s.Error == "" || s.WearUsedPercent != nil || s.Passed != nil {
			t.Errorf("%s: %+v", name, s)
		}
	}
}

func TestAFailingDiskExitIsStillARead(t *testing.T) {
	x := &recordingExec{err: exitError(8), stdout: `{"smartctl":{"exit_status":8},"smart_status":{"passed":false}}`}
	r := NewSMARTReader(x)
	disks := []framev1beta1.ObservedDisk{ssd("/dev/sda")}
	r.Annotate(disks)
	waitFor(t, r, disks)
	if s := disks[0].SMART; s == nil || s.Passed == nil || *s.Passed || s.Error != "" {
		t.Fatalf("%+v", s)
	}
}
