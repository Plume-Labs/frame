package provision

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func goodSpec() Spec {
	return Spec{
		UID:      "b3f1c2d4-0000-4000-8000-000000000001",
		Hostname: "g9",
		BootMode: "UEFI",
		Network: Network{
			Address: "192.0.2.210/24",
			Gateway: "192.0.2.254",
			DNS:     []string{"192.0.2.254"},
		},
		Layout: Layout{Kind: LayoutMirror, Disks: []Disk{
			{ByID: "/dev/disk/by-id/scsi-aaa", SizeBytes: 300 << 30},
			{ByID: "/dev/disk/by-id/scsi-bbb", SizeBytes: 300 << 30},
		}},
		SSHPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILsytToxkJ2CiWuiv8BZ3hYpu7tFXn7Rwz+kc2gjbPSy frame-test-fixture",
		Cluster:      ClusterTarget{Mode: ClusterInit, K3sVersion: "v1.33.4+k3s1"},
	}
}

// parsed renders s and reads the document back as data, so every assertion
// below is about what subiquity will read, not about the text.
func parsed(t *testing.T, s Spec, beaconBase string) map[string]any {
	t.Helper()
	got, err := RenderAutoinstall(s, beaconBase, testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "#cloud-config\n") {
		t.Fatalf("cloud-init only treats user-data as config when its first line is #cloud-config:\n%s", got)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("rendered document is not YAML: %v\n%s", err, got)
	}
	ai, ok := doc["autoinstall"].(map[string]any)
	if !ok {
		t.Fatalf("no autoinstall section:\n%s", got)
	}
	return ai
}

func commands(t *testing.T, ai map[string]any, key string) []string {
	t.Helper()
	raw, ok := ai[key].([]any)
	if !ok {
		t.Fatalf("%s is %T, not a list -- subiquity's schema rejects null", key, ai[key])
	}
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		s, ok := c.(string)
		if !ok {
			t.Fatalf("%s holds a %T, not a string", key, c)
		}
		out = append(out, s)
	}
	return out
}

func TestRenderAutoinstallIsUnattendedAndOffTheArchiveDefaultsItNames(t *testing.T) {
	ai := parsed(t, goodSpec(), testBeaconBase)
	if ai["version"] != 1 {
		t.Errorf("version = %v", ai["version"])
	}
	if l, ok := ai["interactive-sections"].([]any); !ok || len(l) != 0 {
		t.Errorf("interactive-sections = %v; anything but an empty list stops the install for a human", ai["interactive-sections"])
	}
	if ai["updates"] != "security" {
		t.Errorf("updates = %v; the choice is made explicitly", ai["updates"])
	}
	if ai["shutdown"] != "reboot" {
		t.Errorf("shutdown = %v", ai["shutdown"])
	}
	if sw := ai["storage"].(map[string]any)["swap"].(map[string]any)["size"]; sw != 0 {
		t.Errorf("swap size = %v; kubelet nodes run without swap", sw)
	}
}

func TestRenderAutoinstallCarriesTheStaticNetwork(t *testing.T) {
	ai := parsed(t, goodSpec(), "")
	eth := ai["network"].(map[string]any)["ethernets"].(map[string]any)["frame0"].(map[string]any)
	if eth["match"].(map[string]any)["name"] != "en*" || eth["optional"] != true || eth["dhcp4"] != false {
		t.Errorf("stanza must match every en* port, optional, no DHCP: %v", eth)
	}
	if a := eth["addresses"].([]any); len(a) != 1 || a[0] != "192.0.2.210/24" {
		t.Errorf("addresses = %v", a)
	}
	r := eth["routes"].([]any)[0].(map[string]any)
	if r["to"] != "default" || r["via"] != "192.0.2.254" {
		t.Errorf("default route = %v", r)
	}
	if ns := eth["nameservers"].(map[string]any)["addresses"].([]any); len(ns) != 1 || ns[0] != "192.0.2.254" {
		t.Errorf("nameservers = %v", ns)
	}
}

// The address is normalised to the host address and prefix length, whatever
// spelling the spec used for it.
func TestRenderAutoinstallKeepsTheHostAddressNotTheNetwork(t *testing.T) {
	ai := parsed(t, goodSpec(), "")
	eth := ai["network"].(map[string]any)["ethernets"].(map[string]any)["frame0"].(map[string]any)
	if got := eth["addresses"].([]any)[0]; got != "192.0.2.210/24" {
		t.Errorf("address = %v, want the host address, not 192.0.2.0/24", got)
	}
}

func TestRenderAutoinstallLogsInByKeyOnly(t *testing.T) {
	ai := parsed(t, goodSpec(), "")
	ssh := ai["ssh"].(map[string]any)
	if ssh["allow-pw"] != false || ssh["install-server"] != true {
		t.Errorf("ssh = %v", ssh)
	}
	if k := ssh["authorized-keys"].([]any); len(k) != 1 || k[0] != goodSpec().SSHPublicKey {
		t.Errorf("authorized-keys = %v", k)
	}
	id := ai["identity"].(map[string]any)
	if id["username"] != "frame" || id["hostname"] != "g9" || !strings.HasPrefix(id["password"].(string), "$6$") {
		t.Errorf("identity = %v", id)
	}
	if !strings.Contains(strings.Join(commands(t, ai, "late-commands"), "\n"), "passwd -l frame") {
		t.Error("the account's password is not locked")
	}
}

// The four facts the controller and join.go depend on: sudo without a
// password, the install marker, the hostname, and a hosts entry for it (sudo
// prints "unable to resolve host" without one).
func TestRenderAutoinstallWritesWhatFrameChecksAfterReboot(t *testing.T) {
	late := strings.Join(commands(t, parsed(t, goodSpec(), ""), "late-commands"), "\n")
	for _, want := range []string{
		"echo 'frame ALL=(ALL) NOPASSWD:ALL' > /target/etc/sudoers.d/frame",
		"chmod 440 /target/etc/sudoers.d/frame",
		"echo '" + goodSpec().UID + "' > /target" + markerPath,
		"chmod 444 /target" + markerPath,
		"echo 'g9' > /target/etc/hostname",
		"'127.0.1.1 g9' >> /target/etc/hosts",
	} {
		if !strings.Contains(late, want) {
			t.Errorf("late-commands lack %q:\n%s", want, late)
		}
	}
}

// The size assertion runs first, and the early beacon only after it: a
// refused machine must not report `early`, or a deliberate refusal reads like
// progress.
func TestRenderAutoinstallAssertsDiskSizesBeforeAnythingElse(t *testing.T) {
	early := commands(t, parsed(t, goodSpec(), testBeaconBase), "early-commands")
	if len(early) != 4 {
		t.Fatalf("early-commands = %d entries, want assertion, resolution, beacon, heartbeat:\n%s", len(early), strings.Join(early, "\n"))
	}
	if !strings.Contains(early[0], diskSizeAssertion(goodSpec().Layout)) || !strings.Contains(early[0], "exit 1") {
		t.Errorf("first early-command is not the size guard:\n%s", early[0])
	}
	if !strings.Contains(early[1], "/autoinstall.yaml") {
		t.Errorf("second early-command does not resolve the disks:\n%s", early[1])
	}
	if !strings.Contains(early[2], BeaconURL(testBeaconBase, testBeaconToken, CheckpointEarly)) {
		t.Errorf("third early-command is not the early beacon:\n%s", early[2])
	}
	if early[3] != BeaconHeartbeat(testBeaconBase, testBeaconToken, CheckpointEarly) {
		t.Errorf("fourth early-command is not the heartbeat:\n%s", early[3])
	}
}

// subiquity reads each command's output; a heartbeat still holding the
// pipes would hang it at early-commands.
func TestBeaconHeartbeatDetachesAllThreeStreams(t *testing.T) {
	got := BeaconHeartbeat(testBeaconBase, testBeaconToken, CheckpointEarly)
	if !strings.HasSuffix(got, "</dev/null >/dev/null 2>&1 &") {
		t.Errorf("heartbeat not fully detached: %q", got)
	}
}

func TestRenderAutoinstallReportsLateAndFailed(t *testing.T) {
	ai := parsed(t, goodSpec(), testBeaconBase)
	late := commands(t, ai, "late-commands")
	if last := late[len(late)-1]; last != BeaconSend(testBeaconBase, testBeaconToken, CheckpointLate) {
		t.Errorf("last late-command is not the late beacon: %q", last)
	}
	errs := commands(t, ai, "error-commands")
	if len(errs) != 1 || errs[0] != BeaconSend(testBeaconBase, testBeaconToken, CheckpointFailed) {
		t.Errorf("error-commands = %v", errs)
	}
}

// The cold-start path has nothing reading beacons: none are rendered, and
// the lists are still lists.
func TestRenderAutoinstallWithNoBeaconBaseEmitsNoBeacons(t *testing.T) {
	got, err := RenderAutoinstall(goodSpec(), "", testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "/beacon/") || strings.Contains(got, "wget") {
		t.Errorf("beacon rendered with no base:\n%s", got)
	}
	ai := parsed(t, goodSpec(), "")
	if len(commands(t, ai, "error-commands")) != 0 {
		t.Error("error-commands should be an empty list")
	}
}

// Every command is run by sh; one that does not parse runs nothing at all --
// guard included -- and nothing but a parse catches it.
func TestRenderAutoinstallCommandsParseAsShell(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found")
	}
	ai := parsed(t, goodSpec(), testBeaconBase)
	for _, key := range []string{"early-commands", "late-commands", "error-commands"} {
		for _, c := range commands(t, ai, key) {
			cmd := exec.Command(shPath, "-n")
			cmd.Stdin = strings.NewReader(c + "\n")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s entry does not parse (sh -n): %v\n%s\n%s", key, err, out, c)
			}
		}
	}
}

// storage for each layout × boot mode. UEFI gets an ESP on every disk with
// grub_device on the ESP; Legacy a bios_grub partition on every disk with
// grub_device on the disk. A mirror is md0 raid1 over the two root
// partitions, both disks wiped recursively.
func TestRenderAutoinstallStorage(t *testing.T) {
	for _, c := range []struct {
		kind  LayoutKind
		disks []Disk
		mode  string
	}{
		{LayoutMirror, goodSpec().Layout.Disks, "UEFI"},
		{LayoutMirror, goodSpec().Layout.Disks, "Legacy"},
		{LayoutSingleDisk, goodSpec().Layout.Disks[:1], "UEFI"},
		{LayoutSingleDisk, goodSpec().Layout.Disks[:1], "Legacy"},
	} {
		t.Run(string(c.kind)+"/"+c.mode, func(t *testing.T) {
			s := goodSpec()
			s.Layout = Layout{Kind: c.kind, Disks: c.disks}
			s.BootMode = c.mode
			cfg := parsed(t, s, "")["storage"].(map[string]any)["config"].([]any)

			byType := map[string][]map[string]any{}
			for _, e := range cfg {
				m := e.(map[string]any)
				byType[m["type"].(string)] = append(byType[m["type"].(string)], m)
			}
			if len(byType["disk"]) != len(c.disks) {
				t.Fatalf("%d disks in storage, want %d", len(byType["disk"]), len(c.disks))
			}
			for i, d := range byType["disk"] {
				if d["path"] != diskPlaceholders[i] || d["wipe"] != "superblock-recursive" || d["ptable"] != "gpt" {
					t.Errorf("disk %d = %v", i, d)
				}
				if (d["grub_device"] == true) != (c.mode == "Legacy") {
					t.Errorf("disk %d grub_device = %v in %s mode", i, d["grub_device"], c.mode)
				}
			}
			var esp, biosgrub int
			for _, p := range byType["partition"] {
				switch p["flag"] {
				case "boot":
					esp++
					if p["grub_device"] != true {
						t.Errorf("ESP %v lacks grub_device", p)
					}
				case "bios_grub":
					biosgrub++
				}
			}
			wantESP, wantBG := len(c.disks), 0
			if c.mode == "Legacy" {
				wantESP, wantBG = 0, len(c.disks)
			}
			if esp != wantESP || biosgrub != wantBG {
				t.Errorf("ESP=%d bios_grub=%d, want %d/%d", esp, biosgrub, wantESP, wantBG)
			}
			if c.kind == LayoutMirror {
				if len(byType["raid"]) != 1 || byType["raid"][0]["raidlevel"] != "raid1" || len(byType["raid"][0]["devices"].([]any)) != 2 {
					t.Errorf("raid = %v", byType["raid"])
				}
			} else if len(byType["raid"]) != 0 {
				t.Errorf("single-disk has a raid: %v", byType["raid"])
			}
			var root bool
			for _, m := range byType["mount"] {
				root = root || m["path"] == "/"
			}
			if !root {
				t.Error("nothing mounted at /")
			}
		})
	}
}

// Each by-id path is resolved on the machine and written over its
// placeholder; a link that resolves to no block device aborts before
// anything is wiped.
func TestDiskResolutionRewritesEveryPlaceholderAndRefusesAMissingDisk(t *testing.T) {
	got := diskResolution(goodSpec().Layout)
	for i, d := range goodSpec().Layout.Disks {
		if !strings.Contains(got, "readlink -f '"+d.ByID+"'") || !strings.Contains(got, fmt.Sprintf("s#%s#$p%d#g", diskPlaceholders[i], i)) {
			t.Errorf("disk %s not resolved onto %s:\n%s", d.ByID, diskPlaceholders[i], got)
		}
	}
	if strings.Count(got, "is not a block device, refusing") != 2 {
		t.Errorf("each disk must refuse on its own:\n%s", got)
	}
}

// Marshalled, not templated: a key comment carrying YAML syntax stays a
// string and changes no structure.
func TestRenderAutoinstallMarshalsValuesInert(t *testing.T) {
	s := goodSpec()
	s.SSHPublicKey = `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILsytToxkJ2CiWuiv8BZ3hYpu7tFXn7Rwz+kc2gjbPSy a: b #c {d} &e *f "g"`
	ai := parsed(t, s, "")
	if k := ai["ssh"].(map[string]any)["authorized-keys"].([]any)[0]; k != s.SSHPublicKey {
		t.Errorf("key came back as %q", k)
	}
	if len(ai) != len(parsed(t, goodSpec(), "")) {
		t.Error("a value changed the document's top-level keys")
	}
}

func TestRenderAutoinstallNeverEmitsTheJoinToken(t *testing.T) {
	s := goodSpec()
	s.Cluster = ClusterTarget{Mode: ClusterJoin, ServerURL: "https://x:6443", JoinToken: "K10secretsecret", K3sVersion: "v1.33.4+k3s1"}
	got, err := RenderAutoinstall(s, testBeaconBase, testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "K10secretsecret") {
		t.Fatal("the join token is in the served document")
	}
}

func TestValidateSpecRefusals(t *testing.T) {
	for name, mutate := range map[string]func(*Spec){
		"empty UID":            func(s *Spec) { s.UID = "" },
		"UID with a quote":     func(s *Spec) { s.UID = "a'b" },
		"UID with a newline":   func(s *Spec) { s.UID = "a\nb" },
		"empty hostname":       func(s *Spec) { s.Hostname = "" },
		"uppercase hostname":   func(s *Spec) { s.Hostname = "G9" },
		"underscore hostname":  func(s *Spec) { s.Hostname = "a_b" },
		"leading dash":         func(s *Spec) { s.Hostname = "-a" },
		"YAML in hostname":     func(s *Spec) { s.Hostname = "a: b" },
		"64-char hostname":     func(s *Spec) { s.Hostname = strings.Repeat("a", 64) },
		"no boot mode":         func(s *Spec) { s.BootMode = "" },
		"lowercase boot mode":  func(s *Spec) { s.BootMode = "uefi" },
		"bad CIDR":             func(s *Spec) { s.Network.Address = "192.0.2.210" },
		"IPv6":                 func(s *Spec) { s.Network.Address = "fd00::1/64" },
		"bad gateway":          func(s *Spec) { s.Network.Gateway = "gw" },
		"gateway is the host":  func(s *Spec) { s.Network.Gateway = "192.0.2.210" },
		"gateway off-subnet":   func(s *Spec) { s.Network.Gateway = "192.168.3.254" },
		"IPv6 gateway":         func(s *Spec) { s.Network.Gateway = "fd00::1" },
		"no DNS":               func(s *Spec) { s.Network.DNS = nil },
		"non-IP DNS":           func(s *Spec) { s.Network.DNS = []string{"dns.example"} },
		"private key":          func(s *Spec) { s.SSHPublicKey = "-----BEGIN OPENSSH PRIVATE KEY-----" },
		"two lines":            func(s *Spec) { s.SSHPublicKey = goodSpec().SSHPublicKey + "\n" + goodSpec().SSHPublicKey },
		"two keys on one line": func(s *Spec) { s.SSHPublicKey = goodSpec().SSHPublicKey + " " + goodSpec().SSHPublicKey },
		"authorized_keys opts": func(s *Spec) { s.SSHPublicKey = `command="curl x|sh",no-pty ` + goodSpec().SSHPublicKey },
		"not a key":            func(s *Spec) { s.SSHPublicKey = "hello" },
	} {
		t.Run(name, func(t *testing.T) {
			s := goodSpec()
			mutate(&s)
			if err := ValidateSpec(s); err == nil {
				t.Errorf("accepted")
			}
			if _, err := RenderAutoinstall(s, "", testBeaconToken); err == nil {
				t.Errorf("rendered")
			}
		})
	}
}

func TestRenderAutoinstallRefusesABeaconBaseThatWouldBreakTheShellWord(t *testing.T) {
	if _, err := RenderAutoinstall(goodSpec(), "http://x/'; reboot", testBeaconToken); err == nil {
		t.Fatal("accepted")
	}
}
