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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/Plume-Labs/frame/internal/provision"
)

// fakeCluster is an apiserver serving exactly what the join preflight and
// the Ready check read: one Node, one node-password Secret (or its
// absence), and /version. Its state can change mid-test, the way a real
// Node's status does when the reinstalled kubelet registers.
type fakeCluster struct {
	mu           sync.Mutex
	node         *corev1.Node // nil: 404
	secretExists bool
	version      string
	requests     []string
}

func (f *fakeCluster) setNode(n *corev1.Node) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.node = n
}

func (f *fakeCluster) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeCluster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	notFound := func() {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(metav1.Status{
			TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
			Status:   metav1.StatusFailure, Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound,
		})
	}
	switch {
	case r.URL.Path == "/version":
		_ = json.NewEncoder(w).Encode(map[string]string{"gitVersion": f.version})
	case f.node != nil && r.URL.Path == "/api/v1/nodes/"+f.node.Name:
		_ = json.NewEncoder(w).Encode(f.node)
	case strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/kube-system/secrets/") && f.secretExists:
		_ = json.NewEncoder(w).Encode(corev1.Secret{
			TypeMeta:   metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{Name: strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/kube-system/secrets/")},
		})
	default:
		notFound()
	}
}

var longAgo = metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

// w3Node is the Node as the old machine left it: Ready, with the boot and
// machine IDs of the system about to be wiped.
func w3Node(bootID, machineID string, heartbeat metav1.Time) *corev1.Node {
	return &corev1.Node{
		TypeMeta:   metav1.TypeMeta{Kind: "Node", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Name: "w3", UID: types.UID("node-uid-w3")},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.0.2.213"}},
			NodeInfo:  corev1.NodeSystemInfo{BootID: bootID, MachineID: machineID},
			Conditions: []corev1.NodeCondition{{
				Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastHeartbeatTime: heartbeat,
			}},
		},
	}
}

func newFakeCluster(t *testing.T) (*fakeCluster, *httptest.Server) {
	t.Helper()
	f := &fakeCluster{node: w3Node("boot-old", "machine-old", longAgo), version: "v1.33.4+k3s1"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

// joinConfigFor is a loaded join config for w3 at 192.0.2.213 whose
// kubeconfig and serverURL both name srv.
func joinConfigFor(t *testing.T, srv *httptest.Server) Config {
	t.Helper()
	kc := writePrivate(t, t.TempDir(), "existing.kubeconfig", string(kubeconfigFor(srv.URL)), 0o600)
	cfg, err := LoadConfig(writeConfig(t, validJoinConfig(t, func(c *Config) {
		c.Hostname = "w3"
		c.Network.Address = "192.0.2.213/24"
		c.Kubeconfig = kc
		c.Cluster.ServerURL = "https://" + strings.TrimPrefix(srv.URL, "http://")
	})))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// --- preflight ---

// The positive control for every refusal below, and the record the Ready
// check depends on: the old Node's boot and machine IDs.
func TestPreflightJoinRecordsTheExistingNodesIdentity(t *testing.T) {
	_, srv := newFakeCluster(t)
	id, err := preflightJoin(context.Background(), io.Discard, joinConfigFor(t, srv))
	if err != nil {
		t.Fatalf("a cluster ready for w3 to rejoin was refused: %v", err)
	}
	if id != (nodeIdentity{UID: "node-uid-w3", BootID: "boot-old", MachineID: "machine-old"}) {
		t.Errorf("identity = %+v", id)
	}
}

func TestPreflightJoinRefusesAClusterNotReadyForTheRejoin(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(f *fakeCluster)
		want    string
	}{
		{"no Node by that name", func(f *fakeCluster) { f.node = nil }, `has no Node "w3"`},
		{"another InternalIP", func(f *fakeCluster) {
			f.node.Status.Addresses[0].Address = "192.0.2.99"
		}, "InternalIP is"},
		{"a stale node password", func(f *fakeCluster) { f.secretExists = true },
			"kubectl -n kube-system delete secret w3.node-password.k3s"},
		{"a server older than k3sVersion", func(f *fakeCluster) { f.version = "v1.32.9+k3s1" },
			"newer than the server's"},
		{"an older k3s patch on the server", func(f *fakeCluster) { f.version = "v1.33.4+k3s0" },
			"newer than the server's"},
		{"a server that is not k3s", func(f *fakeCluster) { f.version = "v1.33.4" }, "not a k3s version"},
	}
	for _, tc := range cases {
		f, srv := newFakeCluster(t)
		cfg := joinConfigFor(t, srv)
		tc.prepare(f)
		_, err := preflightJoin(context.Background(), io.Discard, cfg)
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: refused, but not by its own guard (want %q): %v", tc.name, tc.want, err)
		}
	}
}

// Older than the server is allowed -- k3s supports an agent behind its
// server -- but said out loud.
func TestPreflightJoinWarnsWhenK3sVersionIsOlderThanTheServer(t *testing.T) {
	f, srv := newFakeCluster(t)
	f.version = "v1.34.1+k3s1"
	var out bytes.Buffer
	if _, err := preflightJoin(context.Background(), &out, joinConfigFor(t, srv)); err != nil {
		t.Fatalf("an agent older than its server was refused: %v", err)
	}
	if !strings.Contains(out.String(), "warning") {
		t.Errorf("no warning for an agent older than its server:\n%s", out.String())
	}
}

// Version components compare as numbers, not text: 2 is older than 10.
// Compared as strings, "10" < "2" and each verdict would flip.
func TestPreflightJoinComparesTwoDigitVersionComponentsAsNumbers(t *testing.T) {
	t.Run("agent v1.36.2 behind server v1.36.10: allowed, warned", func(t *testing.T) {
		f, srv := newFakeCluster(t)
		f.version = "v1.36.10+k3s1"
		cfg := joinConfigFor(t, srv)
		cfg.Cluster.K3sVersion = "v1.36.2+k3s1"
		var out bytes.Buffer
		if _, err := preflightJoin(context.Background(), &out, cfg); err != nil {
			t.Fatalf("an agent older than its server was refused: %v", err)
		}
		if !strings.Contains(out.String(), "older than the server's") {
			t.Errorf("no older-than warning:\n%s", out.String())
		}
	})
	t.Run("agent v1.36.10 ahead of server v1.36.2: refused", func(t *testing.T) {
		f, srv := newFakeCluster(t)
		f.version = "v1.36.2+k3s1"
		cfg := joinConfigFor(t, srv)
		cfg.Cluster.K3sVersion = "v1.36.10+k3s1"
		_, err := preflightJoin(context.Background(), io.Discard, cfg)
		if err == nil || !strings.Contains(err.Error(), "newer than the server's") {
			t.Fatalf("an agent newer than its server was not refused by its own guard: %v", err)
		}
	})
}

// Offline, at load: the node would register with serverURL while the
// preflight and the Ready check read another cluster.
func TestLoadConfigRefusesAServerURLThatIsNotTheKubeconfigsServer(t *testing.T) {
	kc := writePrivate(t, t.TempDir(), "kc", string(kubeconfigFor("https://192.0.2.202:6443")), 0o600)
	_, err := LoadConfig(writeConfig(t, validJoinConfig(t, func(c *Config) { c.Kubeconfig = kc })))
	if err == nil || !strings.Contains(err.Error(), "join one and be checked against the other") {
		t.Fatalf("a serverURL naming another server than the kubeconfig was not refused by its own guard: %v", err)
	}
}

// --- the Ready check does not believe the old Node ---

// The old Node, still Ready from before the wipe, is not the rejoined
// machine; neither is a status with only one of the two IDs changed, nor
// one whose heartbeat predates Joining. Only a new boot ID, a new machine
// ID and a fresh heartbeat together are.
func TestNodeReadyDoesNotBelieveTheOldNode(t *testing.T) {
	f, srv := newFakeCluster(t)
	c := &kubeconfigNodeChecker{
		existing:     kubeconfigFor(srv.URL),
		before:       &nodeIdentity{UID: "node-uid-w3", BootID: "boot-old", MachineID: "machine-old"},
		joiningSince: time.Now(),
	}
	fresh := metav1.NewTime(time.Now().Add(2 * time.Second))

	for _, tc := range []struct {
		name string
		node *corev1.Node
		want string
	}{
		{"the old node, still Ready", w3Node("boot-old", "machine-old", longAgo), "boot ID"},
		{"only the boot ID changed", w3Node("boot-new", "machine-old", fresh), "machine ID"},
		{"only the machine ID changed", w3Node("boot-old", "machine-new", fresh), "boot ID"},
		{"both changed, heartbeat before Joining", w3Node("boot-new", "machine-new", longAgo), "heartbeat"},
	} {
		f.setNode(tc.node)
		ok, err := c.NodeReady(context.Background(), nil, "w3")
		if ok {
			t.Errorf("%s: reported Ready", tc.name)
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: not Ready, but not for its own reason (want %q): %v", tc.name, tc.want, err)
		}
	}

	f.setNode(w3Node("boot-new", "machine-new", fresh))
	ok, err := c.NodeReady(context.Background(), nil, "w3")
	if err != nil || !ok {
		t.Fatalf("the reinstalled machine, Ready with new IDs and a fresh heartbeat, = %v, %v; want Ready", ok, err)
	}
}

// --- execute: run()'s wiring, end to end without a BMC ---

// fakeRejoin stands in for provision.Install on a join: it reports
// Joining, the reinstalled kubelet registers, and Ready is asked of the
// checker execute supplied -- which is how a real Install ends a join.
func fakeRejoin(f *fakeCluster, called *bool) installFunc {
	return func(ctx context.Context, d provision.Deps, s provision.Spec, _ provision.Options) (provision.Result, error) {
		*called = true
		d.Report(provision.PhaseJoining)
		// Before the new kubelet registers, the old Node is still served,
		// still Ready. The checker execute built must not believe it.
		if ok, _ := d.Nodes.NodeReady(ctx, nil, s.Hostname); ok {
			return provision.Result{Phase: provision.PhaseReady, NodeName: s.Hostname},
				errors.New("the checker took the old Node's Ready for the rejoined machine")
		}
		f.setNode(w3Node("boot-new", "machine-new", metav1.NewTime(time.Now().Add(2*time.Second))))
		ok, err := d.Nodes.NodeReady(ctx, nil, s.Hostname)
		if err != nil || !ok {
			return provision.Result{Phase: provision.PhaseFailed, FailedPhase: provision.PhaseReady, NodeName: s.Hostname},
				errors.Join(errors.New("never Ready"), err)
		}
		return provision.Result{Phase: provision.PhaseReady, NodeName: s.Hostname}, nil
	}
}

func TestExecuteRunsAJoinFromPreflightToFinish(t *testing.T) {
	f, srv := newFakeCluster(t)
	cfg := joinConfigFor(t, srv)
	var called bool
	var out bytes.Buffer
	err := execute(context.Background(), &out, cfg, provision.Deps{}, provision.Spec{Hostname: "w3"},
		provision.Options{}, fakeRejoin(f, &called))
	if err != nil {
		t.Fatalf("a join that rejoined was reported as a failure: %v", err)
	}
	if !strings.Contains(out.String(), "rejoined the existing cluster") {
		t.Errorf("finish did not report a rejoin:\n%s", out.String())
	}
	// The preflight read the cluster before Install ran: its first request
	// is the Node, before anything the fake Install did.
	if reqs := f.seen(); len(reqs) == 0 || reqs[0] != "/api/v1/nodes/w3" {
		t.Errorf("requests = %v; want the preflight's Node read first", reqs)
	}
}

// A preflight refusal stops everything: Install -- and so the BMC -- is
// never reached.
func TestExecuteStopsBeforeInstallWhenThePreflightRefuses(t *testing.T) {
	f, srv := newFakeCluster(t)
	cfg := joinConfigFor(t, srv)
	f.secretExists = true
	var called bool
	err := execute(context.Background(), io.Discard, cfg, provision.Deps{}, provision.Spec{Hostname: "w3"},
		provision.Options{}, fakeRejoin(f, &called))
	if err == nil {
		t.Fatal("a join with a stale node password was not refused")
	}
	if called {
		t.Error("Install ran after the preflight refused")
	}
}

// The node-password Secret is checked again at the last point before the
// BMC is driven: between the preflight and the insert, the old agent -- if
// it still runs -- re-creates it, and the reinstalled agent would then be
// refused after the wipe.
func TestExecuteRechecksTheNodePasswordBeforeMediaAttached(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reappears bool
	}{{"still absent", false}, {"re-created after the preflight", true}} {
		f, srv := newFakeCluster(t)
		cfg := joinConfigFor(t, srv)
		var gateErr error
		gated := false
		install := func(
			ctx context.Context, d provision.Deps, s provision.Spec, _ provision.Options,
		) (provision.Result, error) {
			if d.BeforeMediaAttached == nil {
				return provision.Result{Phase: provision.PhaseFailed},
					errors.New("a join installed with no gate before MediaAttached")
			}
			f.mu.Lock()
			f.secretExists = tc.reappears
			f.mu.Unlock()
			gated = true
			gateErr = d.BeforeMediaAttached(ctx)
			if gateErr != nil {
				return provision.Result{Phase: provision.PhaseFailed, FailedPhase: provision.PhasePreparing}, gateErr
			}
			return provision.Result{Phase: provision.PhaseReady, NodeName: s.Hostname}, nil
		}
		err := execute(context.Background(), io.Discard, cfg, provision.Deps{}, provision.Spec{Hostname: "w3"},
			provision.Options{}, install)
		if !gated {
			t.Fatalf("%s: install never reached the gate: %v", tc.name, err)
		}
		if tc.reappears {
			if gateErr == nil || !strings.Contains(gateErr.Error(), "delete secret w3.node-password.k3s") {
				t.Errorf("%s: the gate did not refuse by its own guard: %v", tc.name, gateErr)
			}
		} else if gateErr != nil {
			t.Errorf("%s: the gate refused: %v", tc.name, gateErr)
		}
	}
}

// An old Node that reported no boot ID or no machine ID leaves the Ready
// check with one identity comparison fewer; the preflight says so.
func TestPreflightJoinWarnsWhenTheOldNodeLacksAnIdentity(t *testing.T) {
	for name, node := range map[string]*corev1.Node{
		"no boot ID":    w3Node("", "machine-old", longAgo),
		"no machine ID": w3Node("boot-old", "", longAgo),
	} {
		f, srv := newFakeCluster(t)
		f.node = node
		var out bytes.Buffer
		if _, err := preflightJoin(context.Background(), &out, joinConfigFor(t, srv)); err != nil {
			t.Errorf("%s: refused: %v", name, err)
			continue
		}
		if !strings.Contains(out.String(), "warning") || !strings.Contains(out.String(), "heartbeat") {
			t.Errorf("%s: no warning that the guard rests on the heartbeat:\n%s", name, out.String())
		}
	}
	_, srv := newFakeCluster(t)
	var out bytes.Buffer
	if _, err := preflightJoin(context.Background(), &out, joinConfigFor(t, srv)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "warning") {
		t.Errorf("a Node with both IDs drew a warning:\n%s", out.String())
	}
}
