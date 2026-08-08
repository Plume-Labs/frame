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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	"github.com/rmocq/frame/internal/services/provider"
)

// ownedByLabel records which FrameService a credentials Secret belongs to, as
// "<namespace>.<name>". It is the only thing that tells this controller's own
// Secret apart from one that merely happens to share its name: a Secret
// without this label, or carrying a different value, was not written by this
// controller and must never be created, updated, or deleted by it.
const ownedByLabel = "frame.plume-labs.io/owned-by"

// bindingDegradation is returned by reconcileBinding when it cannot proceed
// for a reason an operator needs to see, not a transient error: a Secret it
// would need to write belongs to someone else, or a namespace listed in
// spec.binding.projectTo does not exist. Reconcile reports both as a Degraded
// condition carrying Reason, the same way an unready provider is, rather than
// a generic error that would bury the cause in controller-runtime's retry
// logging.
type bindingDegradation struct {
	Reason  string
	Message string
}

func (e *bindingDegradation) Error() string { return e.Message }

// ownedByValue is the ownedByLabel value that marks a Secret as belonging to
// svc.
func ownedByValue(svc *servicesv1alpha1.FrameService) string {
	return svc.Namespace + "." + svc.Name
}

// secretName resolves spec.binding.secretName, defaulting to the
// FrameService's own name.
func secretName(svc *servicesv1alpha1.FrameService) string {
	if svc.Spec.Binding.SecretName != "" {
		return svc.Spec.Binding.SecretName
	}
	return svc.Name
}

// reconcileBinding writes the credentials Secret beside the FrameService and
// reconciles its projected copies, returning the name to publish as
// status.binding.secretRef.
//
// The Secret in svc.Namespace is an exposing object under the ownership
// contract documented on Provisioner.Reconcile (see
// internal/services/provider/provider.go): a consumer reads its credentials
// from that Secret, so it carries a controller reference to the FrameService
// and garbage collection removes it the moment the FrameService is deleted,
// regardless of spec.deletionPolicy.
//
// Projected copies in other namespaces CANNOT carry that owner reference —
// owner references do not cross namespaces — so their deletion is handled
// explicitly instead: here, when a namespace leaves projectTo, and in
// reconcileDelete, when the FrameService itself is deleted. A reader used to
// the owner-reference story above should not mistake the missing reference on
// a projected copy for an oversight; it is the only mechanism that could ever
// have worked.
func (r *FrameServiceReconciler) reconcileBinding(
	ctx context.Context,
	svc *servicesv1alpha1.FrameService,
	binding provider.Binding,
) (string, error) {
	name := secretName(svc)

	if err := r.writeOwnedSecret(ctx, svc, svc.Namespace, name, binding.Data, true); err != nil {
		return "", err
	}

	if err := r.reconcileProjections(ctx, svc, name, binding.Data); err != nil {
		return "", err
	}

	return name, nil
}

// reconcileProjections writes the Secret into every namespace listed in
// spec.binding.projectTo and removes it from every namespace that used to be
// listed and no longer is. Copying it anywhere else — a namespace nobody put
// in projectTo — would be a cross-tenant leak dressed as convenience, which is
// why the desired set below comes from nothing but that field.
func (r *FrameServiceReconciler) reconcileProjections(
	ctx context.Context,
	svc *servicesv1alpha1.FrameService,
	name string,
	data map[string][]byte,
) error {
	desired := make(map[string]bool, len(svc.Spec.Binding.ProjectTo))
	for _, ns := range svc.Spec.Binding.ProjectTo {
		if ns == svc.Namespace {
			// Projecting into the service's own namespace would just be the
			// primary Secret again, fought over by two mutate paths — one
			// that sets the owner reference and one that does not. Treating
			// it as a no-op rather than writing it a second time avoids that.
			continue
		}
		if err := r.checkNamespaceExists(ctx, ns); err != nil {
			return err
		}
		if err := r.writeOwnedSecret(ctx, svc, ns, name, data, false); err != nil {
			return err
		}
		desired[ns] = true
	}

	return r.pruneProjections(ctx, svc, desired)
}

// checkNamespaceExists degrades with a named reason instead of letting a
// Secret write into a namespace that does not exist fail with whatever error
// the apiserver happens to phrase that as. A namespace named in projectTo but
// never created is treated the same as any other misconfiguration this
// controller reports rather than retries blindly: Degraded, with the
// namespace named, until an operator creates it or removes it from the list.
func (r *FrameServiceReconciler) checkNamespaceExists(ctx context.Context, ns string) error {
	var namespace corev1.Namespace
	if err := r.Get(ctx, client.ObjectKey{Name: ns}, &namespace); err != nil {
		if apierrors.IsNotFound(err) {
			return &bindingDegradation{
				Reason: "ProjectedNamespaceMissing",
				Message: fmt.Sprintf(
					"namespace %q listed in spec.binding.projectTo does not exist", ns),
			}
		}
		return fmt.Errorf("checking namespace %q: %w", ns, err)
	}
	return nil
}

// writeOwnedSecret creates or converges the one Secret named name in
// namespace, refusing to touch it unless this FrameService already owns it or
// it does not exist yet. Whether a Secret already existed is read off
// CreationTimestamp inside the mutate closure below, rather than with a
// separate Get: controllerutil.CreateOrUpdate already does that Get and
// leaves the object populated before calling the closure, so a second Get
// here would just add a race between the two reads with no reduction in the
// classes of conflict this handles — the label check needs to happen on
// whatever CreateOrUpdate observed, not on a possibly-newer read.
func (r *FrameServiceReconciler) writeOwnedSecret(
	ctx context.Context,
	svc *servicesv1alpha1.FrameService,
	namespace, name string,
	data map[string][]byte,
	primary bool,
) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		if !secret.CreationTimestamp.IsZero() && !isOwnedBy(secret, svc) {
			return &bindingDegradation{
				Reason: "BindingConflict",
				Message: fmt.Sprintf(
					"Secret %s/%s already exists and is not owned by FrameService %s/%s",
					namespace, name, svc.Namespace, svc.Name),
			}
		}
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels[ownedByLabel] = ownedByValue(svc)
		secret.Data = data
		if primary {
			return controllerutil.SetControllerReference(svc, secret, r.Scheme)
		}
		return nil
	})
	if err != nil {
		var degradation *bindingDegradation
		if isBindingDegradation(err, &degradation) {
			return degradation
		}
		return fmt.Errorf("reconciling Secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

// isOwnedBy reports whether secret carries this FrameService's ownership
// label. Called only on a Secret CreateOrUpdate found to already exist.
func isOwnedBy(secret *corev1.Secret, svc *servicesv1alpha1.FrameService) bool {
	return secret.Labels[ownedByLabel] == ownedByValue(svc)
}

// isBindingDegradation reports whether err is a *bindingDegradation,
// populating out on success. controllerutil.CreateOrUpdate returns a mutate
// closure's error unwrapped, so a direct type assertion is enough here; this
// helper exists so callers read as a check rather than a type switch.
func isBindingDegradation(err error, out **bindingDegradation) bool {
	degradation, ok := err.(*bindingDegradation)
	if ok {
		*out = degradation
	}
	return ok
}

// pruneProjections deletes every Secret this FrameService owns outside its
// own namespace whose namespace is not in keep. It is what makes removing a
// namespace from projectTo actually revoke access: without this, a copy of
// the credentials would simply be left behind, silently, in a namespace that
// no longer has any business holding them.
//
// Listing by ownedByLabel rather than walking spec history is deliberate:
// this controller keeps no separate record of which namespaces it has
// projected into, so the label on the Secrets themselves is the only durable
// record of that fact, and it survives a controller restart between one
// projectTo edit and the next.
func (r *FrameServiceReconciler) pruneProjections(
	ctx context.Context,
	svc *servicesv1alpha1.FrameService,
	keep map[string]bool,
) error {
	var secrets corev1.SecretList
	if err := r.List(ctx, &secrets, client.MatchingLabels{ownedByLabel: ownedByValue(svc)}); err != nil {
		return fmt.Errorf("listing projected Secrets for %s/%s: %w", svc.Namespace, svc.Name, err)
	}

	for i := range secrets.Items {
		s := &secrets.Items[i]
		if s.Namespace == svc.Namespace || keep[s.Namespace] {
			continue
		}
		if err := r.Delete(ctx, s); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting projected Secret %s/%s: %w", s.Namespace, s.Name, err)
		}
	}
	return nil
}
