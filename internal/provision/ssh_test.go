package provision

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type fakeSession struct {
	hostKey string
	marker  string
	closed  bool
}

// HostKey carries a trailing newline, matching ssh.MarshalAuthorizedKey's
// real output -- WaitForOurSystem must not rely on any particular Session
// implementation having already cleaned that up.
func (f *fakeSession) HostKey() string                             { return f.hostKey + "\n" }
func (f *fakeSession) Run(context.Context, string) (string, error) { return "", nil }
func (f *fakeSession) Close() error                                { f.closed = true; return nil }
func (f *fakeSession) ReadFile(_ context.Context, path string) ([]byte, error) {
	// An empty marker means the file is not there, which is what a system
	// that never ran our preseed actually presents: `cat` fails. Modelling it
	// as an existing-but-blank file made the no-marker test land on the
	// mismatch branch and left the read guard uncovered.
	if path != markerPath || f.marker == "" {
		return nil, errors.New("no such file")
	}
	return []byte(f.marker + "\n"), nil
}

type fakeSSH struct {
	attempts  int
	failUntil int
	session   Session // an interface, so a recordingSession keeps its own Run/ReadFile
}

func (f *fakeSSH) Dial(context.Context, string, string, []byte, string) (Session, error) {
	f.attempts++
	if f.attempts <= f.failUntil {
		return nil, errors.New("connection refused")
	}
	return f.session, nil
}

// The installer takes many minutes and refuses connections the whole time.
//
// Bounded rather than context.Background(): if a future change ever makes
// this case loop instead of succeeding, it fails as one legible red line
// within a second, not a ten-minute go test timeout and a goroutine dump.
func TestWaitForOurSystemKeepsTryingUntilTheMachineAnswers(t *testing.T) {
	f := &fakeSSH{failUntil: 3, session: &fakeSession{hostKey: "ssh-ed25519 AAAAhost", marker: "the-uid"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	hk, err := WaitForOurSystem(ctx, f, "192.168.2.210:22", "frame", nil, "the-uid", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if f.attempts != 4 {
		t.Errorf("attempts = %d, want 4", f.attempts)
	}
	if hk != "ssh-ed25519 AAAAhost" {
		t.Errorf("host key = %q", hk)
	}
}

// THE discriminating test of this lot. Without the marker check, a machine
// that was never touched -- the one we believed we were overwriting -- answers
// SSH at that address and satisfies "installed".
func TestWaitForOurSystemRefusesAMachineThatIsNotOurs(t *testing.T) {
	f := &fakeSSH{session: &fakeSession{hostKey: "ssh-ed25519 AAAAhost", marker: "a-different-installation"}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := WaitForOurSystem(ctx, f, "192.168.2.210:22", "frame", nil, "the-uid", time.Millisecond); err == nil {
		t.Fatal("a machine carrying someone else's UID was accepted as ours")
	}
}

// A system with no marker at all is not ours either -- that is a pre-existing
// machine at the address, not a failed write of our own marker.
//
// The dedicated readErr branch earns its place by what it says, so that is
// what this asserts (same shape as preseed.go's PRIVATE KEY branch test).
// Remove the branch and the system is still refused -- by the UID
// comparison, now that an empty uid can no longer sneak an empty-vs-empty
// match past it -- but the message stops naming the problem.
func TestWaitForOurSystemRefusesASystemWithNoMarker(t *testing.T) {
	f := &fakeSSH{session: &fakeSession{hostKey: "ssh-ed25519 AAAAhost", marker: ""}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := WaitForOurSystem(ctx, f, "192.168.2.210:22", "frame", nil, "the-uid", time.Millisecond)
	if err == nil {
		t.Fatal("a system with no marker was accepted as ours")
	}
	if !strings.Contains(err.Error(), "has no "+markerPath) {
		t.Errorf("error = %q; it must name the missing marker, not just refuse", err)
	}
}

// Without this, an empty uid matches an unreadable marker: a failed read
// compares as "", "" == "" is true, and a machine we never touched is
// accepted as ours. It must be refused before the first dial, not after --
// there is nothing a retry could fix.
//
// Bounded rather than context.Background(): without the guard, this input
// makes WaitForOurSystem retry forever (nothing ever satisfies "" != uid),
// and go test's default ten-minute timeout turns that into a panic and a
// goroutine dump instead of a plain failing assertion.
func TestWaitForOurSystemRefusesAnEmptyUID(t *testing.T) {
	f := &fakeSSH{session: &fakeSession{hostKey: "ssh-ed25519 AAAAhost", marker: ""}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := WaitForOurSystem(ctx, f, "192.168.2.210:22", "frame", nil, "", time.Millisecond)
	if err == nil {
		t.Fatal("an empty UID was accepted as something to check a machine against")
	}
	if f.attempts != 0 {
		t.Errorf("attempts = %d, want 0: an empty UID must be refused before dialing", f.attempts)
	}
}

func TestWaitForOurSystemStopsWhenTheContextExpires(t *testing.T) {
	f := &fakeSSH{failUntil: 1 << 30, session: &fakeSession{}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := WaitForOurSystem(ctx, f, "a:22", "frame", nil, "u", time.Millisecond); err == nil {
		t.Fatal("want an error when the deadline passes")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("it kept trying past the deadline")
	}
}

// newTestED25519Signer generates a fresh ed25519 keypair for use as either a
// server host key or a client key in the real in-process SSH server tests.
func newTestED25519Signer(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// newTestClientKeyPEM returns a freshly generated client private key,
// PEM-armored the way ssh.ParsePrivateKey (and so sshClient.Dial) requires.
// ssh.MarshalPrivateKey's *pem.Block.Bytes is the raw, unarmored payload --
// that alone is rejected with "ssh: no key found".
func newTestClientKeyPEM(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block)
}

// newTestSSHServer starts an in-process SSH server on loopback presenting
// hostSigner as its host key, and returns the address to dial. It rejects
// every channel -- these tests only care about the handshake, which is the
// one property a fake cannot prove.
func newTestSSHServer(t *testing.T, hostSigner ssh.Signer) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(hostSigner)

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		for ch := range chans {
			_ = ch.Reject(ssh.Prohibited, "no sessions needed for this test")
		}
		_ = sc.Close()
	}()

	return ln.Addr().String()
}

func TestNewSSHClientCapturesTheHostKeyItConnectedTo(t *testing.T) {
	hostSigner := newTestED25519Signer(t)
	addr := newTestSSHServer(t, hostSigner)
	clientKey := newTestClientKeyPEM(t)

	sess, err := NewSSHClient().Dial(context.Background(), addr, "frame", clientKey, "")
	if err != nil {
		t.Fatalf("the handshake did not complete: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// Only the expectation is trimmed. sshClient.Dial trims at the point it
	// captures the key, so this fails if that trim is ever removed -- with
	// both sides trimmed, as this test used to compare, neither shape (the
	// captured value's trailing newline, or its absence) was ever visible.
	want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())))
	if sess.HostKey() != want {
		t.Errorf("HostKey() = %q, want %q", sess.HostKey(), want)
	}
}

// R1's whole point: a captured host key is decoration unless a later dial
// actually checks it. A server presenting a key other than the one the
// caller already holds must be refused -- this is the property a fake
// cannot prove, since a fake never runs a real handshake.
func TestDialRefusesAHostKeyThatDoesNotMatchExpected(t *testing.T) {
	hostSigner := newTestED25519Signer(t)
	addr := newTestSSHServer(t, hostSigner)
	clientKey := newTestClientKeyPEM(t)

	otherSigner := newTestED25519Signer(t)
	wrongExpected := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(otherSigner.PublicKey())))

	if _, err := NewSSHClient().Dial(context.Background(), addr, "frame", clientKey, wrongExpected); err == nil {
		t.Fatal("a server presenting a host key other than the expected one was accepted")
	}
}

// The other half of the same property: a server whose key does match must
// still be accepted, or pinning would refuse every legitimate reconnection
// too, not just a swapped machine.
func TestDialAcceptsAHostKeyThatMatchesExpected(t *testing.T) {
	hostSigner := newTestED25519Signer(t)
	addr := newTestSSHServer(t, hostSigner)
	clientKey := newTestClientKeyPEM(t)

	expected := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())))

	sess, err := NewSSHClient().Dial(context.Background(), addr, "frame", clientKey, expected)
	if err != nil {
		t.Fatalf("a server presenting exactly the expected host key was refused: %v", err)
	}
	defer func() { _ = sess.Close() }()
}
