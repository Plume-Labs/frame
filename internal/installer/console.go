package installer

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
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
		say("\n%s, %s RAM, %s boot, AVX %v, AES-NI %v", clean(hw.CPUModel), humanBytes(hw.MemBytes), bootName(hw), hw.AVX, hw.AESNI)
		for _, n := range hw.NICs {
			say("  port %s %s link up: %v", n.Name, n.MAC, n.Up)
		}

		a, err := askAnswers(ask, say, srv.Keymap, hw, prev)
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
			say("Not confirmed. Starting again.")
			continue
		}
		say("Starting the installer (this can take a few minutes)...")
		if status, err := srv.Run(ctx, plan); err != nil {
			if status == http.StatusConflict {
				say("An installation was already started from the page. Nothing more to do here.")
				return nil
			}
			say("Failed: %v\nRestart the machine before trying again: the installer may already have started.", clean(err.Error()))
			return err
		}
		say("\nInstallation started. The machine restarts by itself: remove the USB key at the first restart.")
		return nil
	}
}

type asker func(prompt, def string) (string, error)

// valueOr returns v, or def when v is empty.
func valueOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// prompt is what the questions below share: how to ask, how to say.
type prompt struct {
	ask asker
	say func(string, ...any)
}

func askAnswers(ask asker, say func(string, ...any), keymap func(string) error, hw Hardware, prev Answers) (Answers, error) {
	p := prompt{ask, say}
	var a Answers
	var err error
	if a.Keyboard, err = p.keyboard(keymap, prev); err != nil {
		return a, err
	}
	if a.Hostname, err = ask("Machine name", prev.Hostname); err != nil {
		return a, err
	}
	if a.Role, err = p.role(); err != nil {
		return a, err
	}
	if a.Disks, err = p.disks(hw); err != nil {
		return a, err
	}
	if err = p.network(&a, prev); err != nil {
		return a, err
	}
	if a.SSHPublicKey, err = ask("SSH public key (one line)", prev.SSHPublicKey); err != nil {
		return a, err
	}
	a.U2FKeys, err = p.securityKeys(prev)
	return a, err
}

// keyboard is asked first, so that everything typed after it lands as typed on
// the keyboard in front of you.
func (p prompt) keyboard(keymap func(string) error, prev Answers) (string, error) {
	kb, err := p.ask("Keyboard layout ("+strings.Join(provision.KeyboardLayouts(), " ")+")", valueOr(prev.Keyboard, "us"))
	if err != nil {
		return "", err
	}
	kb = strings.ToLower(kb)
	if keymap != nil && slices.Contains(provision.KeyboardLayouts(), kb) {
		if e := keymap(kb); e != nil {
			p.say("  could not switch the console keyboard: %v", clean(e.Error()))
		}
	}
	return kb, nil
}

func (p prompt) role() (string, error) {
	roles := append([]string{""}, provision.Roles()...)
	p.say("Role:")
	for i, r := range roles {
		label := r
		if r == "" {
			label = "(not recorded)"
		}
		p.say("  %d) %s", i, label)
	}
	n, err := p.ask("Role number", "0")
	if err != nil {
		return "", err
	}
	if i, e := strconv.Atoi(n); e == nil && i >= 0 && i < len(roles) {
		return roles[i], nil
	}
	p.say("  %q is not a role number: no role recorded.", clean(n))
	return "", nil
}

func (p prompt) disks(hw Hardware) ([]DiskChoice, error) {
	p.say("Disks (everything on the chosen disk is erased):")
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
		p.say("  %d) %s  %s  %s  serial %s%s%s", i+1, d.Path, humanBytes(d.SizeBytes), clean(d.Model), clean(d.Confirm()), clean(contents), note)
	}
	picks, err := p.ask("Disk number (two numbers for a mirror, e.g. \"1 2\")", "")
	if err != nil {
		return nil, err
	}
	var chosen []DiskChoice
	for f := range strings.FieldsSeq(picks) {
		i, e := strconv.Atoi(f)
		if e != nil || i < 1 || i > len(hw.Disks) {
			p.say("  %q is not a disk number: ignored.", clean(f))
			continue
		}
		d := hw.Disks[i-1]
		c, err := p.ask("Retype the serial of "+d.Path+" to allow erasing it", "")
		if err != nil {
			return nil, err
		}
		chosen = append(chosen, DiskChoice{ByID: d.ByID, Confirm: c})
	}
	return chosen, nil
}

func (p prompt) network(a *Answers, prev Answers) error {
	mode, err := p.ask("Network: dhcp or static", "static")
	if err != nil {
		return err
	}
	if strings.EqualFold(mode, "dhcp") {
		a.DHCP = true
		return nil
	}
	if a.Address, err = p.ask("Address with mask (e.g. 192.168.2.201/24)", prev.Address); err != nil {
		return err
	}
	if a.Gateway, err = p.ask("Gateway", prev.Gateway); err != nil {
		return err
	}
	dns, err := p.ask("DNS servers, separated by spaces", strings.Join(prev.DNS, " "))
	a.DNS = strings.Fields(dns)
	return err
}

func (p prompt) securityKeys(prev Answers) ([]string, error) {
	if len(prev.U2FKeys) > 0 {
		keep, err := p.ask(fmt.Sprintf("Keep the %d security-key credential(s) typed before? (y/n)", len(prev.U2FKeys)), "y")
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(strings.ToLower(keep), "y") {
			return append([]string(nil), prev.U2FKeys...), nil
		}
	}
	p.say("Security keys for the local console login (hack/enroll-yubikey.sh output):")
	p.say("paste one credential per line, up to %d; an empty line ends (empty from the start: no console login).", provision.MaxU2FKeys)
	var keys []string
	for len(keys) < provision.MaxU2FKeys {
		k, err := p.ask("Credential "+strconv.Itoa(len(keys)+1), "")
		if err != nil {
			return nil, err
		}
		if k == "" {
			break
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func bootName(hw Hardware) string {
	if hw.UEFI {
		return "UEFI"
	}
	return "BIOS"
}

// clean drops control characters (escape sequences included) from text that came
// from the hardware before it is printed on a terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
