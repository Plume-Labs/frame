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
		startedAt := fa.Spec.StartsAt

		// A second pass on the same reading changes nothing the relay sees.
		reconcile()
		again := alert(storage.CheckDegraded, "md0")
		Expect(again.Spec.StartsAt).To(Equal(startedAt))
		Expect(again.ResourceVersion).To(Equal(fa.ResourceVersion))

		report(cleanMD(), noZFS)
		reconcile()
		Expect(condition(storage.CheckDegraded).Status).To(Equal(metav1.ConditionFalse))
		fa = alert(storage.CheckDegraded, "md0")
		Expect(fa.Status.State).To(Equal(framev1beta1.AlertStateResolved))
		Expect(fa.Spec.EndsAt).NotTo(BeNil())

		// The same array failing again reopens the same object.
		report(failedMD(), noZFS)
		reconcile()
		fa = alert(storage.CheckDegraded, "md0")
		Expect(fa.Status.State).To(Equal(framev1beta1.AlertStateFiring))
		Expect(fa.Spec.EndsAt).To(BeNil())
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
		Expect(k8sClient.Create(ctx, &framev1beta1.StorageHealthPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: framev1beta1.StorageHealthPolicyName},
			Spec:       framev1beta1.StorageHealthPolicySpec{ZFSCapacityPercent: 101},
		})).NotTo(Succeed())
	})
})
