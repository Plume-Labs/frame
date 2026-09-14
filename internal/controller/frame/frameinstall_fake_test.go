/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/rmocq/frame/internal/provision"
)

// fakeInstallBMC, fakeInstallImages, fakeInstallSSH/fakeInstallSession and
// fakeInstallNodes are this file's counterparts of framemachine_fake_test.go's
// fakeRedfish: the seams provision.Install is tested through, with no
// network and no real BMC. Every fake is safe for concurrent use because
// Reconcile drives provision.Install in a goroutine (frameinstall_controller.go's
// runInstall), so a test observes a fake's state from a different goroutine
// than the one mutating it.

// fakeMarkerPath mirrors install.go's unexported markerPath
// ("/etc/frame-install-uid", confirmed by
// internal/provision/preseed_test.go's TestRenderPreseedWritesTheUIDMarker).
// It cannot be imported -- this is a different package -- so it is
// reproduced here by literal, the same way any fixture in this file stands
// in for a piece of internal/provision it cannot reach directly.
const fakeMarkerPath = "/etc/frame-install-uid"

// fakeK3sKubeconfigPath mirrors provision.k3sKubeconfigPath the same way
// fakeMarkerPath mirrors provision.markerPath: an unexported constant this
// package cannot reach, restated where the fake needs it.
const fakeK3sKubeconfigPath = "/etc/rancher/k3s/k3s.yaml"

// fakeK3sKubeconfig is a minimal but real kubeconfig shape:
// provision.Join's ClusterInit path reads exactly this file over SSH and
// hands it to RewriteKubeconfigServer, which fails on anything that does
// not parse as YAML and carry clusters[].cluster.server.
const fakeK3sKubeconfig = `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: QQ==
    server: https://127.0.0.1:6443
  name: default
contexts:
- context: {cluster: default, user: default}
  name: default
current-context: default
kind: Config
users:
- name: default
`

type fakeInstallBMC struct {
	mu sync.Mutex

	serial     string
	failSerial error
	inserted   bool
	calls      []string

	// blockSerial, when non-nil, makes Serial record its call and then
	// block until the channel is closed -- the seam
	// TestFrameInstallDoesNotRunTwoInstallsOnTheSameMachineConcurrently
	// uses to hold a genuinely-running install open deterministically,
	// rather than racing a second reconcile against however fast the fakes
	// happen to resolve.
	blockSerial chan struct{}

	// failEject drives the one shape that ends an install AFTER Join has
	// already returned the new cluster's kubeconfig: a Task 5 ruling made
	// cleanup failure an install failure, so this reaches Failed with
	// Result.Kubeconfig populated.
	failEject error
}

func (b *fakeInstallBMC) Serial(ctx context.Context) (string, error) {
	b.mu.Lock()
	b.calls = append(b.calls, "serial")
	block := b.blockSerial
	failErr := b.failSerial
	serial := b.serial
	b.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if failErr != nil {
		return "", failErr
	}
	return serial, nil
}

func (b *fakeInstallBMC) InsertMedia(_ context.Context, url string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, "insert:"+url)
	b.inserted = true
	return nil
}

func (b *fakeInstallBMC) MediaInserted(context.Context) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inserted, nil
}

func (b *fakeInstallBMC) EjectMedia(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, "eject")
	if b.failEject != nil {
		return b.failEject
	}
	b.inserted = false
	return nil
}

func (b *fakeInstallBMC) SetBootOnce(_ context.Context, target, mode string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, "boot:"+target+":"+mode)
	return nil
}

func (b *fakeInstallBMC) ClearBootOverride(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, "clearboot")
	return nil
}

func (b *fakeInstallBMC) Reset(_ context.Context, resetType string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, "reset:"+resetType)
	return nil
}

// callsContaining is read under the same lock every write takes, so a test
// polling a running install never races runInstall's goroutine.
func (b *fakeInstallBMC) callsContaining(prefix string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, c := range b.calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

func (b *fakeInstallBMC) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

type fakeInstallImages struct {
	mu        sync.Mutex
	built     int
	removed   int
	failBuild error
	// builtUID is the Spec.UID of the last image built. On a real machine
	// the install marker is whatever the preseed inside the booted image
	// wrote, so a fake machine has to answer with the UID of the image it
	// was built from -- not with a value the test chose independently. The
	// controller generates that UID per run and never writes it anywhere
	// readable, so there is no other honest way for the fake machine to
	// know it.
	builtUID string
}

func (i *fakeInstallImages) Build(_ context.Context, s provision.Spec) (string, string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.built++
	i.builtUID = s.UID
	if i.failBuild != nil {
		return "", "", i.failBuild
	}
	return "http://provisiond/iso/tok.iso", "tok", nil
}

// lastBuiltUID is what the machine built from the last image would carry in
// its marker file.
func (i *fakeInstallImages) lastBuiltUID() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.builtUID
}

func (i *fakeInstallImages) Remove(context.Context, string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.removed++
	return nil
}

func (i *fakeInstallImages) buildCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.built
}

// fakeInstallSession answers WaitForOurSystem's marker read and, for a
// ClusterInit spec, Join's read of /etc/rancher/k3s/k3s.yaml after it runs
// "k3s server --cluster-init". Any other path is refused, the same as a
// real machine answers `cat` on a file that is not there -- a fake that
// answered every path would hide a caller that started reading the wrong
// one.
//
// The kubeconfig is answered only to a privileged read, because that is
// what the machine does: k3s writes it root-owned 0600 and Join sets no
// --write-kubeconfig-mode. This fake used to serve it to Session.ReadFile,
// which is a bare `cat` as the frame user -- so every cluster-init test was
// green against a machine that does not exist. The install marker is chmod
// 444 by the preseed and is served to either.
type fakeInstallSession struct {
	mu      sync.Mutex
	hostKey string
	// images, when set, makes this fake machine answer with the UID of the
	// image it was built from -- which is what a real machine does, since
	// the marker is written by the preseed inside that image. marker below
	// overrides it, for the tests that need a machine carrying somebody
	// else's UID.
	images *fakeInstallImages
	marker string
	closed bool
	cmds   []string
	// sudoStderr is what `sudo` writes to stderr while still exiting 0.
	sudoStderr string
}

// markerValue is what this machine's /etc/frame-install-uid holds.
func (s *fakeInstallSession) markerValue() string {
	if s.marker != "" {
		return s.marker
	}
	if s.images != nil {
		return s.images.lastBuiltUID()
	}
	return ""
}

func (s *fakeInstallSession) HostKey() string { return s.hostKey }

// Run merges stderr, the way a real session's CombinedOutput does; Output
// does not. sudoStderr is what sudo writes there while still exiting 0 --
// see provision.Session.Output's doc comment for why that distinction is
// load-bearing for exactly one caller.
func (s *fakeInstallSession) Run(ctx context.Context, cmd string) (string, error) {
	out, err := s.Output(ctx, cmd)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	warn := s.sudoStderr
	s.mu.Unlock()
	if warn != "" && strings.HasPrefix(cmd, "sudo ") {
		return warn + "\n" + out, nil
	}
	return out, nil
}

func (s *fakeInstallSession) Output(_ context.Context, cmd string) (string, error) {
	s.mu.Lock()
	s.cmds = append(s.cmds, cmd)
	s.mu.Unlock()
	if path, ok := strings.CutPrefix(cmd, "sudo -n cat "); ok {
		return s.read(path, true)
	}
	if path, ok := strings.CutPrefix(cmd, "cat "); ok {
		return s.read(path, false)
	}
	return "", nil
}

func (s *fakeInstallSession) read(path string, privileged bool) (string, error) {
	switch path {
	case fakeMarkerPath:
		return s.markerValue(), nil
	case fakeK3sKubeconfigPath:
		if !privileged {
			return "", fmt.Errorf("cat: %s: Permission denied", path)
		}
		return fakeK3sKubeconfig, nil
	default:
		return "", fmt.Errorf("fakeInstallSession: no such file: %s", path)
	}
}

func (s *fakeInstallSession) ReadFile(ctx context.Context, path string) ([]byte, error) {
	out, err := s.Run(ctx, "cat "+path)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

func (s *fakeInstallSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

type fakeInstallSSH struct {
	mu       sync.Mutex
	session  *fakeInstallSession
	attempts int
}

func (f *fakeInstallSSH) Dial(context.Context, string, string, []byte, string) (provision.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	return f.session, nil
}

type fakeInstallNodes struct {
	mu    sync.Mutex
	ready bool
}

func (n *fakeInstallNodes) NodeReady(context.Context, []byte, string) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ready, nil
}

// fakeProgress is provision.ProgressReader for
// TestBeaconStateNeverEndsOrFailsAnInstall: a fixed outcome, so that test can
// drive Reconcile itself through each losing beacon state without a real
// provisiond.
type fakeProgress struct {
	state provision.BeaconState
	known bool
	err   error
}

func (p *fakeProgress) Progress(context.Context, string) (provision.BeaconState, bool, error) {
	return p.state, p.known, p.err
}
