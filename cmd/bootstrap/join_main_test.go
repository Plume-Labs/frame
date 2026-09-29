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
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/gothshoot/frame/internal/provision"
)

// A join produces no kubeconfig, so success is Ready alone: nothing is
// written -- not even to an out path a caller happened to pass -- and the
// operator is told the node rejoined rather than where a file went.
func TestFinishJoinSucceedsOnReadyWithNoKubeconfig(t *testing.T) {
	out := filepath.Join(t.TempDir(), "kubeconfig")
	var buf bytes.Buffer
	res := provision.Result{Phase: provision.PhaseReady, NodeName: "w3"}
	if err := finish(&buf, provision.ClusterJoin, out, res, nil); err != nil {
		t.Fatalf("a join that reached Ready was reported as a failure: %v", err)
	}
	if !strings.Contains(buf.String(), "w3 rejoined the existing cluster") {
		t.Errorf("the final message does not say the node rejoined:\n%s", buf.String())
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("a join wrote something to %s", out)
	}
}

func TestFinishJoinFailsWhenTheInstallFailed(t *testing.T) {
	res := provision.Result{Phase: provision.PhaseFailed, FailedPhase: provision.PhaseReady, NodeName: "w3"}
	err := finish(io.Discard, provision.ClusterJoin, "", res, errors.New("node w3 never became Ready"))
	if err == nil {
		t.Fatal("a failed join was reported as success")
	}
	if !strings.Contains(err.Error(), "phase Ready") {
		t.Errorf("the error does not name the phase it failed in: %v", err)
	}
}

// Install returns a nil error only at Ready today; a nil error in any
// other phase must still not read as a node that rejoined.
func TestFinishJoinRefusesANilErrorShortOfReady(t *testing.T) {
	res := provision.Result{Phase: provision.PhaseJoining, NodeName: "w3"}
	if err := finish(io.Discard, provision.ClusterJoin, "", res, nil); err == nil {
		t.Fatal("a join that never reached Ready was reported as success")
	}
}

// fakeAPIServer answers GET /api/v1/nodes/<name> with a Node whose Ready
// condition is ready, and counts the requests it served.
func fakeAPIServer(t *testing.T, name string, ready corev1.ConditionStatus) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nodes/"+name {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		node := corev1.Node{
			TypeMeta:   metav1.TypeMeta{Kind: "Node", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: ready},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(node)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func kubeconfigFor(server string) []byte {
	return []byte(`apiVersion: v1
kind: Config
clusters:
- name: existing
  cluster: {server: ` + server + `}
contexts:
- name: existing
  context: {cluster: existing, user: admin}
current-context: existing
users:
- name: admin
  user: {token: t}
`)
}

// For a join, Install hands NodeReady a nil kubeconfig (provision.Join
// produces none). That nil must reach the existing cluster the config
// named -- proven against a fake apiserver that actually serves the Node,
// not by inspecting which field was read.
func TestNodeReadyChecksTheExistingClusterWhenInstallPassesNil(t *testing.T) {
	srv, hits := fakeAPIServer(t, "w3", corev1.ConditionTrue)
	c := kubeconfigNodeChecker{existing: kubeconfigFor(srv.URL)}

	ok, err := c.NodeReady(context.Background(), nil, "w3")
	if err != nil {
		t.Fatalf("NodeReady against the existing cluster failed: %v", err)
	}
	if !ok {
		t.Error("a Ready node in the existing cluster was reported not Ready")
	}
	if hits.Load() == 0 {
		t.Error("the existing cluster was never asked")
	}
}

// An init's own kubeconfig still wins over existing: the new cluster is
// the one its node joined, and the two must never be confused.
func TestNodeReadyPrefersTheKubeconfigInstallPasses(t *testing.T) {
	newCluster, newHits := fakeAPIServer(t, "node-zero", corev1.ConditionTrue)
	existing, existingHits := fakeAPIServer(t, "node-zero", corev1.ConditionTrue)
	c := kubeconfigNodeChecker{existing: kubeconfigFor(existing.URL)}

	if _, err := c.NodeReady(context.Background(), kubeconfigFor(newCluster.URL), "node-zero"); err != nil {
		t.Fatal(err)
	}
	if newHits.Load() == 0 || existingHits.Load() != 0 {
		t.Errorf("new cluster asked %d times, existing %d times; want only the new one", newHits.Load(), existingHits.Load())
	}
}

// With neither, it refuses rather than reaching for a client that cannot
// exist on a laptop.
func TestNodeReadyRefusesWithNoKubeconfigAtAll(t *testing.T) {
	if _, err := (kubeconfigNodeChecker{}).NodeReady(context.Background(), nil, "w3"); err == nil {
		t.Fatal("NodeReady with no kubeconfig at all did not refuse")
	}
}
