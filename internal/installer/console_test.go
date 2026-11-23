package installer

import (
	"bytes"
	"context"
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
	// "yes" is not ERASE: it starts over, and the input then ends.
	if err := RunConsole(context.Background(), strings.NewReader(in), &out, srv); err == nil {
		t.Error("expected the input to end")
	}
	if doc != "" {
		t.Error("an installation started without the typed ERASE")
	}
}

func TestConsoleRefusesAWrongSerialAndRestarts(t *testing.T) {
	var doc string
	srv := consoleServer(t, &doc)
	bad := []string{"tacos", "0", diskNumber(t, srv, ssdID), "wrong", "dhcp", testKey}
	good := []string{"tacos", "0", diskNumber(t, srv, ssdID), "2012E2A5B6C1", "dhcp", testKey, "ERASE"}
	var out bytes.Buffer
	in := strings.Join(append(bad, good...), "\n") + "\n"
	if err := RunConsole(context.Background(), strings.NewReader(in), &out, srv); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Refused") || doc == "" {
		t.Errorf("refused=%v started=%v", strings.Contains(out.String(), "Refused"), doc != "")
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
