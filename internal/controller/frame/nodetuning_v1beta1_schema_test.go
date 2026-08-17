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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// These specs are about the v1beta1 *schema*, not a Go zero-value fact: they
// go through the real apiserver (k8sClient / envtest, wired up in
// suite_test.go) so that a `+kubebuilder:default` marker regression is
// something the suite can actually see. See the sibling *_v1beta1_schema_test.go
// files for the same pattern.
//
// Page merging across tenants is a side channel (see KSMSpec's doc comment),
// so "KSM defaults off" has to mean the schema, not a struct literal: a Go
// zero-value NodeTuning has a nil *KSMSpec regardless of what
// +kubebuilder:default says, so asserting on that alone would pass unchanged
// even if the marker were flipped to `default=true` or deleted outright.
var _ = Describe("NodeTuning v1beta1 schema", func() {
	It("leaves spec.ksm nil when the spec says nothing about KSM at all", func() {
		nt := &framev1beta1.NodeTuning{
			ObjectMeta: metav1.ObjectMeta{Name: "ksm-untouched"},
		}
		Expect(k8sClient.Create(ctx, nt)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, nt) })

		back := &framev1beta1.NodeTuning{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "ksm-untouched"}, back)).To(Succeed())
		Expect(back.Spec.KSM).To(BeNil(), "an unset ksm field must stay absent, not get defaulted into an object")
	})

	It("defaults ksm.enabled to false when ksm is declared but enabled is omitted from the wire", func() {
		// A typed client can't exercise this: KSMSpec.Enabled has no
		// `omitempty`, so encoding/json always puts an explicit
		// "enabled": false on the wire for a Go zero-value KSMSpec{},
		// which would satisfy this assertion whatever the schema default
		// said (an explicit value on the wire always wins over a CRD
		// default). Only a request that omits the "enabled" key entirely
		// exercises the `+kubebuilder:default=false` marker, hence the
		// unstructured object with `ksm: {}` and no "enabled" key.
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("NodeTuning"))
		raw.SetName("ksm-enabled-defaulted")
		Expect(unstructured.SetNestedMap(raw.Object, map[string]any{
			"ksm": map[string]any{},
		}, "spec")).To(Succeed())

		Expect(k8sClient.Create(ctx, raw)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })

		back := &framev1beta1.NodeTuning{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "ksm-enabled-defaulted"}, back)).To(Succeed())
		Expect(back.Spec.KSM).NotTo(BeNil())
		Expect(back.Spec.KSM.Enabled).To(BeFalse(),
			"spec.ksm.enabled must default to false — page merging across tenants is a side channel and must stay opt-in")
	})
})

func TestNodeTuningPhaseConstantsAreDistinct(t *testing.T) {
	seen := map[framev1beta1.NodeTuningPhase]bool{}
	for _, p := range []framev1beta1.NodeTuningPhase{
		framev1beta1.PhaseInSync, framev1beta1.PhaseDrifted,
		framev1beta1.PhaseRebootPending, framev1beta1.PhaseApplying,
		framev1beta1.PhaseFailed,
	} {
		if seen[p] {
			t.Fatalf("duplicate phase constant %q", p)
		}
		seen[p] = true
	}
}
