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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	"github.com/rmocq/frame/internal/services/provider"
)

// These specs use two real, separate namespaces for projection — distinct
// from the FrameService's own namespace ("default") — so that a bug in
// resolving "which namespace does this Secret belong in" would actually be
// caught. A fixture that put the projected copy in the same namespace as the
// service could not tell a correct projection from one that never left the
// service's own namespace at all.
const (
	bindingProjectedNamespaceA = "binding-project-alpha"
	bindingProjectedNamespaceB = "binding-project-beta"
)

var _ = Describe("FrameService Binding", func() {
	const name = "binding-svc"
	const ns = "default"
	key := types.NamespacedName{Name: name, Namespace: ns}
	ctx := context.Background()

	var svc *servicesv1alpha1.FrameService
	var fake *fakeProvisioner

	BeforeEach(func() {
		for _, projected := range []string{bindingProjectedNamespaceA, bindingProjectedNamespaceB} {
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: projected}}
			err := k8sClient.Create(ctx, namespace)
			if err != nil && !apierrors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}
		}

		fake = &fakeProvisioner{
			result: provider.Result{Ready: true, Reason: "Provisioned", Message: "Serving"},
			binding: provider.Binding{
				Endpoint: "http://binding-svc.default.svc:8080",
				Data:     map[string][]byte{"endpoint": []byte("http://binding-svc.default.svc:8080")},
			},
		}
		svc = &servicesv1alpha1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       servicesv1alpha1.FrameServiceSpec{Type: "fake", DeletionPolicy: "Retain"},
		}
		Expect(k8sClient.Create(ctx, svc)).To(Succeed())
	})

	AfterEach(func() {
		fresh := &servicesv1alpha1.FrameService{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
		for _, secretNS := range []string{ns, bindingProjectedNamespaceA, bindingProjectedNamespaceB} {
			_ = k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: secretNS},
			})
		}
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

	updateProjectTo := func(namespaces []string) {
		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		svc.Spec.Binding.ProjectTo = namespaces
		Expect(k8sClient.Update(ctx, svc)).To(Succeed())
	}

	It("writes the credentials Secret beside the service", func() {
		_, _ = r().Reconcile(ctx, req) // lands the finalizer
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var secret corev1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &secret)).To(Succeed())
		Expect(secret.Data).To(Equal(fake.binding.Data))
		Expect(secret.Labels).To(HaveKeyWithValue(ownedByLabel, ns+"."+name))

		owner := metav1.GetControllerOf(&secret)
		Expect(owner).NotTo(BeNil())
		Expect(owner.Name).To(Equal(name))

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(svc.Status.Binding.SecretRef).NotTo(BeNil())
		Expect(svc.Status.Binding.SecretRef.Name).To(Equal(name))
		Expect(svc.Status.Binding.Endpoint).To(Equal(fake.binding.Endpoint))
	})

	It("projects the Secret only into the namespaces that were listed", func() {
		_, _ = r().Reconcile(ctx, req) // lands the finalizer
		updateProjectTo([]string{bindingProjectedNamespaceA})

		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var projected corev1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: bindingProjectedNamespaceA}, &projected)).
			To(Succeed())
		Expect(projected.Data).To(Equal(fake.binding.Data))
		Expect(projected.Labels).To(HaveKeyWithValue(ownedByLabel, ns+"."+name))
		// A projected copy cannot carry an owner reference: owner references
		// do not cross namespaces. Its absence here is the intended shape, not
		// a gap — see the comment on reconcileBinding.
		Expect(projected.OwnerReferences).To(BeEmpty())

		// The namespace that was never listed must stay untouched: writing a
		// Secret there would be a cross-tenant leak dressed as convenience.
		err = k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: bindingProjectedNamespaceB}, &corev1.Secret{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("never overwrites a Secret it does not own", func() {
		foreign := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data:       map[string][]byte{"password": []byte("not-frames-to-touch")},
		}
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())

		_, _ = r().Reconcile(ctx, req) // lands the finalizer
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(svc.Status.Phase).To(Equal("Degraded"))
		Expect(readyCondition(svc).Reason).To(Equal("BindingConflict"))

		var untouched corev1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &untouched)).To(Succeed())
		Expect(untouched.Data).To(Equal(foreign.Data))
		Expect(untouched.Labels).NotTo(HaveKey(ownedByLabel))
	})

	It("removes a projected Secret when its namespace leaves projectTo", func() {
		_, _ = r().Reconcile(ctx, req) // lands the finalizer
		updateProjectTo([]string{bindingProjectedNamespaceA})
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: name, Namespace: bindingProjectedNamespaceA}, &corev1.Secret{})).To(Succeed())

		updateProjectTo(nil)
		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		// Otherwise revoking access would silently leave the credentials
		// behind in a namespace nobody is supposed to reach them from anymore.
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx,
				types.NamespacedName{Name: name, Namespace: bindingProjectedNamespaceA}, &corev1.Secret{}))
		}, "5s").Should(BeTrue())
	})

	It("degrades naming the namespace when projectTo lists one that does not exist", func() {
		_, _ = r().Reconcile(ctx, req) // lands the finalizer
		updateProjectTo([]string{"binding-namespace-does-not-exist"})

		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(svc.Status.Phase).To(Equal("Degraded"))
		Expect(readyCondition(svc).Reason).To(Equal("ProjectedNamespaceMissing"))
	})

	It("degrades naming the entry when projectTo lists a syntactically invalid namespace name", func() {
		_, _ = r().Reconcile(ctx, req) // lands the finalizer
		updateProjectTo([]string{"Not_A_Valid_Namespace!"})

		// A syntactically invalid name is rejected by client-go itself before
		// the request ever reaches the apiserver, as a plain error carrying
		// none of the apierrors sentinels a NotFound check would catch. Left
		// unhandled, that used to fall through to a hard reconcile error —
		// retried forever, even though no amount of retrying could ever make
		// "Not_A_Valid_Namespace!" become a real namespace.
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(svc.Status.Phase).To(Equal("Degraded"))
		Expect(readyCondition(svc).Reason).To(Equal("ProjectedNamespaceInvalid"))
	})

	It("leaves a forged-label Secret alone when its namespace was never in projectTo", func() {
		_, _ = r().Reconcile(ctx, req) // lands the finalizer
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		// Planted directly, with no controller involvement, in a namespace
		// this FrameService never listed in projectTo — carrying a label that
		// claims ownership this FrameService never granted through its own
		// status. Under the old label-trusting design, the cluster-wide
		// List(MatchingLabels{...}) in pruneProjections would have picked
		// this up on the very next reconcile and deleted it: a label is data
		// anyone with patch rights on Secrets can set, and using it to decide
		// what Frame's cluster-wide delete privilege acts on is a confused
		// deputy.
		forged := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: bindingProjectedNamespaceB,
				Labels:    map[string]string{ownedByLabel: ns + "." + name},
			},
			Data: map[string][]byte{"planted": []byte("not-yours-to-delete")},
		}
		Expect(k8sClient.Create(ctx, forged)).To(Succeed())

		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var stillThere corev1.Secret
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: name, Namespace: bindingProjectedNamespaceB}, &stillThere)).To(Succeed())
		Expect(stillThere.Data).To(Equal(forged.Data))
	})

	It("degrades instead of overwriting a forged-label Secret sitting at a listed projectTo coordinate", func() {
		// Planted before the FrameService ever reconciles with this
		// projectTo entry, so the coordinate is genuinely new to it — the
		// only case where ownership is ever decided rather than trusted from
		// a prior recorded pass. Carries a forged label matching what this
		// FrameService would itself write, so the old label-based check would
		// have read it as already-owned and overwritten its Data with the
		// real credential instead of raising a conflict.
		forged := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: bindingProjectedNamespaceA,
				Labels:    map[string]string{ownedByLabel: ns + "." + name},
			},
			Data: map[string][]byte{"planted": []byte("not-yours-to-overwrite")},
		}
		Expect(k8sClient.Create(ctx, forged)).To(Succeed())

		_, _ = r().Reconcile(ctx, req) // lands the finalizer
		updateProjectTo([]string{bindingProjectedNamespaceA})

		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		Expect(svc.Status.Phase).To(Equal("Degraded"))
		Expect(readyCondition(svc).Reason).To(Equal("BindingConflict"))

		var untouched corev1.Secret
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: name, Namespace: bindingProjectedNamespaceA}, &untouched)).To(Succeed())
		Expect(untouched.Data).To(Equal(forged.Data))
	})

	It("orphans nothing when spec.binding.secretName is renamed on a live service", func() {
		const renamed = "binding-svc-renamed"

		_, _ = r().Reconcile(ctx, req) // lands the finalizer
		updateProjectTo([]string{bindingProjectedNamespaceA})
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		// The original name exists in both the service's own namespace and
		// the projected one before the rename.
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &corev1.Secret{})).To(Succeed())
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: name, Namespace: bindingProjectedNamespaceA}, &corev1.Secret{})).To(Succeed())

		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())
		svc.Spec.Binding.SecretName = renamed
		Expect(k8sClient.Update(ctx, svc)).To(Succeed())

		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		// The new name exists at both coordinates.
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: renamed, Namespace: ns}, &corev1.Secret{})).To(Succeed())
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: renamed, Namespace: bindingProjectedNamespaceA}, &corev1.Secret{})).To(Succeed())

		// The old name is gone from both — otherwise a rename would leave a
		// second, unrecorded copy of the same credential behind for the rest
		// of the instance's life, invisible to anyone auditing via
		// status.binding.secretRef.
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &corev1.Secret{}))
		}, "5s").Should(BeTrue())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx,
				types.NamespacedName{Name: name, Namespace: bindingProjectedNamespaceA}, &corev1.Secret{}))
		}, "5s").Should(BeTrue())

		// AfterEach only knows to clean up the original name; delete the
		// renamed copies directly so they don't leak into the next spec.
		_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: renamed, Namespace: ns}})
		_ = k8sClient.Delete(ctx,
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: renamed, Namespace: bindingProjectedNamespaceA}})
	})
})
