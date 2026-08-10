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

package services

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

	servicesv1beta1 "github.com/rmocq/frame/api/services/v1beta1"
	"github.com/rmocq/frame/internal/services/provider"
)

// fakeProvisioner records what the controller asked of it, so the tests assert
// the reconcile loop's behaviour rather than any real provider's.
type fakeProvisioner struct {
	result     provider.Result
	err        error
	binding    provider.Binding
	reconciles int
}

func (f *fakeProvisioner) Type() string                      { return "fake" }
func (f *fakeProvisioner) ParameterSchema() *provider.Schema { return &provider.Schema{Type: "object"} }
func (f *fakeProvisioner) Size(map[string]string) (provider.Sizing, error) {
	return provider.Sizing{GPU: "1", GPUMemory: "512Mi", CPU: "1", Memory: "1Gi"}, nil
}
func (f *fakeProvisioner) Reconcile(context.Context, *servicesv1beta1.FrameService) (provider.Result, error) {
	f.reconciles++
	return f.result, f.err
}
func (f *fakeProvisioner) Bind(context.Context, *servicesv1beta1.FrameService) (provider.Binding, error) {
	return f.binding, nil
}

var _ = Describe("FrameService Controller", func() {
	const name = "test-svc"
	const ns = "default"
	key := types.NamespacedName{Name: name, Namespace: ns}
	ctx := context.Background()

	var svc *servicesv1beta1.FrameService
	var fake *fakeProvisioner

	BeforeEach(func() {
		fake = &fakeProvisioner{
			result:  provider.Result{Ready: true, Reason: "Provisioned", Message: "Serving"},
			binding: provider.Binding{Endpoint: "http://test-svc.default.svc:8080"},
		}
		svc = &servicesv1beta1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       servicesv1beta1.FrameServiceSpec{Type: "fake", DeletionPolicy: "Retain"},
		}
		Expect(k8sClient.Create(ctx, svc)).To(Succeed())
	})

	AfterEach(func() {
		fresh := &servicesv1beta1.FrameService{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
		// A spec that reaches Ready has reconcileBinding write a real
		// credentials Secret at (ns, name) via the envtest apiserver, which
		// runs no garbage-collector controller: nothing ever removes it on
		// its own the way a real cluster's owner-reference GC would. Left
		// uncleaned, it would outlive this spec and collide with the next
		// one that reaches Ready under the same coordinate, exactly the way
		// claimNewCoordinates is designed to catch a real foreign Secret —
		// see binding_test.go's AfterEach, which cleans up for the same
		// reason.
		_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}})
	})

	r := func() *FrameServiceReconciler {
		return &FrameServiceReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(100),
			Registry: provider.NewRegistry(fake),
		}
	}
	req := reconcile.Request{NamespacedName: key}

	It("adds its finalizer before doing anything else", func() {
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(svc, frameServiceFinalizer)).To(BeTrue())
		// The provider must not run before the finalizer is durable: a crash in
		// between would leave a provisioned instance with nothing tracking it.
		Expect(fake.reconciles).To(Equal(0))
	})

	It("reports Ready and publishes the endpoint and the derived sizing", func() {
		_, _ = r().Reconcile(ctx, req)
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(svc.Status.Binding.Endpoint).To(Equal("http://test-svc.default.svc:8080"))
		Expect(svc.Status.Sizing.GPUMemory).To(Equal("512Mi"))
		Expect(readyCondition(svc).Status).To(Equal(metav1.ConditionTrue))
		Expect(readyCondition(svc).Reason).To(Equal("Provisioned"))
	})

	// A Ready instance must still be requeued on a timer. This controller
	// deliberately does not watch Secrets (see SetupWithManager), so this
	// requeue is the only thing that converges a binding Secret deleted or
	// overwritten out of band. Returning a bare ctrl.Result{} here would not
	// mean "never" — it would silently fall back to the informer resync, which
	// is controller-runtime's 10h default because cmd/main.go sets no
	// SyncPeriod. That is a repair window three orders of magnitude wider than
	// intended, and it would not fail any other test in this file.
	It("requeues even when Ready, so an out-of-band Secret edit is converged", func() {
		_, _ = r().Reconcile(ctx, req)
		res, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(readyCondition(svc).Status).To(Equal(metav1.ConditionTrue))

		Expect(res.RequeueAfter).To(Equal(readyRequeue))
		// Bounded on both sides rather than just asserted equal to the
		// constant: the point is the magnitude, and a future edit that made
		// this an hour would still pass an equality-only check after the
		// constant moved with it.
		Expect(res.RequeueAfter).To(BeNumerically(">=", 5*time.Minute))
		Expect(res.RequeueAfter).To(BeNumerically("<=", 15*time.Minute))
	})

	It("reports Degraded without wedging when the provider cannot finish", func() {
		fake.result = provider.Result{Ready: false, Reason: "OperatorMissing", Message: "postgresqls CRD absent"}
		_, _ = r().Reconcile(ctx, req)
		res, err := r().Reconcile(ctx, req)

		// Degrading is not an error: returning one would back off and hide the
		// reason behind controller-runtime's retry logging.
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(readyCondition(svc).Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCondition(svc).Reason).To(Equal("OperatorMissing"))
	})

	It("keeps a previously recorded status.Provisioned when a later degrade reports none", func() {
		provisioned := []servicesv1beta1.ProvisionedRef{
			{APIVersion: "apps/v1", Kind: "Deployment", Name: name, Namespace: ns},
		}
		fake.result = provider.Result{Ready: true, Reason: "Provisioned", Message: "Serving", Provisioned: provisioned}
		_, _ = r().Reconcile(ctx, req) // lands the finalizer
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(readyCondition(svc).Status).To(Equal(metav1.ConditionTrue))
		Expect(svc.Status.Provisioned).To(Equal(provisioned))

		// A later pass degrades without reporting any Provisioned at all —
		// exactly what the inference provider's ModelCacheMissing and
		// ModelCacheCheckFailed do. That must not erase the earlier record:
		// it is the only handle reconcileDelete has on data objects under
		// deletionPolicy: Delete, and erasing it here would silently make
		// deletion delete nothing.
		fake.result = provider.Result{Ready: false, Reason: "ModelCacheMissing", Message: "cache gone"}
		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(readyCondition(svc).Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCondition(svc).Reason).To(Equal("ModelCacheMissing"))
		Expect(svc.Status.Provisioned).To(Equal(provisioned))
	})

	It("refuses to reconcile an unknown type, and says so in status", func() {
		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		svc.Spec.Type = "nonexistent"
		Expect(k8sClient.Update(ctx, svc)).To(Succeed())

		_, _ = r().Reconcile(ctx, req)
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(readyCondition(svc).Status).To(Equal(metav1.ConditionFalse))
		Expect(readyCondition(svc).Reason).To(Equal("UnknownType"))
	})

	It("releases its finalizer on delete", func() {
		_, _ = r().Reconcile(ctx, req)
		Expect(k8sClient.Delete(ctx, svc)).To(Succeed())

		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &servicesv1beta1.FrameService{}))
		}, "5s").Should(BeTrue())
	})

	It("deletes what status.Provisioned lists when the policy is Delete", func() {
		_, _ = r().Reconcile(ctx, req) // lands the finalizer

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		svc.Spec.DeletionPolicy = "Delete"
		Expect(k8sClient.Update(ctx, svc)).To(Succeed())

		// A ConfigMap stands in for a data object a real provider would list —
		// the controller only ever acts on the apiVersion/kind/name/namespace in
		// status.Provisioned, never on what kind of object it actually is.
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "provisioned-delete", Namespace: ns}}
		Expect(k8sClient.Create(ctx, cm)).To(Succeed())
		cmKey := types.NamespacedName{Name: cm.Name, Namespace: cm.Namespace}

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		svc.Status.Provisioned = []servicesv1beta1.ProvisionedRef{
			{APIVersion: "v1", Kind: "ConfigMap", Name: cm.Name, Namespace: cm.Namespace},
		}
		Expect(k8sClient.Status().Update(ctx, svc)).To(Succeed())

		Expect(k8sClient.Delete(ctx, svc)).To(Succeed())
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &servicesv1beta1.FrameService{}))
		}, "5s").Should(BeTrue())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, cmKey, &corev1.ConfigMap{}))).To(BeTrue())
	})

	It("leaves status.Provisioned in place when the policy is Retain", func() {
		_, _ = r().Reconcile(ctx, req) // lands the finalizer; DeletionPolicy stays Retain from BeforeEach

		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "provisioned-retain", Namespace: ns}}
		Expect(k8sClient.Create(ctx, cm)).To(Succeed())
		cmKey := types.NamespacedName{Name: cm.Name, Namespace: cm.Namespace}
		defer func() { _ = k8sClient.Delete(ctx, cm) }()

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		svc.Status.Provisioned = []servicesv1beta1.ProvisionedRef{
			{APIVersion: "v1", Kind: "ConfigMap", Name: cm.Name, Namespace: cm.Namespace},
		}
		Expect(k8sClient.Status().Update(ctx, svc)).To(Succeed())

		Expect(k8sClient.Delete(ctx, svc)).To(Succeed())
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &servicesv1beta1.FrameService{}))
		}, "5s").Should(BeTrue())
		// Retain relies on owner-reference GC for exposing objects; a data
		// object named in status.Provisioned is never touched by reconcileDelete.
		Expect(k8sClient.Get(ctx, cmKey, &corev1.ConfigMap{})).To(Succeed())
	})
})

func readyCondition(svc *servicesv1beta1.FrameService) metav1.Condition {
	for _, c := range svc.Status.Conditions {
		if c.Type == "Ready" {
			return c
		}
	}
	return metav1.Condition{}
}
