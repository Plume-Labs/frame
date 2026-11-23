package installer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl test@frame"

const lsblkJSON = `{"blockdevices":[
 {"path":"/dev/loop0","size":100,"rota":false,"model":null,"serial":null,"type":"loop","tran":null,"rm":false},
 {"path":"/dev/sda","size":500107862016,"rota":false,"model":"CT500MX500SSD1","serial":"2012E2A5B6C1","type":"disk","tran":"sata","rm":false},
 {"path":"/dev/sdb","size":8000000000,"rota":false,"model":"USB DISK","serial":"","type":"disk","tran":"usb","rm":true},
 {"path":"/dev/sdc","size":4000787030016,"rota":true,"model":"ST4000","serial":"ZC1ABC","type":"disk","tran":"sata","rm":false}]}`

func fakeRoot(t *testing.T, uefi bool) string {
	t.Helper()
	root := t.TempDir()
	w := func(rel, s string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("proc/cpuinfo", "processor\t: 0\nmodel name\t: Intel(R) Xeon(R) CPU E5-2420\nflags\t\t: fpu sse4_2 avx aes\n")
	w("proc/meminfo", "MemTotal:       16384000 kB\n")
	if uefi {
		w("sys/firmware/efi/.keep", "")
	}
	w("sys/class/net/eth0/device/.keep", "")
	w("sys/class/net/eth0/address", "AA:BB:CC:00:11:22\n")
	w("sys/class/net/eth0/operstate", "up\n")
	w("sys/class/net/lo/address", "00:00:00:00:00:00\n")
	for _, d := range []string{"sda", "sdb", "sdc"} {
		w("dev/"+d, "")
	}
	byid := filepath.Join(root, "dev/disk/by-id")
	if err := os.MkdirAll(byid, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, dev := range map[string]string{
		"wwn-0x500a0751e2a5b6c1": "sda", "ata-CT500MX500SSD1_2012E2A5B6C1": "sda",
		"ata-CT500MX500SSD1_2012E2A5B6C1-part1": "sda",
		"usb-USB_DISK-0:0":                      "sdb", "ata-ST4000_ZC1ABC": "sdc",
	} {
		if err := os.Symlink(filepath.Join(root, "dev", dev), filepath.Join(byid, name)); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func fakeSource(root string) Source {
	return Source{Root: root, Run: func(context.Context, string, ...string) ([]byte, error) { return []byte(lsblkJSON), nil }}
}

func detect(t *testing.T, uefi bool) Hardware {
	t.Helper()
	root := fakeRoot(t, uefi)
	hw, err := Detect(context.Background(), fakeSource(root))
	if err != nil {
		t.Fatal(err)
	}
	// The fake tree's symlinks resolve under root: Detect strips it. Paths here are /dev/sdX.
	return hw
}

func TestDetectReadsTheMachine(t *testing.T) {
	hw := detect(t, true)
	if !hw.UEFI || !hw.AVX || !hw.AESNI || hw.MemBytes != 16384000*1024 || !strings.Contains(hw.CPUModel, "E5-2420") {
		t.Errorf("hardware = %+v", hw)
	}
	if len(hw.NICs) != 1 || hw.NICs[0].MAC != "aa:bb:cc:00:11:22" || !hw.NICs[0].Up {
		t.Errorf("nics = %+v (lo must not appear)", hw.NICs)
	}
	if len(hw.Disks) != 3 {
		t.Fatalf("disks = %+v (loop must not appear)", hw.Disks)
	}
	d, ok := findDisk(hw, "/dev/disk/by-id/ata-CT500MX500SSD1_2012E2A5B6C1")
	if !ok {
		t.Fatalf("model+serial by-id name not preferred over wwn: %+v", hw.Disks)
	}
	if d.SizeBytes != 500107862016 || d.Removable {
		t.Errorf("sda = %+v", d)
	}
	if usb, _ := findDisk(hw, "/dev/disk/by-id/usb-USB_DISK-0:0"); !usb.Removable {
		t.Error("USB disk not marked removable")
	}
}

func TestDetectBIOSWhenNoEFI(t *testing.T) {
	if detect(t, false).UEFI {
		t.Error("UEFI reported without /sys/firmware/efi")
	}
}

const ssdID = "/dev/disk/by-id/ata-CT500MX500SSD1_2012E2A5B6C1"

func answers() Answers {
	return Answers{
		Hostname: "tacos", Role: "control-plane+ceph",
		Disks:   []DiskChoice{{ByID: ssdID, Confirm: "2012E2A5B6C1"}},
		Address: "192.168.2.201/24", Gateway: "192.168.2.1", DNS: []string{"192.168.2.1"},
		SSHPublicKey: testKey,
	}
}

func TestBuildPlanProducesAnAutoinstallForTheChosenDiskOnly(t *testing.T) {
	hw := detect(t, false)
	p, err := BuildPlan(hw, answers(), "uid-1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Spec.BootMode != "Legacy" || p.Spec.Layout.Disks[0].SizeBytes != 500107862016 {
		t.Errorf("spec = %+v", p.Spec)
	}
	doc, err := p.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, ssdID) || strings.Contains(doc, "ZC1ABC") {
		t.Error("document must name the chosen disk and no other")
	}
	if !strings.HasPrefix(p.Summary[0], "ERASE /dev/sda") {
		t.Errorf("summary must lead with what is erased: %v", p.Summary)
	}
}

func TestBuildPlanRefusals(t *testing.T) {
	hw := detect(t, true)
	cases := map[string]func(*Answers){
		"no disk":      func(a *Answers) { a.Disks = nil },
		"unknown disk": func(a *Answers) { a.Disks[0].ByID = "/dev/disk/by-id/ata-nothing" },
		"usb disk": func(a *Answers) {
			a.Disks = []DiskChoice{{ByID: "/dev/disk/by-id/usb-USB_DISK-0:0", Confirm: "usb-USB_DISK-0:0"}}
		},
		"serial not retyped": func(a *Answers) { a.Disks[0].Confirm = "" },
		"wrong serial":       func(a *Answers) { a.Disks[0].Confirm = "ZC1ABC" },
		"bad hostname":       func(a *Answers) { a.Hostname = "Tacos;reboot" },
		"bad role":           func(a *Answers) { a.Role = "admin" },
		"private key":        func(a *Answers) { a.SSHPublicKey = "-----BEGIN OPENSSH PRIVATE KEY-----" },
		"dhcp with address":  func(a *Answers) { a.DHCP = true },
		"gateway off subnet": func(a *Answers) { a.Gateway = "10.9.9.9" },
	}
	for name, mut := range cases {
		a := answers()
		mut(&a)
		if _, err := BuildPlan(hw, a, "uid-1"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBuildPlanMirrorAndDHCP(t *testing.T) {
	hw := detect(t, true)
	a := answers()
	a.DHCP, a.Address, a.Gateway, a.DNS = true, "", "", nil
	a.Disks = append(a.Disks, DiskChoice{ByID: "/dev/disk/by-id/ata-ST4000_ZC1ABC", Confirm: "ZC1ABC"})
	p, err := BuildPlan(hw, a, "uid-2")
	if err != nil {
		t.Fatal(err)
	}
	if p.Spec.Layout.Kind != "mirror" || p.Spec.BootMode != "UEFI" {
		t.Errorf("spec = %+v", p.Spec)
	}
}

func TestNoAVXIsSaidInTheSummary(t *testing.T) {
	hw := detect(t, true)
	hw.AVX = false
	p, err := BuildPlan(hw, answers(), "u")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.Summary, "\n"), "no-avx") {
		t.Errorf("summary = %v", p.Summary)
	}
}

func newServer(t *testing.T, started *string) (*httptest.Server, *Server) {
	t.Helper()
	hw := detect(t, true)
	s := &Server{
		Hardware: func(context.Context) (Hardware, error) { return hw, nil },
		Start:    func(_ context.Context, doc string) error { *started = doc; return nil },
	}
	return httptest.NewServer(s.Handler()), s
}

func post(t *testing.T, url string, v any, hdr map[string]string) *http.Response {
	t.Helper()
	b, _ := json.Marshal(v)
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Body.Close() })
	return r
}

func TestServerInstallsOnceAndServesThePage(t *testing.T) {
	var doc string
	ts, _ := newServer(t, &doc)
	defer ts.Close()

	if r, err := http.Get(ts.URL + "/"); err != nil || r.StatusCode != 200 {
		t.Fatalf("page: %v %v", r, err)
	}
	if r := post(t, ts.URL+"/api/summary", answers(), nil); r.StatusCode != 200 {
		t.Fatalf("summary status %d", r.StatusCode)
	}
	if doc != "" {
		t.Fatal("summary started the installation")
	}
	if r := post(t, ts.URL+"/api/install", answers(), nil); r.StatusCode != http.StatusAccepted {
		t.Fatalf("install status %d", r.StatusCode)
	}
	if !strings.HasPrefix(doc, "#cloud-config") {
		t.Errorf("Start got %q", doc)
	}
	if r := post(t, ts.URL+"/api/install", answers(), nil); r.StatusCode != http.StatusConflict {
		t.Errorf("second install status %d, want 409", r.StatusCode)
	}
}

func TestServerRefusesWithoutWipingOnABadSerial(t *testing.T) {
	var doc string
	ts, _ := newServer(t, &doc)
	defer ts.Close()
	a := answers()
	a.Disks[0].Confirm = "nope"
	if r := post(t, ts.URL+"/api/install", a, nil); r.StatusCode != http.StatusUnprocessableEntity || doc != "" {
		t.Errorf("status %d, started=%v", r.StatusCode, doc != "")
	}
}

func TestServerRefusesForeignOriginsHostsAndFormPosts(t *testing.T) {
	var doc string
	ts, _ := newServer(t, &doc)
	defer ts.Close()
	if r := post(t, ts.URL+"/api/install", answers(), map[string]string{"Origin": "http://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin: %d", r.StatusCode)
	}
	if r := post(t, ts.URL+"/api/install", answers(), map[string]string{"Content-Type": "text/plain"}); r.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d", r.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/hardware", nil)
	req.Host = "evil.example"
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != http.StatusForbidden {
		t.Errorf("rebinding host: %d", r.StatusCode)
	}
	if doc != "" {
		t.Error("a refused request started the installation")
	}
}

func TestServerRejectsUnknownFields(t *testing.T) {
	var doc string
	ts, _ := newServer(t, &doc)
	defer ts.Close()
	if r := post(t, ts.URL+"/api/install", map[string]any{"hostname": "x", "rawStorage": "..."}, nil); r.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d", r.StatusCode)
	}
}

func TestListenLoopbackRefusesOtherAddresses(t *testing.T) {
	for _, a := range []string{"0.0.0.0:8099", ":8099", "192.168.2.5:8099", "example.com:80"} {
		if ln, err := ListenLoopback(a); err == nil {
			ln.Close()
			t.Errorf("%s accepted", a)
		}
	}
	ln, err := ListenLoopback("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}
