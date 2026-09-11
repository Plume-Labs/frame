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

func (f *fakeSession) HostKey() string                             { return f.hostKey }
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

func (f *fakeSSH) Dial(context.Context, string, string, []byte) (Session, error) {
	f.attempts++
	if f.attempts <= f.failUntil {
		return nil, errors.New("connection refused")
	}
	return f.session, nil
}

// The installer takes many minutes and refuses connections the whole time.
func TestWaitForOurSystemKeepsTryingUntilTheMachineAnswers(t *testing.T) {
	f := &fakeSSH{failUntil: 3, session: &fakeSession{hostKey: "ssh-ed25519 AAAAhost", marker: "the-uid"}}
	hk, err := WaitForOurSystem(context.Background(), f, "192.168.2.210:22", "frame", nil, "the-uid", time.Millisecond)
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
func TestWaitForOurSystemRefusesASystemWithNoMarker(t *testing.T) {
	f := &fakeSSH{session: &fakeSession{hostKey: "ssh-ed25519 AAAAhost", marker: ""}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := WaitForOurSystem(ctx, f, "192.168.2.210:22", "frame", nil, "the-uid", time.Millisecond); err == nil {
		t.Fatal("a system with no marker was accepted as ours")
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

func TestNewSSHClientCapturesTheHostKeyItConnectedTo(t *testing.T) {
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	_, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPriv)
	if err != nil {
		t.Fatal(err)
	}
	_ = clientSigner

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

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

	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	// ssh.ParsePrivateKey needs PEM-armored text ("-----BEGIN...-----"), not
	// the block's raw, unarmored Bytes -- that field alone is rejected with
	// "ssh: no key found".
	sess, err := NewSSHClient().Dial(context.Background(), ln.Addr().String(), "frame", pem.EncodeToMemory(block))
	if err != nil {
		t.Fatalf("the handshake did not complete: %v", err)
	}
	defer func() { _ = sess.Close() }()

	want := string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))
	if strings.TrimSpace(sess.HostKey()) != strings.TrimSpace(want) {
		t.Errorf("HostKey() = %q, want %q", sess.HostKey(), want)
	}
}
