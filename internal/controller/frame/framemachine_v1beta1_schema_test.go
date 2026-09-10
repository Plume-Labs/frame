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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// These specs go through the real apiserver (k8sClient / envtest, wired in
// suite_test.go), because the thing under test is the CEL rule the apiserver
// compiles — not a Go zero-value fact. A struct-literal assertion would pass
// unchanged if the marker were deleted.
var _ = Describe("FrameMachine v1beta1 schema", func() {
	newMachine := func(name, address string) *framev1beta1.FrameMachine {
		return &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: framev1beta1.FrameMachineSpec{
				BMC: framev1beta1.BMCSpec{
					Address:        address,
					CredentialsRef: "ilo-credentials",
					TLS:            framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
				},
			},
		}
	}

	It("admits a routable IPv4 management address", func() {
		fm := newMachine("fm-ok", "192.168.2.50")
		Expect(k8sClient.Create(context.Background(), fm)).To(Succeed())
		Expect(k8sClient.Delete(context.Background(), fm)).To(Succeed())
	})

	It("refuses a hostname", func() {
		err := k8sClient.Create(context.Background(), newMachine("fm-host", "ilo.example.com"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must be a valid IP address"))
	})

	It("refuses a loopback address, so the controller cannot be aimed at its own pod", func() {
		err := k8sClient.Create(context.Background(), newMachine("fm-loop", "127.0.0.1"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must not be a loopback address"))
	})

	It("refuses a link-local address, where metadata services live", func() {
		err := k8sClient.Create(context.Background(), newMachine("fm-ll", "169.254.169.254"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must not be a link-local address"))
	})

	It("refuses a TLS block that chooses neither verification nor bypass", func() {
		fm := newMachine("fm-tls", "192.168.2.51")
		fm.Spec.BMC.TLS = framev1beta1.BMCTLSSpec{}
		err := k8sClient.Create(context.Background(), fm)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("insecureSkipVerify or caBundleRef"))
	})

	It("accepts a CA bundle as the other way to satisfy the TLS rule", func() {
		fm := newMachine("fm-ca", "192.168.2.52")
		fm.Spec.BMC.TLS = framev1beta1.BMCTLSSpec{CABundleRef: "ilo-ca"}
		Expect(k8sClient.Create(context.Background(), fm)).To(Succeed())
		Expect(k8sClient.Delete(context.Background(), fm)).To(Succeed())
	})

	It("caps the retained event log at 25 entries", func() {
		fm := newMachine("fm-log", "192.168.2.53")
		Expect(k8sClient.Create(context.Background(), fm)).To(Succeed())
		entries := make([]framev1beta1.EventLogEntry, 0, 26)
		for i := 0; i < 26; i++ {
			entries = append(entries, framev1beta1.EventLogEntry{ID: "e", Severity: "OK", Message: "m"})
		}
		fm.Status.EventLog = entries
		err := k8sClient.Status().Update(context.Background(), fm)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must have at most 25 items"))
		Expect(k8sClient.Delete(context.Background(), fm)).To(Succeed())
	})
})
