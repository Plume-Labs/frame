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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

var _ = Describe("FrameStorage controller", func() {
	var reconciler *FrameStorageReconciler

	BeforeEach(func() {
		reconciler = &FrameStorageReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	})

	It("derive shared du type et ne le lit jamais du spec", func(ctx SpecContext) {
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "derived-shared"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "ceph-rbd", Content: []string{"workload"},
				StorageClassName: "frame-derived-shared", AdoptExisting: false,
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Shared).To(BeTrue())
	})

	It("compte les PVC de sa classe et ceux qui portent l'etiquette", func(ctx SpecContext) {
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "claim-gap"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "ceph-rbd", Content: []string{"workload"},
				StorageClassName: "frame-claim-gap",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		class := "frame-claim-gap"
		mk := func(name string, labels map[string]string) *corev1.PersistentVolumeClaim {
			return &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: labels},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: &class,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
					},
				},
			}
		}
		Expect(k8sClient.Create(ctx, mk("gap-unlabelled-1", nil))).To(Succeed())
		Expect(k8sClient.Create(ctx, mk("gap-unlabelled-2", nil))).To(Succeed())
		Expect(k8sClient.Create(ctx, mk("gap-labelled", map[string]string{"frame.plume-labs.io/usage": "workload"}))).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Claims).NotTo(BeNil())
		Expect(back.Status.Claims.Total).To(Equal(int32(3)))
		Expect(back.Status.Claims.Labelled).To(Equal(int32(1)),
			"the gap between the policy and the cluster is the number that gets displayed")
	})

	It("ne compte pas les PVC d'une autre classe", func(ctx SpecContext) {
		// Without this, Total is just "every PVC in the cluster" and the
		// gap it reports belongs to no entry in particular.
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "class-scoped"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "local-path", Content: []string{"scratch"},
				StorageClassName: "frame-class-scoped",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		other := "some-other-class"
		Expect(k8sClient.Create(ctx, &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "elsewhere", Namespace: "default"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: &other,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		})).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Claims.Total).To(Equal(int32(0)))
	})

	It("marque une entree adoptee et ne cree pas sa classe", func(ctx SpecContext) {
		Expect(k8sClient.Create(ctx, &storagev1.StorageClass{
			ObjectMeta:  metav1.ObjectMeta{Name: "pre-existing"},
			Provisioner: "kubernetes.io/no-provisioner",
		})).To(Succeed())

		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "adopted"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "local-path", Content: []string{"scratch"},
				StorageClassName: "pre-existing", AdoptExisting: true,
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Adopted).To(BeTrue())

		var sc storagev1.StorageClass
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "pre-existing"}, &sc)).To(Succeed())
		// An adopted class keeps no owner reference: an owned object is
		// garbage-collected with its owner, and this one holds volumes.
		Expect(sc.OwnerReferences).To(BeEmpty())
	})

	It("rend Degraded avec le motif quand Ceph est en WARN", func(ctx SpecContext) {
		// A CephCluster CR, as rook publishes it. envtest has no rook CRD, so
		// the reconciler reads it unstructured and a missing CRD is not an
		// error — see the reconciler's comment.
		cc := &unstructured.Unstructured{}
		cc.SetGroupVersionKind(schema.GroupVersionKind{Group: "ceph.rook.io", Version: "v1", Kind: "CephCluster"})
		cc.SetName("rook-ceph")
		cc.SetNamespace("default")
		Expect(unstructured.SetNestedMap(cc.Object, map[string]any{
			"health": "HEALTH_WARN",
		}, "status", "ceph")).To(Succeed())

		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "ceph-health"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "ceph-rbd", Content: []string{"workload"},
				StorageClassName: "frame-ceph-health",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		r := &FrameStorageReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), CephHealth: func(context.Context) (string, []string, error) {
			return "HEALTH_WARN", []string{"1 pool(s) have no replicas configured"}, nil
		}}
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Degraded"))
		cond := meta.FindStatusCondition(back.Status.Conditions, "Healthy")
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Message).To(ContainSubstring("no replicas"),
			"a degraded state without its reason cannot be acted on")
	})

	It("laisse Unknown une entree non-Ceph", func(ctx SpecContext) {
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "local-unknown"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "local-path", Content: []string{"scratch"},
				StorageClassName: "frame-local-unknown",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		// local-path has no cluster-wide health to report. Claiming Ready
		// would be asserting something nothing measured.
		Expect(back.Status.Phase).To(Equal("Unknown"))
	})

	It("laisse Unknown, jamais Ready, quand le controle de sante echoue", func(ctx SpecContext) {
		// The third branch of reconcileHealth: the check could not run at
		// all. "Not knowing" must render as Unknown with a reason, never as
		// Ready — a health check that silently fails open is worse than no
		// health check.
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "ceph-check-failed"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "ceph-rbd", Content: []string{"workload"},
				StorageClassName: "frame-ceph-check-failed",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		r := &FrameStorageReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), CephHealth: func(context.Context) (string, []string, error) {
			return "", nil, errors.New("no matches for kind \"CephCluster\" in version \"ceph.rook.io/v1\"")
		}}
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Phase).To(Equal("Unknown"))
		Expect(back.Status.Phase).NotTo(Equal("Ready"))
		cond := meta.FindStatusCondition(back.Status.Conditions, "Healthy")
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionUnknown))
		Expect(cond.Reason).To(Equal("CheckFailed"))
		Expect(cond.Message).To(ContainSubstring("CephCluster"))
	})

	It("calcule l'utilisable a partir du brut et de la replication", func(ctx SpecContext) {
		// Rule 1: a known replication factor n >= 1 divides raw into usable,
		// and raw is still reported alongside it.
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "ceph-capacity-known"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "ceph-rbd", Content: []string{"workload"},
				StorageClassName: "frame-ceph-capacity-known",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		const oneTiB = uint64(1) << 40
		r := &FrameStorageReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), CephCapacity: func(context.Context, string) (uint64, uint64, int32, error) {
			return 3 * oneTiB, oneTiB, 3, nil
		}}
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Capacity).NotTo(BeNil())
		Expect(back.Status.Capacity.Raw).To(Equal("3Ti"))
		Expect(back.Status.Capacity.Used).To(Equal("1Ti"))
		Expect(back.Status.Capacity.Usable).To(Equal("1Ti"),
			"3Ti raw at replication 3 is 1Ti usable, not 3Ti")
	})

	It("ne rend aucun utilisable quand la replication est inconnue", func(ctx SpecContext) {
		// Rule 2, the load-bearing one: an unknown, zero, or negative
		// replication factor must never fall back to reporting raw as
		// usable. That silent fallback is the capacity incident this rule
		// exists to prevent, reproduced exactly while looking correct.
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "ceph-capacity-unknown-replication"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "ceph-rbd", Content: []string{"workload"},
				StorageClassName: "frame-ceph-capacity-unknown-replication",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		const oneTiB = uint64(1) << 40
		r := &FrameStorageReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), CephCapacity: func(context.Context, string) (uint64, uint64, int32, error) {
			return 3 * oneTiB, oneTiB, 0, nil
		}}
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Capacity).NotTo(BeNil())
		Expect(back.Status.Capacity.Usable).To(BeEmpty(),
			"no usable figure at all, not raw standing in for it")
	})

	It("laisse la capacite intacte et la reconciliation en succes quand la lecture de capacite echoue", func(ctx SpecContext) {
		// Rule 3: a capacity lookup failure must not fail the reconcile and
		// must not flip an otherwise-Ready entry -- same "not knowing is not
		// health" rule as reconcileHealth's CheckFailed branch.
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "ceph-capacity-lookup-failed"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "ceph-rbd", Content: []string{"workload"},
				StorageClassName: "frame-ceph-capacity-lookup-failed",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		r := &FrameStorageReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(),
			CephHealth: func(context.Context) (string, []string, error) {
				return "HEALTH_OK", nil, nil
			},
			CephCapacity: func(context.Context, string) (uint64, uint64, int32, error) {
				return 0, 0, 0, errors.New("reading CephCluster rook-ceph/rook-ceph: connection refused")
			},
		}
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Capacity).To(BeNil())
		Expect(back.Status.Phase).To(Equal("Ready"),
			"a capacity failure must never flip an otherwise-healthy entry")
	})

	It("laisse la capacite absente pour une entree local-path", func(ctx SpecContext) {
		// Rule 4: no cluster-wide usable figure means anything for local
		// storage; inventing one would be worse than absence. Also covers
		// CephCapacity == nil, the default for every other spec in this file.
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "local-path-capacity"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "local-path", Content: []string{"scratch"},
				StorageClassName: "frame-local-path-capacity",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Capacity).To(BeNil())
	})

	It("porte la disponibilite par noeud dans une condition", func(ctx SpecContext) {
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "two-nodes"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "local-path", Content: []string{"scratch"},
				StorageClassName: "frame-two-nodes",
				Nodes:            []string{"w1", "w2"},
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		cond := meta.FindStatusCondition(back.Status.Conditions, "Available")
		Expect(cond).NotTo(BeNil())
		Expect(cond.Message).To(Equal("w1, w2"))
	})

	It("dit 'all nodes' quand la liste est vide", func(ctx SpecContext) {
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "all-nodes"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "ceph-rbd", Content: []string{"workload"},
				StorageClassName: "frame-all-nodes",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(fs)})
		Expect(err).NotTo(HaveOccurred())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(meta.FindStatusCondition(back.Status.Conditions, "Available").Message).To(Equal("all nodes"))
	})

	It("refuse un type de contenu hors de la liste", func(ctx SpecContext) {
		// The content enum is the only thing standing between a typed entry
		// and a free-text field. If the CRD does not enforce it, the
		// content-type webhook later compares against values nobody validated.
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-content"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "ceph-rbd", Content: []string{"workload", "nonsense"},
				StorageClassName: "frame-bad-content",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).NotTo(Succeed())
	})

	It("garde capacite, comptes et conditions a l'aller-retour", func(ctx SpecContext) {
		fs := &framev1beta1.FrameStorage{
			ObjectMeta: metav1.ObjectMeta{Name: "roundtrip"},
			Spec: framev1beta1.FrameStorageSpec{
				Type: "ceph-rbd", Content: []string{"workload", "model"},
				StorageClassName: "frame-roundtrip",
			},
		}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())

		fs.Status.Capacity = &framev1beta1.StorageCapacity{Usable: "1.2Ti", Used: "480Gi", Raw: "3.6Ti"}
		fs.Status.Claims = &framev1beta1.ClaimCounts{Total: 16, Labelled: 0}
		fs.Status.Shared = true
		fs.Status.Phase = "Degraded"
		Expect(k8sClient.Status().Update(ctx, fs)).To(Succeed())

		var back framev1beta1.FrameStorage
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(fs), &back)).To(Succeed())
		Expect(back.Status.Capacity.Usable).To(Equal("1.2Ti"))
		Expect(back.Status.Capacity.Raw).To(Equal("3.6Ti"))
		Expect(back.Status.Claims.Total).To(Equal(int32(16)))
		Expect(back.Status.Claims.Labelled).To(Equal(int32(0)))
		Expect(back.Spec.Content).To(Equal([]string{"workload", "model"}))
	})
})
