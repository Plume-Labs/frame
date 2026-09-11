package provision

import (
	"strings"
	"testing"
)

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
	got, err := RenderPreseed(goodSpec())
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
	got, err := RenderPreseed(goodSpec())
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
		if _, err := RenderPreseed(s); err == nil {
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
	_, err := RenderPreseed(s)
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
		if _, err := RenderPreseed(s); err == nil {
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
	if _, err := RenderPreseed(s); err == nil {
		t.Fatal("two keys on one line were accepted")
	}
}

func TestRenderPreseedRefusesAValueTooLongToBeAKey(t *testing.T) {
	s := goodSpec()
	s.SSHPublicKey = s.SSHPublicKey + " " + strings.Repeat("x", 1024)
	if _, err := RenderPreseed(s); err == nil {
		t.Fatal("a 1100-character value was accepted")
	}
	empty := goodSpec()
	empty.SSHPublicKey = "   "
	if _, err := RenderPreseed(empty); err == nil {
		t.Fatal("an empty key was accepted")
	}
}

func TestRenderPreseedRefusesAKeyThatIsNotAnAuthorizedKeysLine(t *testing.T) {
	s := goodSpec()
	s.SSHPublicKey = "hunter2"
	if _, err := RenderPreseed(s); err == nil {
		t.Fatal("want error for a non-key, got nil")
	}
}

// The join token belongs to the cluster, not to the image. If it ever reaches
// RenderPreseed it must not come out the other side.
func TestRenderPreseedNeverEmitsTheJoinToken(t *testing.T) {
	s := goodSpec()
	s.Cluster = ClusterTarget{Mode: ClusterJoin, ServerURL: "https://192.168.2.201:6443", JoinToken: "K10SECRETTOKEN", K3sVersion: "v1.33.4+k3s1"}
	got, err := RenderPreseed(s)
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
	got, err := RenderPreseed(goodSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "blockdev --getsize64 /dev/disk/by-id/scsi-aaa") {
		t.Error("preseed does not assert the disk size")
	}
	if !strings.Contains(got, "preseed/early_command") {
		t.Error("the assertion must run before partman, in early_command")
	}
}

func TestRenderPreseedRejectsAnEmptyUID(t *testing.T) {
	s := goodSpec()
	s.UID = ""
	if _, err := RenderPreseed(s); err == nil {
		t.Fatal("want error for empty UID, got nil")
	}
}
