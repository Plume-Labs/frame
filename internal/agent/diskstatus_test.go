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

package agent

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// These are plain Go tests, not Ginkgo, and use a fake client rather than
// envtest's k8sClient — the real cross-writer race that matters for this
// function is proved against a real API server in
// internal/controller/frame/framemachine_controller_test.go (see that
// file's "l'agent ecrit ses disques sans effacer l'inventaire BMC" and
// "refuse d'ecrire quand aucune FrameMachine ne nomme ce noeud" specs). What
// belongs here is the logic a fake client can exercise honestly: which
// machine gets picked, and what happens when none does.
func newDiskStatusScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := clientgoscheme.Scheme
	if err := framev1beta1.AddToScheme(s); err != nil {
		t.Fatalf("registering scheme: %v", err)
	}
	return s
}

func TestPatchObservedDisksErrorsWhenNoMachineClaimsTheNode(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(newDiskStatusScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameMachine{}).
		Build()

	err := PatchObservedDisks(context.Background(), c, "node-nobody-claims",
		[]framev1beta1.ObservedDisk{{Path: "/dev/sda", SerialNumber: "X1", SizeGB: 1, Occupancy: "free"}})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "no FrameMachine") {
		t.Errorf("error = %q, want it to mention no FrameMachine", err.Error())
	}
}

func TestPatchObservedDisksWritesStorageOnTheClaimingMachine(t *testing.T) {
	m := &framev1beta1.FrameMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "m1", Namespace: "default"},
		Spec: framev1beta1.FrameMachineSpec{
			BMC:     framev1beta1.BMCSpec{Address: "192.168.2.60", CredentialsRef: "m1-creds", TLS: framev1beta1.BMCTLSSpec{InsecureSkipVerify: true}},
			NodeRef: "node-1",
		},
	}
	other := &framev1beta1.FrameMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "m2", Namespace: "default"},
		Spec: framev1beta1.FrameMachineSpec{
			BMC:     framev1beta1.BMCSpec{Address: "192.168.2.61", CredentialsRef: "m2-creds", TLS: framev1beta1.BMCTLSSpec{InsecureSkipVerify: true}},
			NodeRef: "node-2",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(newDiskStatusScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameMachine{}).
		WithObjects(m, other).
		Build()

	disks := []framev1beta1.ObservedDisk{{Path: "/dev/sdc", SerialNumber: "KZK245ZG", SizeGB: 1200, Occupancy: "free"}}
	if err := PatchObservedDisks(context.Background(), c, "node-1", disks); err != nil {
		t.Fatalf("PatchObservedDisks: %v", err)
	}

	var got framev1beta1.FrameMachine
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(m), &got); err != nil {
		t.Fatalf("Get m1: %v", err)
	}
	if got.Status.Storage == nil {
		t.Fatal("Status.Storage is nil")
	}
	if len(got.Status.Storage.Observed) != 1 || got.Status.Storage.Observed[0].SerialNumber != "KZK245ZG" {
		t.Errorf("Observed = %+v, want the one disk passed in", got.Status.Storage.Observed)
	}
	// No BMC inventory was ever written, so the join reports the observed
	// disk as os-only rather than silently agreeing.
	if len(got.Status.Storage.Divergences) != 1 || got.Status.Storage.Divergences[0].Reason != "os-only" {
		t.Errorf("Divergences = %+v, want one os-only divergence", got.Status.Storage.Divergences)
	}
	if got.Status.Storage.ObservedAt == nil {
		t.Error("ObservedAt is nil")
	}

	var untouched framev1beta1.FrameMachine
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(other), &untouched); err != nil {
		t.Fatalf("Get m2: %v", err)
	}
	if untouched.Status.Storage != nil {
		t.Errorf("the machine that does not claim node-1 was written too: %+v", untouched.Status.Storage)
	}
}
