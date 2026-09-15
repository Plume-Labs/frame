package alerting

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
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
}

func (r *SubscriptionStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var sub framev1beta1.FrameAlertSubscription
	if err := r.Client.Get(ctx, req.NamespacedName, &sub); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	now := r.Now()
	if c := sub.Status.ComputedAt; c != nil && sub.Status.ObservedGeneration == sub.Generation {
		if wait := c.Add(statusRecomputeEvery).Sub(now); wait > 0 {
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
		For(&framev1beta1.FrameAlertSubscription{}).
		Watches(&framev1beta1.FrameAlert{}, allSubs).
		Complete(r)
}
