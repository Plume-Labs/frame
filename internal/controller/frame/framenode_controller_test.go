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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

var _ = Describe("FrameNode Controller", func() {
	const name = "test-framenode"
	const ns = "default"
	key := types.NamespacedName{Name: name, Namespace: ns}
	ctx := context.Background()

	fn := &framev1beta1.FrameNode{}

	BeforeEach(func() {
		*fn = framev1beta1.FrameNode{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.FrameNodeSpec{
				IP: "10.0.0.1", Role: "worker", Disk: "/dev/nvme0n1",
				Rack: "rack1", Zone: "zone-a", ServiceClass: "HIGH",
				Network: framev1beta1.NetworkSpec{
					Address: "10.0.0.1/24",
					Gateway: "10.0.0.1",
					DNS:     []string{"1.1.1.1"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, fn)).To(Succeed())
	})

	AfterEach(func() {
		fresh := &framev1beta1.FrameNode{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &framev1beta1.FrameNode{}))
		}, "5s").Should(BeTrue())
	})

	r := func() *FrameNodeReconciler {
		return &FrameNodeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(100)}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds finalizer on first reconcile", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, fn)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(fn, frameNodeFinalizer)).To(BeTrue())
	})

	It("sets phase=Provisioning when k8s Node does not exist", func() {
		_, _ = r().Reconcile(ctx, req) // add finalizer
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, fn)).To(Succeed())
		Expect(nodePhaseFromStatus(fn)).To(Equal("Provisioning"))
	})

	It("sets Ready=False condition when Provisioning", func() {
		_, _ = r().Reconcile(ctx, req)
		_, _ = r().Reconcile(ctx, req)

		Expect(k8sClient.Get(ctx, key, fn)).To(Succeed())
		var readyCond *metav1.Condition
		for i := range fn.Status.Conditions {
			if fn.Status.Conditions[i].Type == "Ready" {
				readyCond = &fn.Status.Conditions[i]
			}
		}
		Expect(readyCond).NotTo(BeNil())
		Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCond.Reason).To(Equal("Provisioning"))
	})

	// The defect this closes, measured on the live cluster on 2026-09-12: three
	// FrameNode objects 48 days old, all with no spec.disk, all stopped at
	// Discovered -- and not one node carrying frame.plume-labs.io/service-class,
	// while the inference provider uses exactly that label as the nodeSelector
	// of every Deployment it creates. Label projection lived only at the end of
	// the provisioning path, so an unprovisioned FrameNode never reached it.
	//
	// This goes through Reconcile rather than calling reconcileOnline directly.
	// That distinction is the whole test: the existing projection test calls
	// reconcileOnline, which is why it passed for 48 days while nothing in the
	// cluster could reach that function.
	It("projects labels onto an existing node even when the FrameNode was never provisioned", func() {
		ctx := context.Background()

		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "unprovisioned-node"}}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		fn := &framev1beta1.FrameNode{
			ObjectMeta: metav1.ObjectMeta{Name: "unprovisioned", Namespace: "default"},
			Spec: framev1beta1.FrameNodeSpec{
				IP:           "127.0.0.1",
				Hostname:     "unprovisioned-node",
				Role:         "worker",
				Rack:         "rack-09",
				ServiceClass: "HIGH",
				// Disk deliberately empty: this machine was never provisioned
				// by Frame, which is true of every node in this estate.
			},
		}
		Expect(k8sClient.Create(ctx, fn)).To(Succeed())
		DeferCleanup(func() {
			fresh := &framev1beta1.FrameNode{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "unprovisioned", Namespace: "default"}, fresh); err == nil {
				fresh.Finalizers = nil
				_ = k8sClient.Update(ctx, fresh)
				_ = k8sClient.Delete(ctx, fresh)
			}
		})

		reconciler := &FrameNodeReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(20),
		}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "unprovisioned", Namespace: "default"}}
		// The first pass only adds the finalizer and returns.
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		fetched := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "unprovisioned-node"}, fetched)).To(Succeed())
		Expect(fetched.Labels).To(HaveKeyWithValue(nodeLabelServiceClass, "HIGH"),
			"the label the inference provider selects on")
		Expect(fetched.Labels).To(HaveKeyWithValue(nodeLabelRack, "rack-09"))
		Expect(fetched.Labels).To(HaveKeyWithValue(nodeLabelRole, "worker"))

		// And it must not report a discovery it never performed.
		updated := &framev1beta1.FrameNode{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "unprovisioned", Namespace: "default"}, updated)).To(Succeed())
		Expect(nodePhaseFromStatus(updated)).NotTo(Equal(nodePhaseDiscovered),
			"a node that is in the cluster is not awaiting discovery")
	})

	// The escalation the classification guards close. A principal bound to
	// framenode-editor-role has no RBAC on Node objects at all; without these
	// guards it could create a bare FrameNode named after a classified node
	// and have applyNodeLabel strip every Frame label off it -- service-class
	// included, which is the key the inference provider selects on -- with no
	// admission rejection and nothing in the node's own audit trail.
	It("refuses to strip a node's labels for a FrameNode that classifies nothing", func() {
		ctx := context.Background()

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "already-classified-node",
				Labels: map[string]string{
					nodeLabelServiceClass: "HIGH",
					nodeLabelRack:         "rack-01",
				},
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		// Every classification field empty: this object has nothing to say.
		fn := &framev1beta1.FrameNode{
			ObjectMeta: metav1.ObjectMeta{Name: "empty-claimant", Namespace: "default"},
			Spec:       framev1beta1.FrameNodeSpec{IP: "127.0.0.1", Hostname: "already-classified-node"},
		}
		Expect(k8sClient.Create(ctx, fn)).To(Succeed())
		DeferCleanup(func() {
			fresh := &framev1beta1.FrameNode{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "empty-claimant", Namespace: "default"}, fresh); err == nil {
				fresh.Finalizers = nil
				_ = k8sClient.Update(ctx, fresh)
				_ = k8sClient.Delete(ctx, fresh)
			}
		})

		reconciler := &FrameNodeReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(20),
		}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "empty-claimant", Namespace: "default"}}
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		fetched := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "already-classified-node"}, fetched)).To(Succeed())
		Expect(fetched.Labels).To(HaveKeyWithValue(nodeLabelServiceClass, "HIGH"),
			"an object with nothing to project must not unclassify a node")
		Expect(fetched.Labels).To(HaveKeyWithValue(nodeLabelRack, "rack-01"))

		updated := &framev1beta1.FrameNode{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "empty-claimant", Namespace: "default"}, updated)).To(Succeed())
		Expect(nodePhaseFromStatus(updated)).To(Equal(nodePhaseUnclassified))
	})

	It("refuses a second FrameNode claiming a node another one already classifies", func() {
		ctx := context.Background()

		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "contested-node"}}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		mk := func(name, class string) *framev1beta1.FrameNode {
			fn := &framev1beta1.FrameNode{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: framev1beta1.FrameNodeSpec{
					IP: "127.0.0.1", Hostname: "contested-node", Role: "worker",
					ServiceClass: framev1beta1.ServiceClass(class),
				},
			}
			Expect(k8sClient.Create(ctx, fn)).To(Succeed())
			DeferCleanup(func() {
				fresh := &framev1beta1.FrameNode{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, fresh); err == nil {
					fresh.Finalizers = nil
					_ = k8sClient.Update(ctx, fresh)
					_ = k8sClient.Delete(ctx, fresh)
				}
			})
			return fn
		}

		first := mk("owner", "HIGH")
		// A distinct creation timestamp is what decides which object owns the
		// node, and envtest's clock has second granularity.
		time.Sleep(1100 * time.Millisecond)
		second := mk("usurper", "LOW")

		reconciler := &FrameNodeReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(20),
		}
		for _, fn := range []*framev1beta1.FrameNode{first, second} {
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: fn.Name, Namespace: "default"}}
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}

		fetched := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "contested-node"}, fetched)).To(Succeed())
		Expect(fetched.Labels).To(HaveKeyWithValue(nodeLabelServiceClass, "HIGH"),
			"the node keeps answering to the object that claimed it first")

		usurper := &framev1beta1.FrameNode{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "usurper", Namespace: "default"}, usurper)).To(Succeed())
		Expect(nodePhaseFromStatus(usurper)).To(Equal(nodePhaseUnclassified))
	})

	It("projects the frame-prefixed rack label and skips empty values", func() {
		ctx := context.Background()

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "label-projection-node",
				Labels: map[string]string{"topology.kubernetes.io/rack": "stale"},
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		fn := &framev1beta1.FrameNode{
			ObjectMeta: metav1.ObjectMeta{Name: "label-projection", Namespace: "default"},
			Spec: framev1beta1.FrameNodeSpec{
				IP:           "127.0.0.1",
				Role:         "worker",
				Hostname:     "label-projection-node",
				Rack:         "rack-07",
				ServiceClass: "HIGH",
				// Zone and RDMAInterface deliberately left empty.
			},
		}
		Expect(k8sClient.Create(ctx, fn)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, fn) })

		reconciler := &FrameNodeReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(20),
		}
		_, err := reconciler.reconcileOnline(ctx, fn)
		Expect(err).NotTo(HaveOccurred())

		fetched := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "label-projection-node"}, fetched)).To(Succeed())
		Expect(fetched.Labels).To(HaveKeyWithValue(nodeLabelRack, "rack-07"))
		Expect(fetched.Labels).To(HaveKeyWithValue(nodeLabelServiceClass, "HIGH"))
		Expect(fetched.Labels).To(HaveKeyWithValue(nodeLabelRole, "worker"))
		Expect(fetched.Labels).NotTo(HaveKey(nodeLabelZone), "an empty zone must not be written")
		Expect(fetched.Labels).NotTo(HaveKey(nodeLabelRDMA), "no RDMA interface, no label")
		Expect(fetched.Labels).NotTo(HaveKey("topology.kubernetes.io/rack"),
			"the reserved-prefix key must be cleaned up on reconcile")
	})
})
