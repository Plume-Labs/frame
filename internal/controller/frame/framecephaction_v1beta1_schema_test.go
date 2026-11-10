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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// These specs go through the real apiserver (envtest, suite_test.go): the
// thing under test is the CEL rules and the required spec of
// FrameCephAction and FrameOSDReplacement. A missing or null spec skips
// every rule written on it (a transition rule is not evaluated when the
// field is absent on either side), so the spec itself must be required.
// Ginkgo, not a bare Test: see frameinstall_v1beta1_schema_test.go.
var _ = Describe("FrameCephAction and FrameOSDReplacement v1beta1 schema", func() {
	nullSpec := func(kind, name string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": framev1beta1.GroupVersion.String(),
			"kind":       kind,
			"metadata":   map[string]any{"name": name, "namespace": "default"},
			"spec":       nil,
		}}
		return u
	}

	DescribeTable("refuses an object without a spec",
		func(kind string) {
			err := k8sClient.Create(ctx, nullSpec(kind, "schema-nullspec-"+map[string]string{"FrameCephAction": "fca", "FrameOSDReplacement": "for"}[kind]))
			Expect(err).To(HaveOccurred(), "the apiserver accepted spec: null")
			Expect(err.Error()).To(ContainSubstring("spec"))
		},
		Entry("FrameCephAction", "FrameCephAction"),
		Entry("FrameOSDReplacement", "FrameOSDReplacement"),
	)

	It("refuses an update that nulls the spec of a replacement (it would let an approval in)", func() {
		r := &framev1beta1.FrameOSDReplacement{
			ObjectMeta: metav1.ObjectMeta{Name: "schema-for-update", Namespace: "default"},
			Spec:       framev1beta1.FrameOSDReplacementSpec{OSDID: 8, Serial: "Z2AW5NWA"},
		}
		Expect(k8sClient.Create(ctx, r)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, r) })
		err := k8sClient.Patch(ctx, r, client.RawPatch("application/merge-patch+json", []byte(`{"spec":null}`)))
		Expect(err).To(HaveOccurred(), "the apiserver accepted spec: null on update")
	})

	It("refuses an approval written at creation", func() {
		r := &framev1beta1.FrameOSDReplacement{
			ObjectMeta: metav1.ObjectMeta{Name: "schema-for-early-approval", Namespace: "default"},
			Spec:       framev1beta1.FrameOSDReplacementSpec{OSDID: 8, Serial: "Z2AW5NWA", PurgeApproval: &framev1beta1.SerialApproval{Serial: "Z2AW5NWA"}},
		}
		err := k8sClient.Create(ctx, r)
		Expect(err).To(HaveOccurred(), "the apiserver accepted an approval at creation")
	})

	osd := func(id int32) *int32 { return &id }
	DescribeTable("validates the gesture's shape",
		func(slug string, spec framev1beta1.FrameCephActionSpec, wantErr string) {
			a := &framev1beta1.FrameCephAction{ObjectMeta: metav1.ObjectMeta{Name: "schema-fca-" + slug, Namespace: "default"}, Spec: spec}
			err := k8sClient.Create(ctx, a)
			if wantErr == "" {
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, a) })
				return
			}
			Expect(err).To(HaveOccurred(), "the apiserver accepted it")
			Expect(err.Error()).To(ContainSubstring(wantErr))
		},
		Entry("accepts an out", "out", framev1beta1.FrameCephActionSpec{Action: "OSDOut", OSDID: osd(8)}, ""),
		Entry("accepts a flag for a while", "flag", framev1beta1.FrameCephActionSpec{Action: "SetFlag", Flag: "noout", DurationMinutes: 30}, ""),
		Entry("refuses an out without an OSD", "out-no-osd", framev1beta1.FrameCephActionSpec{Action: "OSDOut"}, "every other action takes an osdID"),
		Entry("refuses a flag with an OSD", "flag-osd", framev1beta1.FrameCephActionSpec{Action: "SetFlag", Flag: "noout", OSDID: osd(8)}, "every other action takes an osdID"),
		Entry("refuses a scrub with a duration", "scrub-duration", framev1beta1.FrameCephActionSpec{Action: "OSDScrub", OSDID: osd(8), DurationMinutes: 30}, "every other action takes an osdID"),
		Entry("refuses a flag left to the command line", "flag-pause", framev1beta1.FrameCephActionSpec{Action: "SetFlag", Flag: "pause"}, "Unsupported value"),
	)

	It("keeps the gesture fixed and the release irreversible", func() {
		a := &framev1beta1.FrameCephAction{
			ObjectMeta: metav1.ObjectMeta{Name: "schema-fca-transitions", Namespace: "default"},
			Spec:       framev1beta1.FrameCephActionSpec{Action: "SetFlag", Flag: "noout"},
		}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, a) })

		changed := a.DeepCopy()
		changed.Spec.Flag = "norebalance"
		err := k8sClient.Update(ctx, changed)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("the gesture is fixed at creation"))

		Expect(k8sClient.Patch(ctx, a, client.RawPatch("application/merge-patch+json", []byte(`{"spec":{"release":true}}`)))).To(Succeed())
		err = k8sClient.Patch(ctx, a, client.RawPatch("application/merge-patch+json", []byte(`{"spec":{"release":false}}`)))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("release cannot be taken back"))
		err = k8sClient.Patch(ctx, a, client.RawPatch("application/merge-patch+json", []byte(`{"spec":null}`)))
		Expect(err).To(HaveOccurred(), "the apiserver accepted spec: null on update")
	})
})
