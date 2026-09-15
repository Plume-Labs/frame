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

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// Schema only: both kinds are post-freeze, v1beta1-only, without conversion.
// Table form for the same reason as frametask_v1beta1_schema_test.go — a
// top-level Test would run before BeforeSuite populates k8sClient.
var _ = Describe("FrameAlert v1beta1 schema", func() {
	now := metav1.Now()

	DescribeTable("accepts only a well-formed fingerprint and state",
		func(fp, state string, wantErr bool) {
			obj := &framev1beta1.FrameAlert{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "fa-", Namespace: "default"},
				Spec:       framev1beta1.FrameAlertSpec{Fingerprint: fp, AlertName: "X", StartsAt: now},
			}
			err := k8sClient.Create(ctx, obj)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
				if state != "" {
					obj.Status.State = state
					err = k8sClient.Status().Update(ctx, obj)
				}
			}
			if wantErr {
				Expect(err).To(HaveOccurred())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("hex fingerprint", "a1b2c3d4e5f60718", "Firing", false),
		Entry("uppercase fingerprint", "A1B2", "", true),
		Entry("empty fingerprint", "", "", true),
		Entry("unknown state", "a1b2", "Pending", true),
	)
})

var _ = Describe("FrameAlertSubscription v1beta1 schema", func() {
	It("defaults excludeAlertNames to the two meta alerts when filter is omitted", func() {
		obj := &framev1beta1.FrameAlertSubscription{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "sub-", Namespace: "default"},
			Spec: framev1beta1.FrameAlertSubscriptionSpec{
				URL:            "http://tenant.example/webhook",
				TokenSecretRef: framev1beta1.AlertTokenRef{Name: "tok", Key: "token"},
			},
		}
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
		Expect(obj.Spec.Filter.ExcludeAlertNames).To(Equal([]string{"Watchdog", "InfoInhibitor"}))
	})

	DescribeTable("rejects a URL that is not http(s)",
		func(url string, wantErr bool) {
			obj := &framev1beta1.FrameAlertSubscription{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "sub-", Namespace: "default"},
				Spec: framev1beta1.FrameAlertSubscriptionSpec{
					URL:            url,
					TokenSecretRef: framev1beta1.AlertTokenRef{Name: "tok", Key: "token"},
				},
			}
			err := k8sClient.Create(ctx, obj)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
			}
			if wantErr {
				Expect(err).To(HaveOccurred())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("http", "http://neura.svc:3000/api/it/alerts/webhook", false),
		Entry("https", "https://tenant.example/hook", false),
		Entry("file scheme", "file:///etc/passwd", true),
		Entry("empty", "", true),
	)
})
