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

// The three values spec.scheduler's enum admits. Named because they are read
// in five places between Reconcile, the claim, the release and the GVK
// mapping, and a typo in any one of them is a silent behaviour change rather
// than a compile error.
//
// schedulerDefault is emphatically not the same string as the reserved queue
// name "default" below, which happens to spell the same and means the queue
// Volcano hands to PodGroups that name none. They are never interchangeable.
const (
	schedulerVolcano  = "volcano"
	schedulerYunikorn = "yunikorn"
	schedulerDefault  = "default"
)

// reservedQueueNames are the scheduler Queue names that belong to the
// scheduler rather than to anyone who can write a SchedulingPolicy. Nothing
// this controller does may create, adopt, update or delete one, whatever any
// status record says.
//
// Exact names and not a prefix, unlike reservedPriorityClassPrefix above, and
// the difference is not cosmetic — it is what each system actually reserves.
// Kubernetes reserves a whole namespace of PriorityClass names (system-*).
// Volcano and YuniKorn reserve two specific queues:
//
//   - "default" is the queue Volcano creates at startup and gives to every
//     PodGroup that names none, and Volcano's own admission webhook
//     (volcano-admission-service-queues-validate, which is registered on
//     DELETE as well as CREATE and UPDATE) refuses to delete it outright.
//   - "root" is the root of the queue hierarchy: auto-created with ID "root"
//     when Volcano's capacity plugin runs hierarchical queues, and the name
//     YuniKorn gives the root of its own queue tree.
//
// Both exist on the live cluster, owned by nobody. A prefix rule here would
// instead lock out ordinary names — "default-batch", "root-cause" — that
// nothing reserves.
var reservedQueueNames = map[string]struct{}{
	"default": {},
	"root":    {},
}

// Reasons written to status.conditions[Ready].reason, and to the Warning
// event, when a PriorityClass or a Queue is refused. All are terminal for the
// current generation: Reconcile returns no error and no requeue, so a refusal
// costs one reconcile and then waits for the spec to change rather than
// spinning.
const (
	reasonPriorityClassNotOwned = "PriorityClassNotOwned"
	reasonPriorityClassReserved = "PriorityClassReserved"
	reasonQueueNotOwned         = "QueueNotOwned"
	reasonQueueReserved         = "QueueReserved"
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

// queueKindUnavailable wraps the error claimQueue gets back when the cluster
// has no Queue CRD for the requested scheduler, so discovery cannot even map
// the kind and no request is ever sent.
//
// It exists because this is the one ownership check that must not behave like
// the PriorityClass one. PriorityClass is a core kind and is always there;
// Volcano and YuniKorn are optional, and a Frame install on a cluster without
// them has always degraded here rather than failed — Reconcile logs it,
// records Ready=False/ReconcileError and returns nil. Returning an error
// instead would requeue with backoff forever on every such cluster. So this is
// neither a refusal (nobody is being told no) nor a failure to retry: it is
// routed to the degradation that was already there.
type queueKindUnavailable struct{ err error }

func (e *queueKindUnavailable) Error() string { return e.err.Error() }
func (e *queueKindUnavailable) Unwrap() error { return e.err }

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

	// The Queue is claimed on the same rule and for the same reason: a Volcano
	// Queue is cluster-scoped too, and the claim runs BEFORE AddFinalizer so a
	// refused policy walks away holding no right to delete one.
	//
	// Only the third branch is new, and it is the difference between the two
	// resources: the Queue kind may not exist on this cluster at all. That has
	// always degraded rather than failed here, and must keep degrading — see
	// queueKindUnavailable — so it is carried down to the queue degradation
	// below instead of being returned.
	queueClaimErr := r.claimQueue(ctx, &sp)
	if queueClaimErr != nil {
		var refusal *ownershipRefusal
		var unavailable *queueKindUnavailable
		switch {
		case errors.As(queueClaimErr, &refusal):
			return ctrl.Result{}, r.refuse(ctx, &sp, refusal)
		case errors.As(queueClaimErr, &unavailable):
			// Handled with the rest of the queue degradation, below.
		default:
			return ctrl.Result{}, fmt.Errorf("claiming Queue: %w", queueClaimErr)
		}
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

	if sp.Spec.QueueName != "" && sp.Spec.Scheduler != schedulerDefault {
		// queueClaimErr can only be a *queueKindUnavailable by now: a refusal
		// and any other error both returned above. An unclaimed queue must not
		// fall through to reconcileQueue, which would write to a name this
		// policy does not own — so the claim error stands in for it.
		err := queueClaimErr
		if err == nil {
			err = r.reconcileQueue(ctx, &sp)
		}
		if err != nil {
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

// reservedQueueName reports whether name is one the scheduler reserves for
// itself. Checked on every path that would create, update or delete a Queue,
// including the ones a record in status is supposed to have made safe.
func reservedQueueName(name string) bool {
	_, ok := reservedQueueNames[name]
	return ok
}

// markedBy reports whether an object carrying these labels is one this
// controller writes for sp — the marks go on every PriorityClass and every
// Queue it creates.
//
// It takes the label map rather than an object because the two resources
// arrive differently: a PriorityClass is a typed schedulingv1 struct, a Queue
// is an unstructured.Unstructured whose fields are read by path. Both can hand
// over a map[string]string, and one function over that map is the whole point
// — a second, Queue-shaped ownership check living beside this one would be a
// second mechanism to keep in agreement.
//
// This is never asked in order to decide that something IS ours — labels are
// writable by anyone who can patch the object, and on this cluster the
// PriorityClasses *and* the Queues the old code took over already carry these
// very labels, written during the takeover. It is only ever asked to decide
// that something is NOT ours any more, on top of a status record that already
// said it was.
func markedBy(labels map[string]string, sp *framev1beta1.SchedulingPolicy) bool {
	return labels[policyNamespaceLabel] == sp.Namespace &&
		labels[policyNameLabel] == sp.Name
}

// describeManager names, for the refusal message, whoever the existing object
// looks like it belongs to. Message text only — the refusal itself does not
// depend on finding anything here, because "no recognisable manager" is not a
// licence to adopt. Takes the two metadata maps, for the same reason markedBy
// does.
func describeManager(labels, annotations map[string]string) string {
	if rel := annotations["meta.helm.sh/release-name"]; rel != "" {
		if ns := annotations["meta.helm.sh/release-namespace"]; ns != "" {
			return fmt.Sprintf("it belongs to Helm release %s/%s", ns, rel)
		}
		return fmt.Sprintf("it belongs to Helm release %s", rel)
	}
	if by := labels["app.kubernetes.io/managed-by"]; by != "" {
		return fmt.Sprintf("it is managed by %q", by)
	}
	if _, applied := annotations["kubectl.kubernetes.io/last-applied-configuration"]; applied {
		return "it was created with kubectl apply"
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
					want, sp.Namespace, sp.Name, describeManager(existing.Labels, existing.Annotations)),
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

	if !markedBy(pc.Labels, sp) {
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

// claimQueue brings status.ownedQueue / status.ownedQueueScheduler into
// agreement with spec.queueName / spec.scheduler, and is the only place those
// records are ever written. It is claimPriorityClass, one resource further
// along, and deliberately the same shape: a name is claimable only when
// nothing sits there, an existing Queue is refused and never absorbed, and the
// record — not the cluster, not a label — is afterwards what says the name is
// this policy's.
//
// Two things differ from the PriorityClass, both forced by the resource:
//
//   - The record is a pair. A PriorityClass name identifies an object on its
//     own; "Queue" does not, because spec.scheduler chooses between
//     scheduling.volcano.sh and yunikorn.apache.org. Recording only the name
//     would let a flip of spec.scheduler point the release at the wrong
//     GroupKind, deleting nothing and stranding the Queue this controller did
//     create. So the scheduler is recorded beside the name and the release
//     reads both.
//   - The kind may not exist. Discovery failing to map it is returned wrapped
//     in queueKindUnavailable, which Reconcile routes to the degradation that
//     was already there rather than to a refusal or an error.
//
// Ordering is claimPriorityClass's, for the same reasons: the new name is
// checked free first, so a refused rename leaves the old Queue untouched; then
// the old one is released; and only then does the record move, so a crash
// between the two leaves a record that still names the old Queue and a next
// reconcile that retries the release.
func (r *SchedulingPolicyReconciler) claimQueue(ctx context.Context, sp *framev1beta1.SchedulingPolicy) error {
	// The guard Reconcile has always used for the queue paths, read once
	// here: no queue name, or a scheduler with no Queue kind, means this
	// policy wants no Queue — and wanting none is what releases the one it
	// used to own.
	var wantName, wantScheduler string
	if sp.Spec.QueueName != "" && sp.Spec.Scheduler != schedulerDefault {
		wantName, wantScheduler = sp.Spec.QueueName, sp.Spec.Scheduler
	}
	ownedName, ownedScheduler := sp.Status.OwnedQueue, sp.Status.OwnedQueueScheduler
	if wantName == ownedName && wantScheduler == ownedScheduler {
		return nil
	}

	if wantName != "" {
		if reservedQueueName(wantName) {
			return &ownershipRefusal{
				Reason: reasonQueueReserved,
				Message: fmt.Sprintf(
					"Queue %q is reserved by the scheduler itself, and this controller will neither create, adopt nor delete it",
					wantName),
			}
		}
		gvk, ok := queueGVKFor(wantScheduler)
		if !ok {
			// Unreachable while spec.scheduler is an enum of the three names
			// queueGVKFor knows; here so that widening the enum without
			// widening queueGVKFor cannot silently mean "claim nothing, then
			// write anyway".
			return fmt.Errorf("no Queue kind is known for scheduler %q", wantScheduler)
		}
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(gvk)
		err := r.Get(ctx, types.NamespacedName{Name: wantName}, existing)
		switch {
		case err == nil:
			return &ownershipRefusal{
				Reason: reasonQueueNotOwned,
				Message: fmt.Sprintf(
					"%s Queue %q already exists and was not created by SchedulingPolicy %s/%s (%s); refusing to adopt it",
					wantScheduler, wantName, sp.Namespace, sp.Name,
					describeManager(existing.GetLabels(), existing.GetAnnotations())),
			}
		case meta.IsNoMatchError(err):
			return &queueKindUnavailable{fmt.Errorf("the %s Queue kind is not installed on this cluster: %w", wantScheduler, err)}
		case !apierrors.IsNotFound(err):
			return fmt.Errorf("checking whether %s Queue %q is free: %w", wantScheduler, wantName, err)
		}
	}

	if ownedName != "" {
		if err := r.releaseQueue(ctx, sp, ownedScheduler, ownedName); err != nil {
			return err
		}
	}

	patch := client.MergeFrom(sp.DeepCopy())
	sp.Status.OwnedQueue = wantName
	sp.Status.OwnedQueueScheduler = wantScheduler
	if err := r.Status().Patch(ctx, sp, patch); err != nil {
		return fmt.Errorf("recording ownership of %s Queue %q: %w", wantScheduler, wantName, err)
	}
	return nil
}

// releaseQueue deletes a Queue this policy recorded as its own — and nothing
// else. releasePriorityClass, for the other cluster-scoped resource, with the
// same two conditions and the same reading of each.
//
// The record (status.ownedQueue plus status.ownedQueueScheduler) is the
// authority: every caller passes a name and a scheduler read straight out of
// it, so a Queue this controller never claimed cannot reach this function. The
// labels are corroboration on top of it, read by path off the unstructured
// object rather than off a typed field, for the one case the record cannot
// see — the Queue we created was deleted out from under us and something else
// recreated a different object under the same name. The UID precondition
// closes the same window between this Get and this Delete.
//
// The reserved-name check is unreachable through claimQueue, which refuses
// those names long before they could be recorded. It is here anyway because
// this is a delete of a cluster-scoped object and the check costs one map
// lookup.
func (r *SchedulingPolicyReconciler) releaseQueue(ctx context.Context, sp *framev1beta1.SchedulingPolicy, scheduler, name string) error {
	log := logf.FromContext(ctx)
	if reservedQueueName(name) {
		log.Info("Not deleting a reserved Queue", "queue", name, "scheduler", scheduler)
		return nil
	}
	gvk, ok := queueGVKFor(scheduler)
	if !ok {
		// A record naming a scheduler with no Queue kind. Nothing correct is
		// deletable from it, and guessing a GroupKind is exactly the mistake
		// recording the scheduler exists to prevent.
		log.Info("Not deleting Queue: the ownership record names no Queue kind",
			"queue", name, "scheduler", scheduler, "policy", sp.Namespace+"/"+sp.Name)
		return nil
	}

	q := &unstructured.Unstructured{}
	q.SetGroupVersionKind(gvk)
	err := r.Get(ctx, types.NamespacedName{Name: name}, q)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case meta.IsNoMatchError(err):
		// The CRD is gone, so every object of the kind went with it. Returning
		// an error here would wedge reconcileDelete and leave the finalizer on
		// a SchedulingPolicy forever, over an object that cannot exist.
		log.Info("Not deleting Queue: the kind is no longer installed",
			"queue", name, "scheduler", scheduler)
		return nil
	case err != nil:
		return fmt.Errorf("reading %s Queue %q before deleting it: %w", scheduler, name, err)
	}

	if !markedBy(q.GetLabels(), sp) {
		log.Info("Not deleting Queue: it no longer carries this policy's marks",
			"queue", name, "scheduler", scheduler, "policy", sp.Namespace+"/"+sp.Name)
		return nil
	}

	uid := q.GetUID()
	if err := r.Delete(ctx, q, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting %s Queue %q: %w", scheduler, name, err)
	}
	return nil
}

// refuse records an ownershipRefusal — from either claim — on the
// SchedulingPolicy and stops.
//
// It returns nil and Reconcile returns an empty Result: a refusal is a
// decision about the spec, not a transient failure, so re-running it
// immediately would only reach the same conclusion. The next reconcile comes
// from the spec changing (or the informer's resync), which is exactly when the
// answer could differ.
func (r *SchedulingPolicyReconciler) refuse(ctx context.Context, sp *framev1beta1.SchedulingPolicy, refusal *ownershipRefusal) error {
	logf.FromContext(ctx).Info("Refusing to act on a cluster-scoped object",
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
	// claimQueue runs first in Reconcile and returns nil only once the record
	// and the spec agree, so this can only fire if someone reorders Reconcile
	// — which is precisely the change that would reintroduce the adoption bug.
	// It fails loudly rather than writing to a name this policy does not own.
	if sp.Status.OwnedQueue != sp.Spec.QueueName || sp.Status.OwnedQueueScheduler != sp.Spec.Scheduler {
		return fmt.Errorf("refusing to write %s Queue %q: SchedulingPolicy %s/%s owns %q (%s)",
			sp.Spec.Scheduler, sp.Spec.QueueName, sp.Namespace, sp.Name,
			sp.Status.OwnedQueue, sp.Status.OwnedQueueScheduler)
	}
	if reservedQueueName(sp.Spec.QueueName) {
		return fmt.Errorf("refusing to write reserved Queue %q", sp.Spec.QueueName)
	}

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

// queueGVKFor maps a scheduler name to the Queue kind it uses, and is the only
// place that mapping lives. The bool is false for a scheduler with no Queue
// kind (schedulerDefault) and for anything spec.scheduler's enum might grow to that
// this function has not been taught — callers must not guess.
//
// It is split out of queueGVKAndSpec because the release path needs the kind
// alone: it resolves it from status.ownedQueueScheduler, a record of what was
// claimed, where there is no spec to build a body from.
func queueGVKFor(scheduler string) (schema.GroupVersionKind, bool) {
	switch scheduler {
	case schedulerVolcano:
		return schema.GroupVersionKind{Group: "scheduling.volcano.sh", Version: "v1beta1", Kind: "Queue"}, true
	case schedulerYunikorn:
		return schema.GroupVersionKind{Group: "yunikorn.apache.org", Version: "v1alpha1", Kind: "Queue"}, true
	default:
		return schema.GroupVersionKind{}, false
	}
}

// queueGVKAndSpec returns GVK + spec fields for the scheduler-native queue type.
func queueGVKAndSpec(sp *framev1beta1.SchedulingPolicy) (schema.GroupVersionKind, map[string]any) {
	gvk, ok := queueGVKFor(sp.Spec.Scheduler)
	if !ok {
		// caller guards on scheduler != "default"
		return gvk, nil
	}
	weight := int64(1)
	if sp.Spec.QueueWeight != nil {
		weight = int64(*sp.Spec.QueueWeight)
	}
	if sp.Spec.Scheduler == schedulerYunikorn {
		return gvk, map[string]any{
			"weight": weight,
			"preemption": map[string]any{
				"allowPreemptSelf": sp.Spec.Preemption,
			},
		}
	}
	return gvk, map[string]any{
		"weight":      weight,
		"reclaimable": sp.Spec.Preemption,
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

	// status.ownedQueue, not spec.queueName — and status.ownedQueueScheduler,
	// not spec.scheduler. Same reading as the PriorityClass above, and the
	// same hole it closes: this was a bare Delete by a name the author of the
	// object chose, on a kind the same author chose, against a cluster-scoped
	// resource. Naming any Queue on the cluster was enough to have it deleted.
	if owned := sp.Status.OwnedQueue; owned != "" {
		if err := r.releaseQueue(ctx, sp, sp.Status.OwnedQueueScheduler, owned); err != nil {
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
