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

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/rmocq/frame/internal/provision"
)

func TestAddressWithoutCIDR(t *testing.T) {
	cases := []struct{ in, want string }{
		{"192.168.2.210/24", "192.168.2.210"},
		{"192.168.2.210", "192.168.2.210"},
		{"10.0.0.1/8", "10.0.0.1"},
		{"", ""},
	}
	for _, c := range cases {
		if got := addressWithoutCIDR(c.in); got != c.want {
			t.Errorf("addressWithoutCIDR(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNodeIsReady(t *testing.T) {
	cases := []struct {
		name       string
		conditions []corev1.NodeCondition
		want       bool
	}{
		{"Ready=True", []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}, true},
		{"Ready=False", []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}, false},
		{"Ready=Unknown", []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}}, false},
		{"no conditions at all", nil, false},
		{"only an unrelated condition", []corev1.NodeCondition{{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse}}, false},
		{"Ready alongside an unrelated condition", []corev1.NodeCondition{
			{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue},
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			node := &corev1.Node{Status: corev1.NodeStatus{Conditions: c.conditions}}
			if got := nodeIsReady(node); got != c.want {
				t.Errorf("nodeIsReady() = %v, want %v", got, c.want)
			}
		})
	}
}

var hexUID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func TestNewInstallUID(t *testing.T) {
	uid, err := newInstallUID()
	if err != nil {
		t.Fatal(err)
	}
	if !hexUID.MatchString(uid) {
		t.Errorf("newInstallUID() = %q, want 32 lowercase hex characters", uid)
	}

	other, err := newInstallUID()
	if err != nil {
		t.Fatal(err)
	}
	if uid == other {
		t.Errorf("two calls to newInstallUID produced the same value %q; WaitForOurSystem tells machines apart by this", uid)
	}
}

// The media-listener start race, proven rather than only asserted in
// prose: bind a port first, then ask startMediaListener to serve on the
// same address, and confirm it reports the listener could not start --
// promptly, not by hanging until some caller's own, much longer timeout
// (a real install's MediaAttached phase) gives up instead.
func TestStartMediaListenerReportsAnAlreadyBoundPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	addr := l.Addr().String()

	start := time.Now()
	srv, err := startMediaListener(addr, http.NotFoundHandler())
	elapsed := time.Since(start)

	if err == nil {
		if srv != nil {
			_ = srv.Close()
		}
		t.Fatal("starting a listener on an already-bound address was accepted")
	}
	if !strings.Contains(err.Error(), "media listener") {
		t.Errorf("error does not name the media listener: %v", err)
	}
	// A bind failure is known synchronously by the OS; only a listener that
	// actually came up needs mediaStartGrace to be trusted. Reporting this
	// one at or past that window would mean the fast path degenerated into
	// the same wait a real bind failure exists to avoid.
	if elapsed >= mediaStartGrace {
		t.Errorf("bind failure took %s to report, at or past the %s grace window meant for a listener that came up", elapsed, mediaStartGrace)
	}
}

// The positive control for the test above: a free port is the case
// startMediaListener exists to let through. Without this, a version that
// refused every address would pass the bound-port test for the wrong
// reason.
func TestStartMediaListenerAcceptsAFreePort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	srv, err := startMediaListener(addr, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("a free port was refused: %v", err)
	}
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
}

// I4. provision.Join returns the kubeconfig in the Joining phase; the Ready
// poll and the cleanup defer can both fail the install after that. Those are
// the cases where a cluster exists, has a node, and its only admin
// credential was sitting in a Result that got thrown away because the error
// was checked first.
func TestFinishWritesTheKubeconfigEvenWhenTheInstallFailed(t *testing.T) {
	for name, res := range map[string]provision.Result{
		"cleanup failed after Ready": {
			Phase: provision.PhaseFailed, FailedPhase: provision.PhaseReady,
			Kubeconfig: []byte("apiVersion: v1\n"), NodeName: "node-zero",
		},
		"the node never became Ready": {
			Phase: provision.PhaseFailed, FailedPhase: provision.PhaseReady,
			Kubeconfig: []byte("apiVersion: v1\n"),
		},
	} {
		out := filepath.Join(t.TempDir(), "kubeconfig")
		err := finish(io.Discard, out, res, errors.New("the BMC refused the eject"))
		if err == nil {
			t.Errorf("%s: finish returned nil; a failed install must still fail", name)
		}
		b, readErr := os.ReadFile(out)
		if readErr != nil {
			t.Errorf("%s: the new cluster's kubeconfig was discarded: %v", name, readErr)
			continue
		}
		if string(b) != string(res.Kubeconfig) {
			t.Errorf("%s: kubeconfig on disk = %q, want %q", name, b, res.Kubeconfig)
		}
	}
}

// The positive control for the test above, and the ordinary path: a
// successful install writes it too, and says so.
func TestFinishWritesTheKubeconfigOnSuccess(t *testing.T) {
	out := filepath.Join(t.TempDir(), "kubeconfig")
	var buf bytes.Buffer
	res := provision.Result{Phase: provision.PhaseReady, Kubeconfig: []byte("apiVersion: v1\n"), NodeName: "node-zero"}
	if err := finish(&buf, out, res, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil || string(b) != string(res.Kubeconfig) {
		t.Fatalf("kubeconfig on disk = %q, %v", b, err)
	}
	if !strings.Contains(buf.String(), out) {
		t.Errorf("nothing told the operator where the kubeconfig went:\n%s", buf.String())
	}
}

// And the case that must stay an error: a run that produced no kubeconfig
// at all has nothing to write, and saying "written to ..." would be a lie.
func TestFinishRefusesASuccessThatProducedNoKubeconfig(t *testing.T) {
	out := filepath.Join(t.TempDir(), "kubeconfig")
	err := finish(io.Discard, out, provision.Result{Phase: provision.PhaseReady}, nil)
	if err == nil {
		t.Fatal("a Ready install with no kubeconfig was reported as success")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("something was written to %s anyway", out)
	}
}

// A write failure is never swallowed, even when the install itself
// succeeded: the credential is gone either way and the exit code has to say
// so.
func TestFinishFailsWhenTheKubeconfigCannotBeWritten(t *testing.T) {
	out := filepath.Join(t.TempDir(), "no-such-dir", "kubeconfig")
	res := provision.Result{Phase: provision.PhaseReady, Kubeconfig: []byte("apiVersion: v1\n")}
	if err := finish(io.Discard, out, res, nil); err == nil {
		t.Fatal("an unwritable out was reported as success")
	}
}
