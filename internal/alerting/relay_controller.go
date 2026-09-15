package alerting

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// AlertTokenLabel must be set to "true" on any Secret the relay is allowed to
// read as a subscription's bearer token. A FrameAlertSubscription lives in a
// namespace a frame-admin can write to without holding Secret access there;
// without this check, tokenSecretRef could be pointed at an arbitrary Secret
// (e.g. the receiver's own token) and its value exfiltrated to the
// subscription's URL.
const AlertTokenLabel = "frame.plume-labs.io/alert-token"

// errUnlabelledToken means the Secret exists and has the requested key, but
// is missing AlertTokenLabel: this is a permanent misconfiguration, not a
// transient read failure, so the relay must not retry it.
type errUnlabelledToken struct{ secret string }

func (e *errUnlabelledToken) Error() string {
	return fmt.Sprintf("token Secret %s is not labelled %s=true", e.secret, AlertTokenLabel)
}

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
	state := fa.Status.State
	if state == "" {
		return ctrl.Result{}, nil // the receiver has not written the state yet
	}
	var subs framev1beta1.FrameAlertSubscriptionList
	if err := r.Client.List(ctx, &subs, client.InNamespace(r.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	now := r.Now()

	existing := map[string]framev1beta1.AlertDelivery{}
	for _, d := range fa.Status.Deliveries {
		existing[d.Subscription] = d
	}
	var next []framev1beta1.AlertDelivery
	var requeue time.Duration
	for i := range subs.Items {
		sub := &subs.Items[i]
		d, ok := existing[sub.Name]
		// A subscription still mid-incident (delivered Firing, not yet
		// Excluded) keeps being driven below for the resolution alone, even
		// once its filter stops matching: the tenant already has it open.
		keepDriving := ok && d.DeliveredState == framev1beta1.AlertStateFiring && !d.Excluded
		if !Matches(sub.Spec.Filter, &fa) && !keepDriving {
			// Spec 5.3: a filter that does not match must not manufacture
			// an alert the tenant never had open, nor let a later widen
			// replay one it already missed.
			if state != framev1beta1.AlertStateResolved {
				// Firing, and keepDriving already ruled out anything
				// genuinely open for this subscription: no entry at all
				// (also drops any stale Excluded entry from a prior
				// resolution once the alert reopens as a new incident).
				continue
			}
			switch {
			case ok && d.Excluded:
				// Already recorded as excluded and still resolved: leave
				// it untouched -- not even SubscriptionGeneration -- so an
				// unrelated subscription-spec edit cannot force a status
				// write here.
			case ok && d.DeliveredState == state:
				// Really delivered while the tenant still had the incident
				// open (the resolution-only path below); keep the genuine
				// record as-is, do not relabel it Excluded.
			default:
				// Never delivered Resolved for this subscription: record
				// it as excluded without ever sending, on a clean slate --
				// any Attempts/LastError/PermanentFailure left over from
				// when it still matched the filter no longer describe
				// anything the tenant needs to see.
				d = framev1beta1.AlertDelivery{Subscription: sub.Name, Excluded: true, DeliveredState: state}
			}
			next = append(next, d)
			continue
		}
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
		switch {
		case !ok && state == framev1beta1.AlertStateResolved && sub.CreationTimestamp.After(fa.CreationTimestamp.Time):
			// Spec §5.3: a subscription created after the alert already
			// resolved must not be replayed the incident it never
			// subscribed to. Mark it delivered without sending anything.
			d.DeliveredState = state
		default:
			if wait := r.deliver(ctx, sub, &fa, &d, state, now); wait > 0 && (requeue == 0 || wait < requeue) {
				requeue = wait
			}
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
			// A resourceVersion precondition: if the object changed underneath
			// us since the Get above (e.g. a concurrent delivery write), a
			// stale delete could otherwise discard that write's effect.
			err := r.Client.Delete(ctx, &fa, client.Preconditions{ResourceVersion: &fa.ResourceVersion})
			if apierrors.IsConflict(err) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, client.IgnoreNotFound(err)
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
		var unlabelled *errUnlabelledToken
		if errors.As(err, &unlabelled) {
			return r.fail(sub, d, Permanent, err, state, now)
		}
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
		d.Excluded = false
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
	if s.Labels[AlertTokenLabel] != "true" {
		return "", &errUnlabelledToken{secret: key.Name}
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
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
		For(&framev1beta1.FrameAlert{}).
		// GenerationChangedPredicate: Task 7 writes subscription status (up to
		// once a minute) without touching spec, which must not re-enqueue
		// every FrameAlert in the namespace on that cadence. Delete events
		// still pass through — they carry no generation to compare.
		Watches(&framev1beta1.FrameAlertSubscription{}, allAlerts, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
