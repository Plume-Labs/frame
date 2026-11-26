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
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// The rules under test are the CEL on FrameUpdate and the patterns on
// FrameRelease (docs/specs/2026-11-24-mise-a-jour-en-un-clic.md §3.3). They run
// against the real apiserver (envtest, suite_test.go): a request that names no
// target, or both a target and a rollback, must never reach the updater.
var _ = Describe("FrameUpdate and FrameRelease v1beta1 schema", func() {
	DescribeTable("validates the shape of an update request",
		func(slug string, spec framev1beta1.FrameUpdateSpec, wantErr string) {
			u := &framev1beta1.FrameUpdate{ObjectMeta: metav1.ObjectMeta{Name: "schema-fupd-" + slug, Namespace: "default"}, Spec: spec}
			err := k8sClient.Create(ctx, u)
			if wantErr == "" {
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })
				return
			}
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(wantErr))
		},
		Entry("a target version", "target", framev1beta1.FrameUpdateSpec{TargetVersion: "1.2.0"}, ""),
		Entry("a pre-release target", "pre", framev1beta1.FrameUpdateSpec{TargetVersion: "0.9.0-beta.1"}, ""),
		Entry("a rollback", "rollback", framev1beta1.FrameUpdateSpec{Rollback: true}, ""),
		Entry("neither a target nor a rollback", "neither", framev1beta1.FrameUpdateSpec{}, "not both and not neither"),
		Entry("both a target and a rollback", "both", framev1beta1.FrameUpdateSpec{TargetVersion: "1.2.0", Rollback: true}, "not both and not neither"),
		Entry("a target that is not a version", "badver", framev1beta1.FrameUpdateSpec{TargetVersion: "latest"}, "targetVersion"),
	)

	It("refuses to change a request after creation", func() {
		u := &framev1beta1.FrameUpdate{
			ObjectMeta: metav1.ObjectMeta{Name: "schema-fupd-fixed", Namespace: "default"},
			Spec:       framev1beta1.FrameUpdateSpec{TargetVersion: "1.2.0"},
		}
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, u) })
		err := k8sClient.Patch(ctx, u, client.RawPatch("application/merge-patch+json", []byte(`{"spec":{"targetVersion":"9.9.9"}}`)))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("fixed at creation"))
	})

	It("refuses a release image that is not given as a string map", func() {
		r := &framev1beta1.FrameRelease{
			ObjectMeta: metav1.ObjectMeta{Name: "schema-frel-noimages", Namespace: "default"},
			Spec:       framev1beta1.FrameReleaseSpec{Version: "1.2.0", Channel: "stable"},
		}
		Expect(k8sClient.Create(ctx, r)).NotTo(Succeed())
	})

	It("refuses a release on an unknown channel", func() {
		r := &framev1beta1.FrameRelease{
			ObjectMeta: metav1.ObjectMeta{Name: "schema-frel-channel", Namespace: "default"},
			Spec:       framev1beta1.FrameReleaseSpec{Version: "1.2.0", Channel: "nightly", Images: map[string]string{"ui": "x"}},
		}
		Expect(k8sClient.Create(ctx, r)).NotTo(Succeed())
	})
})
