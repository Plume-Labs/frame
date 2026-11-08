package provision

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Bond and VLAN, declared at installation (lot R-2).
//
// They were declared once before, validated by the apiserver, and dropped on
// the floor on their way here: a FrameInstall asking for a tagged VLAN
// installed untagged. So this time the same document configures both systems
// -- the live installer, which needs the VLAN and the bond to reach the
// gateway at all when the switch ports are tagged or in LACP, and the
// installed system -- and the tests read that document back.
//
// Every port is named by its permanent MAC address, never by kernel name and
// never by the `en*` glob: a bond over "whatever has carrier" is not a bond
// anyone declared. An early-command refuses the install, before any disk is
// touched, when a declared MAC is not on the machine.

// macShape is a MAC as the spec must carry it: six lower-case pairs.
// validateLinks lower-cases first.
var macShape = regexp.MustCompile(`^([0-9a-f]{2}:){5}[0-9a-f]{2}$`)

// The interface names the document gives. Kernel names, so at most 15
// characters: "vlan4094" is the longest.
const (
	bondName = "bond0"
	portName = "frame-p0" // the VLAN's parent port when there is no bond
)

func vlanName(id int32) string { return fmt.Sprintf("vlan%d", id) }

// validateLinks checks the bond and VLAN and returns the MACs the document
// names, lower-cased: the bond's members, or the VLAN's parent port.
func validateLinks(n Network) ([]string, error) {
	var macs []string
	norm := func(what, mac string) (string, error) {
		m := strings.ToLower(strings.TrimSpace(mac))
		if !macShape.MatchString(m) {
			return "", fmt.Errorf("network %s %q is not a MAC address (aa:bb:cc:dd:ee:ff)", what, mac)
		}
		if m == "00:00:00:00:00:00" {
			return "", fmt.Errorf("network %s: the all-zero MAC names no port", what)
		}
		if slices.Contains(macs, m) {
			return "", fmt.Errorf("network %s %s is named twice", what, m)
		}
		return m, nil
	}
	if b := n.Bond; b != nil {
		if len(b.Members) < 2 || len(b.Members) > 8 {
			return nil, fmt.Errorf("network bond: needs 2 to 8 member ports, got %d", len(b.Members))
		}
		for _, mac := range b.Members {
			m, err := norm("bond member", mac)
			if err != nil {
				return nil, err
			}
			macs = append(macs, m)
		}
	}
	if v := n.VLAN; v != nil {
		if v.ID < 1 || v.ID > 4094 {
			return nil, fmt.Errorf("network VLAN id %d: must be 1-4094", v.ID)
		}
		switch {
		case n.Bond != nil && v.ParentMAC != "":
			return nil, fmt.Errorf("network VLAN: its parent is the bond; parentMAC must be empty")
		case n.Bond == nil && v.ParentMAC == "":
			return nil, fmt.Errorf("network VLAN: without a bond, parentMAC must name the port it is tagged on")
		case n.Bond == nil:
			m, err := norm("VLAN parentMAC", v.ParentMAC)
			if err != nil {
				return nil, err
			}
			macs = append(macs, m)
		}
	}
	return macs, nil
}

// declaresLinks reports whether the network declares a bond or a VLAN.
func declaresLinks(n Network) bool { return n.Bond != nil || n.VLAN != nil }

// Netplan stanzas for the declared case. MACs marshal double-quoted (see
// quoted): written plain, a MAC is a base-60 integer to a YAML 1.1 reader.
type portStanza struct {
	Match map[string]quoted `yaml:"match"`
	DHCP4 bool              `yaml:"dhcp4"`
	// Optional is set on bond members: one port without a cable must not
	// hold systemd-networkd-wait-online to its timeout at every boot. The
	// bond itself stays required.
	Optional bool `yaml:"optional,omitempty"`
}

type ipStanza struct {
	Addresses   []string            `yaml:"addresses"`
	Routes      []map[string]string `yaml:"routes"`
	Nameservers map[string][]string `yaml:"nameservers"`
}

type bondStanza struct {
	Interfaces []string          `yaml:"interfaces"`
	DHCP4      bool              `yaml:"dhcp4"`
	Parameters map[string]string `yaml:"parameters"`
	*ipStanza  `yaml:",inline,omitempty"`
}

type vlanStanza struct {
	ID       int32  `yaml:"id"`
	Link     string `yaml:"link"`
	DHCP4    bool   `yaml:"dhcp4"`
	ipStanza `yaml:",inline"`
}

// bondParameters are LACP's: fast LACPDUs so a dead member leaves in seconds
// rather than ninety, link monitoring, and a hash that spreads flows by
// address and port (layer2 would pin every flow between two hosts to one
// member).
var bondParameters = map[string]string{
	"mode":                 "802.3ad",
	"lacp-rate":            "fast",
	"mii-monitor-interval": "100",
	"transmit-hash-policy": "layer3+4",
}

// declaredNetwork is the netplan document for a network with a bond, a VLAN
// or both. The address, route and resolvers go on the top interface: the
// VLAN when there is one, the bond otherwise. Ports carry no address.
func declaredNetwork(v validated, s Spec) netplan {
	ip := ipStanza{
		Addresses:   []string{v.cidr},
		Routes:      []map[string]string{{"to": "default", "via": s.Network.Gateway}},
		Nameservers: map[string][]string{"addresses": s.Network.DNS},
	}
	doc := netplan{Version: 2, Ethernets: map[string]any{}}
	link := portName
	if s.Network.Bond != nil {
		members := make([]string, 0, len(v.macs))
		for i, mac := range v.macs {
			name := fmt.Sprintf("frame-m%d", i)
			doc.Ethernets[name] = portStanza{Match: map[string]quoted{"macaddress": quoted(mac)}, Optional: true}
			members = append(members, name)
		}
		bond := bondStanza{Interfaces: members, Parameters: bondParameters}
		if s.Network.VLAN == nil {
			bond.ipStanza = &ip
		}
		doc.Bonds = map[string]any{bondName: bond}
		link = bondName
	} else {
		doc.Ethernets[portName] = portStanza{Match: map[string]quoted{"macaddress": quoted(v.macs[0])}}
	}
	if s.Network.VLAN != nil {
		doc.VLANs = map[string]any{vlanName(s.Network.VLAN.ID): vlanStanza{ID: s.Network.VLAN.ID, Link: link, ipStanza: ip}}
	}
	return doc
}

// liveNetwork is the installer's own network.
func liveNetwork(v validated, s Spec) netplan {
	if declaresLinks(s.Network) {
		return declaredNetwork(v, s)
	}
	return netplan{Version: 2, Ethernets: map[string]any{
		// The LIVE system's network only: one stanza over every en* port.
		// networkd only configures a link with carrier, so on a machine
		// with four ports and one cable the address lands on the cabled
		// one without Frame knowing its name or MAC. UNPROVEN ON
		// HARDWARE -- recette step 1.
		//
		// Not the installed system's: subiquity would copy this stanza to
		// /target, where every en* port that later gains a carrier takes
		// the same address. netplanPin replaces it there with one stanza
		// matched by MAC.
		"frame0": ethernet{
			Match:       map[string]string{"name": "en*"},
			Optional:    true,
			DHCP4:       false,
			Addresses:   []string{v.cidr},
			Routes:      []map[string]string{{"to": "default", "via": s.Network.Gateway}},
			Nameservers: map[string][]string{"addresses": s.Network.DNS},
		},
	}}
}

// installedNetwork is the late-command that leaves the installed system its
// network: the declared document when there is a bond or a VLAN, otherwise
// the port the live system reached the gateway through (netplanPin).
func installedNetwork(v validated, s Spec, target string) string {
	if !declaresLinks(s.Network) {
		return netplanPin(v, s, target, "/sys/class/net")
	}
	b, err := yaml.Marshal(map[string]any{"network": declaredNetwork(v, s)})
	if err != nil {
		panic(err) // structs of strings, ints and bools; cannot fail
	}
	return writeNetplan(target, b)
}

// writeNetplan replaces the target's netplan with doc and keeps cloud-init
// from rendering its own -- the same three steps as netplanPin, whose comment
// says why each is needed.
func writeNetplan(target string, doc []byte) string {
	pinned := target + "/etc/netplan/60-frame.yaml"
	return fmt.Sprintf("rm -f '%s/etc/netplan/'*.yaml '%s' && echo '%s' | base64 -d > '%s' && chmod 600 '%s' && %s",
		target, target+"/etc/cloud/cloud.cfg.d/90-installer-network.cfg",
		base64.StdEncoding.EncodeToString(doc), pinned, pinned,
		writeFileCommand(target+"/etc/cloud/cloud.cfg.d/99-zz-frame-network.cfg", "network: {config: disabled}\n", "644"))
}

// macGuard refuses the install (exit 1, before any disk is touched) unless
// every declared MAC is a port of this machine. The MACs passed macShape, so
// they cannot leave the single quotes they sit in.
func macGuard(macs []string, sysClassNet string) string {
	parts := make([]string, 0, len(macs))
	for _, m := range macs {
		parts = append(parts, fmt.Sprintf(
			"cat '%s'/*/address 2>/dev/null | grep -qix '%s' || { echo 'FRAME: no port has MAC %s, refusing' > /dev/console; exit 1; }",
			sysClassNet, m, m))
	}
	return strings.Join(parts, "; ")
}
