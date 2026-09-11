package provision

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHClient dials the machine under installation.
type SSHClient interface {
	// Dial opens a session to addr ("host:22") as user, with key.
	//
	// expectedHostKey empty means trust on first use: whatever the machine
	// presents is accepted and captured. Non-empty means the machine must
	// present that key, and a mismatch is refused -- which is what makes the
	// captured value worth capturing.
	Dial(ctx context.Context, addr, user string, key []byte, expectedHostKey string) (Session, error)
}

// Session is an open connection to the machine under installation.
type Session interface {
	HostKey() string                                     // "ssh-ed25519 AAAA..." as seen on connect
	Run(ctx context.Context, cmd string) (string, error) // combined output
	ReadFile(ctx context.Context, path string) ([]byte, error)
	Close() error
}

// sshClient is the real SSHClient, over golang.org/x/crypto/ssh.
type sshClient struct{}

// NewSSHClient returns the real client, over golang.org/x/crypto/ssh.
func NewSSHClient() SSHClient {
	return sshClient{}
}

func (sshClient) Dial(ctx context.Context, addr, user string, key []byte, expectedHostKey string) (Session, error) {
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parsing SSH key: %w", err)
	}

	// Trimmed once so the comparison below is never fooled by whitespace on
	// either side -- the value came from a previous WaitForOurSystem return,
	// which is itself canonicalized, but this must hold regardless of caller.
	wantHostKey := strings.TrimSpace(expectedHostKey)

	var hostKey string
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		// Frame cannot know the host key in advance on first contact: baking
		// it into the image would make the image a secret carrier, which the
		// design forbids. So an empty expectedHostKey trusts whatever is
		// presented and captures it. But capturing a value "for pinning" is
		// decoration unless a later call actually checks it -- so once the
		// caller has a key to compare against, a mismatch here aborts the
		// handshake instead of silently reconnecting to whatever answered.
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			hostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
			if wantHostKey != "" && hostKey != wantHostKey {
				return fmt.Errorf("host key changed for %s: presented host key %s, expected %s", addr, hostKey, wantHostKey)
			}
			return nil
		},
		Timeout: 5 * time.Second,
	}

	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}

	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	client := ssh.NewClient(c, chans, reqs)
	return &sshSession{client: client, hostKey: hostKey}, nil
}

type sshSession struct {
	client  *ssh.Client
	hostKey string
}

func (s *sshSession) HostKey() string {
	return s.hostKey
}

func (s *sshSession) Run(ctx context.Context, cmd string) (string, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return "", err
	}
	defer func() { _ = sess.Close() }()

	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := sess.CombinedOutput(cmd)
		done <- result{out: out, err: err}
	}()

	select {
	case <-ctx.Done():
		_ = sess.Close()
		return "", ctx.Err()
	case r := <-done:
		return string(r.out), r.err
	}
}

func (s *sshSession) ReadFile(ctx context.Context, path string) ([]byte, error) {
	out, err := s.Run(ctx, "cat "+path)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

func (s *sshSession) Close() error {
	return s.client.Close()
}

var _ io.Closer = (*sshSession)(nil)

// WaitForOurSystem blocks until the machine at addr is reachable over SSH,
// accepts our key, and carries this installation's UID in its marker file.
//
// The marker is what makes this a proof rather than a coincidence. "Something
// answers SSH at 192.168.2.210" is satisfied by any machine already at that
// address -- including the one we believed we were overwriting and in fact
// never touched. The UID existed nowhere but inside the image we built.
func WaitForOurSystem(ctx context.Context, c SSHClient, addr, user string, key []byte, uid string, every time.Duration) (string, error) {
	// Trimmed once, up front, and every later comparison uses this value.
	// Otherwise a uid with incidental whitespace passes this guard but can
	// never equal a correctly-trimmed marker below, refusing a good
	// installation forever instead of failing fast on a bad input.
	uid = strings.TrimSpace(uid)

	// Without this, an empty uid makes every unreadable marker look like a
	// match: a failed read leaves the compared content empty, "" == "" is
	// true, and a machine we never touched is accepted as ours -- the exact
	// failure this whole mechanism exists to prevent. So this is checked
	// before the loop even dials once.
	if uid == "" {
		return "", fmt.Errorf("waiting for %s: no install UID to check against, so no machine could be told apart from any other at that address", addr)
	}

	var last error
	for {
		select {
		case <-ctx.Done():
			if last == nil {
				last = ctx.Err()
			}
			return "", fmt.Errorf("waiting for %s to come up as our system: %w", addr, last)
		default:
		}

		// This is first contact: WaitForOurSystem has nothing to pin against
		// yet, so trust on first use. A later dial by a caller who already
		// holds the host key this call returns is what makes the pin real.
		sess, err := c.Dial(ctx, addr, user, key, "")
		if err != nil {
			last = err
		} else {
			// Canonical regardless of what this particular Session
			// implementation hands back -- the real client already trims at
			// capture, but WaitForOurSystem must not rely on that, since
			// Session is an interface and any implementation could differ.
			hostKey := strings.TrimSpace(sess.HostKey())
			b, readErr := sess.ReadFile(ctx, markerPath)
			_ = sess.Close()
			switch {
			// This branch is for the message, not for the refusal -- the UID
			// comparison below already refuses every unreadable marker too,
			// since a failed read leaves b empty and uid can never be empty
			// (guarded above), so "" != uid always holds. But "this is a
			// system we did not install" tells an operator something that
			// "carries UID %q, not %q" does not, especially when the first
			// %q would print empty, so its test asserts the message rather
			// than merely the refusal.
			case readErr != nil:
				last = fmt.Errorf("%s has no %s: this is a system we did not install", addr, markerPath)
			case strings.TrimSpace(string(b)) != uid:
				last = fmt.Errorf("%s carries install UID %q, not %q: this is a different system at that address",
					addr, strings.TrimSpace(string(b)), uid)
			default:
				return hostKey, nil
			}
		}

		select {
		case <-ctx.Done():
		case <-time.After(every):
		}
	}
}
