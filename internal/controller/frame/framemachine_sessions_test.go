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
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/gothshoot/frame/api/frame/v1beta1"
	"github.com/gothshoot/frame/internal/redfish"
	"github.com/gothshoot/frame/internal/redfish/redfishtest"
)

// Observed on the real ML350 Gen9 (iLO4 2.77): the controller authenticated
// every request of every probe with Basic auth, which the iLO logs as a
// login and a logout each — enough to wrap its event log in about three
// hours and erase an incident's traces. These specs drive the production
// client builder (BuildRedfishClient) against a fake BMC that counts
// sessions.
var _ = Describe("FrameMachine Redfish sessions", func() {
	const probes = 5

	// The CRD accepts only a routable IP in spec.bmc.address, and the fake
	// BMC listens on loopback: each fake gets a documentation address
	// (RFC 5737), which NewClient maps back to the fake's host:port before
	// handing the spec to the production builder.
	var hosts map[string]string
	addressOf := func(bmc *redfishtest.FakeBMC) string {
		addr := fmt.Sprintf("192.0.2.%d", 100+len(hosts))
		hosts[addr] = strings.TrimPrefix(bmc.URL(), "https://")
		return addr
	}
	BeforeEach(func() { hosts = map[string]string{} })

	machineOn := func(name string, bmc *redfishtest.FakeBMC) *framev1beta1.FrameMachine {
		return &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: framev1beta1.FrameMachineSpec{BMC: framev1beta1.BMCSpec{
				Address:        addressOf(bmc),
				CredentialsRef: name + "-creds",
				// httptest's certificate is self-signed.
				TLS: framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
			}},
		}
	}
	createMachine := func(name string, bmc *redfishtest.FakeBMC) ctrl.Request {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-creds", Namespace: "default"},
			Data: map[string][]byte{
				"username": []byte(redfishtest.Username),
				"password": []byte(redfishtest.Password),
			},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, machineOn(name, bmc))).To(Succeed())
		return ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}}
	}
	newReconciler := func() *FrameMachineReconciler {
		return &FrameMachineReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(32),
			NewClient: func(ctx context.Context, kube client.Client, ns string, bmc framev1beta1.BMCSpec) (redfish.Client, error) {
				bmc.Address = hosts[bmc.Address]
				return BuildRedfishClient(ctx, kube, ns, bmc)
			},
		}
	}

	It("creates one session over many probes, and ends it when the machine is deleted", func() {
		bmc := redfishtest.NewFakeBMC(GinkgoT())
		req := createMachine("fm-session-reuse", bmc)
		r := newReconciler()

		for range probes {
			res, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(DefaultProbeInterval))
		}
		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		Expect(got.Status.LastProbeAt).NotTo(BeNil(), "the probes must actually have reached the fake")
		Expect(bmc.Created()).To(Equal(1), "sessions created over %d probes", probes)
		Expect(bmc.BasicRequests()).To(BeZero(), "requests the iLO would log as a login and a logout each")

		Expect(k8sClient.Delete(ctx, &got)).To(Succeed())
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(bmc.Deleted()).To(Equal(1))
		Expect(bmc.Live()).To(BeZero())
	})

	It("ends the old session when the machine's BMC changes", func() {
		oldBMC, newBMC := redfishtest.NewFakeBMC(GinkgoT()), redfishtest.NewFakeBMC(GinkgoT())
		req := createMachine("fm-session-move", oldBMC)
		r := newReconciler()
		for range 2 {
			_, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		base := got.DeepCopy()
		got.Spec.BMC.Address = addressOf(newBMC)
		Expect(k8sClient.Patch(ctx, &got, client.MergeFrom(base))).To(Succeed())
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(oldBMC.Created()).To(Equal(1))
		Expect(oldBMC.Live()).To(BeZero(), "the replaced client's session must be deleted, not left to time out")
		Expect(newBMC.Created()).To(Equal(1))
	})

	It("ends every session when the manager stops", func() {
		bmc := redfishtest.NewFakeBMC(GinkgoT())
		req := createMachine("fm-session-stop", bmc)
		r := newReconciler()
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(bmc.Live()).To(Equal(1))

		mctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- r.sessionLogoutOnStop().Start(mctx) }()
		Consistently(bmc.Live, 200*time.Millisecond, 20*time.Millisecond).Should(Equal(1),
			"sessions stay open while the manager runs")
		cancel()
		Eventually(done, 5*time.Second).Should(Receive(BeNil()))
		Expect(bmc.Live()).To(BeZero())
	})

	It("spaces probes by the interval, never under 60 s, and backs off on failure", func() {
		fake := &fakeRedfish{snapshot: &redfish.Snapshot{PowerState: "On"}}
		Expect(k8sClient.Create(ctx, &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "fm-cadence", Namespace: "default"},
			Spec: framev1beta1.FrameMachineSpec{BMC: framev1beta1.BMCSpec{
				Address: "192.0.2.10", CredentialsRef: "unused-the-client-is-faked",
				TLS: framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
			}},
		})).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-cadence", Namespace: "default"}}
		r := &FrameMachineReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(32),
			NewClient: func(context.Context, client.Client, string, framev1beta1.BMCSpec) (redfish.Client, error) {
				return fake, nil
			},
		}
		next := func() time.Duration {
			res, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			return res.RequeueAfter
		}

		Expect(next()).To(Equal(60*time.Second), "unset: the default")
		r.ProbeInterval = 10 * time.Second
		Expect(next()).To(Equal(60*time.Second), "under the floor: raised to it")
		r.ProbeInterval = 90 * time.Second
		Expect(next()).To(Equal(90*time.Second), "over the floor: as configured")

		fake.probeErr = redfish.ErrTLS
		backoff := make([]time.Duration, 0, 5)
		for range 5 {
			backoff = append(backoff, next())
		}
		Expect(backoff).To(Equal([]time.Duration{
			5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 30 * time.Minute,
		}))

		fake.probeErr = nil
		Expect(next()).To(Equal(90*time.Second), "one success returns to the interval")
		fake.probeErr = redfish.ErrTLS
		Expect(next()).To(Equal(5*time.Minute), "and resets the backoff")
	})
})
