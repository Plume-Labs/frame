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
	"errors"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/redfish"
)

// fakeRedfish is the seam the whole controller is tested through: no network,
// no fixtures, just the two outcomes the controller has to tell apart.
type fakeRedfish struct {
	snapshot *redfish.Snapshot
	probeErr error

	resets   []string
	clearLog int
	ledCalls []bool
}

func (f *fakeRedfish) Probe(context.Context) (*redfish.Snapshot, error) {
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	return f.snapshot, nil
}
func (f *fakeRedfish) Reset(_ context.Context, t string) error {
	f.resets = append(f.resets, t)
	return nil
}
func (f *fakeRedfish) ClearLog(context.Context) error { f.clearLog++; return nil }
func (f *fakeRedfish) SetIndicatorLED(_ context.Context, on bool) error {
	f.ledCalls = append(f.ledCalls, on)
	return nil
}

// fakeTimeoutError satisfies net.Error without pulling in a real network
// timeout, so probeFailureReason's net.Error branch can be driven
// deterministically. net.Error still requires the deprecated Temporary()
// method as of this Go version — omitting it makes errors.As silently
// return false rather than fail to compile, since the assertion happens by
// reflection against the interface, not at compile time.
type fakeTimeoutError struct{}

func (fakeTimeoutError) Error() string   { return "i/o timeout" }
func (fakeTimeoutError) Timeout() bool   { return true }
func (fakeTimeoutError) Temporary() bool { return true }

var _ = Describe("FrameMachine controller", func() {
	var (
		fake *fakeRedfish
		r    *FrameMachineReconciler
	)

	newSecret := func(name string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			StringData: map[string]string{"username": "admin", "password": "secret"},
		}
	}

	newMachine := func(name string) *framev1beta1.FrameMachine {
		return &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: framev1beta1.FrameMachineSpec{
				BMC: framev1beta1.BMCSpec{
					Address:        "192.168.2.60",
					CredentialsRef: name + "-creds",
					TLS:            framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
				},
			},
		}
	}

	BeforeEach(func() {
		fake = &fakeRedfish{snapshot: &redfish.Snapshot{
			PowerState:   "On",
			IndicatorLED: "Off",
			Inventory: redfish.Inventory{
				Model:          "ProLiant ML350 Gen9",
				SerialNumber:   "CZJ1234567",
				TotalMemoryGiB: 64,
			},
			Sensors: redfish.Sensors{
				Temperatures:       []redfish.Temperature{{Name: "01-Inlet Ambient", Celsius: 22, Health: "OK"}},
				PowerConsumedWatts: 118,
			},
			Log:       []redfish.LogEntry{{ID: "3", Severity: "Critical", Message: "Fan 3 Failed"}},
			LogTotal:  3,
			LogCounts: map[string]int{"Critical": 1, "Warning": 1, "OK": 1},
		}}
		r = &FrameMachineReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
			// Task 5's power path emits Events. A nil Recorder panics there,
			// and the panic would surface three tasks after the omission.
			Recorder: record.NewFakeRecorder(32),
			NewClient: func(context.Context, client.Client, string, framev1beta1.BMCSpec) (redfish.Client, error) {
				return fake, nil
			},
		}
	})

	It("writes what it read, with the time it read it", func() {
		// Named fm-probed, not fm-ok: framemachine_v1beta1_schema_test.go
		// creates and deletes its own "fm-ok" in this same "default"
		// namespace, and this spec's object is never deleted (a controller
		// spec wants it to persist so Reconcile can read/write status). Two
		// specs both claiming "fm-ok" is a real, seed-dependent collision —
		// AlreadyExists whenever ginkgo's random order runs this spec first —
		// not a hypothetical one; a different name is the whole fix.
		Expect(k8sClient.Create(ctx, newSecret("fm-probed-creds"))).To(Succeed())
		fm := newMachine("fm-probed")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())

		res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-probed", Namespace: "default"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(60 * time.Second))

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "fm-probed", Namespace: "default"}, &got)).To(Succeed())
		Expect(got.Status.PowerState).To(Equal("On"))
		Expect(got.Status.Inventory).NotTo(BeNil())
		Expect(got.Status.Inventory.SerialNumber).To(Equal("CZJ1234567"))
		Expect(got.Status.Sensors.PowerConsumedWatts).To(BeNumerically("==", 118))
		Expect(got.Status.EventLogTotal).To(BeNumerically("==", 3))
		Expect(got.Status.EventLogCounts).To(HaveKeyWithValue("Critical", int32(1)))
		Expect(got.Status.LastProbeAt).NotTo(BeNil())
		Expect(meta.IsStatusConditionTrue(got.Status.Conditions, "Reachable")).To(BeTrue())
	})

	It("keeps the last reading when a probe fails, and says why", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-flap-creds"))).To(Succeed())
		fm := newMachine("fm-flap")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-flap", Namespace: "default"}}

		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		fake.probeErr = redfish.ErrTLS
		res, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(5 * time.Minute))

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		cond := meta.FindStatusCondition(got.Status.Conditions, "Reachable")
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("TLSError"))
		// The last good reading survives, because the screen shows it beside
		// its age rather than showing nothing.
		Expect(got.Status.Inventory).NotTo(BeNil())
		Expect(got.Status.Inventory.SerialNumber).To(Equal("CZJ1234567"))
	})

	It("distinguishes an authentication failure from a TLS one", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-auth-creds"))).To(Succeed())
		Expect(k8sClient.Create(ctx, newMachine("fm-auth"))).To(Succeed())
		fake.probeErr = redfish.ErrAuth

		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-auth", Namespace: "default"}}
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		Expect(meta.FindStatusCondition(got.Status.Conditions, "Reachable").Reason).To(Equal("AuthFailed"))
	})

	It("reports a missing credentials Secret without contacting anything", func() {
		Expect(k8sClient.Create(ctx, newMachine("fm-nosecret"))).To(Succeed())
		r.NewClient = func(context.Context, client.Client, string, framev1beta1.BMCSpec) (redfish.Client, error) {
			return nil, errors.New("secrets \"fm-nosecret-creds\" not found")
		}

		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-nosecret", Namespace: "default"}}
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		cond := meta.FindStatusCondition(got.Status.Conditions, "Reachable")
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("CredentialsUnavailable"))
	})

	It("truncates the retained log to the schema's cap, keeping the newest entries", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-long-creds"))).To(Succeed())
		Expect(k8sClient.Create(ctx, newMachine("fm-long"))).To(Succeed())
		// internal/redfish hands the controller Log sorted newest-first
		// (decode.go's readLog sorts by Created.After), so index 0 here
		// stands in for "just happened" and Created strictly decreases as
		// the index rises, exactly like the real client's output. A test
		// that only counted survivors would pass whichever end got kept;
		// asserting which IDs survive is what makes this discriminating.
		entries := make([]redfish.LogEntry, 0, 40)
		now := time.Now()
		for i := 0; i < 40; i++ {
			entries = append(entries, redfish.LogEntry{
				ID:       strconv.Itoa(i),
				Severity: "OK",
				Message:  "m",
				Created:  now.Add(-time.Duration(i) * time.Minute),
			})
		}
		fake.snapshot.Log = entries

		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-long", Namespace: "default"}}
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		Expect(got.Status.EventLog).To(HaveLen(25))
		// The retained window is the 25 newest, not the 25 oldest: index 0
		// ("just happened") survives, and index 24 (the 25th newest) is the
		// last one kept — index 25 onward (older) does not survive.
		Expect(got.Status.EventLog[0].ID).To(Equal("0"))
		Expect(got.Status.EventLog[24].ID).To(Equal("24"))
	})

	It("reports a timeout distinctly from other probe failures", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-timeout-creds"))).To(Succeed())
		Expect(k8sClient.Create(ctx, newMachine("fm-timeout"))).To(Succeed())
		fake.probeErr = fakeTimeoutError{}

		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-timeout", Namespace: "default"}}
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		Expect(meta.FindStatusCondition(got.Status.Conditions, "Reachable").Reason).To(Equal("Timeout"))
	})

	It("falls back to ProbeFailed for an error none of the sentinels explain", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-unknown-creds"))).To(Succeed())
		Expect(k8sClient.Create(ctx, newMachine("fm-unknown"))).To(Succeed())
		fake.probeErr = errors.New("boom")

		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-unknown", Namespace: "default"}}
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		Expect(meta.FindStatusCondition(got.Status.Conditions, "Reachable").Reason).To(Equal("ProbeFailed"))
	})
})
