package installer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Plume-Labs/frame/internal/provision"
)

// DiskChoice names a disk to install on and carries the person's proof that
// they meant it: the disk's serial, retyped.
type DiskChoice struct {
	ByID    string `json:"byID"`
	Confirm string `json:"confirm"`
}

// Answers is everything the guided screens collect.
type Answers struct {
	Hostname     string       `json:"hostname"`
	Role         string       `json:"role"`
	Keyboard     string       `json:"keyboard"`
	Disks        []DiskChoice `json:"disks"` // one disk, or two for a mirror
	DHCP         bool         `json:"dhcp"`
	Address      string       `json:"address"`
	Gateway      string       `json:"gateway"`
	DNS          []string     `json:"dns"`
	SSHPublicKey string       `json:"sshPublicKey"`
	U2FKeys      []string     `json:"u2fKeys"`
}

// Plan is a validated Answers: the Spec it makes, and a plain description of
// what will happen, for the summary screen.
type Plan struct {
	Spec    provision.Spec
	Summary []string
}

// NewUID returns a fresh installation identifier.
func NewUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails on Linux
	return hex.EncodeToString(b)
}

// BuildPlan turns answers into a Spec for hw, refusing anything the machine
// does not back up: a disk it does not have, a removable one, a serial that was
// not retyped exactly. Everything else is provision.ValidateSpec's to refuse.
func BuildPlan(hw Hardware, a Answers, uid string) (Plan, error) {
	if len(a.Disks) == 0 {
		return Plan{}, fmt.Errorf("choose a system disk")
	}
	var disks []provision.Disk
	var lines []string
	for _, c := range a.Disks {
		d, ok := findDisk(hw, c.ByID)
		if !ok {
			return Plan{}, fmt.Errorf("disk %q is not one this machine reports", c.ByID)
		}
		if d.Removable {
			return Plan{}, fmt.Errorf("disk %s is removable (USB or SD): it is probably the key this installer booted from", d.Path)
		}
		if d.InUse {
			return Plan{}, fmt.Errorf("disk %s has a mounted partition: it is in use by the live system", d.Path)
		}
		// The expected value is not repeated here: the page shows it, and a
		// refusal that spelled it out would turn the check into a formality.
		if strings.TrimSpace(c.Confirm) != d.Confirm() {
			return Plan{}, fmt.Errorf("the serial retyped for %s does not match the disk's", d.Path)
		}
		disks = append(disks, provision.Disk{ByID: d.ByID, SizeBytes: d.SizeBytes})
		line := fmt.Sprintf("ERASE %s (%s %s, %s, serial %s)", d.Path, d.Model, d.Transport, humanBytes(d.SizeBytes), d.Confirm())
		if len(d.Contents) > 0 {
			line += ", currently holding: " + strings.Join(d.Contents, ", ")
		}
		lines = append(lines, line)
	}
	kind := provision.LayoutSingleDisk
	if len(disks) == 2 {
		kind = provision.LayoutMirror
	}
	boot := "Legacy"
	if hw.UEFI {
		boot = "UEFI"
	}
	spec := provision.Spec{
		UID:          uid,
		Hostname:     strings.TrimSpace(a.Hostname),
		Role:         a.Role,
		Keyboard:     a.Keyboard,
		BootMode:     boot,
		SSHPublicKey: strings.TrimSpace(a.SSHPublicKey),
		U2FKeys:      a.U2FKeys,
		Layout:       provision.Layout{Kind: kind, Disks: disks},
		Network: provision.Network{
			DHCP: a.DHCP, Address: strings.TrimSpace(a.Address),
			Gateway: strings.TrimSpace(a.Gateway), DNS: a.DNS,
		},
	}
	if err := provision.ValidateSpec(spec); err != nil {
		return Plan{}, err
	}
	lines = append(lines, fmt.Sprintf("Install Ubuntu 24.04 (%s boot), %s layout, hostname %s", boot, kind, spec.Hostname))
	if a.DHCP {
		lines = append(lines, "Network: DHCP")
	} else {
		lines = append(lines, fmt.Sprintf("Network: %s via %s, DNS %s", spec.Network.Address, spec.Network.Gateway, strings.Join(spec.Network.DNS, ", ")))
	}
	if spec.Role != "" {
		lines = append(lines, "Role recorded: "+spec.Role+" (joining the cluster stays a separate step after the install)")
	}
	if spec.Keyboard != "" && spec.Keyboard != "us" {
		lines = append(lines, "Keyboard layout: "+spec.Keyboard)
	}
	if len(spec.U2FKeys) > 0 {
		lines = append(lines, fmt.Sprintf("Console login with %d security key(s)", len(spec.U2FKeys)))
	}
	if !hw.AVX {
		lines = append(lines, "Note: this CPU has no AVX; workloads that need it must stay off this node (taint no-avx)")
	}
	return Plan{Spec: spec, Summary: lines}, nil
}

// Render is the autoinstall document for the plan. No beacon: nothing outside
// this machine watches it.
func (p Plan) Render() (string, error) {
	return provision.RenderAutoinstall(p.Spec, "", "")
}

func findDisk(hw Hardware, byID string) (Disk, bool) {
	for _, d := range hw.Disks {
		if d.ByID == byID {
			return d, true
		}
	}
	return Disk{}, false
}

func humanBytes(n int64) string {
	const gb = 1_000_000_000
	if n >= 1000*gb {
		return fmt.Sprintf("%.1f TB", float64(n)/(1000*gb))
	}
	return fmt.Sprintf("%d GB", n/gb)
}

func rolesList() []string { return provision.Roles() }

func keyboardsList() []string { return provision.KeyboardLayouts() }
