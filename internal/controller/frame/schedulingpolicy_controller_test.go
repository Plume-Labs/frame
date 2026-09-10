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
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

var _ = Describe("SchedulingPolicy Controller", func() {
	const name = "test-policy"
	const ns = "default"
	const pcName = "frame-test-pc"
	key := types.NamespacedName{Name: name, Namespace: ns}
	ctx := context.Background()

	sp := &framev1beta1.SchedulingPolicy{}

	prio := int32(500)

	BeforeEach(func() {
		*sp = framev1beta1.SchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.SchedulingPolicySpec{
				Scheduler:     "default",
				PriorityClass: pcName,
				PriorityValue: &prio,
			},
		}
		Expect(k8sClient.Create(ctx, sp)).To(Succeed())
	})

	AfterEach(func() {
		fresh := &framev1beta1.SchedulingPolicy{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &framev1beta1.SchedulingPolicy{}))
		}, "5s").Should(BeTrue())
		_ = k8sClient.Delete(ctx, &schedulingv1.PriorityClass{
			ObjectMeta: metav1.ObjectMeta{Name: pcName},
		})
	})

	r := func() *SchedulingPolicyReconciler {
		return &SchedulingPolicyReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(100)}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds finalizer on first reconcile", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, sp)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(sp, schedulingPolicyFinalizer)).To(BeTrue())
	})

	It("creates a PriorityClass with the configured value", func() {
		_, _ = r().Reconcile(ctx, req) // add finalizer
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		pc := &schedulingv1.PriorityClass{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pcName}, pc)).To(Succeed())
		Expect(pc.Value).To(Equal(int32(500)))
		Expect(pc.Labels["frame.plume-labs.io/policy-namespace"]).To(Equal(ns))
		Expect(pc.Labels["frame.plume-labs.io/policy-name"]).To(Equal(name))

		pp := corev1.PreemptNever
		Expect(pc.PreemptionPolicy).To(Equal(&pp))
	})

	It("sets PreemptLowerPriority on PriorityClass when Preemption=true", func() {
		Expect(k8sClient.Get(ctx, key, sp)).To(Succeed())
		sp.Spec.Preemption = true
		Expect(k8sClient.Update(ctx, sp)).To(Succeed())

		_, _ = r().Reconcile(ctx, req)
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		pc := &schedulingv1.PriorityClass{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pcName}, pc)).To(Succeed())
		pp := corev1.PreemptLowerPriority
		Expect(pc.PreemptionPolicy).To(Equal(&pp))
	})

	It("sets Ready=True condition after successful reconcile", func() {
		_, _ = r().Reconcile(ctx, req)
		_, _ = r().Reconcile(ctx, req)

		Expect(k8sClient.Get(ctx, key, sp)).To(Succeed())
		cond := findCondition(sp.Status.Conditions)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal("Applied"))
	})

	It("degrades Ready condition when queue CRD is missing (volcano scheduler)", func() {
		// Volcano Queue CRD is not installed in envtest — controller must degrade gracefully.
		weight := int32(2)
		spV := &framev1beta1.SchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "volcano-policy", Namespace: ns},
			Spec: framev1beta1.SchedulingPolicySpec{
				Scheduler:   "volcano",
				QueueName:   "hpc",
				QueueWeight: &weight,
			},
		}
		Expect(k8sClient.Create(ctx, spV)).To(Succeed())
		defer func() {
			fresh := &framev1beta1.SchedulingPolicy{}
			vKey := types.NamespacedName{Name: "volcano-policy", Namespace: ns}
			if err := k8sClient.Get(ctx, vKey, fresh); err == nil {
				fresh.Finalizers = nil
				_ = k8sClient.Update(ctx, fresh)
				_ = k8sClient.Delete(ctx, fresh)
			}
		}()

		vReq := reconcile.Request{NamespacedName: types.NamespacedName{Name: "volcano-policy", Namespace: ns}}
		_, _ = r().Reconcile(ctx, vReq) // add finalizer
		_, err := r().Reconcile(ctx, vReq)
		Expect(err).NotTo(HaveOccurred()) // must not propagate queue error

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "volcano-policy", Namespace: ns}, spV)).To(Succeed())
		cond := findCondition(spV.Status.Conditions)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("ReconcileError"))
	})
})

var _ = Describe("SchedulingPolicy PriorityClass ownership", func() {
	const ns = "default"
	ctx := context.Background()

	r := func() *SchedulingPolicyReconciler {
		return &SchedulingPolicyReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(100),
		}
	}

	// newPolicy creates a SchedulingPolicy and registers its teardown,
	// including the finalizer removal a refused policy will not have needed.
	newPolicy := func(name, priorityClass string, value int32) (*framev1beta1.SchedulingPolicy, reconcile.Request) {
		v := value
		sp := &framev1beta1.SchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.SchedulingPolicySpec{
				Scheduler:     "default",
				PriorityClass: priorityClass,
				PriorityValue: &v,
			},
		}
		Expect(k8sClient.Create(ctx, sp)).To(Succeed())
		key := types.NamespacedName{Name: name, Namespace: ns}
		DeferCleanup(func() {
			fresh := &framev1beta1.SchedulingPolicy{}
			if err := k8sClient.Get(ctx, key, fresh); err == nil {
				fresh.Finalizers = nil
				_ = k8sClient.Update(ctx, fresh)
				_ = k8sClient.Delete(ctx, fresh)
			}
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, key, &framev1beta1.SchedulingPolicy{}))
			}, "5s").Should(BeTrue())
		})
		return sp, reconcile.Request{NamespacedName: key}
	}

	// newForeignPriorityClass plants a PriorityClass that belongs to somebody
	// else — the shape of neura-bulk on the live cluster: a Helm release's
	// object, carrying Helm's ownership markers and its own description.
	newForeignPriorityClass := func(name string, value int32) *schedulingv1.PriorityClass {
		// value and preemptionPolicy are both immutable on PriorityClass, and
		// both are set here to exactly what the controller would write for
		// this policy. That is deliberate: if they differed, the unfixed
		// controller would fail with a 422 and the test would go red for the
		// wrong reason — an apiserver rejection rather than a refusal to
		// adopt. Matching them is the silent case, and the one the cluster
		// actually lived through: neura-bulk kept its Helm labels and got
		// Frame's description written over it, with no error anywhere.
		preemptNever := corev1.PreemptNever
		pc := &schedulingv1.PriorityClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "Helm",
					"helm.sh/chart":                "neura-0.1.0",
				},
				Annotations: map[string]string{
					"meta.helm.sh/release-name":      "neura",
					"meta.helm.sh/release-namespace": "neura",
				},
			},
			Value:            value,
			PreemptionPolicy: &preemptNever,
			Description:      "Neura ingest queue, owned by the Neura Helm release",
		}
		Expect(k8sClient.Create(ctx, pc)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: name}})
		})
		return pc
	}

	get := func(name string) *schedulingv1.PriorityClass {
		pc := &schedulingv1.PriorityClass{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, pc)).To(Succeed())
		return pc
	}

	Context("a PriorityClass this policy did not create", func() {
		const pcName = "foreign-bulk"

		It("is refused, not adopted, and is still there unchanged afterwards", func() {
			// The value matches spec.priorityValue on purpose: PriorityClass.value
			// is immutable, so a mismatch would make the old code fail with a 422
			// and look like a refusal. Matching values are the silent-takeover
			// case — the one that actually happened on the cluster.
			before := newForeignPriorityClass(pcName, 1000)
			sp, req := newPolicy("adopts-foreign", pcName, 1000)

			// Twice: under the old ordering the first reconcile only added the
			// finalizer and the second did the adoption.
			_, err := r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			_, err = r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			// The object itself, field by field, first: "still there, unchanged"
			// is the claim, and a description rewritten to "Managed by
			// SchedulingPolicy ..." is what adoption looks like from outside.
			after := get(pcName)
			Expect(after.Description).NotTo(ContainSubstring("Managed by SchedulingPolicy"))
			Expect(after.Description).To(Equal("Neura ingest queue, owned by the Neura Helm release"))
			Expect(after.Labels).NotTo(HaveKey(policyNameLabel))
			Expect(after.Labels).NotTo(HaveKey(policyNamespaceLabel))
			Expect(after.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "Helm"))
			Expect(after.Value).To(Equal(int32(1000)))
			Expect(after.UID).To(Equal(before.UID))
			Expect(after.ResourceVersion).To(Equal(before.ResourceVersion))

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())

			// No finalizer: a refused policy holds no standing right to delete.
			Expect(controllerutil.ContainsFinalizer(sp, schedulingPolicyFinalizer)).To(BeFalse())
			Expect(sp.Status.OwnedPriorityClass).To(BeEmpty())

			// And the refusal is visible in status, not silent.
			cond := findCondition(sp.Status.Conditions)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(reasonPriorityClassNotOwned))
			Expect(cond.Message).To(ContainSubstring("Helm release neura/neura"))
		})

		It("survives the deletion of a policy that names it", func() {
			// The live-cluster shape: a SchedulingPolicy written by the old build
			// already carries the finalizer, and its status carries no ownership
			// record because the old build never wrote one.
			before := newForeignPriorityClass(pcName, 1000)
			sp, _ := newPolicy("deletes-foreign", pcName, 1000)
			controllerutil.AddFinalizer(sp, schedulingPolicyFinalizer)
			Expect(k8sClient.Update(ctx, sp)).To(Succeed())
			Expect(sp.Status.OwnedPriorityClass).To(BeEmpty())

			Expect(k8sClient.Delete(ctx, sp)).To(Succeed())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			Expect(sp.DeletionTimestamp.IsZero()).To(BeFalse())

			_, err := r().reconcileDelete(ctx, sp)
			Expect(err).NotTo(HaveOccurred())

			after := get(pcName)
			Expect(after.UID).To(Equal(before.UID))
			Expect(after.Description).To(Equal("Neura ingest queue, owned by the Neura Helm release"))

			// the policy still finishes deleting — refusing must not wedge it.
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), &framev1beta1.SchedulingPolicy{}))
			}, "5s").Should(BeTrue())
		})
	})

	Context("a PriorityClass this policy did create", func() {
		const pcName = "owned-by-frame"

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: pcName}})
		})

		It("is created, recorded, and deleted with the policy", func() {
			sp, req := newPolicy("owns-its-pc", pcName, 700)

			_, err := r().Reconcile(ctx, req) // claim + finalizer
			Expect(err).NotTo(HaveOccurred())
			_, err = r().Reconcile(ctx, req) // create the PriorityClass
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(sp, schedulingPolicyFinalizer)).To(BeTrue())
			Expect(sp.Status.OwnedPriorityClass).To(Equal(pcName))
			Expect(findCondition(sp.Status.Conditions).Reason).To(Equal("Applied"))

			pc := get(pcName)
			Expect(pc.Value).To(Equal(int32(700)))
			Expect(pc.Labels).To(HaveKeyWithValue(policyNameLabel, "owns-its-pc"))

			// and deleting the policy takes it away — the normal path must not
			// have been broken by the refusal machinery.
			Expect(k8sClient.Delete(ctx, sp)).To(Succeed())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			_, err = r().reconcileDelete(ctx, sp)
			Expect(err).NotTo(HaveOccurred())

			Expect(apierrors.IsNotFound(
				k8sClient.Get(ctx, types.NamespacedName{Name: pcName}, &schedulingv1.PriorityClass{}),
			)).To(BeTrue())
		})

		It("is released when spec.priorityClass is renamed away from it", func() {
			const renamed = "owned-by-frame-2"
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: renamed}})
			})
			sp, req := newPolicy("renames-its-pc", pcName, 700)
			_, _ = r().Reconcile(ctx, req)
			_, _ = r().Reconcile(ctx, req)
			Expect(get(pcName).Value).To(Equal(int32(700)))

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			sp.Spec.PriorityClass = renamed
			Expect(k8sClient.Update(ctx, sp)).To(Succeed())

			_, err := r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			Expect(sp.Status.OwnedPriorityClass).To(Equal(renamed))
			Expect(apierrors.IsNotFound(
				k8sClient.Get(ctx, types.NamespacedName{Name: pcName}, &schedulingv1.PriorityClass{}),
			)).To(BeTrue())
		})
	})

	Context("a reserved system-* PriorityClass", func() {
		const reserved = "system-cluster-critical"

		It("is refused even though it exists, and is not deleted by any path", func() {
			// system-cluster-critical is bootstrapped by the apiserver itself.
			before := get(reserved)

			sp, req := newPolicy("names-system-pc", reserved, 1000000000)
			_, err := r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			_, err = r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(sp, schedulingPolicyFinalizer)).To(BeFalse())
			cond := findCondition(sp.Status.Conditions)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal(reasonPriorityClassReserved))

			// "quoi qu'il arrive": even handed a forged ownership record and a
			// finalizer, the delete path must not touch it.
			controllerutil.AddFinalizer(sp, schedulingPolicyFinalizer)
			Expect(k8sClient.Update(ctx, sp)).To(Succeed())
			sp.Status.OwnedPriorityClass = reserved
			_, err = r().reconcileDelete(ctx, sp)
			Expect(err).NotTo(HaveOccurred())

			after := get(reserved)
			Expect(after.UID).To(Equal(before.UID))
			Expect(after.Value).To(Equal(before.Value))
		})
	})

	It("will not write to a PriorityClass it has not recorded, whatever the spec says", func() {
		// A direct call, because the only way to reach this state is to reorder
		// Reconcile — which is exactly the change that would bring the bug back.
		sp := &framev1beta1.SchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "unrecorded", Namespace: ns},
			Spec:       framev1beta1.SchedulingPolicySpec{Scheduler: "default", PriorityClass: "somebody-elses"},
		}
		err := r().reconcilePriorityClass(ctx, sp)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("refusing to write PriorityClass"))
		Expect(apierrors.IsNotFound(
			k8sClient.Get(ctx, types.NamespacedName{Name: "somebody-elses"}, &schedulingv1.PriorityClass{}),
		)).To(BeTrue())
	})
})

// findCondition returns the Ready condition — the only condition type any
// caller looks up — or nil if it hasn't been set yet.
func findCondition(conditions []metav1.Condition) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionTypeReady {
			return &conditions[i]
		}
	}
	return nil
}
