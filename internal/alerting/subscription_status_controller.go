package alerting

import (
	"context"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

const statusRecomputeEvery = time.Minute

// SubscriptionStatusReconciler summarises the relay records into the
// subscription's status: the health of the link, visible in the console.
type SubscriptionStatusReconciler struct {
	Client    client.Client
	Namespace string
	Now       func() time.Time

	// lastComputed backs the once-a-minute gate for reconciles that end up
	// skipping the write (see computedAt below): ComputedAt in status only
	// advances when something actually changed, so an event-triggered
	// reconcile shortly after a no-op pass cannot rely on it alone to avoid
	// recomputing again within the same minute.
	mu           sync.Mutex
	lastComputed map[string]time.Time
}

// computedAt returns the most recent time this subscription's status was
// last recomputed (written or not), or the zero time if never. It combines
// the persisted ComputedAt (valid across a restart, as long as the
// generation it was computed against still matches) with the in-memory
// record of no-op passes.
func (r *SubscriptionStatusReconciler) computedAt(key types.NamespacedName, sub *framev1beta1.FrameAlertSubscription) time.Time {
	var last time.Time
	if sub.Status.ComputedAt != nil && sub.Status.ObservedGeneration == sub.Generation {
		last = sub.Status.ComputedAt.Time
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.lastComputed[key.String()]; ok && t.After(last) {
		last = t
	}
	return last
}

func (r *SubscriptionStatusReconciler) rememberComputedAt(key types.NamespacedName, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastComputed == nil {
		r.lastComputed = make(map[string]time.Time)
	}
	r.lastComputed[key.String()] = now
}

func (r *SubscriptionStatusReconciler) forgetComputedAt(key types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.lastComputed, key.String())
}

func (r *SubscriptionStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var sub framev1beta1.FrameAlertSubscription
	if err := r.Client.Get(ctx, req.NamespacedName, &sub); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Subscription was deleted; clean up the gauge series
			pendingDeliveries.DeleteLabelValues(req.Name)
			r.forgetComputedAt(req.NamespacedName)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	now := r.Now()
	if last := r.computedAt(req.NamespacedName, &sub); !last.IsZero() {
		if wait := last.Add(statusRecomputeEvery).Sub(now); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}

	var alerts framev1beta1.FrameAlertList
	if err := r.Client.List(ctx, &alerts, client.InNamespace(r.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	var pending int32
	var lastSuccess, lastErrorAt *metav1.Time
	lastError := ""
	for i := range alerts.Items {
		a := &alerts.Items[i]
		if a.Status.State == "" || !Matches(sub.Spec.Filter, a) {
			continue
		}
		var d *framev1beta1.AlertDelivery
		for j := range a.Status.Deliveries {
			if a.Status.Deliveries[j].Subscription == sub.Name {
				d = &a.Status.Deliveries[j]
			}
		}
		if d == nil || d.DeliveredState != a.Status.State {
			pending++
		}
		if d == nil {
			continue
		}
		if d.LastDeliveredAt != nil && (lastSuccess == nil || d.LastDeliveredAt.After(lastSuccess.Time)) {
			lastSuccess = d.LastDeliveredAt
		}
		if d.LastError != "" && d.LastAttemptAt != nil && (lastErrorAt == nil || d.LastAttemptAt.After(lastErrorAt.Time)) {
			lastErrorAt, lastError = d.LastAttemptAt, d.LastError
		}
	}
	pendingDeliveries.WithLabelValues(sub.Name).Set(float64(pending))

	// Preserve lastSuccessAt: never regress to an older value
	if lastSuccess == nil || (sub.Status.LastSuccessAt != nil && sub.Status.LastSuccessAt.After(lastSuccess.Time)) {
		lastSuccess = sub.Status.LastSuccessAt
	}

	// This reconcile counts as "computed" whether or not anything below
	// changes: gates the next minute either way.
	r.rememberComputedAt(req.NamespacedName, now)

	changed := sub.Status.ObservedGeneration != sub.Generation ||
		sub.Status.PendingDeliveries != pending ||
		!equalTimes(sub.Status.LastSuccessAt, lastSuccess) ||
		sub.Status.LastError != lastError
	if !changed {
		return ctrl.Result{RequeueAfter: statusRecomputeEvery}, nil
	}

	orig := sub.DeepCopy()
	computed := metav1.NewTime(now)
	sub.Status.ObservedGeneration = sub.Generation
	sub.Status.PendingDeliveries = pending
	sub.Status.LastSuccessAt = lastSuccess
	sub.Status.LastError = lastError
	sub.Status.ComputedAt = &computed
	if err := r.Client.Status().Patch(ctx, &sub, client.MergeFrom(orig)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: statusRecomputeEvery}, nil
}

func equalTimes(a, b *metav1.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(b)
}

func (r *SubscriptionStatusReconciler) SetupWithManager(mgr ctrl.Manager) error {
	allSubs := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		var list framev1beta1.FrameAlertSubscriptionList
		if err := mgr.GetClient().List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(list.Items))
		for _, s := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: s.Namespace, Name: s.Name}})
		}
		return reqs
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("framealertsubscription-status").
		For(&framev1beta1.FrameAlertSubscription{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&framev1beta1.FrameAlert{}, allSubs).
		Complete(r)
}
