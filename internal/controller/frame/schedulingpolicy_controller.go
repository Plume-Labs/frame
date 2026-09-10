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
	"fmt"
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

const schedulingPolicyFinalizer = "frame.plume-labs.io/schedulingpolicy"

// The marks written onto every PriorityClass and Queue this controller
// creates. They are display and corroboration only, never an authority: see
// releasePriorityClass, and the ownership note on
// SchedulingPolicyStatus.OwnedPriorityClass.
const (
	policyNamespaceLabel = "frame.plume-labs.io/policy-namespace"
	policyNameLabel      = "frame.plume-labs.io/policy-name"
	policySchedulerLabel = "frame.plume-labs.io/scheduler"
)

// reservedPriorityClassPrefix is the prefix Kubernetes reserves for
// cluster-critical PriorityClasses (system-cluster-critical,
// system-node-critical). Nothing this controller does may create, adopt,
// update or delete a name under it, whatever any status record says: those
// objects are referenced by pods across every namespace, PriorityClass.value
// is immutable, and so a deletion cannot be undone without evicting every
// pod that names it.
const reservedPriorityClassPrefix = "system-"

// Reasons written to status.conditions[Ready].reason, and to the Warning
// event, when a PriorityClass is refused. Both are terminal for the current
// generation: Reconcile returns no error and no requeue, so a refusal costs
// one reconcile and then waits for the spec to change rather than spinning.
const (
	reasonPriorityClassNotOwned = "PriorityClassNotOwned"
	reasonPriorityClassReserved = "PriorityClassReserved"
)

// ownershipRefusal is returned by claimPriorityClass when it will not act on
// a cluster-scoped name — because the name is reserved, or because an object
// already sits there that this SchedulingPolicy did not create. It is a
// decision an operator has to see, not a transient failure to retry, so
// Reconcile surfaces it as a Ready=False condition carrying Reason rather
// than as an error. Same shape and same intent as bindingDegradation in
// internal/controller/services/binding.go.
type ownershipRefusal struct {
	Reason  string
	Message string
}

func (e *ownershipRefusal) Error() string { return e.Message }

// SchedulingPolicyReconciler reconciles a SchedulingPolicy object
type SchedulingPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=schedulingpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=schedulingpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=schedulingpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scheduling.volcano.sh,resources=queues,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=yunikorn.apache.org,resources=queues,verbs=get;list;watch;create;update;patch;delete

func (r *SchedulingPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var sp framev1beta1.SchedulingPolicy
	if err := r.Get(ctx, req.NamespacedName, &sp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !sp.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &sp)
	}

	// Ownership of the cluster-scoped PriorityClass is settled BEFORE the
	// finalizer goes on, not after. The finalizer is a standing right to
	// delete whatever the policy names at deletion time; adding it first and
	// checking ownership later — the previous order — meant a SchedulingPolicy
	// naming a PriorityClass it does not own still walked away holding that
	// right, and reconcileDelete then exercised it. A refused policy now ends
	// its reconcile with no finalizer at all, so deleting it can touch
	// nothing.
	if err := r.claimPriorityClass(ctx, &sp); err != nil {
		var refusal *ownershipRefusal
		if errors.As(err, &refusal) {
			return ctrl.Result{}, r.refuse(ctx, &sp, refusal)
		}
		return ctrl.Result{}, fmt.Errorf("claiming PriorityClass: %w", err)
	}

	if !controllerutil.ContainsFinalizer(&sp, schedulingPolicyFinalizer) {
		controllerutil.AddFinalizer(&sp, schedulingPolicyFinalizer)
		return ctrl.Result{}, r.Update(ctx, &sp)
	}

	var errs []string

	if sp.Spec.PriorityClass != "" {
		if err := r.reconcilePriorityClass(ctx, &sp); err != nil {
			// PriorityClass is a core K8s type — failure is always notable.
			return ctrl.Result{}, fmt.Errorf("reconciling PriorityClass: %w", err)
		}
	}

	if sp.Spec.QueueName != "" && sp.Spec.Scheduler != "default" {
		if err := r.reconcileQueue(ctx, &sp); err != nil {
			// Queue CRD may not be installed — degrade rather than hard-fail.
			log.Info("Queue reconcile failed (scheduler CRD may not be installed)",
				"scheduler", sp.Spec.Scheduler, "queue", sp.Spec.QueueName, "err", err)
			errs = append(errs, fmt.Sprintf("Queue: %v", err))
			r.Recorder.Event(&sp, corev1.EventTypeWarning, "QueueCRDMissing",
				fmt.Sprintf("%s queue CRD not installed: %v", sp.Spec.Scheduler, err))
		}
	}

	patch := client.MergeFrom(sp.DeepCopy())
	sp.Status.ObservedGeneration = sp.Generation
	if len(errs) > 0 {
		meta.SetStatusCondition(&sp.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             "ReconcileError",
			Message:            fmt.Sprintf("%v", errs),
			ObservedGeneration: sp.Generation,
		})
	} else {
		meta.SetStatusCondition(&sp.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionTrue,
			Reason:             "Applied",
			Message:            appliedMsg(&sp),
			ObservedGeneration: sp.Generation,
		})
		r.Recorder.Event(&sp, corev1.EventTypeNormal, "Applied", appliedMsg(&sp))
		schedulingPolicyApplied.Inc()
	}
	log.Info("Reconciled SchedulingPolicy", "scheduler", sp.Spec.Scheduler,
		"priorityClass", sp.Spec.PriorityClass, "queue", sp.Spec.QueueName)
	return ctrl.Result{}, r.Status().Patch(ctx, &sp, patch)
}

// reservedPriorityClassName reports whether name is one Kubernetes reserves
// for itself. Checked on every path that would create, update or delete a
// PriorityClass, including the ones a record in status is supposed to have
// made safe.
func reservedPriorityClassName(name string) bool {
	return strings.HasPrefix(name, reservedPriorityClassPrefix)
}

// markedBy reports whether pc carries the labels this controller writes onto
// the PriorityClasses it creates for sp.
//
// This is never asked in order to decide that something IS ours — labels are
// writable by anyone who can patch the object, and on this cluster the two
// PriorityClasses the old code took over already carry these very labels,
// written during the takeover. It is only ever asked to decide that something
// is NOT ours any more, on top of a status record that already said it was.
func markedBy(pc *schedulingv1.PriorityClass, sp *framev1beta1.SchedulingPolicy) bool {
	return pc.Labels[policyNamespaceLabel] == sp.Namespace &&
		pc.Labels[policyNameLabel] == sp.Name
}

// describeManager names, for the refusal message, whoever the existing
// PriorityClass looks like it belongs to. Message text only — the refusal
// itself does not depend on finding anything here, because "no recognisable
// manager" is not a licence to adopt.
func describeManager(pc *schedulingv1.PriorityClass) string {
	if rel := pc.Annotations["meta.helm.sh/release-name"]; rel != "" {
		if ns := pc.Annotations["meta.helm.sh/release-namespace"]; ns != "" {
			return fmt.Sprintf("it belongs to Helm release %s/%s", ns, rel)
		}
		return fmt.Sprintf("it belongs to Helm release %s", rel)
	}
	if by := pc.Labels["app.kubernetes.io/managed-by"]; by != "" {
		return fmt.Sprintf("it is managed by %q", by)
	}
	return "it predates this SchedulingPolicy"
}

// claimPriorityClass brings status.ownedPriorityClass into agreement with
// spec.priorityClass, and is the only place that record is ever written.
//
// A name is claimable only when nothing sits there: an existing PriorityClass
// is refused, never absorbed. Once claimed, the record — not the cluster, not
// a label — is what says the name is this policy's, exactly as
// status.binding.projected does for FrameService Secrets.
//
// Ordering, when spec.priorityClass changes from one name to another: the new
// name is checked free first, so a refused rename leaves the old PriorityClass
// untouched; then the old one is released; and only then does the record move.
// Releasing before recording is what makes a crash between the two harmless —
// the record still names the old class, so the next reconcile retries the
// release and finds it already gone. Recording first would strand a
// PriorityClass this controller created with nothing left that remembers it.
func (r *SchedulingPolicyReconciler) claimPriorityClass(ctx context.Context, sp *framev1beta1.SchedulingPolicy) error {
	want := sp.Spec.PriorityClass
	owned := sp.Status.OwnedPriorityClass
	if want == owned {
		return nil
	}

	if want != "" {
		if reservedPriorityClassName(want) {
			return &ownershipRefusal{
				Reason: reasonPriorityClassReserved,
				Message: fmt.Sprintf(
					"PriorityClass %q is reserved: Kubernetes reserves the %q prefix, and this controller will neither create, adopt nor delete a name under it",
					want, reservedPriorityClassPrefix),
			}
		}
		var existing schedulingv1.PriorityClass
		err := r.Get(ctx, types.NamespacedName{Name: want}, &existing)
		switch {
		case err == nil:
			return &ownershipRefusal{
				Reason: reasonPriorityClassNotOwned,
				Message: fmt.Sprintf(
					"PriorityClass %q already exists and was not created by SchedulingPolicy %s/%s (%s); refusing to adopt it",
					want, sp.Namespace, sp.Name, describeManager(&existing)),
			}
		case !apierrors.IsNotFound(err):
			return fmt.Errorf("checking whether PriorityClass %q is free: %w", want, err)
		}
	}

	if owned != "" {
		if err := r.releasePriorityClass(ctx, sp, owned); err != nil {
			return err
		}
	}

	patch := client.MergeFrom(sp.DeepCopy())
	sp.Status.OwnedPriorityClass = want
	if err := r.Status().Patch(ctx, sp, patch); err != nil {
		return fmt.Errorf("recording ownership of PriorityClass %q: %w", want, err)
	}
	return nil
}

// releasePriorityClass deletes a PriorityClass this policy recorded as its
// own — and nothing else.
//
// Two conditions, and both must hold. The record in status.ownedPriorityClass
// is the authority: the caller only ever passes a name it read from there, so
// a name this controller never claimed can never reach this function. The
// labels are corroboration on top of it, for the one case the record cannot
// see — the PriorityClass we created was deleted out from under us and
// something else recreated a different object under the same name. The
// UID precondition closes the same window between this Get and this Delete.
//
// The reserved-prefix check is unreachable through claimPriorityClass, which
// refuses the prefix long before it could be recorded. It is here anyway
// because this is a delete of a cluster-scoped object and the check costs one
// string comparison.
func (r *SchedulingPolicyReconciler) releasePriorityClass(ctx context.Context, sp *framev1beta1.SchedulingPolicy, name string) error {
	log := logf.FromContext(ctx)
	if reservedPriorityClassName(name) {
		log.Info("Not deleting a reserved PriorityClass", "priorityClass", name)
		return nil
	}

	var pc schedulingv1.PriorityClass
	err := r.Get(ctx, types.NamespacedName{Name: name}, &pc)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("reading PriorityClass %q before deleting it: %w", name, err)
	}

	if !markedBy(&pc, sp) {
		log.Info("Not deleting PriorityClass: it no longer carries this policy's marks",
			"priorityClass", name, "policy", sp.Namespace+"/"+sp.Name)
		return nil
	}

	uid := pc.UID
	if err := r.Delete(ctx, &pc, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting PriorityClass %q: %w", name, err)
	}
	return nil
}

// refuse records an ownershipRefusal on the SchedulingPolicy and stops.
//
// It returns nil and Reconcile returns an empty Result: a refusal is a
// decision about the spec, not a transient failure, so re-running it
// immediately would only reach the same conclusion. The next reconcile comes
// from the spec changing (or the informer's resync), which is exactly when the
// answer could differ.
func (r *SchedulingPolicyReconciler) refuse(ctx context.Context, sp *framev1beta1.SchedulingPolicy, refusal *ownershipRefusal) error {
	logf.FromContext(ctx).Info("Refusing PriorityClass",
		"policy", sp.Namespace+"/"+sp.Name, "reason", refusal.Reason, "message", refusal.Message)
	r.Recorder.Event(sp, corev1.EventTypeWarning, refusal.Reason, refusal.Message)
	schedulingPolicyRefused.WithLabelValues(refusal.Reason).Inc()

	patch := client.MergeFrom(sp.DeepCopy())
	sp.Status.ObservedGeneration = sp.Generation
	meta.SetStatusCondition(&sp.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             refusal.Reason,
		Message:            refusal.Message,
		ObservedGeneration: sp.Generation,
	})
	if err := r.Status().Patch(ctx, sp, patch); err != nil {
		return fmt.Errorf("recording %s on SchedulingPolicy %s/%s: %w", refusal.Reason, sp.Namespace, sp.Name, err)
	}
	return nil
}

func (r *SchedulingPolicyReconciler) reconcilePriorityClass(ctx context.Context, sp *framev1beta1.SchedulingPolicy) error {
	// claimPriorityClass runs first in Reconcile and returns nil only once
	// these two agree, so this can only fire if someone reorders Reconcile.
	// That is precisely the change that would reintroduce the adoption bug,
	// so it fails loudly instead of writing to a name this policy does not
	// own.
	if sp.Status.OwnedPriorityClass != sp.Spec.PriorityClass {
		return fmt.Errorf("refusing to write PriorityClass %q: SchedulingPolicy %s/%s owns %q",
			sp.Spec.PriorityClass, sp.Namespace, sp.Name, sp.Status.OwnedPriorityClass)
	}
	if reservedPriorityClassName(sp.Spec.PriorityClass) {
		return fmt.Errorf("refusing to write reserved PriorityClass %q", sp.Spec.PriorityClass)
	}

	val := int32(0)
	if sp.Spec.PriorityValue != nil {
		val = *sp.Spec.PriorityValue
	}
	pc := &schedulingv1.PriorityClass{
		ObjectMeta: metav1.ObjectMeta{Name: sp.Spec.PriorityClass},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pc, func() error {
		pc.Value = val
		pc.GlobalDefault = false
		pc.Description = fmt.Sprintf("Managed by SchedulingPolicy %s/%s", sp.Namespace, sp.Name)
		if pc.Labels == nil {
			pc.Labels = map[string]string{}
		}
		pc.Labels[policyNamespaceLabel] = sp.Namespace
		pc.Labels[policyNameLabel] = sp.Name
		pc.Labels[policySchedulerLabel] = sp.Spec.Scheduler
		pp := corev1.PreemptNever
		if sp.Spec.Preemption {
			pp = corev1.PreemptLowerPriority
		}
		pc.PreemptionPolicy = &pp
		return nil
	})
	return err
}

func (r *SchedulingPolicyReconciler) reconcileQueue(ctx context.Context, sp *framev1beta1.SchedulingPolicy) error {
	gvk, spec := queueGVKAndSpec(sp)
	q := &unstructured.Unstructured{}
	q.SetGroupVersionKind(gvk)
	q.SetName(sp.Spec.QueueName)
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, q, func() error {
		existing, _ := q.Object["spec"].(map[string]any)
		if existing == nil {
			existing = map[string]any{}
		}
		maps.Copy(existing, spec)
		q.Object["spec"] = existing
		labels := q.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[policyNamespaceLabel] = sp.Namespace
		labels[policyNameLabel] = sp.Name
		q.SetLabels(labels)
		return nil
	})
	return err
}

// queueGVKAndSpec returns GVK + spec fields for the scheduler-native queue type.
func queueGVKAndSpec(sp *framev1beta1.SchedulingPolicy) (schema.GroupVersionKind, map[string]any) {
	weight := int64(1)
	if sp.Spec.QueueWeight != nil {
		weight = int64(*sp.Spec.QueueWeight)
	}
	switch sp.Spec.Scheduler {
	case "volcano":
		return schema.GroupVersionKind{
				Group: "scheduling.volcano.sh", Version: "v1beta1", Kind: "Queue",
			}, map[string]any{
				"weight":      weight,
				"reclaimable": sp.Spec.Preemption,
			}
	case "yunikorn":
		return schema.GroupVersionKind{
				Group: "yunikorn.apache.org", Version: "v1alpha1", Kind: "Queue",
			}, map[string]any{
				"weight": weight,
				"preemption": map[string]any{
					"allowPreemptSelf": sp.Spec.Preemption,
				},
			}
	default:
		// caller guards on scheduler != "default"
		return schema.GroupVersionKind{}, nil
	}
}

func (r *SchedulingPolicyReconciler) reconcileDelete(ctx context.Context, sp *framev1beta1.SchedulingPolicy) (ctrl.Result, error) {
	// status.ownedPriorityClass, not spec.priorityClass. The spec is what the
	// author of this object asked for and can name anything in the cluster;
	// the status is what this controller actually created. Deleting by the
	// former is how a namespaced right to write a SchedulingPolicy became a
	// cluster-scoped delete.
	if owned := sp.Status.OwnedPriorityClass; owned != "" {
		if err := r.releasePriorityClass(ctx, sp, owned); err != nil {
			return ctrl.Result{}, err
		}
	}

	if sp.Spec.QueueName != "" && sp.Spec.Scheduler != "default" {
		gvk, _ := queueGVKAndSpec(sp)
		q := &unstructured.Unstructured{}
		q.SetGroupVersionKind(gvk)
		q.SetName(sp.Spec.QueueName)
		if err := r.Delete(ctx, q); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(sp, schedulingPolicyFinalizer)
	return ctrl.Result{}, r.Update(ctx, sp)
}

func appliedMsg(sp *framev1beta1.SchedulingPolicy) string {
	msg := fmt.Sprintf("scheduler=%s", sp.Spec.Scheduler)
	if sp.Spec.PriorityClass != "" {
		msg += fmt.Sprintf(" priorityClass=%s", sp.Spec.PriorityClass)
	}
	if sp.Spec.QueueName != "" {
		msg += fmt.Sprintf(" queue=%s", sp.Spec.QueueName)
	}
	return msg
}

// SetupWithManager sets up the controller with the Manager.
func (r *SchedulingPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.SchedulingPolicy{}).
		Named("schedulingpolicy").
		Complete(r)
}
