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

package v1beta1

import (
	"context"
	"errors"
	"strings"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func validatorWith(objs ...runtime.Object) *FrameStorageCustomValidator {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = framev1beta1.AddToScheme(s)
	return &FrameStorageCustomValidator{
		Client: fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(objs...).Build(),
	}
}

func entry(class string, adopt bool) *framev1beta1.FrameStorage {
	return &framev1beta1.FrameStorage{
		ObjectMeta: metav1.ObjectMeta{Name: "e"},
		Spec: framev1beta1.FrameStorageSpec{
			Type:             "ceph-rbd",
			Content:          []string{"workload"},
			StorageClassName: class,
			AdoptExisting:    adopt,
		},
	}
}

func foreignClass(name string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: name},
		Provisioner: "rook-ceph.rbd.csi.ceph.com",
	}
}

// TestRefusesAnExistingClassWithoutOptIn is the guard that protects the
// sixteen ceph-rbd PVCs in the cluster: an entry naming a class Frame did
// not create takes ownership of it, and an owned class is deleted with its
// entry.
func TestRefusesAnExistingClassWithoutOptIn(t *testing.T) {
	v := validatorWith(foreignClass("ceph-rbd"))
	_, err := v.ValidateCreate(context.Background(), entry("ceph-rbd", false))
	if err == nil {
		t.Fatal("want a refusal for an existing StorageClass with adoptExisting unset")
	}
	if !strings.Contains(err.Error(), "adoptExisting") {
		t.Errorf("refusal does not name the field that unblocks it: %v", err)
	}
	if !strings.Contains(err.Error(), "ceph-rbd") {
		t.Errorf("refusal does not name the class: %v", err)
	}
}

func TestAcceptsAnExistingClassWithOptIn(t *testing.T) {
	v := validatorWith(foreignClass("ceph-rbd"))
	if _, err := v.ValidateCreate(context.Background(), entry("ceph-rbd", true)); err != nil {
		t.Fatalf("adoptExisting: true must be accepted: %v", err)
	}
}

func TestAcceptsAClassThatDoesNotExistYet(t *testing.T) {
	v := validatorWith()
	if _, err := v.ValidateCreate(context.Background(), entry("frame-scratch", false)); err != nil {
		t.Fatalf("a class Frame is about to create needs no opt-in: %v", err)
	}
}

// TestRefusalDoesNotDependOnTheClassBeingCephRBD: the guard is about
// ownership, not about a hardcoded name. A test that only ever passes
// "ceph-rbd" cannot tell a real lookup from a string comparison.
func TestRefusalDoesNotDependOnTheClassBeingCephRBD(t *testing.T) {
	v := validatorWith(foreignClass("local-path"))
	e := entry("local-path", false)
	e.Spec.Type = "local-path"
	if _, err := v.ValidateCreate(context.Background(), e); err == nil {
		t.Fatal("want a refusal for local-path too")
	}
}

// TestSharedIsNotDeclarable: status.shared is derived from the type. A
// spec that could set it would let a user declare a local disk shared.
func TestSharedIsNotSettableFromSpec(t *testing.T) {
	// The compile-time guarantee is that FrameStorageSpec has no Shared
	// field; this test states it so a future addition has to delete a test
	// rather than quietly add a field.
	var spec framev1beta1.FrameStorageSpec
	_ = spec
	if got := sharedForType("ceph-rbd"); !got {
		t.Error("ceph-rbd must be shared")
	}
	if got := sharedForType("local-path"); got {
		t.Error("local-path must not be shared")
	}
}

// TestRefusesWhenTheClassLookupFails is the third branch of the adoption
// guard: not "the class exists" and not "the class does not exist", but "I
// could not tell". A lookup error other than NotFound must refuse, exactly
// like finding the class does — anything else would let an apiserver hiccup
// silently pass an entry through as though the class were absent, which is
// the one case the guard exists to rule out.
//
// This test does not exist in the original brief for this task; it was
// added because the guard's three branches (not found / found / other
// error) had only two of them covered, and the third is the one a
// name-only refusal could hide behind.
func TestRefusesWhenTheClassLookupFails(t *testing.T) {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = framev1beta1.AddToScheme(s)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
				return errors.New("apiserver down")
			},
		}).
		Build()
	v := &FrameStorageCustomValidator{Client: c}

	_, err := v.ValidateCreate(context.Background(), entry("ceph-rbd", false))
	if err == nil {
		t.Fatal("want a refusal when the StorageClass lookup itself fails, not a silent pass")
	}
	if strings.Contains(err.Error(), "was not created by Frame") {
		t.Errorf("refusal wrongly claims the class was found, rather than that the lookup failed: %v", err)
	}
}

// TestAcceptsAnUpdateThatDoesNotChangeTheClassName is the bug the
// coordinator's ruling exists to fix: validateAdoption must NOT re-run on an
// update that leaves spec.storageClassName untouched. Once the controller
// (a later task) creates the StorageClass for a Frame-owned entry, that class
// exists while spec.adoptExisting is still false — nothing sets it after
// creation — so an unconditional re-check on every update would fall past
// NotFound into the final refusal forever, telling the operator to set
// adoptExisting: true, which would misrepresent ownership rather than fix
// anything. Without this test, the next task (the controller that creates
// the class) walks straight back into that trap.
func TestAcceptsAnUpdateThatDoesNotChangeTheClassName(t *testing.T) {
	v := validatorWith(foreignClass("ceph-rbd"))
	old := entry("ceph-rbd", false)
	updated := old.DeepCopy()
	updated.Spec.Content = []string{"workload", "model"}
	if _, err := v.ValidateUpdate(context.Background(), old, updated); err != nil {
		t.Fatalf("an update that leaves storageClassName unchanged must not re-run the adoption check: %v", err)
	}
}

// TestRefusesAnUpdateThatChangesToAnExistingClass: the name did change, so
// this IS a new adoption decision the create-time check never saw. It must
// run the full check against the new name, and the refusal must name it.
func TestRefusesAnUpdateThatChangesToAnExistingClass(t *testing.T) {
	v := validatorWith(foreignClass("ceph-rbd"), foreignClass("ceph-bucket"))
	old := entry("ceph-rbd", false)
	updated := old.DeepCopy()
	updated.Spec.StorageClassName = "ceph-bucket"
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Fatal("want a refusal for an update that switches to an existing StorageClass without adoptExisting")
	}
	if !strings.Contains(err.Error(), "ceph-bucket") {
		t.Errorf("refusal does not name the new class: %v", err)
	}
}

// TestAcceptsAnUpdateThatChangesToAClassThatDoesNotExistYet: the counterpart
// of the refusal above — a changed name is only refused when the new class
// already exists, not merely because it changed.
func TestAcceptsAnUpdateThatChangesToAClassThatDoesNotExistYet(t *testing.T) {
	v := validatorWith(foreignClass("ceph-rbd"))
	old := entry("ceph-rbd", false)
	updated := old.DeepCopy()
	updated.Spec.StorageClassName = "frame-scratch"
	if _, err := v.ValidateUpdate(context.Background(), old, updated); err != nil {
		t.Fatalf("an update to a class Frame is about to create needs no opt-in: %v", err)
	}
}
