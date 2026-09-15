package alerting

import (
	"context"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framealerts,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framealerts/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framealertsubscriptions,verbs=get;list;watch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framealertsubscriptions/status,verbs=get;patch;update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// RelayReconciler delivers each FrameAlert's current state to the
// subscriptions whose filter accepts it, then purges it once resolved,
// delivered everywhere and past retention. Runs on the leader only.
type RelayReconciler struct {
	Client      client.Client
	TokenReader client.Reader
	Namespace   string
	Sender      *Sender
	Retention   time.Duration
	Now         func() time.Time
}

func (r *RelayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fa framev1beta1.FrameAlert
	if err := r.Client.Get(ctx, req.NamespacedName, &fa); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var subs framev1beta1.FrameAlertSubscriptionList
	if err := r.Client.List(ctx, &subs, client.InNamespace(r.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	now := r.Now()
	state := fa.Status.State
	if state == "" {
		return ctrl.Result{}, nil // the receiver has not written the state yet
	}

	existing := map[string]framev1beta1.AlertDelivery{}
	for _, d := range fa.Status.Deliveries {
		existing[d.Subscription] = d
	}
	var next []framev1beta1.AlertDelivery
	var requeue time.Duration
	for i := range subs.Items {
		sub := &subs.Items[i]
		if !Matches(sub.Spec.Filter, &fa) {
			continue // not in the filter (any more): no entry, nothing sent
		}
		d, ok := existing[sub.Name]
		if !ok {
			d = framev1beta1.AlertDelivery{Subscription: sub.Name}
		}
		if d.SubscriptionGeneration != sub.Generation {
			d.SubscriptionGeneration = sub.Generation
			d.PermanentFailure, d.FailedState = false, ""
		}
		if d.PermanentFailure && d.FailedState != state {
			d.PermanentFailure, d.FailedState = false, ""
		}
		if wait := r.deliver(ctx, sub, &fa, &d, state, now); wait > 0 && (requeue == 0 || wait < requeue) {
			requeue = wait
		}
		next = append(next, d)
	}
	sort.Slice(next, func(i, j int) bool { return next[i].Subscription < next[j].Subscription })

	if !equality.Semantic.DeepEqual(next, fa.Status.Deliveries) {
		orig := fa.DeepCopy()
		fa.Status.Deliveries = next
		// Merge patch of deliveries only: state/lastReceivedAt belong to the receiver.
		if err := r.Client.Status().Patch(ctx, &fa, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
	}

	if state == framev1beta1.AlertStateResolved && fa.Spec.EndsAt != nil && allDelivered(next, state) {
		purgeAt := fa.Spec.EndsAt.Add(r.Retention)
		if !now.Before(purgeAt) {
			return ctrl.Result{}, client.IgnoreNotFound(r.Client.Delete(ctx, &fa))
		}
		if wait := purgeAt.Sub(now); requeue == 0 || wait < requeue {
			requeue = wait
		}
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// deliver updates d in place and returns how long to wait before the next
// attempt (0 when nothing is due).
func (r *RelayReconciler) deliver(ctx context.Context, sub *framev1beta1.FrameAlertSubscription,
	fa *framev1beta1.FrameAlert, d *framev1beta1.AlertDelivery, state string, now time.Time) time.Duration {
	if sub.Spec.Paused || d.PermanentFailure || d.DeliveredState == state {
		return 0
	}
	if d.Attempts > 0 && d.LastAttemptAt != nil {
		if due := d.LastAttemptAt.Add(Backoff(d.Attempts)); now.Before(due) {
			return due.Sub(now)
		}
	}
	token, err := r.token(ctx, sub)
	if err != nil {
		return r.fail(sub, d, Retry, err, state, now)
	}
	sequence := []string{state}
	if d.DeliveredState == "" && state == framev1beta1.AlertStateResolved {
		// Otherwise the tenant gets a resolution for a fingerprint it never
		// saw, ignores it, and the incident exists nowhere.
		sequence = []string{framev1beta1.AlertStateFiring, state}
	}
	for _, st := range sequence {
		if d.DeliveredState == st {
			continue
		}
		out, err := r.Sender.Send(ctx, sub.Spec.URL, token, sub.Name, fa, st)
		if out != Delivered {
			return r.fail(sub, d, out, err, state, now)
		}
		deliveries.WithLabelValues(sub.Name, Delivered.String()).Inc()
		t := metav1.NewTime(now)
		d.DeliveredState, d.Attempts, d.LastError, d.LastAttemptAt, d.LastDeliveredAt = st, 0, "", &t, &t
	}
	return 0
}

func (r *RelayReconciler) fail(sub *framev1beta1.FrameAlertSubscription, d *framev1beta1.AlertDelivery,
	out Outcome, err error, state string, now time.Time) time.Duration {
	deliveries.WithLabelValues(sub.Name, out.String()).Inc()
	t := metav1.NewTime(now)
	d.LastAttemptAt = &t
	if err != nil {
		d.LastError = truncate(err.Error(), 1024)
	}
	if out == Permanent {
		d.PermanentFailure, d.FailedState = true, state
		return 0
	}
	d.Attempts++
	return Backoff(d.Attempts)
}

func (r *RelayReconciler) token(ctx context.Context, sub *framev1beta1.FrameAlertSubscription) (string, error) {
	var s corev1.Secret
	key := types.NamespacedName{Namespace: sub.Namespace, Name: sub.Spec.TokenSecretRef.Name}
	if err := r.TokenReader.Get(ctx, key, &s); err != nil {
		return "", err
	}
	v, ok := s.Data[sub.Spec.TokenSecretRef.Key]
	if !ok || len(v) == 0 {
		return "", apierrors.NewNotFound(corev1.Resource("secrets"), key.Name+"/"+sub.Spec.TokenSecretRef.Key)
	}
	return string(v), nil
}

func allDelivered(ds []framev1beta1.AlertDelivery, state string) bool {
	for _, d := range ds {
		if d.DeliveredState != state {
			return false
		}
	}
	return true
}

func (r *RelayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// ponytail: a subscription change re-enqueues every alert in the namespace
	// (hundreds at most). Narrow to Firing + pending if that ever shows up in
	// reconcile latency.
	allAlerts := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		var list framev1beta1.FrameAlertList
		if err := mgr.GetClient().List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(list.Items))
		for _, a := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}})
		}
		return reqs
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("framealert-relay").
		For(&framev1beta1.FrameAlert{}).
		Watches(&framev1beta1.FrameAlertSubscription{}, allAlerts).
		Complete(r)
}
