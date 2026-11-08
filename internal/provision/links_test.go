package provision

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Bond and VLAN declared at installation (R-2). The tests read the rendered
// document back -- the last time these fields existed, they validated and
// then vanished on the way to the installer.

const (
	macA = "52:54:00:aa:00:01"
	macB = "52:54:00:aa:00:02"
)

func bondVLANSpec() Spec {
	s := goodSpec()
	s.Network.Bond = &Bond{Members: []string{"52:54:00:AA:00:01", macB}}
	s.Network.VLAN = &VLAN{ID: 20}
	return s
}

// The netplan the test reads back, typed as netplan's own schema.
type readNetplan struct {
	Version   int `yaml:"version"`
	Ethernets map[string]struct {
		Match     map[string]string `yaml:"match"`
		Addresses []string          `yaml:"addresses"`
		Optional  bool              `yaml:"optional"`
	} `yaml:"ethernets"`
	Bonds map[string]struct {
		Interfaces []string            `yaml:"interfaces"`
		Parameters map[string]string   `yaml:"parameters"`
		Addresses  []string            `yaml:"addresses"`
		Routes     []map[string]string `yaml:"routes"`
	} `yaml:"bonds"`
	VLANs map[string]struct {
		ID          int                 `yaml:"id"`
		Link        string              `yaml:"link"`
		Addresses   []string            `yaml:"addresses"`
		Routes      []map[string]string `yaml:"routes"`
		Nameservers map[string][]string `yaml:"nameservers"`
	} `yaml:"vlans"`
}

func liveOf(t *testing.T, s Spec) readNetplan {
	t.Helper()
	got, err := RenderAutoinstall(s, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Autoinstall struct {
			Network readNetplan `yaml:"network"`
		} `yaml:"autoinstall"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Autoinstall.Network
}

// installedOf runs the late-command that writes the installed netplan against
// a fake /target and reads the file it left.
func installedOf(t *testing.T, s Spec) readNetplan {
	t.Helper()
	v, err := validate(s)
	if err != nil {
		t.Fatal(err)
	}
	late := strings.Join(commands(t, parsed(t, s, ""), "late-commands"), "\n")
	if !strings.Contains(late, installedNetwork(v, s, targetRoot)) {
		t.Fatalf("no late-command writes the installed network:\n%s", late)
	}
	target := pinTarget(t)
	if code, out := runSh(t, installedNetwork(v, s, target), t.TempDir()); code != 0 {
		t.Fatalf("late-command exited %d:\n%s", code, out)
	}
	for _, gone := range []string{"etc/netplan/00-installer-config.yaml", "etc/netplan/50-cloud-init.yaml", "etc/cloud/cloud.cfg.d/90-installer-network.cfg"} {
		if _, err := os.Stat(filepath.Join(target, gone)); err == nil {
			t.Errorf("%s left in place: its en* stanza would come back", gone)
		}
	}
	path := filepath.Join(target, "etc/netplan/60-frame.yaml")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s mode %v, want 0600", path, fi.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	var doc struct {
		Network readNetplan `yaml:"network"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	return doc.Network
}

func TestBondAndVLANReachBothTheInstallerAndTheInstalledSystem(t *testing.T) {
	s := bondVLANSpec()
	for name, np := range map[string]readNetplan{"live": liveOf(t, s), "installed": installedOf(t, s)} {
		t.Run(name, func(t *testing.T) {
			bond, ok := np.Bonds["bond0"]
			if !ok {
				t.Fatalf("no bond0: %+v", np)
			}
			want := map[string]string{"mode": "802.3ad", "lacp-rate": "fast", "mii-monitor-interval": "100", "transmit-hash-policy": "layer3+4"}
			if !maps.Equal(bond.Parameters, want) {
				t.Errorf("bond parameters %v, want %v", bond.Parameters, want)
			}
			if len(bond.Addresses) != 0 {
				t.Errorf("the bond carries %v; with a VLAN the address is on the VLAN", bond.Addresses)
			}
			var macs []string
			for _, m := range bond.Interfaces {
				p, ok := np.Ethernets[m]
				if !ok {
					t.Fatalf("bond member %s has no ethernet stanza", m)
				}
				if len(p.Addresses) != 0 || p.Match["name"] != "" || !p.Optional {
					t.Errorf("member %s: %+v; a member is matched by MAC, optional, and carries no address", m, p)
				}
				macs = append(macs, p.Match["macaddress"])
			}
			if strings.Join(macs, ",") != macA+","+macB {
				t.Errorf("members %v, want %s,%s (lower-cased, in order)", macs, macA, macB)
			}
			vlan, ok := np.VLANs["vlan20"]
			if !ok || vlan.ID != 20 || vlan.Link != "bond0" {
				t.Fatalf("vlans = %+v, want vlan20 id 20 on bond0", np.VLANs)
			}
			if len(vlan.Addresses) != 1 || vlan.Addresses[0] != "192.0.2.210/24" ||
				len(vlan.Routes) != 1 || vlan.Routes[0]["via"] != "192.0.2.254" ||
				len(vlan.Nameservers["addresses"]) != 1 {
				t.Errorf("vlan20 = %+v, want the address, the default route and the resolver", vlan)
			}
			for n, e := range np.Ethernets {
				if e.Match["name"] != "" {
					t.Errorf("ethernet %s matches by name %q: an en* glob would bring the address back on any port", n, e.Match["name"])
				}
			}
		})
	}
}

func TestABondAloneCarriesTheAddress(t *testing.T) {
	s := bondVLANSpec()
	s.Network.VLAN = nil
	np := installedOf(t, s)
	if b := np.Bonds["bond0"]; len(b.Addresses) != 1 || len(b.Routes) != 1 {
		t.Fatalf("bond0 = %+v, want the address and the route", b)
	}
	if len(np.VLANs) != 0 {
		t.Fatalf("vlans = %+v, want none", np.VLANs)
	}
}

func TestAVLANWithoutBondIsTaggedOnTheNamedPort(t *testing.T) {
	s := goodSpec()
	s.Network.VLAN = &VLAN{ID: 4094, ParentMAC: macA}
	for name, np := range map[string]readNetplan{"live": liveOf(t, s), "installed": installedOf(t, s)} {
		v, ok := np.VLANs["vlan4094"]
		if !ok || v.Link != "frame-p0" || np.Ethernets["frame-p0"].Match["macaddress"] != macA || len(v.Addresses) != 1 {
			t.Fatalf("%s: netplan = %+v, want vlan4094 with the address on frame-p0 matched by %s", name, np, macA)
		}
		if len(np.Bonds) != 0 || np.Ethernets["frame-p0"].Optional {
			t.Fatalf("%s: netplan = %+v, want no bond and the one port required", name, np)
		}
	}
}

// Without a bond or a VLAN nothing changes: the en* live stanza and the
// route-discovered pin.
func TestNoLinksKeepsTheEnStanzaAndThePin(t *testing.T) {
	s := goodSpec()
	if np := liveOf(t, s); np.Ethernets["frame0"].Match["name"] != "en*" || len(np.Bonds)+len(np.VLANs) != 0 {
		t.Fatalf("live = %+v", np)
	}
	v, _ := validate(s)
	if installedNetwork(v, s, targetRoot) != netplanPin(v, s, targetRoot, "/sys/class/net") {
		t.Fatal("the installed network is no longer the pin")
	}
	for _, c := range commands(t, parsed(t, s, ""), "early-commands") {
		if strings.Contains(c, "no port has MAC") {
			t.Fatalf("a MAC guard with no MAC declared: %s", c)
		}
	}
}

func TestValidateLinksRefuses(t *testing.T) {
	for name, n := range map[string]Network{
		"one member":          {Bond: &Bond{Members: []string{macA}}},
		"nine members":        {Bond: &Bond{Members: nineMACs()}},
		"twice the same MAC":  {Bond: &Bond{Members: []string{macA, strings.ToUpper(macA)}}},
		"not a MAC":           {Bond: &Bond{Members: []string{macA, "52:54:00:aa:00"}}},
		"a shell word":        {Bond: &Bond{Members: []string{macA, "'; reboot; '"}}},
		"all zero":            {Bond: &Bond{Members: []string{macA, "00:00:00:00:00:00"}}},
		"VLAN 0":              {VLAN: &VLAN{ID: 0, ParentMAC: macA}},
		"VLAN 4095":           {VLAN: &VLAN{ID: 4095, ParentMAC: macA}},
		"VLAN with no parent": {VLAN: &VLAN{ID: 20}},
		"VLAN with both":      {Bond: &Bond{Members: []string{macA, macB}}, VLAN: &VLAN{ID: 20, ParentMAC: macA}},
	} {
		s := goodSpec()
		n.Address, n.Gateway, n.DNS = s.Network.Address, s.Network.Gateway, s.Network.DNS
		s.Network = n
		if err := ValidateSpec(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The MAC guard, run: it lets through a machine that has every declared
// port, and refuses -- before any disk is touched -- one that lacks one.
func TestMACGuardRefusesAMissingPortWhenRun(t *testing.T) {
	sys := t.TempDir()
	// eno3's address contains macB without being it: the guard matches
	// whole lines only.
	for dev, mac := range map[string]string{"eno1": "52:54:00:AA:00:01", "eno2": "52:54:00:bb:00:09", "eno3": macB + "0", "lo": "00:00:00:00:00:00"} {
		if err := os.MkdirAll(filepath.Join(sys, dev), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sys, dev, "address"), []byte(mac+"\n"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if code, out := runSh(t, macGuard([]string{macA}, sys), t.TempDir()); code != 0 {
		t.Errorf("present MAC refused (%d):\n%s", code, out)
	}
	if code, _ := runSh(t, macGuard([]string{macA, macB}, sys), t.TempDir()); code != 1 {
		t.Errorf("missing MAC exited %d, want 1", code)
	}
	// In the rendered document it comes after the disk guards and before
	// the early beacon: found by content, ordered against both.
	early := commands(t, parsed(t, bondVLANSpec(), testBeaconBase), "early-commands")
	guard, beacon, lastDisk := -1, -1, -1
	for i, c := range early {
		switch {
		case strings.Contains(c, "no port has MAC "+macA) && strings.Contains(c, "no port has MAC "+macB):
			guard = i
		case strings.Contains(c, testBeaconBase):
			if beacon < 0 {
				beacon = i
			}
		case strings.Contains(c, "/dev/disk/by-id"):
			lastDisk = i
		}
	}
	if guard < 0 || lastDisk < 0 || beacon < 0 || guard < lastDisk || guard > beacon {
		t.Fatalf("guard at %d, last disk guard at %d, beacon at %d; want disks < guard < beacon:\n%s", guard, lastDisk, beacon, strings.Join(early, "\n---\n"))
	}
}

// A MAC marshals quoted: plain, 52:54:00:… is a base-60 integer to YAML 1.1.
func TestMACsAreQuotedInTheDocument(t *testing.T) {
	v, _ := validate(bondVLANSpec())
	cmd := installedNetwork(v, bondVLANSpec(), targetRoot)
	m := regexp.MustCompile(`echo '([A-Za-z0-9+/=]+)' \| base64 -d > '/target/etc/netplan/60-frame.yaml'`).FindStringSubmatch(cmd)
	if m == nil {
		t.Fatalf("no netplan payload in %s", cmd)
	}
	b, _ := base64.StdEncoding.DecodeString(m[1])
	if !strings.Contains(string(b), `"`+macA+`"`) {
		t.Fatalf("MAC not double-quoted:\n%s", b)
	}
}

// provisiond receives the Spec as JSON (HTTPImageStore.Build, then
// BuildHandler): a bond or VLAN lost on that leg installs the machine
// untagged with every other test green.
func TestBondAndVLANSurviveTheJSONLegToProvisiond(t *testing.T) {
	b, err := json.Marshal(bondVLANSpec())
	if err != nil {
		t.Fatal(err)
	}
	var got Spec
	if err := json.NewDecoder(bytes.NewReader(b)).Decode(&got); err != nil {
		t.Fatal(err)
	}
	np := liveOf(t, got)
	if _, ok := np.Bonds["bond0"]; !ok {
		t.Fatalf("no bond after the JSON leg (%s)", b)
	}
	if v, ok := np.VLANs["vlan20"]; !ok || v.Link != "bond0" {
		t.Fatalf("no vlan20 on bond0 after the JSON leg (%s)", b)
	}
	vs := goodSpec()
	vs.Network.VLAN = &VLAN{ID: 30, ParentMAC: macA}
	b, _ = json.Marshal(vs)
	got = Spec{}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Network.VLAN == nil || *got.Network.VLAN != *vs.Network.VLAN {
		t.Fatalf("VLAN after the JSON leg = %+v, want %+v (%s)", got.Network.VLAN, vs.Network.VLAN, b)
	}
}

// nineMACs is nine distinct MACs: one past the bond's bound, and no
// duplicate for the other rule to refuse it on instead.
func nineMACs() []string {
	out := make([]string, 9)
	for i := range out {
		out[i] = fmt.Sprintf("52:54:00:cc:00:%02x", i+1)
	}
	return out
}
