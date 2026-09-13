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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// fakeRunner is a CommandRunner that records every invocation and never
// execs anything. The agent's whole restart half is "which command, with
// which arguments, how many times", so the recording is the assertion
// surface. out lets a test hand back real command output (e.g. lsblk's JSON
// for agent.ObserveDisks) where the caller actually parses it; every
// existing test leaves it at its zero value ("") because nothing they drive
// reads Run's return value.
type fakeRunner struct {
	out   string
	err   error
	calls [][]string
}

func (f *fakeRunner) Run(name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.out, f.err
}

func nodeWithRequest(value string) *corev1.Node {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	if value != "" {
		node.Annotations = map[string]string{
			framev1beta1.TuningRestartRequestedAnnotation: value,
		}
	}
	return node
}

// The load-bearing test of this task. The controller re-issues the restart
// request — same annotation, new timestamp — when a halted rollout is
// released by hand, and clears the old one when a rollout starts.
//
// A broken implementation has exactly two shapes and this test fails both:
// one that acts whenever the annotation is present schedules a second restart
// on the unchanged request (first assertion), and one that latches a boolean
// "already restarted once" never acts on the re-issued request (last
// assertion). Only comparing the value passes.
func TestServiceRestartRequestActsOnTheValueNotThePresence(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	first := "2026-08-17T05:00:00.000000001Z"

	serviceRestartRequest(nodeWithRequest(first), root, "k3s", runner)
	if len(runner.calls) != 1 {
		t.Fatalf("want the first request serviced exactly once, got %v", runner.calls)
	}

	// Same request, later tick: the node is mid-restart and the controller
	// has not cleared anything yet. Nothing new may be scheduled.
	serviceRestartRequest(nodeWithRequest(first), root, "k3s", runner)
	if len(runner.calls) != 1 {
		t.Fatalf("an unchanged request must not be replayed, got %v", runner.calls)
	}

	// Released halt: the controller asks again with a new value.
	serviceRestartRequest(nodeWithRequest("2026-08-17T06:00:00.000000002Z"), root, "k3s", runner)
	if len(runner.calls) != 2 {
		t.Fatalf("want a re-issued request serviced, got %v", runner.calls)
	}
}

// The latch has to survive this process dying, because acting on the request
// restarts k3s, which restarts this pod. A version holding the latch in
// memory comes back with an empty one while the request is still on the node
// — the controller only clears it after it has verified the restart — and
// restarts the node again, and again. Simulated here by servicing the same
// request through a second, independent call chain against the same root.
func TestServiceRestartRequestLatchSurvivesTheProcess(t *testing.T) {
	root := t.TempDir()
	value := "2026-08-17T05:00:00Z"

	first := &fakeRunner{}
	serviceRestartRequest(nodeWithRequest(value), root, "k3s", first)
	if len(first.calls) != 1 {
		t.Fatalf("want one schedule, got %v", first.calls)
	}

	// Asserted on disk, deliberately: a second in-process call would be
	// answered just as well by a package variable, so it proves nothing about
	// surviving the restart the agent is about to cause itself. The file is
	// the only part of the latch a new process can still see.
	raw, err := os.ReadFile(filepath.Join(root, restartRequestCachePath))
	if err != nil {
		t.Fatalf("the serviced request must be recorded where a restarted agent can read it: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != value {
		t.Fatalf("want the request value %q recorded, got %q", value, got)
	}

	afterRestart := &fakeRunner{}
	serviceRestartRequest(nodeWithRequest(value), root, "k3s", afterRestart)
	if len(afterRestart.calls) != 0 {
		t.Fatalf("a restarted agent must not replay a request it already serviced, got %v", afterRestart.calls)
	}
}

// A node with no request annotation is the overwhelmingly common case: every
// node, every tick, outside a rollout. Nothing may be run for it.
func TestServiceRestartRequestIgnoresANodeWithNoRequest(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}

	serviceRestartRequest(nodeWithRequest(""), root, "k3s", runner)

	if len(runner.calls) != 0 {
		t.Fatalf("no request means nothing to do, got %v", runner.calls)
	}
	if _, err := os.Stat(filepath.Join(root, restartRequestCachePath)); !os.IsNotExist(err) {
		t.Fatal("nothing may be latched when no restart was requested")
	}
}

// Recording before the command succeeds would swallow the request for good:
// the controller would sit out its restart timeout and halt the whole
// campaign over one transient systemd-run failure. A version that latched
// first passes every other test here and fails this one.
func TestServiceRestartRequestRetriesAfterAFailedSchedule(t *testing.T) {
	root := t.TempDir()
	value := "2026-08-17T05:00:00Z"

	failing := &fakeRunner{err: errors.New("systemd-run: connection refused")}
	serviceRestartRequest(nodeWithRequest(value), root, "k3s", failing)
	if len(failing.calls) != 1 {
		t.Fatalf("want one attempt, got %v", failing.calls)
	}

	retry := &fakeRunner{}
	serviceRestartRequest(nodeWithRequest(value), root, "k3s", retry)
	if len(retry.calls) != 1 {
		t.Fatalf("a failed schedule must be retried on the next tick, got %v", retry.calls)
	}
}

// The request reaches this function from a Node annotation, and the agent is
// privileged with hostPID, so an unchecked restart target is arbitrary root
// on every node. A version that skipped the allowlist would run systemd-run
// against sshd here; this test fails it on both counts — nothing executed,
// and nothing latched either, so a later legitimate request is still
// serviced.
func TestServiceRestartRequestRefusesAUnitNotOnTheAllowlist(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}

	serviceRestartRequest(nodeWithRequest("2026-08-17T05:00:00Z"), root, "sshd", runner)

	if len(runner.calls) != 0 {
		t.Fatalf("must not run anything for a unit off the allowlist, got %v", runner.calls)
	}
	if _, err := os.Stat(filepath.Join(root, restartRequestCachePath)); !os.IsNotExist(err) {
		t.Fatal("a refused request must not be recorded as serviced")
	}
}

// The controller parses both annotations with time.Parse(time.RFC3339Nano)
// and treats anything else as "no timestamp yet" — forever, silently. A
// version publishing time.Time's default format, or under a different key,
// deadlocks the rollout: the controller refuses to cordon a node whose
// restart it could not afterwards verify. This test reads the values back the
// way the controller does.
func TestSetNodeStatePublishesTheProtocolTheControllerReads(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	activeEnter := time.Date(2026, 8, 17, 3, 19, 0, 123456000, time.UTC)

	setNodeState(node, "k3s-agent", activeEnter, "a100-4x2g")

	if got := node.Annotations[framev1beta1.TuningUnitAnnotation]; got != "k3s-agent" {
		t.Fatalf("want the detected unit published, got %q", got)
	}
	raw, ok := node.Annotations[framev1beta1.TuningUnitActiveEnterAnnotation]
	if !ok {
		t.Fatalf("want %s published, got annotations %v", framev1beta1.TuningUnitActiveEnterAnnotation, node.Annotations)
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("the controller parses this with RFC3339Nano and %q does not: %v", raw, err)
	}
	if !parsed.Equal(activeEnter) {
		t.Fatalf("want %s, got %s", activeEnter, parsed)
	}
	if got := node.Labels[migConfigLabel]; got != "a100-4x2g" {
		t.Fatalf("want the MIG profile projected onto %s, got %q", migConfigLabel, got)
	}
}

// A unit that has never been active has no timestamp. Publishing the zero
// time would hand the controller a baseline from year 1 that literally any
// later reading beats, "verifying" a restart that never happened — so the
// last real value stays instead.
func TestSetNodeStateKeepsTheLastTimestampWhenThereIsNoNewOne(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "node-1",
		Annotations: map[string]string{
			framev1beta1.TuningUnitActiveEnterAnnotation: "2026-08-17T03:19:00Z",
		},
	}}

	setNodeState(node, "k3s", time.Time{}, "")

	if got := node.Annotations[framev1beta1.TuningUnitActiveEnterAnnotation]; got != "2026-08-17T03:19:00Z" {
		t.Fatalf("want the previously published timestamp untouched, got %q", got)
	}
	if _, ok := node.Labels[migConfigLabel]; ok {
		t.Fatalf("no recorded MIG profile must not label the node, got %v", node.Labels)
	}
}

// Two NodeTunings selecting one node is a configuration error the controller
// reports as Failed and applies nothing for. An agent that applied anyway
// would let the two specs take turns rewriting the same drop-in every 30
// seconds, with the winner decided by list order — the node's effective
// configuration becoming unpredictable, which is the exact failure the
// overlap rule exists to prevent.
func TestApplyMatchedRefusesWhenTwoNodeTuningsSelectTheNode(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "etc/systemd/system/k3s.service"), "[Unit]\n")
	dropIn := filepath.Join(root, "etc/systemd/system/k3s.service.d/10-ksm.conf")

	one := &framev1beta1.NodeTuning{
		ObjectMeta: metav1.ObjectMeta{Name: "a"},
		Spec:       framev1beta1.NodeTuningSpec{KSM: &framev1beta1.KSMSpec{Enabled: true}},
	}
	two := &framev1beta1.NodeTuning{
		ObjectMeta: metav1.ObjectMeta{Name: "b"},
		Spec:       framev1beta1.NodeTuningSpec{KSM: &framev1beta1.KSMSpec{Enabled: false}},
	}

	applyMatched(root, []*framev1beta1.NodeTuning{one, two}, &fakeRunner{})
	if _, err := os.Stat(dropIn); !os.IsNotExist(err) {
		t.Fatal("nothing may be applied to a node two NodeTunings select")
	}

	// The same call with a single object must apply, or the test above would
	// also pass an agent that applies nothing at all, ever.
	applyMatched(root, []*framev1beta1.NodeTuning{one}, &fakeRunner{})
	got, err := os.ReadFile(dropIn)
	if err != nil {
		t.Fatal(err)
	}
	if want := "[Service]\nMemoryKSM=yes\n"; string(got) != want {
		t.Fatalf("want drop-in %q, got %q", want, string(got))
	}
}

// TestTickPublishesDisksEvenWhenObserveFails is the regression test for step
// 6's independence from step 5: a NodeTuning-observe failure (here, a
// corrupted KSM sysfs counter — the one branch in agent.Observe that
// returns an error instead of defaulting a missing file to its zero value)
// must not suppress publishing the node's disks. The storage lot's task 5
// brief states this as two halves of one requirement ("A node no
// FrameMachine claims is logged... but cmd/agent/main.go logs it and
// carries on, because the tuning half of that loop must keep working");
// this test is the other direction of the same guarantee — a broken tuning
// half must not silence the storage half.
//
// tick's DetectKSMUnit and NodeTuning-selection steps are left to fail/no-op
// naturally against an otherwise-empty fixture root and an empty
// NodeTuningList: neither is what this test is about, and both already have
// their own coverage elsewhere in this file and in internal/agent.
func TestTickPublishesDisksEvenWhenObserveFails(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "sys/kernel/mm/ksm/general_profit"), "not-a-number\n")

	sch := clientgoscheme.Scheme
	if err := framev1beta1.AddToScheme(sch); err != nil {
		t.Fatalf("registering scheme: %v", err)
	}

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	machine := &framev1beta1.FrameMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "m1", Namespace: "default"},
		Spec: framev1beta1.FrameMachineSpec{
			BMC:     framev1beta1.BMCSpec{Address: "192.168.2.60", CredentialsRef: "m1-creds", TLS: framev1beta1.BMCTLSSpec{InsecureSkipVerify: true}},
			NodeRef: "node-1",
		},
	}
	kc := fake.NewClientBuilder().
		WithScheme(sch).
		WithStatusSubresource(&framev1beta1.FrameMachine{}).
		WithObjects(node, machine).
		Build()

	runner := &fakeRunner{out: `{"blockdevices":[` +
		`{"name":"sdc","path":"/dev/sdc","serial":"KZK245ZG","size":1200000000000,"type":"disk","fstype":null,"mountpoint":null}` +
		`]}`}

	tick(context.Background(), kc, "node-1", root, runner)

	var got framev1beta1.FrameMachine
	if err := kc.Get(context.Background(), client.ObjectKeyFromObject(machine), &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status.Storage == nil {
		t.Fatal("status.storage is nil: step 6 did not run when step 5's Observe failed")
	}
	if len(got.Status.Storage.Observed) != 1 || got.Status.Storage.Observed[0].SerialNumber != "KZK245ZG" {
		t.Errorf("Observed = %+v, want the one disk lsblk reported", got.Status.Storage.Observed)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
