package installer

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func consoleServer(t *testing.T, doc *string) *Server {
	t.Helper()
	hw := detect(t, false)
	return &Server{
		Hardware: func(context.Context) (Hardware, error) { return hw, nil },
		Start:    func(_ context.Context, d string) error { *doc = d; return nil },
	}
}

// Disk numbers follow the order Detect returns them in; find the SSD's.
func diskNumber(t *testing.T, srv *Server, byID string) string {
	t.Helper()
	hw, _ := srv.Hardware(context.Background())
	for i, d := range hw.Disks {
		if d.ByID == byID {
			return string(rune('1' + i))
		}
	}
	t.Fatalf("disk %s not found", byID)
	return ""
}

func TestConsoleInstallsAfterTheTypedConfirmation(t *testing.T) {
	var doc string
	srv := consoleServer(t, &doc)
	in := strings.Join([]string{
		"tacos", "1", diskNumber(t, srv, ssdID), "2012E2A5B6C1",
		"static", "192.168.2.201/24", "192.168.2.1", "192.168.2.1", testKey, "ERASE", "",
	}, "\n")
	var out bytes.Buffer
	if err := RunConsole(context.Background(), strings.NewReader(in), &out, srv); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.HasPrefix(doc, "#cloud-config") || !strings.Contains(doc, ssdID) {
		t.Errorf("Start got %q", doc)
	}
	if !strings.Contains(out.String(), "ERASE /dev/sda") {
		t.Error("the summary did not say what is erased")
	}
}

func TestConsoleDoesNothingWithoutErase(t *testing.T) {
	var doc string
	srv := consoleServer(t, &doc)
	in := strings.Join([]string{
		"tacos", "0", diskNumber(t, srv, ssdID), "2012E2A5B6C1",
		"static", "192.168.2.201/24", "192.168.2.1", "192.168.2.1", testKey, "yes",
	}, "\n") + "\n"
	var out bytes.Buffer
	// "yes" is not ERASE: it starts over, and the input then ends (EOF is an error, not a loop).
	if err := RunConsole(context.Background(), strings.NewReader(in), &out, srv); err == nil {
		t.Error("expected the input to end")
	}
	if doc != "" || !strings.Contains(out.String(), "Not confirmed") {
		t.Errorf("started=%v, output lacks the refusal:\n%s", doc != "", out.String())
	}
}

func TestConsoleRefusesAWrongSerialAndRestarts(t *testing.T) {
	var doc string
	srv := consoleServer(t, &doc)
	disk := diskNumber(t, srv, ssdID)
	in := strings.Join([]string{
		"tacos", "0", disk, "wrong", "dhcp", testKey,
		"tacos", "0", disk, "2012E2A5B6C1", "dhcp", testKey, "ERASE",
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := RunConsole(context.Background(), strings.NewReader(in), &out, srv); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Refused") || doc == "" {
		t.Errorf("refused=%v started=%v", strings.Contains(out.String(), "Refused"), doc != "")
	}
	if strings.Contains(out.String(), "ZC1ABC") && !strings.Contains(out.String(), "serial ZC1ABC") {
		t.Error("unexpected serial in the output")
	}
}

func TestConsoleSaysWhatItIgnoresAndRefusesAnUnusableDisk(t *testing.T) {
	var doc string
	srv := consoleServer(t, &doc)
	live := diskNumber(t, srv, "/dev/disk/by-id/ata-LIVE_LIVE1")
	in := strings.Join([]string{"tacos", "9", live + " x", "LIVE1", "dhcp", testKey}, "\n") + "\n"
	var out bytes.Buffer
	_ = RunConsole(context.Background(), strings.NewReader(in), &out, srv)
	o := out.String()
	for _, want := range []string{`"9" is not a role number`, `"x" is not a disk number`, "mounted partition"} {
		if !strings.Contains(o, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if doc != "" {
		t.Error("an in-use disk started an installation")
	}
}

func TestConsoleMirrorOfTwoDisks(t *testing.T) {
	var doc string
	srv := consoleServer(t, &doc)
	one := diskNumber(t, srv, ssdID)
	two := diskNumber(t, srv, "/dev/disk/by-id/ata-ST4000_ZC1ABC")
	in := strings.Join([]string{"tacos", "0", one + " " + two, "2012E2A5B6C1", "ZC1ABC", "dhcp", testKey, "ERASE"}, "\n") + "\n"
	var out bytes.Buffer
	if err := RunConsole(context.Background(), strings.NewReader(in), &out, srv); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(doc, ssdID) || !strings.Contains(doc, "ZC1ABC") {
		t.Error("a mirror must name both disks")
	}
}

func TestConsoleReportsAFailedStartAndStops(t *testing.T) {
	hw := detect(t, false)
	srv := &Server{
		Hardware: func(context.Context) (Hardware, error) { return hw, nil },
		Start:    func(context.Context, string) error { return errors.New("restart timed out") },
	}
	in := strings.Join([]string{"tacos", "0", "1", "2012E2A5B6C1", "dhcp", testKey, "ERASE"}, "\n") + "\n"
	var out bytes.Buffer
	if err := RunConsole(context.Background(), strings.NewReader(in), &out, srv); err == nil {
		t.Fatal("a failed Start must be returned")
	}
	if !strings.Contains(out.String(), "Failed: restart timed out") {
		t.Errorf("the person is not told:\n%s", out.String())
	}
}

func TestCleanDropsEscapeSequences(t *testing.T) {
	if got := clean("evil\x1b[2Jdisk\r\n"); got != "evil[2Jdisk" {
		t.Errorf("clean = %q", got)
	}
}

func TestConsoleAndPageShareTheOneInstallationLock(t *testing.T) {
	var doc string
	srv := consoleServer(t, &doc)
	hw, _ := srv.Hardware(context.Background())
	plan, err := BuildPlan(hw, answers(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Run(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if status, err := srv.Run(context.Background(), plan); err == nil || status != 409 {
		t.Errorf("second run: %d, %v", status, err)
	}
}
