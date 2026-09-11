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
	Dial(ctx context.Context, addr, user string, key []byte) (Session, error)
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

func (sshClient) Dial(ctx context.Context, addr, user string, key []byte) (Session, error) {
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parsing SSH key: %w", err)
	}

	var hostKey string
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		// Frame cannot know the host key in advance: baking it into the image
		// would make the image a secret carrier, which the design forbids.
		// So the callback captures the key seen on connect for pinning rather
		// than verifying it against a known value.
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			hostKey = string(ssh.MarshalAuthorizedKey(key))
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
	// Without this, an empty uid makes every unreadable marker look like a
	// match: a failed read leaves the compared content empty, "" == "" is
	// true, and a machine we never touched is accepted as ours -- the exact
	// failure this whole mechanism exists to prevent. So this is checked
	// before the loop even dials once.
	if strings.TrimSpace(uid) == "" {
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

		sess, err := c.Dial(ctx, addr, user, key)
		if err != nil {
			last = err
		} else {
			hostKey := sess.HostKey()
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
