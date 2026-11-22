package provision

import (
	"strings"
	"testing"
)

func dhcpSpec() Spec {
	s := goodSpec()
	s.Network = Network{DHCP: true}
	s.BootMode = "Legacy"
	s.Layout = Layout{Kind: LayoutSingleDisk, Disks: []Disk{{ByID: "/dev/disk/by-id/scsi-aaa", SizeBytes: 300 << 30}}}
	return s
}

// A DHCP network asks for DHCP on the live system and carries no address, route
// or resolver; the installed system keeps subiquity's stanza, so no pin command
// runs and nothing in the document names an address.
func TestRenderAutoinstallDHCPAsksForAnAddressAndPinsNothing(t *testing.T) {
	ai := parsed(t, dhcpSpec(), "")
	eth := ai["network"].(map[string]any)["ethernets"].(map[string]any)["frame0"].(map[string]any)
	if eth["dhcp4"] != true {
		t.Errorf("dhcp4 = %v, want true", eth["dhcp4"])
	}
	for _, k := range []string{"addresses", "routes", "nameservers"} {
		if _, ok := eth[k]; ok {
			t.Errorf("DHCP stanza carries %q", k)
		}
	}
	for _, c := range commands(t, ai, "late-commands") {
		if strings.Contains(c, "60-frame.yaml") {
			t.Errorf("DHCP install pins a static netplan: %s", c)
		}
	}
	if got := commands(t, ai, "late-commands"); !strings.Contains(got[len(got)-1], "99-zz-frame-lock") {
		t.Errorf("the relock command must still run last: %v", got)
	}
}

// Two sources for one address are refused, and so is DHCP with a bond or a VLAN.
func TestValidateSpecRefusesDHCPBesideAStaticNetwork(t *testing.T) {
	for name, mutate := range map[string]func(*Network){
		"address": func(n *Network) { n.Address = "192.0.2.210/24" },
		"gateway": func(n *Network) { n.Gateway = "192.0.2.254" },
		"dns":     func(n *Network) { n.DNS = []string{"192.0.2.254"} },
		"a bond":  func(n *Network) { n.Bond = &Bond{Members: []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"}} },
		"a VLAN":  func(n *Network) { n.VLAN = &VLAN{ID: 20, ParentMAC: "aa:bb:cc:dd:ee:01"} },
	} {
		t.Run(name, func(t *testing.T) {
			s := dhcpSpec()
			mutate(&s.Network)
			if err := ValidateSpec(s); err == nil {
				t.Error("accepted")
			}
		})
	}
	if err := ValidateSpec(dhcpSpec()); err != nil {
		t.Errorf("a plain DHCP spec was refused: %v", err)
	}
}

// DHCP still goes through the disk rules.
func TestValidateSpecChecksTheLayoutOnTheDHCPPathToo(t *testing.T) {
	s := dhcpSpec()
	s.Layout.Disks = nil
	if err := ValidateSpec(s); err == nil {
		t.Error("a DHCP spec with no disk was accepted")
	}
}

// An installation waits for SSH at a known address: DHCP is for burning a key.
func TestInstallRefusesDHCP(t *testing.T) {
	if err := validatePendingSpec(dhcpSpec(), "192.0.2.210"); err == nil || !strings.Contains(err.Error(), "dhcp") {
		t.Errorf("err = %v, want a refusal naming dhcp", err)
	}
}
