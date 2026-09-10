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

// fakeRedfish and fakeTimeoutError live in framemachine_fake_test.go.

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
			PowerState: "On",
			// PostState/SensorsTrustworthy stand in for a fully booted,
			// finished-POST machine — the shape every controller spec except
			// the Finding 1 one below has always assumed. See
			// internal/redfish.Snapshot.SensorsTrustworthy: false is what a
			// zero-value Snapshot would carry, which would make every
			// existing "reads its Sensors" assertion below fail against
			// applySnapshot's new clearing branch for the wrong reason.
			PostState:          "InPostDiscoveryComplete",
			SensorsTrustworthy: true,
			IndicatorLED:       "Off",
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

	// Finding 1 (internal/redfish/client.go): the captured iLO4 replays a
	// cached sensor reading as live regardless of power state. This is the
	// discriminating test named in the brief: it drives a snapshot with
	// SensorsTrustworthy false but non-empty Sensors — the exact shape a
	// pre-fix applySnapshot would render as a plausible, healthy reading —
	// and proves the controller clears it instead. It fails on the pre-fix
	// code (the clearing branch removed) exactly as
	// setcondition_regression_test.go's sibling specs do for their own
	// defects: see task-6b-report.md for the recorded red/green runs.
	It("clears untrustworthy sensors instead of storing a stale reading beside a caveat", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-untrusted-creds"))).To(Succeed())
		fm := newMachine("fm-untrusted")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-untrusted", Namespace: "default"}}

		// First probe: trustworthy, so SensorsValidAt gets set. This is the
		// value the second (untrustworthy) probe must leave untouched.
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		var afterFirst framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &afterFirst)).To(Succeed())
		Expect(afterFirst.Status.Sensors).NotTo(BeNil())
		Expect(afterFirst.Status.SensorsValidAt).NotTo(BeNil())
		firstValidAt := afterFirst.Status.SensorsValidAt.DeepCopy()

		// Second probe: powered off, mid-POST-shaped — SensorsTrustworthy
		// false — but the fake still hands back a full, plausible-looking
		// Sensors block, exactly as the real BMC does (Finding 1's captures:
		// CPU1 at 40C, Status.State Enabled, whichever power state).
		fake.snapshot.PowerState = "Off"
		fake.snapshot.PostState = "PowerOff"
		fake.snapshot.SensorsTrustworthy = false
		fake.snapshot.Sensors = redfish.Sensors{
			Temperatures: []redfish.Temperature{{Name: "02-CPU 1", Celsius: 40, Health: "OK"}},
		}
		time.Sleep(time.Millisecond) // guarantee SensorsValidAt would move if the bug reintroduces itself

		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		Expect(got.Status.Sensors).To(BeNil())
		Expect(got.Status.PostState).To(Equal("PowerOff"))
		Expect(got.Status.PowerState).To(Equal("Off"))
		Expect(got.Status.SensorsValidAt.Time).To(BeTemporally("==", firstValidAt.Time))
		// LastProbeAt still advances: the probe succeeded and read the
		// machine, it just didn't read trustworthy sensors.
		Expect(got.Status.LastProbeAt).NotTo(BeNil())
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

	It("executes a request whose timestamp is newer than the last action", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-pwr-creds"))).To(Succeed())
		fm := newMachine("fm-pwr")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-pwr", Namespace: "default"}}

		fm.Spec.PowerRequest = &framev1beta1.PowerRequestSpec{
			Action:      framev1beta1.PowerActionGracefulShutdown,
			RequestedAt: metav1.Now(),
		}
		Expect(k8sClient.Update(ctx, fm)).To(Succeed())

		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.resets).To(Equal([]string{"GracefulShutdown"}))

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		Expect(got.Status.LastPowerAction).To(Equal("GracefulShutdown"))
		Expect(got.Status.LastPowerActionAt).NotTo(BeNil())
	})

	// The guard, and the reason the field is a timestamp rather than a desired
	// state: a second reconcile of the same object must not shut the machine
	// down twice.
	It("does not repeat an action on the next reconcile", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-once-creds"))).To(Succeed())
		fm := newMachine("fm-once")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-once", Namespace: "default"}}

		fm.Spec.PowerRequest = &framev1beta1.PowerRequestSpec{
			Action:      framev1beta1.PowerActionForceRestart,
			RequestedAt: metav1.Now(),
		}
		Expect(k8sClient.Update(ctx, fm)).To(Succeed())

		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(fake.resets).To(HaveLen(1))
	})

	It("ignores a request older than the last action it performed", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-stale-creds"))).To(Succeed())
		fm := newMachine("fm-stale")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-stale", Namespace: "default"}}

		now := metav1.Now()
		earlier := metav1.NewTime(now.Add(-time.Hour))

		var stored framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &stored)).To(Succeed())
		stored.Status.LastPowerActionAt = &now
		Expect(k8sClient.Status().Update(ctx, &stored)).To(Succeed())

		Expect(k8sClient.Get(ctx, req.NamespacedName, &stored)).To(Succeed())
		stored.Spec.PowerRequest = &framev1beta1.PowerRequestSpec{
			Action:      framev1beta1.PowerActionForceOff,
			RequestedAt: earlier,
		}
		Expect(k8sClient.Update(ctx, &stored)).To(Succeed())

		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.resets).To(BeEmpty())
	})

	It("routes the non-power actions to their own calls", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-led-creds"))).To(Succeed())
		fm := newMachine("fm-led")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-led", Namespace: "default"}}

		fm.Spec.PowerRequest = &framev1beta1.PowerRequestSpec{
			Action:      framev1beta1.PowerActionIndicatorLedOn,
			RequestedAt: metav1.Now(),
		}
		Expect(k8sClient.Update(ctx, fm)).To(Succeed())
		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.ledCalls).To(Equal([]bool{true}))
		Expect(fake.resets).To(BeEmpty())

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		got.Spec.PowerRequest = &framev1beta1.PowerRequestSpec{
			Action:      framev1beta1.PowerActionClearSEL,
			RequestedAt: metav1.NewTime(time.Now().Add(time.Second)),
		}
		Expect(k8sClient.Update(ctx, &got)).To(Succeed())
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.clearLog).To(Equal(1))
	})

	// The defect this guards against: lastPowerActionAt advances on failure
	// too (deliberately — see runPowerRequest), and the Warning Event that
	// also carries the failure ages out within the hour. Without
	// LastPowerActionError, an operator looking at the object later sees
	// "ForceOff at <time>" and nothing else — indistinguishable from a
	// success. This spec fails on the pre-fix code: it doesn't set the
	// field, so the first Expect on it fails.
	It("records why a power action failed, and clears the record on the next success", func() {
		Expect(k8sClient.Create(ctx, newSecret("fm-pwr-err-creds"))).To(Succeed())
		fm := newMachine("fm-pwr-err")
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-pwr-err", Namespace: "default"}}

		fake.resetErr = errors.New("BMC rejected the reset")
		fm.Spec.PowerRequest = &framev1beta1.PowerRequestSpec{
			Action:      framev1beta1.PowerActionForceOff,
			RequestedAt: metav1.Now(),
		}
		Expect(k8sClient.Update(ctx, fm)).To(Succeed())

		_, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.resets).To(Equal([]string{"ForceOff"}))

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		// The action is recorded as having happened, even though it failed:
		// that is the point of the timestamp guard (see runPowerRequest) —
		// but it means the error has to live somewhere durable too.
		Expect(got.Status.LastPowerAction).To(Equal("ForceOff"))
		Expect(got.Status.LastPowerActionAt).NotTo(BeNil())
		Expect(got.Status.LastPowerActionError).To(ContainSubstring("BMC rejected the reset"))

		// A second, newer request that succeeds clears the earlier failure.
		got.Spec.PowerRequest = &framev1beta1.PowerRequestSpec{
			Action:      framev1beta1.PowerActionForceOff,
			RequestedAt: metav1.NewTime(time.Now().Add(time.Second)),
		}
		Expect(k8sClient.Update(ctx, &got)).To(Succeed())

		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.resets).To(Equal([]string{"ForceOff", "ForceOff"}))

		Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		Expect(got.Status.LastPowerActionError).To(BeEmpty())
	})

	// The CRD's +kubebuilder:validation:Enum on PowerAction makes an
	// out-of-enum value unreachable through the API server (envtest enforces
	// it, same as a real apiserver would), so this drives runPowerRequest
	// directly rather than through k8sClient.Update, to prove the default
	// branch of the action switch runs without panicking at least once.
	It("records an out-of-enum action instead of panicking", func() {
		fm := newMachine("fm-badaction")
		fm.Spec.PowerRequest = &framev1beta1.PowerRequestSpec{
			Action:      framev1beta1.PowerAction("Frobnicate"),
			RequestedAt: metav1.Now(),
		}

		acted, err := r.runPowerRequest(ctx, fm, fake)
		Expect(err).NotTo(HaveOccurred())
		Expect(acted).To(BeTrue())
		Expect(fm.Status.LastPowerActionError).To(ContainSubstring("Frobnicate"))
		Expect(fake.resets).To(BeEmpty())
		Expect(fake.clearLog).To(Equal(0))
		Expect(fake.ledCalls).To(BeEmpty())
	})
})
