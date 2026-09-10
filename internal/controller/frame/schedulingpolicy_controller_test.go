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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

	It("degrades Ready condition when queue CRD is missing (yunikorn scheduler)", func() {
		// This spec turns on the scheduler whose Queue CRD envtest does NOT
		// have. It used to name volcano, and had to move: testdata/crds now
		// installs scheduling.volcano.sh Queue so the ownership specs below
		// can plant a real foreign Queue and watch it not be adopted. The
		// yunikorn.apache.org Queue kind is still absent, which is what this
		// spec is about — the controller must degrade, not fail, on a cluster
		// that has no such CRD. Losing that coverage to the fixture would have
		// been the fix quietly deleting a test.
		weight := int32(2)
		spV := &framev1beta1.SchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "volcano-policy", Namespace: ns},
			Spec: framev1beta1.SchedulingPolicySpec{
				Scheduler:   "yunikorn",
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

var _ = Describe("SchedulingPolicy Queue ownership", func() {
	const ns = "default"
	ctx := context.Background()

	// Volcano, not YuniKorn, throughout: it is the scheduler installed on the
	// live cluster, its Queue is genuinely cluster-scoped there
	// (`kubectl get crd queues.scheduling.volcano.sh -o jsonpath='{.spec.scope}'`
	// says Cluster), and testdata/crds mirrors that scope. The YuniKorn kind
	// is deliberately left uninstalled — the "CRD missing" spec above depends
	// on it, and two specs at the bottom of this file use it to reach the
	// kind-unavailable branches.
	queueGVK := schema.GroupVersionKind{Group: "scheduling.volcano.sh", Version: "v1beta1", Kind: "Queue"}

	r := func() *SchedulingPolicyReconciler {
		return &SchedulingPolicyReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(100),
		}
	}

	blankQueue := func(name string) *unstructured.Unstructured {
		q := &unstructured.Unstructured{}
		q.SetGroupVersionKind(queueGVK)
		q.SetName(name)
		return q
	}

	deleteQueue := func(name string) { _ = k8sClient.Delete(ctx, blankQueue(name)) }

	getQueue := func(name string) *unstructured.Unstructured {
		GinkgoHelper()
		q := blankQueue(name)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, q)).To(Succeed())
		return q
	}

	queueGone := func(name string) bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: name}, blankQueue(name)))
	}

	queueSpec := func(q *unstructured.Unstructured) map[string]any {
		spec, _ := q.Object["spec"].(map[string]any)
		return spec
	}

	// newPolicy creates a SchedulingPolicy naming a Volcano queue and nothing
	// else — no priorityClass, so the PriorityClass claim is a no-op and every
	// assertion below is about the Queue alone.
	newPolicy := func(name, queueName string, weight int32) (*framev1beta1.SchedulingPolicy, reconcile.Request) {
		w := weight
		sp := &framev1beta1.SchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.SchedulingPolicySpec{
				Scheduler:   "volcano",
				QueueName:   queueName,
				QueueWeight: &w,
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

	// newForeignQueue plants a Queue that belongs to somebody else, in the
	// exact shape neura-ingest has on the live cluster right now: a Helm
	// release's object, carrying Helm's ownership markers.
	//
	// weight is 100 and reclaimable false, which is what the policies below
	// ask for too. That is deliberate, and it is the same reasoning as
	// newForeignPriorityClass: if the spec differed, an unfixed controller
	// would still take the object over, but the test could be read as
	// catching a conflict rather than an adoption. Matching values make the
	// takeover completely silent — which is what happened: neura-ingest kept
	// its Helm labels and picked up frame.plume-labs.io/policy-name:
	// neura-ingest with no error anywhere.
	newForeignQueue := func(name string) *unstructured.Unstructured {
		q := blankQueue(name)
		q.SetLabels(map[string]string{
			"app.kubernetes.io/managed-by": "Helm",
			"app.kubernetes.io/instance":   "neura",
			"helm.sh/chart":                "neura-0.1.0",
		})
		q.SetAnnotations(map[string]string{
			"meta.helm.sh/release-name":      "neura",
			"meta.helm.sh/release-namespace": "neura",
		})
		q.Object["spec"] = map[string]any{"weight": int64(100), "reclaimable": false}
		Expect(k8sClient.Create(ctx, q)).To(Succeed())
		DeferCleanup(func() { deleteQueue(name) })
		return q
	}

	// startDeleting deletes the policy and reads back the copy the finalizer
	// is holding, so reconcileDelete can be called on a genuinely deleting
	// object rather than on a hand-built one.
	startDeleting := func(sp *framev1beta1.SchedulingPolicy) *framev1beta1.SchedulingPolicy {
		GinkgoHelper()
		Expect(k8sClient.Delete(ctx, sp)).To(Succeed())
		deleting := &framev1beta1.SchedulingPolicy{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), deleting)).To(Succeed())
		Expect(deleting.DeletionTimestamp.IsZero()).To(BeFalse())
		return deleting
	}

	Context("a Queue this policy did not create", func() {
		const queueName = "foreign-ingest"

		It("is refused, not adopted, and is still there unchanged afterwards", func() {
			before := newForeignQueue(queueName)
			sp, req := newPolicy("adopts-foreign-queue", queueName, 100)

			// Twice: under the old ordering the first reconcile only added the
			// finalizer and the second did the adoption.
			_, err := r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			_, err = r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			// The object itself first, field by field: "still there,
			// unchanged" is the claim, and Frame's two policy labels appearing
			// on somebody else's Queue is what adoption looks like from
			// outside.
			after := getQueue(queueName)
			Expect(after.GetLabels()).NotTo(HaveKey(policyNameLabel))
			Expect(after.GetLabels()).NotTo(HaveKey(policyNamespaceLabel))
			Expect(after.GetLabels()).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "Helm"))
			Expect(after.GetAnnotations()).To(HaveKeyWithValue("meta.helm.sh/release-name", "neura"))
			Expect(queueSpec(after)).To(Equal(map[string]any{"weight": int64(100), "reclaimable": false}))
			Expect(after.GetUID()).To(Equal(before.GetUID()))
			Expect(after.GetResourceVersion()).To(Equal(before.GetResourceVersion()))

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())

			// No finalizer: a refused policy holds no standing right to delete.
			Expect(controllerutil.ContainsFinalizer(sp, schedulingPolicyFinalizer)).To(BeFalse())
			Expect(sp.Status.OwnedQueue).To(BeEmpty())
			Expect(sp.Status.OwnedQueueScheduler).To(BeEmpty())

			// And the refusal is visible in status, not silent.
			cond := findCondition(sp.Status.Conditions)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(reasonQueueNotOwned))
			Expect(cond.Message).To(ContainSubstring("Helm release neura/neura"))
		})

		It("survives the deletion of a policy that names it", func() {
			// The live-cluster shape: a SchedulingPolicy written by the old
			// build already carries the finalizer, and its status carries no
			// ownership record because the old build never wrote one.
			before := newForeignQueue(queueName)
			sp, _ := newPolicy("deletes-foreign-queue", queueName, 100)
			controllerutil.AddFinalizer(sp, schedulingPolicyFinalizer)
			Expect(k8sClient.Update(ctx, sp)).To(Succeed())
			Expect(sp.Status.OwnedQueue).To(BeEmpty())

			deleting := startDeleting(sp)
			_, err := r().reconcileDelete(ctx, deleting)
			Expect(err).NotTo(HaveOccurred())

			after := getQueue(queueName)
			Expect(after.GetUID()).To(Equal(before.GetUID()))
			Expect(after.GetLabels()).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "Helm"))

			// and the policy still finishes deleting — refusing must not wedge it.
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), &framev1beta1.SchedulingPolicy{}))
			}, "5s").Should(BeTrue())
		})
	})

	Context("a Queue this policy did create", func() {
		const queueName = "queue-owned-by-frame"

		AfterEach(func() { deleteQueue(queueName) })

		It("is created, recorded, and deleted with the policy", func() {
			sp, req := newPolicy("owns-its-queue", queueName, 7)

			_, err := r().Reconcile(ctx, req) // claim + finalizer
			Expect(err).NotTo(HaveOccurred())
			_, err = r().Reconcile(ctx, req) // create the Queue
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(sp, schedulingPolicyFinalizer)).To(BeTrue())
			Expect(sp.Status.OwnedQueue).To(Equal(queueName))
			Expect(sp.Status.OwnedQueueScheduler).To(Equal("volcano"))
			Expect(findCondition(sp.Status.Conditions).Reason).To(Equal("Applied"))

			q := getQueue(queueName)
			Expect(queueSpec(q)).To(HaveKeyWithValue("weight", int64(7)))
			Expect(q.GetLabels()).To(HaveKeyWithValue(policyNameLabel, "owns-its-queue"))
			Expect(q.GetLabels()).To(HaveKeyWithValue(policyNamespaceLabel, ns))

			// and deleting the policy takes it away — the normal path must not
			// have been broken by the refusal machinery.
			deleting := startDeleting(sp)
			_, err = r().reconcileDelete(ctx, deleting)
			Expect(err).NotTo(HaveOccurred())
			Expect(queueGone(queueName)).To(BeTrue())
		})

		It("is not deleted once it stops carrying this policy's marks", func() {
			// The one case the record alone cannot see: our Queue was deleted
			// out from under us and something else took the name. The record
			// still says it is ours; the object says otherwise, and the object
			// wins in the direction of not deleting.
			sp, req := newPolicy("loses-its-marks", queueName, 7)
			_, _ = r().Reconcile(ctx, req)
			_, _ = r().Reconcile(ctx, req)
			Expect(getQueue(queueName).GetLabels()).To(HaveKey(policyNameLabel))

			stripped := getQueue(queueName)
			stripped.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "somebody-else"})
			Expect(k8sClient.Update(ctx, stripped)).To(Succeed())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			Expect(sp.Status.OwnedQueue).To(Equal(queueName))
			deleting := startDeleting(sp)
			_, err := r().reconcileDelete(ctx, deleting)
			Expect(err).NotTo(HaveOccurred())

			Expect(getQueue(queueName).GetUID()).To(Equal(stripped.GetUID()))
		})
	})

	// The rename path gets four specs rather than one on purpose. It is the
	// part of this change with no equivalent in the code it replaces — the old
	// reconcileQueue never released anything, so every line here is new — and
	// new code inside a fix is where the fix's own bug lives. Each spec pins a
	// different half of claimQueue's ordering contract: check the new name
	// free BEFORE releasing the old, release using the RECORD's scheduler
	// rather than the spec's, and treat "no queue wanted" as a release rather
	// than as nothing to do.
	Context("renaming or dropping the queue a policy owns", func() {
		const first = "renamer-first"
		const second = "renamer-second"

		AfterEach(func() {
			deleteQueue(first)
			deleteQueue(second)
		})

		// own drives a policy to steady state: Queue created, record written.
		own := func(policy, queueName string) (*framev1beta1.SchedulingPolicy, reconcile.Request) {
			GinkgoHelper()
			sp, req := newPolicy(policy, queueName, 7)
			_, err := r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			_, err = r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			Expect(sp.Status.OwnedQueue).To(Equal(queueName))
			Expect(getQueue(queueName).GetLabels()).To(HaveKeyWithValue(policyNameLabel, policy))
			return sp, req
		}

		It("releases the old Queue and records the new one", func() {
			sp, req := own("renames-its-queue", first)

			sp.Spec.QueueName = second
			Expect(k8sClient.Update(ctx, sp)).To(Succeed())

			_, err := r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			Expect(sp.Status.OwnedQueue).To(Equal(second))
			Expect(sp.Status.OwnedQueueScheduler).To(Equal("volcano"))
			Expect(queueGone(first)).To(BeTrue())

			// and the new name is actually built out on the next pass, rather
			// than only recorded.
			_, err = r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(getQueue(second).GetLabels()).To(HaveKeyWithValue(policyNameLabel, "renames-its-queue"))
		})

		It("keeps the old Queue when the new name belongs to somebody else", func() {
			// The ordering claim, stated as an outcome: the new name is
			// checked free BEFORE the old one is released. Release-then-check
			// would pass every other spec in this file and destroy a Queue
			// here.
			sp, req := own("renames-onto-foreign", first)
			foreign := newForeignQueue(second)

			sp.Spec.QueueName = second
			Expect(k8sClient.Update(ctx, sp)).To(Succeed())

			_, err := r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			// The foreign Queue is untouched...
			after := getQueue(second)
			Expect(after.GetUID()).To(Equal(foreign.GetUID()))
			Expect(after.GetResourceVersion()).To(Equal(foreign.GetResourceVersion()))
			Expect(after.GetLabels()).NotTo(HaveKey(policyNameLabel))

			// ...and so is the one this policy really owns, record included.
			Expect(getQueue(first).GetLabels()).To(HaveKeyWithValue(policyNameLabel, "renames-onto-foreign"))
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			Expect(sp.Status.OwnedQueue).To(Equal(first))
			Expect(findCondition(sp.Status.Conditions).Reason).To(Equal(reasonQueueNotOwned))
		})

		It("releases the Queue when spec.queueName is cleared", func() {
			sp, req := own("clears-its-queue", first)

			sp.Spec.QueueName = ""
			Expect(k8sClient.Update(ctx, sp)).To(Succeed())

			_, err := r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Expect(queueGone(first)).To(BeTrue())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			Expect(sp.Status.OwnedQueue).To(BeEmpty())
			Expect(sp.Status.OwnedQueueScheduler).To(BeEmpty())
		})

		It("releases the Volcano Queue when the scheduler moves to default", func() {
			// spec.scheduler is now "default", which has no Queue kind at all.
			// The release has to come off the RECORDED scheduler; a release
			// that read spec.scheduler would resolve no kind and leak the
			// Queue.
			sp, req := own("drops-its-scheduler", first)

			sp.Spec.Scheduler = "default"
			Expect(k8sClient.Update(ctx, sp)).To(Succeed())

			_, err := r().Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Expect(queueGone(first)).To(BeTrue())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
			Expect(sp.Status.OwnedQueue).To(BeEmpty())
		})
	})

	Context("a queue name the scheduler reserves", func() {
		for _, reserved := range []string{"default", "root"} {
			It("is refused, and is not deleted by any path: "+reserved, func() {
				// Both names exist on the live cluster, created by Volcano and
				// owned by nobody. "default" is the queue every PodGroup that
				// names none lands in, and Volcano's own admission webhook
				// refuses to delete it; "root" is the root of the hierarchy.
				// Planted here with no Frame marks, as they are there.
				planted := blankQueue(reserved)
				planted.Object["spec"] = map[string]any{"weight": int64(1)}
				Expect(k8sClient.Create(ctx, planted)).To(Succeed())
				DeferCleanup(func() { deleteQueue(reserved) })

				sp, req := newPolicy("names-reserved-"+reserved, reserved, 7)
				_, err := r().Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				_, err = r().Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())

				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), sp)).To(Succeed())
				Expect(controllerutil.ContainsFinalizer(sp, schedulingPolicyFinalizer)).To(BeFalse())
				Expect(sp.Status.OwnedQueue).To(BeEmpty())
				cond := findCondition(sp.Status.Conditions)
				Expect(cond).NotTo(BeNil())
				Expect(cond.Reason).To(Equal(reasonQueueReserved))

				// "whatever happens": even handed a forged ownership record
				// and a finalizer, the delete path must not touch it.
				controllerutil.AddFinalizer(sp, schedulingPolicyFinalizer)
				Expect(k8sClient.Update(ctx, sp)).To(Succeed())
				deleting := startDeleting(sp)
				deleting.Status.OwnedQueue = reserved
				deleting.Status.OwnedQueueScheduler = "volcano"
				_, err = r().reconcileDelete(ctx, deleting)
				Expect(err).NotTo(HaveOccurred())

				Expect(getQueue(reserved).GetUID()).To(Equal(planted.GetUID()))
			})
		}
	})

	It("will not write to a Queue it has not recorded, whatever the spec says", func() {
		// A direct call, because the only way to reach this state is to
		// reorder Reconcile — which is exactly the change that would bring the
		// bug back.
		sp := &framev1beta1.SchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "unrecorded-queue", Namespace: ns},
			Spec:       framev1beta1.SchedulingPolicySpec{Scheduler: "volcano", QueueName: "somebody-elses-queue"},
		}
		err := r().reconcileQueue(ctx, sp)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("refusing to write volcano Queue"))
		Expect(queueGone("somebody-elses-queue")).To(BeTrue())
	})

	It("will not write to a Queue recorded under a different scheduler", func() {
		// The name agrees and the scheduler does not. A guard that compared
		// only the name would write a Volcano Queue on the strength of a
		// YuniKorn record.
		sp := &framev1beta1.SchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "cross-scheduler", Namespace: ns},
			Spec:       framev1beta1.SchedulingPolicySpec{Scheduler: "volcano", QueueName: "cross-queue"},
			Status: framev1beta1.SchedulingPolicyStatus{
				OwnedQueue: "cross-queue", OwnedQueueScheduler: "yunikorn",
			},
		}
		err := r().reconcileQueue(ctx, sp)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("refusing to write volcano Queue"))
		Expect(queueGone("cross-queue")).To(BeTrue())
	})

	Context("an ownership record the cluster cannot resolve", func() {
		const queueName = "unresolvable"

		AfterEach(func() { deleteQueue(queueName) })

		It("does not wedge deletion when the recorded kind is not installed", func() {
			// yunikorn.apache.org has no CRD in this suite. reconcileDelete
			// must finish anyway: a Delete that errors here leaves the
			// finalizer on forever, over an object of a kind that cannot
			// exist.
			sp, _ := newPolicy("records-missing-kind", queueName, 7)
			controllerutil.AddFinalizer(sp, schedulingPolicyFinalizer)
			Expect(k8sClient.Update(ctx, sp)).To(Succeed())

			deleting := startDeleting(sp)
			deleting.Status.OwnedQueue = queueName
			deleting.Status.OwnedQueueScheduler = "yunikorn"
			_, err := r().reconcileDelete(ctx, deleting)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(sp), &framev1beta1.SchedulingPolicy{}))
			}, "5s").Should(BeTrue())
		})

		It("deletes nothing when the recorded scheduler has no Queue kind", func() {
			// The discriminating half: a Volcano Queue really does sit at this
			// name, and the record says the scheduler is "default", which owns
			// no Queue kind. A release that fell back to spec.scheduler, or
			// that guessed Volcano because Volcano is what is installed, would
			// delete it.
			planted := blankQueue(queueName)
			planted.SetLabels(map[string]string{
				policyNamespaceLabel: ns,
				policyNameLabel:      "records-kindless-scheduler",
			})
			planted.Object["spec"] = map[string]any{"weight": int64(1)}
			Expect(k8sClient.Create(ctx, planted)).To(Succeed())

			sp, _ := newPolicy("records-kindless-scheduler", queueName, 7)
			controllerutil.AddFinalizer(sp, schedulingPolicyFinalizer)
			Expect(k8sClient.Update(ctx, sp)).To(Succeed())

			deleting := startDeleting(sp)
			deleting.Status.OwnedQueue = queueName
			deleting.Status.OwnedQueueScheduler = "default"
			_, err := r().reconcileDelete(ctx, deleting)
			Expect(err).NotTo(HaveOccurred())

			Expect(getQueue(queueName).GetUID()).To(Equal(planted.GetUID()))
		})
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
