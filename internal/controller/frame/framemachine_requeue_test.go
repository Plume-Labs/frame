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
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	"github.com/Plume-Labs/frame/internal/redfish"
)

// countingRedfish counts probes. The fake in framemachine_fake_test.go is
// driven by direct Reconcile calls from one goroutine; this one is probed by
// a running manager, so its counter is atomic.
type countingRedfish struct {
	fakeRedfish
	probes atomic.Int32
}

func (c *countingRedfish) Probe(ctx context.Context) (*redfish.Snapshot, error) {
	c.probes.Add(1)
	return c.fakeRedfish.Probe(ctx)
}

// The other FrameMachine specs call Reconcile by hand, which cannot see what
// triggers a reconcile — and that is where the defect was. Observed on the
// real ML350 Gen9: the controller logged "Probed FrameMachine" every ~13 s,
// the duration of one Redfish walk, instead of every 60 s. Each probe's own
// status write came back as a watch event and started the next probe. This
// spec runs the controller under a real manager and counts probes.
var _ = Describe("FrameMachine controller under a manager", func() {
	It("probes once per interval, not once per status write, and still wakes on a spec change", func() {
		const ns = "fm-requeue"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())

		fake := &countingRedfish{fakeRedfish: fakeRedfish{snapshot: &redfish.Snapshot{
			PowerState:         "On",
			PostState:          "FinishedPost",
			SensorsTrustworthy: true,
			Inventory:          redfish.Inventory{Model: "ProLiant ML350 Gen9", TotalMemoryGiB: 80},
		}}}

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:  scheme.Scheme,
			Metrics: metricsserver.Options{BindAddress: "0"},
			// Only this spec's namespace: the other specs leave FrameMachines
			// in "default", and they must neither be probed by this fake nor
			// counted.
			Cache:      cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}},
			Controller: config.Controller{SkipNameValidation: new(true)},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect((&FrameMachineReconciler{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			Recorder: record.NewFakeRecorder(32),
			NewClient: func(context.Context, client.Client, string, framev1beta1.BMCSpec) (redfish.Client, error) {
				return fake, nil
			},
		}).SetupWithManager(mgr)).To(Succeed())

		mctx, mcancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			Expect(mgr.Start(mctx)).To(Succeed())
		}()
		DeferCleanup(func() { mcancel(); wg.Wait() })

		fm := &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "fm-requeue", Namespace: ns},
			Spec: framev1beta1.FrameMachineSpec{BMC: framev1beta1.BMCSpec{
				Address:        "192.0.2.10",
				CredentialsRef: "unused-the-client-is-faked",
				TLS:            framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
			}},
		}
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())
		key := types.NamespacedName{Name: fm.Name, Namespace: ns}

		// The create is the first probe, and it writes status.
		Eventually(fake.probes.Load, 10*time.Second, 50*time.Millisecond).Should(BeNumerically(">=", 1))
		Eventually(func(g Gomega) {
			var got framev1beta1.FrameMachine
			g.Expect(k8sClient.Get(ctx, key, &got)).To(Succeed())
			g.Expect(got.Status.LastProbeAt).NotTo(BeNil())
		}, 10*time.Second, 50*time.Millisecond).Should(Succeed())

		// A status write from someone else — the agent's status.storage, in
		// production — must not wake it either.
		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, key, &got)).To(Succeed())
		base := got.DeepCopy()
		got.Status.Storage = &framev1beta1.MachineStorage{ObservedAt: new(metav1.Now())}
		Expect(k8sClient.Status().Patch(ctx, &got, client.MergeFrom(base))).To(Succeed())

		// The next probe is due in 60 s. Before the fix this reached 4-5
		// within the window: every status write (lastProbeAt has one-second
		// resolution) bounced straight back into a probe.
		Consistently(fake.probes.Load, 4*time.Second, 100*time.Millisecond).Should(BeNumerically("==", 1))

		// The filter must not be "ignore everything": a spec change bumps the
		// generation, and a power request made from the console has to act now,
		// not at the next interval.
		Expect(k8sClient.Get(ctx, key, &got)).To(Succeed())
		base = got.DeepCopy()
		got.Spec.NodeRef = "some-node"
		Expect(k8sClient.Patch(ctx, &got, client.MergeFrom(base))).To(Succeed())
		Eventually(fake.probes.Load, 10*time.Second, 50*time.Millisecond).Should(BeNumerically("==", 2))
	})
})
