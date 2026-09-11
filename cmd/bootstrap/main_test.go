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
	"context"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
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
