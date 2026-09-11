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
	"strings"
	"testing"
	"time"

	"github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/provision"
)

// Fixtures are prefixed fi-ctrl- throughout this file, unique to it, so a
// FrameInstall or FrameMachine created here can never collide with one from
// framemachine_controller_test.go or any other spec sharing the same fake
// client's "default" namespace.

func fiTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := framev1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func fiTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(fiTestScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameInstall{}, &framev1beta1.FrameTask{}).
		WithObjects(objs...).
		Build()
}

// fiTestPhaseTimeout is short enough that a test proving a real timeout
// (none currently do, but the option is there) does not slow the suite,
// while leaving every fake's zero latency free to resolve well within it.
var fiTestPhaseTimeout = map[provision.Phase]time.Duration{
	provision.PhasePending:       time.Second,
	provision.PhasePreparing:     time.Second,
	provision.PhaseMediaAttached: time.Second,
	provision.PhaseInstalling:    time.Second,
	provision.PhaseJoining:       time.Second,
	provision.PhaseReady:         time.Second,
}

func fiTestReconciler(c client.Client, bmc provision.BMC, images provision.ImageStore, ssh provision.SSHClient, nodes provision.NodeChecker) *FrameInstallReconciler {
	return &FrameInstallReconciler{
		Client: c,
		Scheme: c.Scheme(),
		// A nil Recorder panics the first time finishStatus/failPending/
		// failRestarted emit an Event -- the same choice
		// FrameMachineReconciler's own tests make (framemachine_controller_
		// test.go), deliberately: a reconciler under test with no Recorder
		// wired is a setup bug that should surface immediately, not be
		// silently tolerated by a nil check in production code.
		Recorder: record.NewFakeRecorder(32),
		Images:   images,
		SSH:      ssh,
		Nodes:    nodes,
		NewBMC: func(context.Context, client.Client, string, framev1beta1.BMCSpec) (provision.BMC, error) {
			return bmc, nil
		},
		PhaseTimeout: fiTestPhaseTimeout,
		Poll:         time.Millisecond,
	}
}

// fiMachine builds a FrameMachine with no disk inventory: nothing in this
// package reads FrameMachine.Status.Inventory.Drives any more (C3 —
// checkDisksKnown was removed; see frameinstall_controller.go's comment
// where it used to live), and a fixture that populated Drives with values
// agreeing with a FrameInstall's own layout.disks was itself how a defect
// that could never fire against real Redfish data went unnoticed.
func fiMachine(name, serial string) *framev1beta1.FrameMachine {
	return &framev1beta1.FrameMachine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: framev1beta1.FrameMachineSpec{
			BMC: framev1beta1.BMCSpec{
				Address:        "192.168.2.60",
				CredentialsRef: name + "-creds",
				TLS:            framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
			},
		},
		Status: framev1beta1.FrameMachineStatus{
			Inventory: &framev1beta1.MachineInventory{SerialNumber: serial},
		},
	}
}

func fiSSHSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Data: map[string][]byte{
			"id":     []byte("not-a-real-private-key"),
			"id.pub": []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILsytToxkJ2CiWuiv8BZ3hYpu7tFXn7Rwz+kc2gjbPSy fi-ctrl-fixture"),
		},
	}
}

// fiInstall builds an otherwise-fully-valid FrameInstall targeting one
// single-disk machine. Its UID equals its own name, so a test can set a
// fakeInstallSession's marker to the FrameInstall's own name and get the
// exact provision.Spec.UID WaitForOurSystem checks against, without needing
// a second round trip through the fake client to learn a generated UID.
//
// Generation is set to 1 explicitly, matching what a real apiserver assigns
// a freshly created object with a status subresource -- the fake client
// (sigs.k8s.io/controller-runtime@v0.23.3's client/fake) does not simulate
// metadata.generation at all: verified directly, it stays exactly 0 through
// Create and Update calls, spec-changing or not, if never set. Reconcile's
// restart-recovery check compares status.observedGeneration against
// metadata.generation; leaving Generation at the fake client's un-simulated
// 0 would make every fixture read as "already committed" (0 == 0, the
// status field's own zero value) before a single reconcile ever ran, which
// is exactly the false positive this fixture change exists to rule out.
func fiInstall(name, machineRef string) *framev1beta1.FrameInstall {
	return &framev1beta1.FrameInstall{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name), Generation: 1},
		Spec: framev1beta1.FrameInstallSpec{
			MachineRef:    machineRef,
			ConfirmSerial: "CZ3xxxxxxx",
			Hostname:      name,
			Network: framev1beta1.InstallNetwork{
				Address: "192.168.2.210/24",
				Gateway: "192.168.2.254",
			},
			Layout: framev1beta1.InstallLayout{
				Kind:  "single-disk",
				Disks: []framev1beta1.InstallDisk{{ByID: "/dev/disk/by-id/scsi-aaa", SizeBytes: 300 << 30}},
			},
			Cluster:   framev1beta1.InstallCluster{Mode: "init", K3sVersion: "v1.33.4+k3s1"},
			BootMode:  "UEFI",
			SSHKeyRef: machineRef + "-ssh",
		},
	}
}

func fiKey(fi *framev1beta1.FrameInstall) types.NamespacedName {
	return types.NamespacedName{Name: fi.Name, Namespace: fi.Namespace}
}

// fiAddFinalizer runs the first reconcile every non-deleting FrameInstall
// gets: Reconcile's own finalizer-add branch returns before doing anything
// else, so every test below needs this once before the reconcile it is
// actually testing.
func fiAddFinalizer(t *testing.T, r *FrameInstallReconciler, key types.NamespacedName) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile (finalizer add): %v", err)
	}
}

func fiGet(t *testing.T, c client.Client, key types.NamespacedName) framev1beta1.FrameInstall {
	t.Helper()
	var got framev1beta1.FrameInstall
	if err := c.Get(context.Background(), key, &got); err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	return got
}

func TestFrameInstallReconcilerMissingMachineFailsWithoutTouchingBMC(t *testing.T) {
	fi := fiInstall("fi-ctrl-missing", "fi-ctrl-missing-machine")
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	c := fiTestClient(t, fi, secret)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := fiGet(t, c, key)
	if got.Status.Phase != string(provision.PhaseFailed) {
		t.Errorf("phase = %q, want Failed", got.Status.Phase)
	}
	if got.Status.FailedPhase != string(provision.PhasePending) {
		t.Errorf("failedPhase = %q, want Pending", got.Status.FailedPhase)
	}
	if !strings.Contains(got.Status.Message, fi.Spec.MachineRef) {
		t.Errorf("message %q does not name the missing machine %q", got.Status.Message, fi.Spec.MachineRef)
	}
	if n := bmc.callCount(); n != 0 {
		t.Errorf("the BMC was touched: %d calls", n)
	}
}

// TestFrameInstallRefusesToReinstallALiveClusterMember is written out in
// full per the task brief, then TestFrameInstallRefusesToReinstallALiveClusterMemberByAddress
// below repeats its shape for the address match.
func TestFrameInstallRefusesToReinstallALiveClusterMember(t *testing.T) {
	fi := fiInstall("fi-ctrl-live", "fi-ctrl-live-machine")
	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "fi-ctrl-live"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.168.2.55"}},
		},
	}
	c := fiTestClient(t, fi, fm, secret, node)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := fiGet(t, c, key)
	if got.Status.Phase != string(provision.PhaseFailed) {
		t.Errorf("phase = %q, want Failed", got.Status.Phase)
	}
	if got.Status.FailedPhase != string(provision.PhasePending) {
		t.Errorf("failedPhase = %q, want Pending", got.Status.FailedPhase)
	}
	if !strings.Contains(got.Status.Message, "remove it from the cluster") {
		t.Errorf("message %q does not say the node must leave the cluster first", got.Status.Message)
	}
	if n := bmc.callCount(); n != 0 {
		t.Errorf("the BMC recorded calls: %d", n)
	}
}

func TestFrameInstallRefusesToReinstallALiveClusterMemberByAddress(t *testing.T) {
	fi := fiInstall("fi-ctrl-liveaddr", "fi-ctrl-liveaddr-machine")
	// fi's own hostname does not match the live node's name, so only the
	// address match can be what refuses this -- the discriminating shape
	// the brief asks for on this second case.
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "some-other-node"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.168.2.210"}},
		},
	}
	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	c := fiTestClient(t, fi, fm, secret, node)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := fiGet(t, c, key)
	if got.Status.Phase != string(provision.PhaseFailed) {
		t.Errorf("phase = %q, want Failed", got.Status.Phase)
	}
	if got.Status.FailedPhase != string(provision.PhasePending) {
		t.Errorf("failedPhase = %q, want Pending", got.Status.FailedPhase)
	}
	if !strings.Contains(got.Status.Message, "remove it from the cluster") {
		t.Errorf("message %q does not say the node must leave the cluster first", got.Status.Message)
	}
	if n := bmc.callCount(); n != 0 {
		t.Errorf("the BMC recorded calls: %d", n)
	}
}

// There is no TestFrameInstallRefusesAnUnknownDisk here any more. It tested
// checkDisksKnown, removed in this same fix round (C3): the guard it tested
// could never fire against real Redfish data (internal/redfish leaves
// Inventory.Drives nil by design) and could only ever pass against a
// fixture built to agree with itself — see frameinstall_controller.go's
// comment where checkDisksKnown used to live.

// TestFrameInstallRefusesAHostnameThatCollidesWithANotReadyNode is the
// structural half of the hostname guard that is not the live-member case:
// a Node occupying the name exists, but it is not Ready, so
// checkNotALiveClusterMember does not fire and only checkHostnameNotTaken
// can be what refuses this.
func TestFrameInstallRefusesAHostnameThatCollidesWithANotReadyNode(t *testing.T) {
	fi := fiInstall("fi-ctrl-notready", "fi-ctrl-notready-machine")
	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: fi.Spec.Hostname},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}},
		},
	}
	c := fiTestClient(t, fi, fm, secret, node)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := fiGet(t, c, key)
	if got.Status.Phase != string(provision.PhaseFailed) || got.Status.FailedPhase != string(provision.PhasePending) {
		t.Fatalf("phase=%s failedPhase=%s, want Failed/Pending", got.Status.Phase, got.Status.FailedPhase)
	}
	if !strings.Contains(got.Status.Message, "already exists") {
		t.Errorf("message %q does not describe a Node name collision", got.Status.Message)
	}
	if n := bmc.callCount(); n != 0 {
		t.Errorf("the BMC recorded calls: %d", n)
	}
}

// TestFrameInstallRefusesAHostnameThatCollidesWithAnotherNonTerminalInstall
// is the second structural case: no Node at all, but another FrameInstall
// already claims the same hostname and has not reached Ready or Failed.
func TestFrameInstallRefusesAHostnameThatCollidesWithAnotherNonTerminalInstall(t *testing.T) {
	fi := fiInstall("fi-ctrl-dupe-b", "fi-ctrl-dupe-machine")
	other := fiInstall("fi-ctrl-dupe-a", "fi-ctrl-dupe-machine")
	other.Spec.Hostname = fi.Spec.Hostname
	other.Status.Phase = string(provision.PhasePreparing) // non-terminal

	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	c := fiTestClient(t, fi, other, fm, secret)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := fiGet(t, c, key)
	if got.Status.Phase != string(provision.PhaseFailed) || got.Status.FailedPhase != string(provision.PhasePending) {
		t.Fatalf("phase=%s failedPhase=%s, want Failed/Pending", got.Status.Phase, got.Status.FailedPhase)
	}
	if !strings.Contains(got.Status.Message, other.Name) {
		t.Errorf("message %q does not name the colliding FrameInstall %q", got.Status.Message, other.Name)
	}
	if n := bmc.callCount(); n != 0 {
		t.Errorf("the BMC recorded calls: %d", n)
	}
}

// A non-terminal FrameInstall targeting the same hostname must not be
// refused by comparing against itself -- otherwise every install would
// refuse on its own second reconcile regardless of any other object.
// Covered implicitly by every other test in this file reaching Ready or
// Failed against a single FrameInstall in the fake client; this positive
// control makes the self-exclusion explicit rather than merely assumed.
func TestFrameInstallDoesNotCollideWithItself(t *testing.T) {
	fi := fiInstall("fi-ctrl-notself", "fi-ctrl-notself-machine")
	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	c := fiTestClient(t, fi, fm, secret)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if err := r.checkHostnameNotTaken(context.Background(), fi); err != nil {
		t.Errorf("a FrameInstall was refused for colliding with itself: %v", err)
	}
}

// TestFrameInstallRecordsFrameTaskOnCreateAndTerminal covers both audit
// entries: one on starting the install, one on reaching its terminal phase
// -- exercised here via the successful path, and again via the guard-refusal
// path in TestFrameInstallReconcilerMissingMachineFailsWithoutTouchingBMC's
// sibling assertions below (a guard refusal reaches Pending/Failed directly,
// without ever calling provision.Install, and still must record a terminal
// entry).
func TestFrameInstallRecordsFrameTaskOnCreateAndTerminal(t *testing.T) {
	fi := fiInstall("fi-ctrl-audit", "fi-ctrl-audit-machine")
	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	c := fiTestClient(t, fi, fm, secret)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	sess := &fakeInstallSession{hostKey: "ssh-ed25519 AAAAhost", marker: string(fi.UID)}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{session: sess}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	g := gomega.NewWithT(t)
	g.Eventually(func() string {
		return fiGet(t, c, key).Status.Phase
	}, 2*time.Second, 10*time.Millisecond).Should(gomega.Equal(string(provision.PhaseReady)))

	// finishStatus (which records the terminal FrameTask) runs after
	// provision.Install returns, strictly later than reportPhase's own
	// write of status.Phase == Ready above -- so the terminal entry can
	// still be a moment from existing even once the phase itself reads
	// Ready. Waited for separately, rather than assumed simultaneous.
	var tasks framev1beta1.FrameTaskList
	g.Eventually(func() int {
		tasks = framev1beta1.FrameTaskList{}
		if err := c.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
			t.Fatal(err)
		}
		n := 0
		for i := range tasks.Items {
			if tasks.Items[i].Spec.Target.Name == fi.Name && tasks.Items[i].Spec.Verb == framev1beta1.TaskVerbUpdate {
				n++
			}
		}
		return n
	}, 2*time.Second, 10*time.Millisecond).Should(gomega.BeNumerically(">=", 1))

	var creating, terminal *framev1beta1.FrameTask
	for i := range tasks.Items {
		task := &tasks.Items[i]
		if task.Spec.Target.Name != fi.Name {
			continue
		}
		switch task.Spec.Verb {
		case framev1beta1.TaskVerbCreate:
			creating = task
		case framev1beta1.TaskVerbUpdate:
			terminal = task
		}
	}
	if creating == nil {
		t.Fatal("no FrameTask recorded for creating the install")
	}
	if !strings.Contains(creating.Spec.Action, fi.Spec.MachineRef) {
		t.Errorf("create action %q does not name the machine", creating.Spec.Action)
	}
	if terminal == nil {
		t.Fatal("no FrameTask recorded for reaching a terminal phase")
	}
	if !strings.Contains(terminal.Spec.Action, fi.Spec.MachineRef) {
		t.Errorf("terminal action %q does not name the machine", terminal.Spec.Action)
	}
}

// TestFrameInstallRefusesSerialMismatchAndBuildsNoImage exercises
// provision.Install's own Pending-phase guard through the controller: every
// other input is valid, so only the serial comparison can be what refuses
// it.
func TestFrameInstallRefusesSerialMismatchAndBuildsNoImage(t *testing.T) {
	fi := fiInstall("fi-ctrl-badserial", "fi-ctrl-badserial-machine")
	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	c := fiTestClient(t, fi, fm, secret)
	bmc := &fakeInstallBMC{serial: "CZ3-not-the-confirmed-one"}
	images := &fakeInstallImages{}
	r := fiTestReconciler(c, bmc, images, &fakeInstallSSH{}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Waited for on FailedPhase, not Phase: reportPhase (Deps.Report) writes
	// status.Phase == Failed as the last of Install's own phase reports,
	// strictly before Install returns and finishStatus runs its own,
	// separate write of FailedPhase/Message/HostKey/NodeName. Polling Phase
	// alone would be racy against that second write -- proven under -race,
	// not assumed.
	g := gomega.NewWithT(t)
	g.Eventually(func() string {
		return fiGet(t, c, key).Status.FailedPhase
	}, 2*time.Second, 10*time.Millisecond).Should(gomega.Equal(string(provision.PhasePending)))

	got := fiGet(t, c, key)
	if got.Status.Phase != string(provision.PhaseFailed) {
		t.Errorf("phase = %q, want Failed", got.Status.Phase)
	}
	if got := images.buildCount(); got != 0 {
		t.Errorf("images.buildCount() = %d, want 0 -- no image should be built for a machine whose serial did not match", got)
	}

	// I6: a failed run must record a terminal FrameTask exactly the way a
	// successful one does (TestFrameInstallRecordsFrameTaskOnCreateAndTerminal
	// covers the Ready path) -- an audit trail that only ever records
	// successes would miss the runs that most need explaining.
	var terminal *framev1beta1.FrameTask
	g.Eventually(func() bool {
		var tasks framev1beta1.FrameTaskList
		if err := c.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
			t.Fatal(err)
		}
		for i := range tasks.Items {
			task := &tasks.Items[i]
			if task.Spec.Target.Name == fi.Name && task.Spec.Verb == framev1beta1.TaskVerbUpdate {
				terminal = task
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond).Should(gomega.BeTrue(), "no terminal FrameTask was recorded for the failed run")
	if !strings.Contains(terminal.Spec.Action, fi.Spec.MachineRef) {
		t.Errorf("terminal action %q does not name the machine", terminal.Spec.Action)
	}
}

// TestFrameInstallSuccessfulRunReachesReady is the happy path: mode init,
// so it also proves the kubeconfig Secret's shape.
func TestFrameInstallSuccessfulRunReachesReady(t *testing.T) {
	fi := fiInstall("fi-ctrl-happy", "fi-ctrl-happy-machine")
	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	c := fiTestClient(t, fi, fm, secret)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	sess := &fakeInstallSession{hostKey: "ssh-ed25519 AAAAhost", marker: string(fi.UID)}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{session: sess}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Waited for on NodeName, not Phase: reportPhase already writes
	// status.Phase == Ready as the last of Install's own phase reports,
	// strictly before Install returns and finishStatus writes
	// NodeName/HostKey/KubeconfigSecret in a second, separate patch. Polling
	// Phase alone would be racy against that second write.
	g := gomega.NewWithT(t)
	g.Eventually(func() string {
		return fiGet(t, c, key).Status.NodeName
	}, 2*time.Second, 10*time.Millisecond).Should(gomega.Equal(fi.Spec.Hostname))

	got := fiGet(t, c, key)
	if got.Status.Phase != string(provision.PhaseReady) {
		t.Errorf("phase = %q, want Ready", got.Status.Phase)
	}
	if got.Status.HostKey == "" {
		t.Error("hostKey was not pinned")
	}
	wantSecret := fi.Name + "-kubeconfig"
	if got.Status.KubeconfigSecret != wantSecret {
		t.Fatalf("kubeconfigSecret = %q, want %q", got.Status.KubeconfigSecret, wantSecret)
	}

	var sec corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Name: wantSecret, Namespace: "default"}, &sec); err != nil {
		t.Fatalf("kubeconfig secret was not created: %v", err)
	}
	if sec.Type != corev1.SecretTypeOpaque {
		t.Errorf("secret type = %s, want Opaque", sec.Type)
	}
	if len(sec.Data) != 1 {
		t.Errorf("secret carries %d keys, want exactly 1: %v", len(sec.Data), sec.Data)
	}
	if _, ok := sec.Data["kubeconfig"]; !ok {
		t.Error("secret has no kubeconfig key")
	}
}

// TestFrameInstallFinalizerEjectsMediaOnDelete simulates the case the
// finalizer exists for: a manager restart lost track of the goroutine
// (inFlight is empty), but the object's own status still shows a mid-install
// phase -- media may genuinely still be attached.
func TestFrameInstallFinalizerEjectsMediaOnDelete(t *testing.T) {
	fi := fiInstall("fi-ctrl-delmidflight", "fi-ctrl-delmidflight-machine")
	fi.Finalizers = []string{frameInstallFinalizer}
	fi.Status.Phase = string(provision.PhaseInstalling)
	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	c := fiTestClient(t, fi, fm)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	if err := c.Delete(context.Background(), fi); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile (finalize): %v", err)
	}

	if n := bmc.callsContaining("eject"); n == 0 {
		t.Error("the finalizer did not eject media")
	}

	var got framev1beta1.FrameInstall
	err := c.Get(context.Background(), key, &got)
	if err == nil {
		t.Fatalf("object still exists after its finalizer removed itself: %+v", got)
	}
}

// TestFrameInstallFinalizerWaitsForARunningInstallRatherThanRacingIt proves
// the other half of finalize's behaviour: while running(machineRef) is true
// -- meaning this same process has a provision.Install call in flight for
// this machine -- the finalizer must not issue its own, concurrent BMC
// calls. Without this, deleting an object mid-flight in the same process
// could eject media out from under the goroutine that is still using it.
func TestFrameInstallFinalizerWaitsForARunningInstallRatherThanRacingIt(t *testing.T) {
	fi := fiInstall("fi-ctrl-delrunning", "fi-ctrl-delrunning-machine")
	fi.Finalizers = []string{frameInstallFinalizer}
	fi.Status.Phase = string(provision.PhaseInstalling)
	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	c := fiTestClient(t, fi, fm)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	if !r.startInstall(fi.Spec.MachineRef, fi.UID) {
		t.Fatal("startInstall refused a machineRef with nothing running yet")
	}

	if err := c.Delete(context.Background(), fi); err != nil {
		t.Fatalf("delete: %v", err)
	}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("reconcile (finalize, running): %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Error("finalize did not requeue while the install was still running")
	}
	if n := bmc.callCount(); n != 0 {
		t.Errorf("the finalizer touched the BMC while an install was still running: %d calls", n)
	}

	r.finishInstall(fi.Spec.MachineRef)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile (finalize, now idle): %v", err)
	}
	if n := bmc.callsContaining("eject"); n == 0 {
		t.Error("the finalizer did not eject media once the install was no longer running")
	}
}

// TestFrameInstallSecondReconcileDoesNotStartASecondInstall is the run-once
// guard's own test. It carries a positive control deliberately: without
// proving the first install actually ran (images.buildCount() == 1, phase
// == Ready) before checking that a second reconcile leaves that count
// unchanged, a version that never starts *any* install would pass this test
// exactly as well as a correct one.
func TestFrameInstallSecondReconcileDoesNotStartASecondInstall(t *testing.T) {
	fi := fiInstall("fi-ctrl-once", "fi-ctrl-once-machine")
	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	c := fiTestClient(t, fi, fm, secret)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	images := &fakeInstallImages{}
	sess := &fakeInstallSession{hostKey: "ssh-ed25519 AAAAhost", marker: string(fi.UID)}
	r := fiTestReconciler(c, bmc, images, &fakeInstallSSH{session: sess}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	g := gomega.NewWithT(t)
	g.Eventually(func() string {
		return fiGet(t, c, key).Status.Phase
	}, 2*time.Second, 10*time.Millisecond).Should(gomega.Equal(string(provision.PhaseReady)))

	// Positive control: the first install actually ran. Without this check,
	// the assertions below would pass identically for a reconciler that
	// never started any install at all.
	if got := images.buildCount(); got != 1 {
		t.Fatalf("images.buildCount() = %d after the first reconcile reached Ready, want 1 -- "+
			"this test proves nothing about a second install if the first one never happened", got)
	}
	bootCalls := bmc.callsContaining("boot:")
	if bootCalls == 0 {
		t.Fatal("the first install never set a boot override -- this test proves nothing about a second install if the first one never happened")
	}

	// runInstall's own goroutine clears r.running(machineRef) in a defer,
	// which runs after finishStatus -- so status.Phase can already read
	// Ready (finishStatus's write) for a brief window in which
	// running(machineRef) is still true. Waited out explicitly: with it
	// still true, Reconcile's own *other* guard (r.running) would block a
	// second install by accident, on a call that says nothing about the
	// terminal-phase check this test exists to prove. Checked: without this
	// wait, this test still passed after deleting the terminal-phase guard
	// entirely, for exactly that reason -- it wasn't the guard under test
	// holding, it was timing. See task-9-report.md for that red/green pair.
	g.Eventually(func() bool {
		return r.running(fi.Spec.MachineRef)
	}, 2*time.Second, 10*time.Millisecond).Should(gomega.BeFalse())

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	// Reconcile only starts a second install asynchronously (go
	// r.runInstall(...)) and returns immediately; a synchronous check right
	// here would race a second goroutine rather than prove one never ran --
	// checked, this exact shape passed even with the run-once guard deleted,
	// because the assertion ran before a second Build() call had any chance
	// to land. Consistently instead holds the assertion open for a real
	// window, which is what actually gives a second install time to show
	// up if the guard above did not hold.
	g.Consistently(func() int {
		return images.buildCount()
	}, 500*time.Millisecond, 10*time.Millisecond).Should(gomega.Equal(1),
		"the run-once guard did not hold: a second install started")
	if got := bmc.callsContaining("boot:"); got != bootCalls {
		t.Errorf("boot override was set again on the second reconcile (%d -> %d): the run-once guard did not hold", bootCalls, got)
	}
}

// TestFrameInstallRefusesAHostnameThatCollidesAcrossNamespaces is I5's own
// test: checkHostnameNotTaken used to List FrameInstalls scoped to
// client.InNamespace(fi.Namespace), so two objects in different namespaces
// naming the same hostname passed this guard and would have collided on the
// Node name itself once both tried to install. Hostnames become Node names,
// which are cluster-global, and this reconciler's RBAC is already a
// ClusterRole -- scoping the List bought no safety, only a blind spot.
func TestFrameInstallRefusesAHostnameThatCollidesAcrossNamespaces(t *testing.T) {
	fi := fiInstall("fi-ctrl-crossns-b", "fi-ctrl-crossns-machine")
	other := fiInstall("fi-ctrl-crossns-a", "fi-ctrl-crossns-machine")
	other.Namespace = "fi-ctrl-other-namespace"
	other.Spec.Hostname = fi.Spec.Hostname
	other.Status.Phase = string(provision.PhasePreparing) // non-terminal

	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	c := fiTestClient(t, fi, other, fm, secret)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := fiGet(t, c, key)
	if got.Status.Phase != string(provision.PhaseFailed) || got.Status.FailedPhase != string(provision.PhasePending) {
		t.Fatalf("phase=%s failedPhase=%s, want Failed/Pending -- a same-hostname collision in a different namespace was not refused", got.Status.Phase, got.Status.FailedPhase)
	}
	if !strings.Contains(got.Status.Message, other.Namespace+"/"+other.Name) {
		t.Errorf("message %q does not name the colliding FrameInstall %s/%s", got.Status.Message, other.Namespace, other.Name)
	}
	if n := bmc.callCount(); n != 0 {
		t.Errorf("the BMC recorded calls: %d", n)
	}
}

// TestFrameInstallRefusesASameNamedFrameInstallInADifferentNamespace is the
// test the review found missing: checkHostnameNotTaken excludes "myself"
// from the collision scan by comparing UID, not Name -- Name is only
// unique within a namespace, and a comparison by Name would wrongly treat
// two distinct objects that happen to share a Name across namespaces as
// the same object, silently skipping a real collision.
//
// Every other fixture pair in this file that exercises the collision guard
// also has a different Name, so a reviewer reverting the comparison to
// other.Name == fi.Name found every existing test -- including
// TestFrameInstallRefusesAHostnameThatCollidesAcrossNamespaces above --
// still green. This is the one built to actually need the UID comparison:
// fi and other share both Name and Hostname, and differ only in Namespace
// and UID (fiInstall derives UID from Name, so it has to be overridden by
// hand here -- otherwise this fixture pair would share a UID too, which no
// two real Kubernetes objects ever do, and the test would still prove
// nothing).
func TestFrameInstallRefusesASameNamedFrameInstallInADifferentNamespace(t *testing.T) {
	fi := fiInstall("fi-ctrl-samename", "fi-ctrl-samename-machine")
	other := fiInstall("fi-ctrl-samename", "fi-ctrl-samename-machine") // same Name as fi
	other.Namespace = "fi-ctrl-other-namespace"
	other.UID = types.UID("fi-ctrl-samename-other-ns-uid") // distinct UID; real objects never share one
	// fiInstall already sets Spec.Hostname == the object's own Name, so fi
	// and other already share a hostname by construction here -- no
	// override needed, and leaving it implicit is itself part of what makes
	// this fixture pair look, at a glance, like "the same object" the way
	// the bug this test exists for would have treated them.
	other.Status.Phase = string(provision.PhasePreparing) // non-terminal

	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	c := fiTestClient(t, fi, other, fm, secret)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := fiGet(t, c, key)
	if got.Status.Phase != string(provision.PhaseFailed) || got.Status.FailedPhase != string(provision.PhasePending) {
		t.Fatalf("phase=%s failedPhase=%s, want Failed/Pending -- a same-named FrameInstall in a different namespace was wrongly excluded as \"myself\"", got.Status.Phase, got.Status.FailedPhase)
	}
	if !strings.Contains(got.Status.Message, other.Namespace+"/"+other.Name) {
		t.Errorf("message %q does not name the colliding FrameInstall %s/%s", got.Status.Message, other.Namespace, other.Name)
	}
	if n := bmc.callCount(); n != 0 {
		t.Errorf("the BMC recorded calls: %d", n)
	}
}

// TestFrameInstallDoesNotRunTwoInstallsOnTheSameMachineConcurrently is I4's
// own test. Before this fix, inFlight was keyed by the FrameInstall's own
// UID, so two different objects naming the same machineRef each carried a
// distinct key and could both pass every guard and both call the BMC at
// once. It uses fakeInstallBMC.blockSerial to hold fi-A's install open
// deterministically -- with instantaneous fakes, a second reconcile racing
// however fast fi-A happens to finish would not reliably catch a
// regression.
func TestFrameInstallDoesNotRunTwoInstallsOnTheSameMachineConcurrently(t *testing.T) {
	fiA := fiInstall("fi-ctrl-race-a", "fi-ctrl-race-machine")
	fiB := fiInstall("fi-ctrl-race-b", "fi-ctrl-race-machine")
	// Different hostnames: checkHostnameNotTaken must not be what blocks
	// fiB here -- only the machineRef-keyed inFlight guard should be.
	fm := fiMachine(fiA.Spec.MachineRef, fiA.Spec.ConfirmSerial)
	secretA := fiSSHSecret(fiA.Spec.SSHKeyRef)
	c := fiTestClient(t, fiA, fiB, fm, secretA)

	block := make(chan struct{})
	bmc := &fakeInstallBMC{serial: fiA.Spec.ConfirmSerial, blockSerial: block}
	// fiA's install must be able to run to completion once unblocked, to
	// prove the guard did not wrongly wedge it forever -- so it needs a
	// real session behind it, not the zero-value fakeInstallSSH a
	// guard-refusal test can get away with.
	sessA := &fakeInstallSession{hostKey: "ssh-ed25519 AAAAhost", marker: string(fiA.UID)}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{session: sessA}, &fakeInstallNodes{ready: true})
	keyA, keyB := fiKey(fiA), fiKey(fiB)

	fiAddFinalizer(t, r, keyA)
	fiAddFinalizer(t, r, keyB)

	// fiA's reconcile starts a goroutine that calls bmc.Serial(), which
	// blocks on `block`. Reconcile itself still returns immediately --
	// only the goroutine it spawned is stuck.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: keyA}); err != nil {
		t.Fatalf("reconcile fiA: %v", err)
	}

	// Waited for on the Serial call itself, not merely running(machineRef):
	// startInstall claims the machineRef synchronously inside Reconcile,
	// strictly before the goroutine it spawns ever calls bmc.Serial() --
	// running() can read true a moment before that call has actually landed
	// in fakeInstallBMC.calls, which raced this assertion (checked: it
	// failed intermittently on exactly this line before the wait target
	// changed).
	g := gomega.NewWithT(t)
	g.Eventually(func() int {
		return bmc.callsContaining("serial")
	}, 2*time.Second, 10*time.Millisecond).Should(gomega.Equal(1))
	if !r.running(fiA.Spec.MachineRef) {
		t.Fatalf("running(%q) = false once fiA's Serial call landed, want true", fiA.Spec.MachineRef)
	}

	resB, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: keyB})
	if err != nil {
		t.Fatalf("reconcile fiB: %v", err)
	}
	if resB.RequeueAfter <= 0 {
		t.Error("fiB's reconcile did not requeue while fiA's install was running on the same machine")
	}
	// The discriminating assertion: fiB must not have reached the BMC at
	// all. Before this fix, fiB would have called Serial a second time here
	// (a second, concurrent goroutine racing fiA's).
	if n := bmc.callsContaining("serial"); n != 1 {
		t.Errorf("serial calls = %d after fiB's reconcile, want still 1 -- fiB started its own install on a machine fiA is already installing", n)
	}

	close(block)
	g.Eventually(func() string {
		return fiGet(t, c, keyA).Status.NodeName
	}, 2*time.Second, 10*time.Millisecond).Should(gomega.Equal(fiA.Spec.Hostname))
}

// TestFrameInstallRestartMidInstallFailsRatherThanResumes is C1's own test.
// It simulates exactly what a manager restart leaves behind: a FrameInstall
// whose current generation was already committed to provision.Install
// (status.observedGeneration == metadata.generation, stamped synchronously
// by Reconcile before it starts a goroutine) and a non-terminal
// status.phase, but nothing running in *this* process's inFlight map (a
// fresh reconciler here, the same way a fresh process after a restart has
// an empty one). Before this fix, Reconcile read that combination as
// "nothing to do, fall through to the guards and start a new install" --
// replaying the destructive sequence because a process died.
func TestFrameInstallRestartMidInstallFailsRatherThanResumes(t *testing.T) {
	fi := fiInstall("fi-ctrl-restart", "fi-ctrl-restart-machine")
	fi.Finalizers = []string{frameInstallFinalizer}
	fi.Status.Phase = string(provision.PhaseMediaAttached)
	fi.Status.ObservedGeneration = fi.Generation // "already committed"
	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	c := fiTestClient(t, fi, fm)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	if r.running(fi.Spec.MachineRef) {
		t.Fatal("test setup bug: nothing should be running in this fresh reconciler")
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := fiGet(t, c, key)
	if got.Status.Phase != string(provision.PhaseFailed) {
		t.Errorf("phase = %q, want Failed", got.Status.Phase)
	}
	if got.Status.FailedPhase != string(provision.PhaseMediaAttached) {
		t.Errorf("failedPhase = %q, want MediaAttached (the phase it actually was in), not overwritten by anything else", got.Status.FailedPhase)
	}
	if !strings.Contains(got.Status.Message, "restart") {
		t.Errorf("message %q does not mention a restart", got.Status.Message)
	}
	// Cleanup was attempted through the same BMC path finalize uses.
	if n := bmc.callsContaining("eject"); n == 0 {
		t.Error("no cleanup eject was attempted for the orphaned install")
	}
	if n := bmc.callsContaining("clearboot"); n == 0 {
		t.Error("no cleanup clearboot was attempted for the orphaned install")
	}
	// Discriminating: Serial/Build/boot must never be called -- this is a
	// refusal, not a resumed or retried install.
	if n := bmc.callsContaining("serial"); n != 0 {
		t.Errorf("the serial confirmation guard ran (%d calls): this must be a refusal, not an install attempt", n)
	}
}

// TestFrameInstallDoesNotTreatANonTerminalPhaseAloneAsARestart is the
// positive control for the test above: a non-terminal status.phase by
// itself -- without status.observedGeneration actually matching
// metadata.generation -- must not trigger the restart-recovery path.
// Without this, a version that read "phase is non-terminal" instead of
// "observedGeneration was actually stamped for this generation" would pass
// the restart test above for the wrong reason (both fixtures have a
// non-terminal phase) and this one would catch it: a fresh object always
// has ObservedGeneration 0, which must not incidentally equal Generation.
func TestFrameInstallDoesNotTreatANonTerminalPhaseAloneAsARestart(t *testing.T) {
	fi := fiInstall("fi-ctrl-notarestart", "fi-ctrl-notarestart-machine")
	// A non-terminal phase with no corresponding ObservedGeneration commit
	// -- not the shape Reconcile's own synchronous stamp ever produces, but
	// exactly the shape that isolates "non-terminal phase" from "actually
	// committed" as the two are otherwise only ever set together.
	fi.Status.Phase = string(provision.PhasePreparing)
	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	sess := &fakeInstallSession{hostKey: "ssh-ed25519 AAAAhost", marker: string(fi.UID)}
	c := fiTestClient(t, fi, fm, secret)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{session: sess}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	g := gomega.NewWithT(t)
	g.Eventually(func() string {
		return fiGet(t, c, key).Status.NodeName
	}, 2*time.Second, 10*time.Millisecond).Should(gomega.Equal(fi.Spec.Hostname))

	got := fiGet(t, c, key)
	if got.Status.Phase != string(provision.PhaseReady) {
		t.Errorf("phase = %q, want Ready -- a non-terminal phase alone was wrongly treated as a restart to recover from", got.Status.Phase)
	}
}

// TestFrameInstallKubeconfigWriteFailureGoesFailedNotReady is I1's own
// test. The install itself succeeds -- there is a Ready node -- but the
// kubeconfig Secret cannot be written, driven here by pre-creating a Secret
// at the exact name writeKubeconfigSecret will try to Create, so it fails
// with a real AlreadyExists rather than a hand-rolled failing client. That
// is a different, unrecoverable fact from Ready: the kubeconfig only ever
// existed in the goroutine's memory, and this reconciler's RBAC grants
// secrets create but not update.
func TestFrameInstallKubeconfigWriteFailureGoesFailedNotReady(t *testing.T) {
	fi := fiInstall("fi-ctrl-kcfail", "fi-ctrl-kcfail-machine")
	fm := fiMachine(fi.Spec.MachineRef, fi.Spec.ConfirmSerial)
	secret := fiSSHSecret(fi.Spec.SSHKeyRef)
	colliding := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: fi.Name + "-kubeconfig", Namespace: "default"}}
	c := fiTestClient(t, fi, fm, secret, colliding)
	bmc := &fakeInstallBMC{serial: fi.Spec.ConfirmSerial}
	sess := &fakeInstallSession{hostKey: "ssh-ed25519 AAAAhost", marker: string(fi.UID)}
	r := fiTestReconciler(c, bmc, &fakeInstallImages{}, &fakeInstallSSH{session: sess}, &fakeInstallNodes{ready: true})
	key := fiKey(fi)

	fiAddFinalizer(t, r, key)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	g := gomega.NewWithT(t)
	g.Eventually(func() string {
		return fiGet(t, c, key).Status.FailedPhase
	}, 2*time.Second, 10*time.Millisecond).Should(gomega.Equal(string(provision.PhaseReady)))

	got := fiGet(t, c, key)
	if got.Status.Phase != string(provision.PhaseFailed) {
		t.Errorf("phase = %q, want Failed -- Ready is unrecoverable and this failure must not hide behind it", got.Status.Phase)
	}
	if !strings.Contains(got.Status.Message, fi.Name+"-kubeconfig") {
		t.Errorf("message %q does not name the kubeconfig Secret", got.Status.Message)
	}
	if got.Status.KubeconfigSecret != "" {
		t.Errorf("kubeconfigSecret = %q, want empty -- the write failed", got.Status.KubeconfigSecret)
	}
}
