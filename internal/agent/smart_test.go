package agent

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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

// The options of smartctl that write to a disk or its settings, in long form.
// Short forms are refused whole, since they take their value joined (-tshort),
// and --log takes only devstat: some of its values write (scterc,70,70,
// sataphy,reset).
func TestSMARTCommandCarriesNoWritingOption(t *testing.T) {
	if err := writingOption(smartArgvPrefix); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"-tshort", "-t", "--test=short", "--log=scterc,70,70", "--log=sataphy,reset", "--smart=off", "--set=standby,now"} {
		if writingOption(append(slices.Clone(smartArgvPrefix), bad)) == nil {
			t.Errorf("%q passed", bad)
		}
	}
}

func writingOption(prefix []string) error {
	forbidden := []string{"--test", "--abort", "--captive", "--smart", "--saveauto", "--offlineauto", "--set", "--firmwarebug", "--vendorattribute", "--device", "--drivedb"}
	for _, arg := range prefix[1:] {
		name, value, _ := strings.Cut(arg, "=")
		switch {
		case !strings.HasPrefix(arg, "--"), slices.Contains(forbidden, name), name == "--log" && value != "devstat":
			return fmt.Errorf("%q", arg)
		}
	}
	return nil
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
		"/dev/sda\x00", "/dev/SDA", "/dev/mem", "/dev/nvme0", "/dev/sdabcd", "/dev/nvme1234n1", "/dev/nvme0n1234",
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
		"empty":             nil,
		"no device":         smartArgvPrefix,
		"self-test":         withTest,
		"extra argument":    append(slices.Clone(good), "--test=short"),
		"option in between": append(slices.Clone(smartArgvPrefix), "--test=short", "/dev/sda"),
		"option as device":  append(slices.Clone(smartArgvPrefix), "-t"),
		"other path":        append(slices.Clone(smartArgvPrefix), "/etc/shadow"),
		"other tool":        {"sh", "-c", "id"},
		"storage command":   storageCommands[0],
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
			"ata_smart_attributes":{"table":[{"id":233,"name":"Media_Wearout_Indicator","value":10}]}}`, new(int32(12))},
		{"an NVMe reading below nothing", `{"nvme_smart_health_information_log":{"percentage_used":-1}}`, new(int32(0))},
		{"Media_Wearout_Indicator counts down", `{"ata_smart_attributes":{"table":[{"id":233,"name":"Media_Wearout_Indicator","value":97}]}}`, new(int32(3))},
		{"attribute order", `{"ata_smart_attributes":{"table":[{"id":177,"name":"Wear_Leveling_Count","value":50},{"id":231,"name":"SSD_Life_Left","value":90}]}}`, new(int32(10))},
		{"a value past 100 is not a percentage", `{"ata_smart_attributes":{"table":[{"id":177,"name":"Wear_Leveling_Count","value":253}]}}`, nil},
		{"nothing known", `{"ata_smart_attributes":{"table":[{"id":9,"name":"Power_On_Hours","value":99}]}}`, nil},
		{"233 means flash writes here, not wear", `{"ata_smart_attributes":{"table":[{"id":233,"name":"Flash_Writes_GiB","value":100},{"id":231,"name":"SSD_Life_Left","value":3}]}}`, new(int32(97))},
		{"233 is SandForce's own", `{"ata_smart_attributes":{"table":[{"id":233,"name":"SandForce_Internal","value":0},{"id":231,"name":"SSD_Life_Left","value":95}]}}`, new(int32(5))},
		{"231 is a temperature", `{"ata_smart_attributes":{"table":[{"id":231,"name":"Controller_Temperature","value":40}]}}`, nil},
		{"the same life by another ID", `{"ata_smart_attributes":{"table":[{"id":169,"name":"Remaining_Lifetime_Perc","value":80}]}}`, new(int32(20))},
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
	if _, err := parseSMART(`{"smartctl":{"exit_status":1}}`); err == nil {
		t.Error("bad command line read as a report")
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

// smartctl sets passed to false on any NVMe critical warning. A disk only too hot
// is not failing; one with any other bit, alone or with heat, is.
func TestNVMeHeatIsNotAFailingDisk(t *testing.T) {
	for warning, passed := range map[string]bool{"2": true, "4": false, "6": false, "1": false} {
		got, err := parseSMART(`{"smart_status":{"passed":false},"nvme_smart_health_information_log":{"critical_warning":` + warning + `}}`)
		if err != nil || got.Passed == nil || *got.Passed != passed {
			t.Errorf("warning %s: %+v %v", warning, got, err)
		}
	}
	for _, silent := range []string{
		`{"nvme_smart_health_information_log":{"critical_warning":2}}`,
		`{"smart_status":{},"nvme_smart_health_information_log":{"critical_warning":2}}`,
	} {
		got, err := parseSMART(silent)
		if err != nil || got.Passed != nil {
			t.Fatalf("a verdict made up: %+v %v", got, err)
		}
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

func TestReaderRunsUnderTheTimeout(t *testing.T) {
	x := &recordingExec{stdout: `{}`}
	r := NewSMARTReader(x)
	disks := []framev1beta1.ObservedDisk{ssd("/dev/sda")}
	r.Annotate(disks)
	waitFor(t, r, disks)
	want := append(append(slices.Clone(timeoutPrefix), smartArgvPrefix...), "/dev/sda")
	if len(x.calls) != 1 || !slices.Equal(x.calls[0], want) {
		t.Fatalf("%q", x.calls)
	}
}

// smartctl comes from the image: through nsenter it would be the node's, or none.
func TestImageExecutorDoesNotEnterTheHost(t *testing.T) {
	x := NewImageStorageExecutor()
	if len(x.Prefix) != 0 {
		t.Fatalf("%q", x.Prefix)
	}
	// The one bound on a smartctl stuck in the kernel.
	if host := NewHostStorageExecutor(); x.HardDeadline != host.HardDeadline {
		t.Fatalf("%s", x.HardDeadline)
	}
	reader, ok := NewNodeSMARTReader().exec.(*HostStorageExecutor)
	if !ok || len(reader.Prefix) != 0 {
		t.Fatalf("%+v", reader)
	}
}

func TestReaderIsNotCalledAgainBeforeItsInterval(t *testing.T) {
	x := &recordingExec{stdout: `{"smart_status":{"passed":true}}`}
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
	clock = clock.Add(smartRetry) // a good read waits its whole interval
	r.Annotate(disks)
	waitFor(t, r, disks)
	if len(x.calls) != 1 {
		t.Fatalf("%d calls", len(x.calls))
	}
	clock = clock.Add(smartInterval - smartRetry)
	r.Annotate(disks)
	waitFor(t, r, disks)
	if len(x.calls) != 2 {
		t.Fatalf("%d calls", len(x.calls))
	}
}

// A disk can fail in every way smartctl reports at once, and that is 124, the
// code timeout uses for itself.
func TestAReportBeatsWhatItsExitCodeLooksLike(t *testing.T) {
	x := &recordingExec{err: exitError(124), stdout: `{"smartctl":{"exit_status":124},"smart_status":{"passed":false}}`}
	r := NewSMARTReader(x)
	disks := []framev1beta1.ObservedDisk{ssd("/dev/sda")}
	r.Annotate(disks)
	waitFor(t, r, disks)
	if s := disks[0].SMART; s == nil || s.Passed == nil || *s.Passed || s.Error != "" || s.ReadAt == nil {
		t.Fatalf("%+v", s)
	}
}

func TestAFailedReadIsTriedAgainSooner(t *testing.T) {
	x := &recordingExec{err: errors.New("boom")}
	r := NewSMARTReader(x)
	clock := time.Now()
	r.now = func() time.Time { return clock }
	disks := []framev1beta1.ObservedDisk{ssd("/dev/sda")}
	r.Annotate(disks)
	waitFor(t, r, disks)
	clock = clock.Add(smartRetry - time.Minute)
	r.Annotate(disks)
	waitFor(t, r, disks)
	if len(x.calls) != 1 {
		t.Fatalf("%d calls", len(x.calls))
	}
	clock = clock.Add(time.Minute)
	r.Annotate(disks)
	waitFor(t, r, disks)
	if len(x.calls) != 2 {
		t.Fatalf("%d calls", len(x.calls))
	}
}

type perDevice map[string]string

func (p perDevice) Exec(argv []string) (string, string, error) {
	return p[argv[len(argv)-1]], "", nil
}

type countingExec struct {
	StorageExecutor
	mu    sync.Mutex
	calls map[string]int
}

func (c *countingExec) Exec(argv []string) (string, string, error) {
	c.mu.Lock()
	c.calls[argv[len(argv)-1]]++
	c.mu.Unlock()
	return c.StorageExecutor.Exec(argv)
}

// One disk failing for good must not mean the others are read every half hour,
// wherever it sits in the list.
func TestRetriesAreCountedPerDisk(t *testing.T) {
	x := &countingExec{StorageExecutor: perDevice{"/dev/sdb": `{"smart_status":{"passed":true}}`}, calls: map[string]int{}}
	r := NewSMARTReader(x)
	clock := time.Now()
	r.now = func() time.Time { return clock }
	disks := []framev1beta1.ObservedDisk{ssd("/dev/sda"), ssd("/dev/sdb")}
	for range 3 {
		r.Annotate(disks)
		waitFor(t, r, disks)
		clock = clock.Add(smartRetry)
	}
	if x.calls["/dev/sda"] != 3 || x.calls["/dev/sdb"] != 1 {
		t.Fatalf("%+v", x.calls)
	}
}

// Bit 2 set, no self-assessment, a wear figure all the same: that figure counts.
func TestAPartialReportIsKeptDespiteItsExitCode(t *testing.T) {
	x := &recordingExec{err: exitError(4), stdout: `{"smartctl":{"exit_status":4},"nvme_smart_health_information_log":{"percentage_used":7}}`}
	r := NewSMARTReader(x)
	disks := []framev1beta1.ObservedDisk{ssd("/dev/sda")}
	r.Annotate(disks)
	waitFor(t, r, disks)
	if s := disks[0].SMART; s == nil || s.Error != "" || s.WearUsedPercent == nil || *s.WearUsedPercent != 7 {
		t.Fatalf("%+v", s)
	}
}

func TestNoOutputAndNoErrorIsAFailedRead(t *testing.T) {
	x := &recordingExec{}
	r := NewSMARTReader(x)
	disks := []framev1beta1.ObservedDisk{ssd("/dev/sda")}
	r.Annotate(disks)
	waitFor(t, r, disks)
	if s := disks[0].SMART; s == nil || s.Error == "" {
		t.Fatalf("%+v", s)
	}
}

// A report that says nothing, next to a failure, is the failure.
func TestAnEmptyReportWithAnErrorKeepsTheError(t *testing.T) {
	for _, stdout := range []string{"null", "{}"} {
		x := &recordingExec{err: errors.New("boom"), stdout: stdout}
		r := NewSMARTReader(x)
		disks := []framev1beta1.ObservedDisk{ssd("/dev/sda")}
		r.Annotate(disks)
		waitFor(t, r, disks)
		if s := disks[0].SMART; s == nil || !strings.Contains(s.Error, "boom") {
			t.Fatalf("%s: %+v", stdout, s)
		}
	}
}

func TestSmartctlsOwnWordsReachTheError(t *testing.T) {
	x := &recordingExec{err: exitError(2), stdout: `{"smartctl":{"exit_status":2,"messages":[{"string":"Smartctl open device failed"}]}}`}
	r := NewSMARTReader(x)
	disks := []framev1beta1.ObservedDisk{ssd("/dev/sda")}
	r.Annotate(disks)
	waitFor(t, r, disks)
	if s := disks[0].SMART; s == nil || !strings.Contains(s.Error, "open device failed") {
		t.Fatalf("%+v", s)
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
	for name, tt := range map[string]struct {
		err  error
		says string
	}{
		"absent":  {exitError(127), "not found"},
		"timeout": {exitError(124), "timed out"},
		"killed":  {exitError(137), "timed out"},
		"busy":    {errBusy, errBusy.Error()},
		"other":   {errors.New("boom"), "boom"},
	} {
		x := &recordingExec{err: tt.err}
		r := NewSMARTReader(x)
		disks := []framev1beta1.ObservedDisk{ssd("/dev/sda")}
		r.Annotate(disks)
		waitFor(t, r, disks)
		s := disks[0].SMART
		if s == nil || !strings.Contains(s.Error, tt.says) || s.WearUsedPercent != nil || s.Passed != nil {
			t.Errorf("%s: %+v", name, s)
		}
	}
}

// The error is published in a CR field of 256 at most: one past it would get the
// whole status refused.
func TestALongErrorFitsItsField(t *testing.T) {
	x := &recordingExec{err: errors.New(strings.Repeat("é", 1000))}
	r := NewSMARTReader(x)
	disks := []framev1beta1.ObservedDisk{ssd("/dev/sda")}
	r.Annotate(disks)
	waitFor(t, r, disks)
	if s := disks[0].SMART; s == nil || utf8.RuneCountInString(s.Error) != 256 {
		t.Fatalf("%+v", s)
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
