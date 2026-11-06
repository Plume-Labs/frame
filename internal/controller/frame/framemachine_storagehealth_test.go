package controller

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	"github.com/Plume-Labs/frame/internal/alerting"
	"github.com/Plume-Labs/frame/internal/redfish"
	"github.com/Plume-Labs/frame/internal/storage"
)

// failingReader answers what a denied or broken read answers.
type failingReader struct{}

func (failingReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("forbidden")
}

func (failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("forbidden")
}

// failingAlertList breaks only the FrameAlert list.
type failingAlertList struct{ client.Client }

func (f failingAlertList) List(ctx context.Context, l client.ObjectList, opts ...client.ListOption) error {
	if _, ok := l.(*framev1beta1.FrameAlertList); ok {
		return errors.New("list failed")
	}
	return f.Client.List(ctx, l, opts...)
}

// Real objects on envtest: the transitions below are only as good as what the
// apiserver lets through, and the alert's whole memory is the FrameAlert.
var _ = Describe("FrameMachine storage health", func() {
	const ns = "default"
	var (
		fake *fakeRedfish
		r    *FrameMachineReconciler
		name string
	)

	key := func() types.NamespacedName { return types.NamespacedName{Name: name, Namespace: ns} }

	reconcile := func() {
		GinkgoHelper()
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key()})
		Expect(err).NotTo(HaveOccurred())
	}

	// report writes status.storage the way the agent does: that field alone.
	report := func(md framev1beta1.MDStatus, zfs framev1beta1.ZFSStatus) {
		GinkgoHelper()
		var fm framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, key(), &fm)).To(Succeed())
		seen := metav1.Now()
		fm.Status.Storage = &framev1beta1.MachineStorage{ObservedAt: &seen, Local: &framev1beta1.LocalStorage{
			MD: md, ZFS: zfs, LVM: framev1beta1.LVMStatus{State: framev1beta1.StackAbsent},
		}}
		Expect(k8sClient.Status().Update(ctx, &fm)).To(Succeed())
	}

	cleanMD := func() framev1beta1.MDStatus {
		return framev1beta1.MDStatus{State: framev1beta1.StackPresent, Arrays: []framev1beta1.MDArray{{
			Name: "md0", Level: "raid1", State: "active", RaidDisks: 2, ActiveDisks: 2, Slots: "UU",
		}}}
	}
	failedMD := func() framev1beta1.MDStatus {
		md := cleanMD()
		md.Arrays[0].Degraded, md.Arrays[0].ActiveDisks, md.Arrays[0].Slots = true, 1, "U_"
		return md
	}
	noZFS := framev1beta1.ZFSStatus{State: framev1beta1.StackAbsent}
	pool := func(capacity int32) framev1beta1.ZFSStatus {
		return framev1beta1.ZFSStatus{State: framev1beta1.StackPresent, Pools: []framev1beta1.ZPool{
			{Name: "tank", Health: "ONLINE", CapacityPercent: &capacity},
		}}
	}

	condition := func(check string) metav1.Condition {
		GinkgoHelper()
		var fm framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, key(), &fm)).To(Succeed())
		c := meta.FindStatusCondition(fm.Status.Conditions, check)
		Expect(c).NotTo(BeNil(), check)
		return *c
	}

	alert := func(check, device string) *framev1beta1.FrameAlert {
		GinkgoHelper()
		var fa framev1beta1.FrameAlert
		fp := alerting.LocalFingerprint(storageAlertSource, ns, name, check, device)
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: alerting.AlertObjectName(fp)}, &fa)
		if apierrors.IsNotFound(err) {
			return nil
		}
		Expect(err).NotTo(HaveOccurred())
		return &fa
	}

	state := func(check, device string) string {
		GinkgoHelper()
		fa := alert(check, device)
		if fa == nil {
			return ""
		}
		return fa.Status.State
	}

	setPolicy := func(spec framev1beta1.StorageHealthPolicySpec) {
		GinkgoHelper()
		p := &framev1beta1.StorageHealthPolicy{ObjectMeta: metav1.ObjectMeta{Name: framev1beta1.StorageHealthPolicyName}, Spec: spec}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, p)).To(Succeed()) })
	}

	BeforeEach(func() {
		name = "fm-health-" + time.Now().Format("150405.000000000")
		name = "fm-h" + name[len(name)-9:]
		fake = &fakeRedfish{snapshot: &redfish.Snapshot{PowerState: "On", SensorsTrustworthy: true}}
		r = &FrameMachineReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(32),
			NewClient: func(context.Context, client.Client, string, framev1beta1.BMCSpec) (redfish.Client, error) {
				return fake, nil
			},
			Alerts: &alerting.Receiver{Client: k8sClient, Namespace: ns, Now: time.Now},
		}
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-creds", Namespace: ns},
			StringData: map[string]string{"username": "admin", "password": "secret"},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.FrameMachineSpec{
				NodeRef: "node-a",
				BMC: framev1beta1.BMCSpec{
					Address: "192.0.2.61", CredentialsRef: name + "-creds",
					TLS: framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
				},
			},
		})).To(Succeed())
	})

	It("goes healthy, degraded, restored on one alert object", func() {
		report(cleanMD(), noZFS)
		reconcile()
		Expect(condition(storage.CheckDegraded).Status).To(Equal(metav1.ConditionFalse))
		Expect(alert(storage.CheckDegraded, "md0")).To(BeNil())

		report(failedMD(), noZFS)
		reconcile()
		c := condition(storage.CheckDegraded)
		Expect(c.Status).To(Equal(metav1.ConditionTrue))
		Expect(c.Message).To(ContainSubstring("md0"))
		fa := alert(storage.CheckDegraded, "md0")
		Expect(fa.Status.State).To(Equal(framev1beta1.AlertStateFiring))
		Expect(fa.Spec.AlertName).To(Equal(storage.CheckDegraded))
		Expect(fa.Spec.Severity).To(Equal("critical"))
		Expect(fa.Spec.Labels).To(HaveKeyWithValue(labelNode, "node-a"))
		// A subscription scoped to namespaces must not receive it.
		Expect(fa.Spec.Namespace).To(BeEmpty())

		// A second pass on the same reading changes nothing the relay sees,
		// the start included: moved back an hour so a rewrite cannot hide in
		// the same second.
		earlier := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
		fa.Spec.StartsAt = earlier
		Expect(k8sClient.Update(ctx, fa)).To(Succeed())
		reconcile()
		again := alert(storage.CheckDegraded, "md0")
		Expect(again.Spec.StartsAt.Time).To(BeTemporally("==", earlier.Time))
		Expect(again.ResourceVersion).To(Equal(fa.ResourceVersion))

		report(cleanMD(), noZFS)
		reconcile()
		Expect(condition(storage.CheckDegraded).Status).To(Equal(metav1.ConditionFalse))
		fa = alert(storage.CheckDegraded, "md0")
		Expect(fa.Status.State).To(Equal(framev1beta1.AlertStateResolved))
		Expect(fa.Spec.EndsAt).NotTo(BeNil())

		// A pass after the resolution leaves it closed, its end where it was.
		endedAt := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
		fa.Spec.EndsAt = &endedAt
		Expect(k8sClient.Update(ctx, fa)).To(Succeed())
		reconcile()
		Expect(alert(storage.CheckDegraded, "md0").Spec.EndsAt.Time).To(BeTemporally("==", endedAt.Time))

		// The same array failing again reopens the same object, as a new
		// incident: it starts now, not when the last one did.
		report(failedMD(), noZFS)
		reconcile()
		fa = alert(storage.CheckDegraded, "md0")
		Expect(fa.Status.State).To(Equal(framev1beta1.AlertStateFiring))
		Expect(fa.Spec.EndsAt).To(BeNil())
		Expect(fa.Spec.StartsAt.Time).To(BeTemporally(">", earlier.Time))
	})

	It("keeps the alert of a pool the next read no longer lists", func() {
		degraded := pool(50)
		degraded.Pools[0].Health = "DEGRADED"
		report(cleanMD(), degraded)
		reconcile()
		Expect(state(storage.CheckDegraded, "tank")).To(Equal(framev1beta1.AlertStateFiring))

		for _, gone := range []framev1beta1.ZFSStatus{{State: framev1beta1.StackPresent}, noZFS} {
			report(cleanMD(), gone)
			reconcile()
			Expect(state(storage.CheckDegraded, "tank")).To(Equal(framev1beta1.AlertStateFiring))
			Expect(condition(storage.CheckDegraded).Reason).To(Equal("LastReadHeld"))
		}
	})

	It("holds everything when the agent stops reporting", func() {
		report(failedMD(), noZFS)
		reconcile()

		var fm framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, key(), &fm)).To(Succeed())
		long := metav1.NewTime(time.Now().Add(-storage.AgentSilentAfter - time.Minute))
		fm.Status.Storage.ObservedAt = &long
		fm.Status.Storage.Local.MD = cleanMD()
		Expect(k8sClient.Status().Update(ctx, &fm)).To(Succeed())
		reconcile()
		Expect(state(storage.CheckDegraded, "md0")).To(Equal(framev1beta1.AlertStateFiring))
		Expect(condition(storage.CheckDegraded).Reason).To(Equal("LastReadHeld"))
	})

	It("raises wear from the BMC, and only from a probe that answered", func() {
		used := int32(95)
		fake.snapshot.Inventory.Drives = []redfish.Drive{{SerialNumber: "WORN", Location: "1:1", MediaType: "SSD", SSDEnduranceUsedPercent: &used}}
		report(cleanMD(), noZFS)
		reconcile()
		Expect(state(storage.CheckWearHigh, "WORN")).To(Equal(framev1beta1.AlertStateFiring))

		// The drive reads fine in the stale status, and the probe failed:
		// nothing may clear on what was not read.
		var fm framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, key(), &fm)).To(Succeed())
		low := int32(5)
		fm.Status.Inventory.Drives[0].SSDEnduranceUsedPercent = &low
		Expect(k8sClient.Status().Update(ctx, &fm)).To(Succeed())
		fake.probeErr = errors.New("connection refused")
		reconcile()
		Expect(state(storage.CheckWearHigh, "WORN")).To(Equal(framev1beta1.AlertStateFiring))

		fake.probeErr = nil
		fake.snapshot.Inventory.Drives[0].SSDEnduranceUsedPercent = &low
		reconcile()
		Expect(state(storage.CheckWearHigh, "WORN")).To(Equal(framev1beta1.AlertStateResolved))
	})

	It("does not raise wear from a stale inventory", func() {
		used := int32(95)
		fake.snapshot.Inventory.Drives = []redfish.Drive{{SerialNumber: "OK", Location: "1:1", MediaType: "SSD"}}
		report(cleanMD(), noZFS)
		reconcile()
		var fm framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, key(), &fm)).To(Succeed())
		fm.Status.Inventory.Drives[0].SSDEnduranceUsedPercent = &used
		Expect(k8sClient.Status().Update(ctx, &fm)).To(Succeed())

		for _, broken := range []func(){
			func() { fake.probeErr = errors.New("connection refused") },
			func() {
				r.NewClient = func(context.Context, client.Client, string, framev1beta1.BMCSpec) (redfish.Client, error) {
					return nil, errors.New("no credentials")
				}
			},
		} {
			broken()
			reconcile()
			Expect(alert(storage.CheckWearHigh, "OK")).To(BeNil())
		}
	})

	It("assesses the node's stacks when the BMC credentials are unusable", func() {
		r.NewClient = func(context.Context, client.Client, string, framev1beta1.BMCSpec) (redfish.Client, error) {
			return nil, errors.New("no credentials")
		}
		report(failedMD(), noZFS)
		reconcile()
		Expect(state(storage.CheckDegraded, "md0")).To(Equal(framev1beta1.AlertStateFiring))
	})

	It("leaves storage as it was when the policy cannot be read, and still writes the probe", func() {
		report(failedMD(), noZFS)
		reconcile()
		report(cleanMD(), noZFS)
		r.PolicyReader = failingReader{}
		fake.probeErr = errors.New("connection refused")
		reconcile()
		Expect(state(storage.CheckDegraded, "md0")).To(Equal(framev1beta1.AlertStateFiring))
		Expect(condition(storage.CheckDegraded).Status).To(Equal(metav1.ConditionTrue))
		Expect(condition(conditionReachable).Status).To(Equal(metav1.ConditionFalse))
	})

	It("leaves storage as it was when the open alerts cannot be listed", func() {
		report(failedMD(), noZFS)
		reconcile()
		report(cleanMD(), noZFS)
		r.Client = failingAlertList{k8sClient}
		reconcile()
		r.Client = k8sClient
		Expect(state(storage.CheckDegraded, "md0")).To(Equal(framev1beta1.AlertStateFiring))
		Expect(condition(storage.CheckDegraded).Status).To(Equal(metav1.ConditionTrue))
	})

	It("does not take another namespace's machine of the same name for its own", func() {
		report(failedMD(), noZFS)
		reconcile()
		theirs := alert(storage.CheckDegraded, "md0")

		other := "fm-h-other-" + name[len(name)-5:]
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: other}})).To(Succeed())
		Expect(k8sClient.Create(ctx, &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: other},
			Spec: framev1beta1.FrameMachineSpec{BMC: framev1beta1.BMCSpec{
				Address: "192.0.2.63", CredentialsRef: "unused",
				TLS: framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
			}},
		})).To(Succeed())
		var fm framev1beta1.FrameMachine
		key := types.NamespacedName{Name: name, Namespace: other}
		Expect(k8sClient.Get(ctx, key, &fm)).To(Succeed())
		seen := metav1.Now()
		fm.Status.Storage = &framev1beta1.MachineStorage{ObservedAt: &seen, Local: &framev1beta1.LocalStorage{
			MD: cleanMD(), ZFS: noZFS, LVM: framev1beta1.LVMStatus{State: framev1beta1.StackAbsent},
		}}
		Expect(k8sClient.Status().Update(ctx, &fm)).To(Succeed())
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(alert(storage.CheckDegraded, "md0").ResourceVersion).To(Equal(theirs.ResourceVersion))
	})

	It("closes an alert whose machine vanished while nobody watched", func() {
		report(failedMD(), noZFS)
		reconcile()
		orphan := name
		var fm framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, key(), &fm)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &fm)).To(Succeed())

		// No reconcile of the deleted machine: another machine's pass finds it.
		name = orphan + "z"
		Expect(k8sClient.Create(ctx, &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.FrameMachineSpec{BMC: framev1beta1.BMCSpec{
				Address: "192.0.2.64", CredentialsRef: orphan + "-creds",
				TLS: framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
			}},
		})).To(Succeed())
		report(cleanMD(), noZFS)
		reconcile()
		name = orphan
		Expect(state(storage.CheckDegraded, "md0")).To(Equal(framev1beta1.AlertStateResolved))
	})

	DescribeTable("a read that cannot be trusted leaves an open alert open",
		func(unread framev1beta1.StackState) {
			report(failedMD(), noZFS)
			reconcile()

			md := cleanMD()
			md.State, md.Error = unread, "mdstat unreadable"
			report(md, noZFS)
			reconcile()
			Expect(state(storage.CheckDegraded, "md0")).To(Equal(framev1beta1.AlertStateFiring))
			c := condition(storage.CheckDegraded)
			Expect(c.Status).To(Equal(metav1.ConditionTrue))
			Expect(c.Reason).To(Equal("LastReadHeld"))

			report(cleanMD(), noZFS)
			reconcile()
			Expect(state(storage.CheckDegraded, "md0")).To(Equal(framev1beta1.AlertStateResolved))
		},
		Entry("error", framev1beta1.StackError),
		Entry("busy", framev1beta1.StackBusy),
	)

	DescribeTable("a read that cannot be trusted raises nothing",
		func(unread framev1beta1.StackState) {
			md := failedMD()
			md.State, md.Error = unread, "mdstat unreadable"
			report(md, noZFS)
			reconcile()
			Expect(alert(storage.CheckDegraded, "md0")).To(BeNil())
			Expect(condition(storage.CheckDegraded).Status).To(Equal(metav1.ConditionUnknown))
		},
		Entry("error", framev1beta1.StackError),
		Entry("busy", framev1beta1.StackBusy),
	)

	It("follows a threshold moved in the policy, both ways", func() {
		report(cleanMD(), pool(85))
		reconcile()
		Expect(state(storage.CheckCapacityHigh, "tank")).To(Equal(framev1beta1.AlertStateFiring))

		setPolicy(framev1beta1.StorageHealthPolicySpec{ZFSCapacityPercent: 95, SSDWearPercent: 80})
		reconcile()
		Expect(state(storage.CheckCapacityHigh, "tank")).To(Equal(framev1beta1.AlertStateResolved))
		Expect(condition(storage.CheckCapacityHigh).Status).To(Equal(metav1.ConditionFalse))

		var p framev1beta1.StorageHealthPolicy
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: framev1beta1.StorageHealthPolicyName}, &p)).To(Succeed())
		p.Spec.ZFSCapacityPercent = 70
		Expect(k8sClient.Update(ctx, &p)).To(Succeed())
		reconcile()
		Expect(state(storage.CheckCapacityHigh, "tank")).To(Equal(framev1beta1.AlertStateFiring))
	})

	It("still sees a failed mirror while the BMC does not answer", func() {
		fake.probeErr = errors.New("connection refused")
		report(failedMD(), noZFS)
		reconcile()
		Expect(state(storage.CheckDegraded, "md0")).To(Equal(framev1beta1.AlertStateFiring))
		Expect(condition(conditionReachable).Status).To(Equal(metav1.ConditionFalse))
		Expect(condition(storage.CheckWearHigh).Status).To(Equal(metav1.ConditionUnknown))
	})

	It("keeps Reachable beside the storage conditions", func() {
		report(cleanMD(), noZFS)
		reconcile()
		Expect(condition(conditionReachable).Status).To(Equal(metav1.ConditionTrue))
		Expect(condition(storage.CheckWearHigh).Status).To(Equal(metav1.ConditionUnknown))
	})

	It("keeps two machines' alerts on the same device apart", func() {
		report(failedMD(), noZFS)
		reconcile()
		first := name
		firstAlert := alert(storage.CheckDegraded, "md0")

		other := first + "b"
		Expect(k8sClient.Create(ctx, &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: other, Namespace: ns},
			Spec: framev1beta1.FrameMachineSpec{BMC: framev1beta1.BMCSpec{
				Address: "192.0.2.62", CredentialsRef: first + "-creds",
				TLS: framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
			}},
		})).To(Succeed())
		name = other
		report(cleanMD(), noZFS)
		reconcile()

		name = first
		Expect(alert(storage.CheckDegraded, "md0").ResourceVersion).To(Equal(firstAlert.ResourceVersion))
	})

	It("resolves what a deleted machine leaves open", func() {
		report(failedMD(), noZFS)
		reconcile()
		var fm framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, key(), &fm)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &fm)).To(Succeed())
		reconcile()
		Expect(state(storage.CheckDegraded, "md0")).To(Equal(framev1beta1.AlertStateResolved))
	})

	It("takes the policy's defaults from the CRD", func() {
		p := &framev1beta1.StorageHealthPolicy{ObjectMeta: metav1.ObjectMeta{Name: framev1beta1.StorageHealthPolicyName}}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, p)).To(Succeed()) })
		Expect(p.Spec.ZFSCapacityPercent).To(Equal(framev1beta1.DefaultZFSCapacityPercent))
		Expect(p.Spec.SSDWearPercent).To(Equal(framev1beta1.DefaultSSDWearPercent))
	})

	It("refuses a second policy and a threshold out of range", func() {
		Expect(k8sClient.Create(ctx, &framev1beta1.StorageHealthPolicy{ObjectMeta: metav1.ObjectMeta{Name: "other"}})).
			To(MatchError(ContainSubstring("singleton")))
		for _, spec := range []framev1beta1.StorageHealthPolicySpec{
			{ZFSCapacityPercent: 101},
			{SSDWearPercent: 101},
			{ZFSCapacityPercent: -1},
			{SSDWearPercent: -1},
		} {
			Expect(k8sClient.Create(ctx, &framev1beta1.StorageHealthPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: framev1beta1.StorageHealthPolicyName},
				Spec:       spec,
			})).NotTo(Succeed())
		}
	})
})
