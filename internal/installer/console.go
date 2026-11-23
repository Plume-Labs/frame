package installer

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Plume-Labs/frame/internal/provision"
)

// RunConsole is the text version of the assistant, for a machine whose screen
// cannot show a page (a BMC's VGA, a serial console, a browser-less live
// system). It asks the same questions, builds the same plan through BuildPlan
// and starts it through srv.Run, so it has the same refusals and the same
// one-installation lock; it only talks to a person over in and out. It returns
// when the installation has started or the input ends.
func RunConsole(ctx context.Context, in io.Reader, out io.Writer, srv *Server) error {
	r := bufio.NewReader(in)
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format+"\n", a...) }
	ask := func(prompt, def string) (string, error) {
		if def != "" {
			prompt += " [" + def + "]"
		}
		_, _ = fmt.Fprint(out, prompt+": ")
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		if line = strings.TrimSpace(line); line != "" {
			return line, nil
		}
		return def, nil
	}

	say("\n=== Hearthrack guided installer (text mode) ===")
	prev := Answers{}
	for {
		hw, err := srv.Hardware(ctx)
		if err != nil {
			return err
		}
		say("\n%s, %s RAM, %s boot, AVX %v, AES-NI %v", hw.CPUModel, humanBytes(hw.MemBytes), bootName(hw), hw.AVX, hw.AESNI)
		for _, n := range hw.NICs {
			say("  port %s %s link up: %v", n.Name, n.MAC, n.Up)
		}

		a, err := askAnswers(ask, say, hw, prev)
		if err != nil {
			return err
		}
		prev = a
		plan, err := BuildPlan(hw, a, NewUID())
		if err != nil {
			say("\nRefused: %v\nLet's start again.", err)
			continue
		}
		say("\nSummary:")
		for _, l := range plan.Summary {
			say("  - %s", l)
		}
		ok, err := ask("\nType ERASE (capitals) to install, anything else to start again", "")
		if err != nil {
			return err
		}
		if ok != "ERASE" {
			continue
		}
		if _, err := srv.Run(ctx, plan); err != nil {
			return err
		}
		say("\nInstallation started. The machine restarts by itself: remove the USB key at the first restart.")
		return nil
	}
}

type asker func(prompt, def string) (string, error)

func askAnswers(ask asker, say func(string, ...any), hw Hardware, prev Answers) (Answers, error) {
	var a Answers
	var err error
	if a.Hostname, err = ask("Machine name", prev.Hostname); err != nil {
		return a, err
	}
	roles := append([]string{""}, provision.Roles()...)
	say("Role:")
	for i, r := range roles {
		label := r
		if r == "" {
			label = "(not recorded)"
		}
		say("  %d) %s", i, label)
	}
	n, err := ask("Role number", "0")
	if err != nil {
		return a, err
	}
	if i, e := strconv.Atoi(n); e == nil && i >= 0 && i < len(roles) {
		a.Role = roles[i]
	}

	say("Disks (everything on the chosen disk is erased):")
	for i, d := range hw.Disks {
		note := ""
		switch {
		case d.Removable:
			note = "  [removable: cannot be chosen]"
		case d.InUse:
			note = "  [in use: cannot be chosen]"
		}
		contents := ""
		if len(d.Contents) > 0 {
			contents = "  holds: " + strings.Join(d.Contents, ", ")
		}
		say("  %d) %s  %s  %s  serial %s%s%s", i+1, d.Path, humanBytes(d.SizeBytes), d.Model, d.Confirm(), contents, note)
	}
	picks, err := ask("Disk number (two numbers for a mirror, e.g. \"1 2\")", "")
	if err != nil {
		return a, err
	}
	for _, f := range strings.Fields(picks) {
		i, e := strconv.Atoi(f)
		if e != nil || i < 1 || i > len(hw.Disks) {
			continue // BuildPlan refuses an empty choice
		}
		d := hw.Disks[i-1]
		c, err := ask("Retype the serial of "+d.Path+" to allow erasing it", "")
		if err != nil {
			return a, err
		}
		a.Disks = append(a.Disks, DiskChoice{ByID: d.ByID, Confirm: c})
	}

	netMode, err := ask("Network: dhcp or static", "static")
	if err != nil {
		return a, err
	}
	if strings.EqualFold(netMode, "dhcp") {
		a.DHCP = true
	} else {
		if a.Address, err = ask("Address with mask (e.g. 192.168.2.201/24)", prev.Address); err != nil {
			return a, err
		}
		if a.Gateway, err = ask("Gateway", prev.Gateway); err != nil {
			return a, err
		}
		dns, err := ask("DNS servers, separated by spaces", strings.Join(prev.DNS, " "))
		if err != nil {
			return a, err
		}
		a.DNS = strings.Fields(dns)
	}
	if a.SSHPublicKey, err = ask("SSH public key (one line)", prev.SSHPublicKey); err != nil {
		return a, err
	}
	return a, nil
}

func bootName(hw Hardware) string {
	if hw.UEFI {
		return "UEFI"
	}
	return "BIOS"
}
