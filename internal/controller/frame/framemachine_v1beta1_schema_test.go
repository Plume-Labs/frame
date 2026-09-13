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
	"sigs.k8s.io/controller-runtime/pkg/client"

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

	It("garde le numero de serie, la baie et les motifs d'etat d'un disque", func(ctx SpecContext) {
		m := newMachine("drive-fields", "192.168.2.60")
		Expect(k8sClient.Create(ctx, m)).To(Succeed())

		m.Status.Inventory = &framev1beta1.MachineInventory{
			Drives: []framev1beta1.DriveInfo{{
				Name:          "2I:6:8",
				Model:         "MM1000GFJTE",
				SizeGB:        1000,
				Protocol:      "SATA",
				Health:        "OK",
				SerialNumber:  "W4722RRA",
				Location:      "2I:6:8",
				MediaType:     "HDD",
				StatusReasons: []string{"None"},
			}},
		}
		Expect(k8sClient.Status().Update(ctx, m)).To(Succeed())

		var back framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(m), &back)).To(Succeed())
		Expect(back.Status.Inventory.Drives).To(HaveLen(1))
		d := back.Status.Inventory.Drives[0]
		// The serial is the join key: a CRD that drops it silently makes
		// every divergence in internal/storage.Join read "os-only".
		Expect(d.SerialNumber).To(Equal("W4722RRA"))
		Expect(d.Location).To(Equal("2I:6:8"))
		Expect(d.MediaType).To(Equal("HDD"))
		Expect(d.StatusReasons).To(Equal([]string{"None"}))

		Expect(k8sClient.Delete(ctx, m)).To(Succeed())
	})
})
