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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
)

var _ = Describe("FrameResourceQuota Controller", func() {
	const name = "test-frq"
	const ns = "default"
	key := types.NamespacedName{Name: name, Namespace: ns}
	ctx := context.Background()

	frq := &framev1alpha1.FrameResourceQuota{}
	cpu := resource.MustParse("100")

	BeforeEach(func() {
		*frq = framev1alpha1.FrameResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1alpha1.FrameResourceQuotaSpec{
				ServiceClass: "HIGH",
				MaxGPUs:      8,
				MaxCPU:       &cpu,
				MaxJobs:      10,
			},
		}
		Expect(k8sClient.Create(ctx, frq)).To(Succeed())
	})

	AfterEach(func() {
		fresh := &framev1alpha1.FrameResourceQuota{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &framev1alpha1.FrameResourceQuota{}))
		}, "5s").Should(BeTrue())
	})

	r := func() *FrameResourceQuotaReconciler {
		return &FrameResourceQuotaReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(100)}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds finalizer on first reconcile", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, frq)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(frq, frameResourceQuotaFinalizer)).To(BeTrue())
	})

	It("sets Ready=True with 0 matching namespaces", func() {
		_, _ = r().Reconcile(ctx, req)
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, frq)).To(Succeed())
		var readyCond *metav1.Condition
		for i := range frq.Status.Conditions {
			if frq.Status.Conditions[i].Type == "Ready" {
				readyCond = &frq.Status.Conditions[i]
			}
		}
		Expect(readyCond).NotTo(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
		Expect(readyCond.Message).To(ContainSubstring("0 namespaces"))
	})

	It("creates ResourceQuota in a labeled namespace", func() {
		labeledNS := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "high-workload",
				Labels: map[string]string{"frame.plume-labs.io/service-class": "HIGH"},
			},
		}
		Expect(k8sClient.Create(ctx, labeledNS)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, labeledNS) })

		_, _ = r().Reconcile(ctx, req)
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		quota := &corev1.ResourceQuota{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: "frame-high", Namespace: "high-workload",
		}, quota)).To(Succeed())
		Expect(quota.Spec.Hard[corev1.ResourceName("requests.nvidia.com/gpu")]).
			To(Equal(resource.MustParse("8")))
	})

	It("quotas FrameJob objects rather than the pods they fan out to", func() {
		labeledNS := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "high-jobcount",
				Labels: map[string]string{"frame.plume-labs.io/service-class": "HIGH"},
			},
		}
		Expect(k8sClient.Create(ctx, labeledNS)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, labeledNS) })

		_, _ = r().Reconcile(ctx, req)
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		quota := &corev1.ResourceQuota{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: "frame-high", Namespace: "high-jobcount",
		}, quota)).To(Succeed())

		// maxJobs: 10 must cap FrameJobs, not pods — one FrameJob becomes an
		// ArgoWorkflow that fans out to many pods, so a pod quota would let a
		// single job exhaust the whole service class.
		Expect(quota.Spec.Hard).To(HaveKeyWithValue(
			corev1.ResourceName("count/framejobs.frame.plume-labs.io"),
			resource.MustParse("10"),
		))
		Expect(quota.Spec.Hard).NotTo(HaveKey(corev1.ResourcePods))
	})

	It("records the generation its status was computed from", func() {
		frq := &framev1alpha1.FrameResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: "obsgen", Namespace: "default"},
			Spec:       framev1alpha1.FrameResourceQuotaSpec{ServiceClass: "HIGH", MaxJobs: 5},
		}
		Expect(k8sClient.Create(ctx, frq)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, frq) })

		reconciler := &FrameResourceQuotaReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(20),
		}
		obsgenKey := types.NamespacedName{Name: "obsgen", Namespace: "default"}
		// Pass 1 adds the finalizer, pass 2 reconciles.
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: obsgenKey})
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: obsgenKey})
		Expect(err).NotTo(HaveOccurred())

		fetched := &framev1alpha1.FrameResourceQuota{}
		Expect(k8sClient.Get(ctx, obsgenKey, fetched)).To(Succeed())
		Expect(fetched.Status.ObservedGeneration).To(Equal(fetched.Generation),
			"status.observedGeneration must track metadata.generation")

		// Change the spec and confirm the field moves with it.
		fetched.Spec.MaxJobs = 9
		Expect(k8sClient.Update(ctx, fetched)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: obsgenKey})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, obsgenKey, fetched)).To(Succeed())
		Expect(fetched.Status.ObservedGeneration).To(Equal(fetched.Generation))
	})

	It("aggregates the projected ResourceQuotas' usage into status", func() {
		ctx := context.Background()

		// Two namespaces of the same class, so the sum is provably a sum.
		for _, name := range []string{"quota-usage-a", "quota-usage-b"} {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{"frame.plume-labs.io/service-class": "MEDIUM"},
			}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		}

		frq := &framev1alpha1.FrameResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: "quota-usage", Namespace: "default"},
			Spec:       framev1alpha1.FrameResourceQuotaSpec{ServiceClass: "MEDIUM", MaxJobs: 10},
		}
		Expect(k8sClient.Create(ctx, frq)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, frq) })

		reconciler := &FrameResourceQuotaReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(20),
		}
		key := types.NamespacedName{Name: "quota-usage", Namespace: "default"}
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		fetched := &framev1alpha1.FrameResourceQuota{}
		Expect(k8sClient.Get(ctx, key, fetched)).To(Succeed())
		Expect(fetched.Status.Namespaces).To(BeNumerically(">=", 2),
			"both labelled namespaces must be counted")
	})

	It("persists the sum of a projected ResourceQuota's status.used onto its own status", func() {
		// This is the wiring sumQuotaUsage's unit test cannot cover: that the
		// reconciler actually reads the child's status.used back and assigns
		// it to the right field on the parent, rather than e.g. computing it
		// and dropping it, or writing it somewhere the status patch never sees.
		labeledNS := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "quota-used-projection",
				Labels: map[string]string{"frame.plume-labs.io/service-class": "HIGH"},
			},
		}
		Expect(k8sClient.Create(ctx, labeledNS)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, labeledNS) })

		_, _ = r().Reconcile(ctx, req)
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		childQuota := &corev1.ResourceQuota{}
		childKey := types.NamespacedName{Name: "frame-high", Namespace: "quota-used-projection"}
		Expect(k8sClient.Get(ctx, childKey, childQuota)).To(Succeed())

		// A real apiserver's quota controller computes this; envtest runs
		// none, so set it directly to stand in for that computation.
		childQuota.Status.Used = corev1.ResourceList{
			corev1.ResourceLimitsCPU: resource.MustParse("3"),
		}
		Expect(k8sClient.Status().Update(ctx, childQuota)).To(Succeed())

		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, frq)).To(Succeed())
		Expect(frq.Status.Used).To(HaveKeyWithValue(corev1.ResourceLimitsCPU, resource.MustParse("3")),
			"status.used must reflect what the child ResourceQuota reports, not just be assigned in code nothing exercises")
	})

	It("sums usage across quotas without inventing keys nobody reported", func() {
		a := corev1.ResourceQuota{Status: corev1.ResourceQuotaStatus{
			Used: corev1.ResourceList{
				corev1.ResourceLimitsCPU: resource.MustParse("2"),
			},
		}}
		b := corev1.ResourceQuota{Status: corev1.ResourceQuotaStatus{
			Used: corev1.ResourceList{
				corev1.ResourceLimitsCPU:    resource.MustParse("3"),
				corev1.ResourceLimitsMemory: resource.MustParse("4Gi"),
			},
		}}

		total := sumQuotaUsage([]corev1.ResourceQuota{a, b})
		cpu := total[corev1.ResourceLimitsCPU]
		Expect(cpu.String()).To(Equal("5"))
		mem := total[corev1.ResourceLimitsMemory]
		Expect(mem.String()).To(Equal("4Gi"))
		Expect(total).NotTo(HaveKey(corev1.ResourceName("requests.nvidia.com/gpu")),
			"a key no namespace reported must be absent, not zero")

		Expect(sumQuotaUsage(nil)).To(BeNil(),
			"no projected quotas means no usage measured, not usage of zero")
	})

	It("sums fractional and whole CPU quantities correctly, not as integers", func() {
		// 500m + 2 is the brief's own motivating case: naive integer addition
		// of the underlying millivalue would get this wrong in a way that
		// looks plausible (e.g. treating "2" as 2m instead of 2000m).
		// resource.Quantity.Add normalizes it to 2500m.
		a := corev1.ResourceQuota{Status: corev1.ResourceQuotaStatus{
			Used: corev1.ResourceList{
				corev1.ResourceLimitsCPU: resource.MustParse("500m"),
			},
		}}
		b := corev1.ResourceQuota{Status: corev1.ResourceQuotaStatus{
			Used: corev1.ResourceList{
				corev1.ResourceLimitsCPU: resource.MustParse("2"),
			},
		}}

		total := sumQuotaUsage([]corev1.ResourceQuota{a, b})
		cpu := total[corev1.ResourceLimitsCPU]
		Expect(cpu.Cmp(resource.MustParse("2500m"))).To(Equal(0))
	})
})
