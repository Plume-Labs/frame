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
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	"github.com/rmocq/frame/internal/services/provider"
)

// ownedByLabel is written onto every Secret this controller creates, purely
// so a human running `kubectl get secret --show-labels` can see which
// FrameService a Secret belongs to. It is NOT an authority: a label is data,
// and anyone able to patch one onto an arbitrary Secret — a strictly weaker
// right than deleting or overwriting that Secret — must never be able to
// make this controller do either on their behalf by forging it. Ownership is
// decided by status.binding.projected instead (see reconcileBinding); do not
// reintroduce a label-based ownership check here.
const ownedByLabel = "frame.plume-labs.io/owned-by"

// bindingDegradation is returned by reconcileBinding when it cannot proceed
// for a reason an operator needs to see, not a transient error: a Secret it
// would need to write already exists at a coordinate this FrameService has
// never recorded as its own, or a namespace listed in spec.binding.projectTo
// does not exist or is not a valid namespace name. Reconcile reports all of
// these as a Degraded condition carrying Reason, the same way an unready
// provider is, rather than a generic error that would bury the cause in
// controller-runtime's retry logging.
type bindingDegradation struct {
	Reason  string
	Message string
}

func (e *bindingDegradation) Error() string { return e.Message }

// ownedByValue is the ownedByLabel value written onto every Secret this
// FrameService's controller creates. Display-only — see ownedByLabel.
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
// # Ownership: a record, not a label
//
// Earlier, ownership of a Secret was decided by whether it carried this
// FrameService's ownedByLabel, and pruneProjections found what to delete by
// listing Secrets cluster-wide on that same label. Both readings trusted data
// an attacker could write for free: patching a label onto a Secret is a
// weaker right than deleting or overwriting it, so anyone who could patch one
// could have Frame delete or overwrite that Secret for them — a confused
// deputy, and a security review caught it experimentally on both paths.
//
// Ownership is now decided by status.binding.projected: the list of
// namespace/name coordinates this controller has itself recorded there. A
// coordinate already in that list is trusted without re-checking the cluster,
// because only this controller's status-subresource RBAC can have put it
// there. A coordinate NOT in that list is new, and is only ever claimed after
// claimNewCoordinates confirms no Secret already sits at it — a forged label
// changes nothing about that check, because nothing ever reads the label to
// decide ownership. pruneProjections and reconcileDelete's projected-Secret
// cleanup work the same way: they diff status.binding.projected against what
// is wanted and delete only at coordinates the controller itself recorded,
// never by asking the cluster who owns what.
//
// # Recording before writing
//
// A newly claimed coordinate is recorded in status BEFORE its Secret is
// created (persistBindingStatus, called from here). That ordering, not the
// reverse, is what makes a crash mid-reconcile self-healing: if the process
// dies after recording but before creating, the next reconcile finds the
// coordinate already recorded and safely creates it. Recording only after
// creating would do the opposite — a crash between the two would leave a
// Secret this controller just created looking, to the next reconcile, exactly
// like a foreign Secret sitting at an unrecorded coordinate, and
// claimNewCoordinates would refuse to touch its own work. What recording
// first does not close: a real Secret created at one of these exact
// coordinates in the narrow window between claimNewCoordinates' check and the
// write that follows would be silently adopted next pass, since by then the
// coordinate is already recorded as ours. Closing that fully would need a
// per-coordinate lock, out of scope here — what matters is that this window
// requires precise timing against a specific coordinate, unlike the label it
// replaces, which required none at all and could be exploited at leisure.
//
// # Ownership contract with the rest of the provider machinery
//
// The Secret in svc.Namespace is additionally an exposing object under the
// ownership contract documented on Provisioner.Reconcile (see
// internal/services/provider/provider.go): a consumer reads its credentials
// from that Secret, so it carries a controller reference to the FrameService
// and garbage collection removes it the moment the FrameService is deleted,
// regardless of spec.deletionPolicy.
//
// Projected copies in other namespaces CANNOT carry that owner reference —
// owner references do not cross namespaces — so their deletion is handled
// explicitly instead: here, when a namespace leaves projectTo or secretName
// changes, and in reconcileDelete, when the FrameService itself is deleted. A
// reader used to the owner-reference story above should not mistake the
// missing reference on a projected copy for an oversight; it is the only
// mechanism that could ever have worked.
func (r *FrameServiceReconciler) reconcileBinding(
	ctx context.Context,
	svc *servicesv1alpha1.FrameService,
	binding provider.Binding,
) (string, error) {
	name := secretName(svc)

	desired := []servicesv1alpha1.ProjectedSecretRef{{Namespace: svc.Namespace, Name: name}}
	for _, ns := range svc.Spec.Binding.ProjectTo {
		if ns == svc.Namespace {
			// Projecting into the service's own namespace would just be the
			// primary Secret again, fought over by two mutate paths — one
			// that sets the owner reference and one that does not. Treating
			// it as a no-op rather than writing it a second time avoids that.
			continue
		}
		if err := r.checkNamespaceExists(ctx, ns); err != nil {
			return "", err
		}
		desired = append(desired, servicesv1alpha1.ProjectedSecretRef{Namespace: ns, Name: name})
	}

	oldRecorded := svc.Status.Binding.Projected
	toAdd := coordsMissingFrom(desired, oldRecorded)
	if len(toAdd) > 0 {
		if err := r.claimNewCoordinates(ctx, svc, toAdd); err != nil {
			return "", err
		}
		svc.Status.Binding.Projected = append(
			append([]servicesv1alpha1.ProjectedSecretRef{}, oldRecorded...), toAdd...)
		if err := r.persistBindingStatus(ctx, svc); err != nil {
			return "", err
		}
	}

	for _, coord := range desired {
		if err := r.writeSecret(ctx, svc, coord, binding.Data); err != nil {
			return "", err
		}
	}

	stale := coordsMissingFrom(oldRecorded, desired)
	if err := r.deleteSecrets(ctx, stale); err != nil {
		return "", err
	}

	svc.Status.Binding.Projected = desired
	return name, nil
}

// claimNewCoordinates verifies every coordinate in toAdd is actually free —
// no Secret already sits there — before this FrameService is allowed to
// record it as its own. This is the only place a genuinely new coordinate is
// still checked against live cluster state; a coordinate already in
// status.binding.projected from an earlier reconcile is trusted without
// re-checking, because that record is what ownership means now, not a label.
func (r *FrameServiceReconciler) claimNewCoordinates(
	ctx context.Context,
	svc *servicesv1alpha1.FrameService,
	toAdd []servicesv1alpha1.ProjectedSecretRef,
) error {
	for _, coord := range toAdd {
		var existing corev1.Secret
		err := r.Get(ctx, types.NamespacedName{Name: coord.Name, Namespace: coord.Namespace}, &existing)
		switch {
		case err == nil:
			return &bindingDegradation{
				Reason: "BindingConflict",
				Message: fmt.Sprintf(
					"Secret %s/%s already exists and was not written by FrameService %s/%s",
					coord.Namespace, coord.Name, svc.Namespace, svc.Name),
			}
		case apierrors.IsNotFound(err):
			continue
		default:
			return fmt.Errorf("checking Secret %s/%s: %w", coord.Namespace, coord.Name, err)
		}
	}
	return nil
}

// persistBindingStatus durably records status.binding.projected before any
// Secret named in it is written. See the "Recording before writing" section
// on reconcileBinding for exactly what this ordering buys and what it leaves
// possible.
func (r *FrameServiceReconciler) persistBindingStatus(ctx context.Context, svc *servicesv1alpha1.FrameService) error {
	var fresh servicesv1alpha1.FrameService
	if err := r.Get(ctx, client.ObjectKeyFromObject(svc), &fresh); err != nil {
		return fmt.Errorf("re-fetching FrameService %s/%s before recording binding: %w", svc.Namespace, svc.Name, err)
	}
	fresh.Status.Binding.Projected = svc.Status.Binding.Projected
	if err := r.Status().Update(ctx, &fresh); err != nil {
		return fmt.Errorf("recording projected Secrets for %s/%s: %w", svc.Namespace, svc.Name, err)
	}
	return nil
}

// checkNamespaceExists degrades with a named reason instead of letting a
// Secret write into a namespace that cannot be used fail in whatever way the
// apiserver, or the client itself, happens to phrase that. Two distinct
// failures are given distinct reasons:
//
//   - A syntactically invalid namespace name never resolves to a real
//     Namespace object and never will, no matter how many times this is
//     retried: client-go rejects it locally before the request ever reaches
//     the apiserver, as a plain error carrying none of the apierrors
//     sentinels (not NotFound, not Invalid, not BadRequest) the branch below
//     checks for. Validating the name with the same rule Kubernetes itself
//     applies to a Namespace's own name, before ever calling Get, turns that
//     into a clean degrade instead of a hard reconcile error that backs off
//     forever without ever having a chance of succeeding.
//   - A well-formed name that simply does not exist yet is the ordinary,
//     recoverable case: an operator creates the namespace, or removes it from
//     projectTo, and the next reconcile proceeds.
func (r *FrameServiceReconciler) checkNamespaceExists(ctx context.Context, ns string) error {
	if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
		return &bindingDegradation{
			Reason: "ProjectedNamespaceInvalid",
			Message: fmt.Sprintf(
				"namespace %q listed in spec.binding.projectTo is not a valid namespace name: %s",
				ns, strings.Join(errs, "; ")),
		}
	}

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

// writeSecret creates or converges the Secret at coord with data. Whether
// this FrameService may write there was already decided before writeSecret is
// ever called — by claimNewCoordinates for anything new this pass, or by an
// earlier pass's successful reconcile for anything already in
// status.binding.projected — so this function's only job is converging
// content, never gating access.
func (r *FrameServiceReconciler) writeSecret(
	ctx context.Context,
	svc *servicesv1alpha1.FrameService,
	coord servicesv1alpha1.ProjectedSecretRef,
	data map[string][]byte,
) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: coord.Name, Namespace: coord.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels[ownedByLabel] = ownedByValue(svc)
		secret.Data = data
		if coord.Namespace == svc.Namespace {
			return controllerutil.SetControllerReference(svc, secret, r.Scheme)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling Secret %s/%s: %w", coord.Namespace, coord.Name, err)
	}
	return nil
}

// deleteSecrets removes the Secret at each coordinate in stale — coordinates
// this FrameService had recorded as its own but no longer desires, because a
// namespace left projectTo or spec.binding.secretName changed. Driven by an
// explicit list rather than a cluster-wide label lookup: see reconcileBinding.
func (r *FrameServiceReconciler) deleteSecrets(ctx context.Context, stale []servicesv1alpha1.ProjectedSecretRef) error {
	for _, coord := range stale {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: coord.Name, Namespace: coord.Namespace}}
		if err := r.Delete(ctx, secret); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting Secret %s/%s: %w", coord.Namespace, coord.Name, err)
		}
	}
	return nil
}

// deleteAllProjections removes every projected Secret this FrameService ever
// recorded — everything in status.binding.projected outside its own
// namespace. Called from reconcileDelete: those Secrets carry no owner
// reference (owner references do not cross namespaces), so this explicit
// delete, driven by the controller's own record rather than a cluster-wide
// label lookup, is the only thing that will ever remove them.
func (r *FrameServiceReconciler) deleteAllProjections(ctx context.Context, svc *servicesv1alpha1.FrameService) error {
	var stale []servicesv1alpha1.ProjectedSecretRef
	for _, coord := range svc.Status.Binding.Projected {
		if coord.Namespace != svc.Namespace {
			stale = append(stale, coord)
		}
	}
	return r.deleteSecrets(ctx, stale)
}

// coordsMissingFrom returns the entries of coords not present in other.
func coordsMissingFrom(
	coords []servicesv1alpha1.ProjectedSecretRef,
	other []servicesv1alpha1.ProjectedSecretRef,
) []servicesv1alpha1.ProjectedSecretRef {
	have := make(map[servicesv1alpha1.ProjectedSecretRef]bool, len(other))
	for _, c := range other {
		have[c] = true
	}
	var missing []servicesv1alpha1.ProjectedSecretRef
	for _, c := range coords {
		if !have[c] {
			missing = append(missing, c)
		}
	}
	return missing
}
