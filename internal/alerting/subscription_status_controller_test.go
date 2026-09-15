package alerting

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func alertWith(name, state string, ds ...framev1beta1.AlertDelivery) *framev1beta1.FrameAlert {
	return &framev1beta1.FrameAlert{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       framev1beta1.FrameAlertSpec{Fingerprint: "ab", AlertName: "X"},
		Status:     framev1beta1.FrameAlertStatus{State: state, Deliveries: ds},
	}
}

func at(min int) *metav1.Time {
	t := metav1.NewTime(time.Date(2026, 9, 15, 12, min, 0, 0, time.UTC))
	return &t
}

func TestSubscriptionStatusCountsPendingAndReportsLatestSuccessAndError(t *testing.T) {
	sub := subscription("neura", "http://x", framev1beta1.AlertFilter{})
	objs := []client.Object{
		sub,
		alertWith("fa-1", "Firing", framev1beta1.AlertDelivery{Subscription: "neura", DeliveredState: "Firing", LastDeliveredAt: at(1)}),
		alertWith("fa-2", "Resolved", framev1beta1.AlertDelivery{Subscription: "neura", DeliveredState: "Firing", LastDeliveredAt: at(3),
			LastError: "HTTP 503", LastAttemptAt: at(4)}),
		alertWith("fa-3", "Firing"), // matches, never evaluated by the relay yet: pending
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameAlertSubscription{}).WithObjects(objs...).Build()
	ck := &clock{t: time.Date(2026, 9, 15, 12, 10, 0, 0, time.UTC)}
	r := &SubscriptionStatusReconciler{Client: c, Namespace: ns, Now: ck.now}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "neura"}}); err != nil {
		t.Fatal(err)
	}
	var got framev1beta1.FrameAlertSubscription
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "neura"}, &got)
	if got.Status.PendingDeliveries != 2 {
		t.Errorf("pending = %d, want 2 (fa-2 resolution, fa-3 never sent)", got.Status.PendingDeliveries)
	}
	if got.Status.LastSuccessAt == nil || !got.Status.LastSuccessAt.Equal(at(3)) {
		t.Errorf("lastSuccessAt = %v, want 12:03", got.Status.LastSuccessAt)
	}
	if got.Status.LastError != "HTTP 503" {
		t.Errorf("lastError = %q", got.Status.LastError)
	}
}

func TestSubscriptionStatusIsRecomputedAtMostOncePerMinute(t *testing.T) {
	sub := subscription("neura", "http://x", framev1beta1.AlertFilter{})
	sub.Status.ComputedAt = at(10)
	sub.Status.ObservedGeneration = 1
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameAlertSubscription{}).WithObjects(sub, alertWith("fa-3", "Firing")).Build()
	ck := &clock{t: time.Date(2026, 9, 15, 12, 10, 20, 0, time.UTC)}
	r := &SubscriptionStatusReconciler{Client: c, Namespace: ns, Now: ck.now}

	res, _ := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "neura"}})
	var got framev1beta1.FrameAlertSubscription
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "neura"}, &got)
	if got.Status.PendingDeliveries != 0 {
		t.Fatal("recomputed within the minute")
	}
	if res.RequeueAfter != 40*time.Second {
		t.Fatalf("requeue %v, want 40s", res.RequeueAfter)
	}
}

func TestSubscriptionStatusDropsTheGaugeOfADeletedSubscription(t *testing.T) {
	// Pre-populate the gauge for the deleted subscription
	pendingDeliveries.WithLabelValues("gone").Set(3)

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameAlertSubscription{}).Build()
	ck := &clock{t: time.Date(2026, 9, 15, 12, 10, 0, 0, time.UTC)}
	r := &SubscriptionStatusReconciler{Client: c, Namespace: ns, Now: ck.now}

	// Reconcile a request for a subscription that does not exist
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "gone"}})
	if err != nil {
		t.Fatal(err)
	}

	// Verify the gauge series is removed by checking that a second delete returns false
	// (the first delete happens in Reconcile, the second here should return false)
	if deleted := pendingDeliveries.DeleteLabelValues("gone"); deleted {
		t.Error("gauge series not deleted; second DeleteLabelValues returned true")
	}
}

// M2: recomputing every minute is fine, but writing the status every minute
// even when nothing changed is ~1440 unnecessary PATCHes/day per
// subscription. When ObservedGeneration, PendingDeliveries, LastSuccessAt and
// LastError all come out the same as what's already stored, Reconcile must
// not call Status().Patch at all — it still requeues after a minute.
func TestSubscriptionStatusSkipsTheWriteWhenNothingChanged(t *testing.T) {
	sub := subscription("neura", "http://x", framev1beta1.AlertFilter{})
	sub.Status.ObservedGeneration = 1 // matches sub.Generation set by subscription()
	sub.Status.PendingDeliveries = 1  // matches what fa-3 below will recompute to
	sub.Status.ComputedAt = at(0)
	writes := 0
	funcs := interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subr string, obj client.Object, p client.Patch, o ...client.SubResourcePatchOption) error {
			writes++
			return c.SubResource(subr).Patch(ctx, obj, p, o...)
		},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameAlertSubscription{}).
		WithObjects(sub, alertWith("fa-3", "Firing")). // matches, never delivered: pending stays 1
		WithInterceptorFuncs(funcs).Build()
	ck := &clock{t: time.Date(2026, 9, 15, 12, 11, 0, 0, time.UTC)} // past the 1-minute gate from ComputedAt=12:00
	r := &SubscriptionStatusReconciler{Client: c, Namespace: ns, Now: ck.now}

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "neura"}})
	if err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatalf("unchanged recompute wrote the status %d times, want 0", writes)
	}
	if res.RequeueAfter != statusRecomputeEvery {
		t.Fatalf("requeue %v, want %v", res.RequeueAfter, statusRecomputeEvery)
	}
}

// M3: PendingDeliveries carried `omitempty`, so a merge patch to 0 sends
// null and the field disappears from the stored status, while the rollout
// proof reads `pendingDeliveries: 0`. Force a real write (ObservedGeneration
// stale) with pending draining to 0 and check the field survives JSON
// marshalling explicitly, not just the Go struct's zero value.
func TestSubscriptionStatusKeepsPendingDeliveriesZeroExplicitInJSON(t *testing.T) {
	sub := subscription("neura", "http://x", framev1beta1.AlertFilter{})
	sub.Status.PendingDeliveries = 3
	sub.Status.ObservedGeneration = 0 // stale vs sub.Generation == 1: forces a write
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameAlertSubscription{}).WithObjects(sub).Build() // no FrameAlerts: pending drains to 0
	ck := &clock{t: time.Date(2026, 9, 15, 12, 10, 0, 0, time.UTC)}
	r := &SubscriptionStatusReconciler{Client: c, Namespace: ns, Now: ck.now}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "neura"}}); err != nil {
		t.Fatal(err)
	}
	var got framev1beta1.FrameAlertSubscription
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "neura"}, &got)
	if got.Status.PendingDeliveries != 0 {
		t.Fatalf("pending = %d, want 0", got.Status.PendingDeliveries)
	}
	b, err := json.Marshal(got.Status)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"pendingDeliveries":0`) {
		t.Fatalf("pendingDeliveries not explicit in the JSON status: %s", b)
	}
}

// Finding 1, rule (e): an Excluded entry (recorded without sending, because
// the filter did not match the alert when it resolved) must not count as
// pending or as a success once the filter widens enough to see it again.
func TestSubscriptionStatusExcludedEntriesDoNotCountAsPendingOrSuccess(t *testing.T) {
	sub := subscription("neura", "http://x", framev1beta1.AlertFilter{}) // now matches everything
	objs := []client.Object{
		sub,
		alertWith("fa-1", "Resolved", framev1beta1.AlertDelivery{Subscription: "neura", DeliveredState: "Resolved", Excluded: true}),
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameAlertSubscription{}).WithObjects(objs...).Build()
	ck := &clock{t: time.Date(2026, 9, 15, 12, 10, 0, 0, time.UTC)}
	r := &SubscriptionStatusReconciler{Client: c, Namespace: ns, Now: ck.now}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "neura"}}); err != nil {
		t.Fatal(err)
	}
	var got framev1beta1.FrameAlertSubscription
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "neura"}, &got)
	if got.Status.PendingDeliveries != 0 {
		t.Errorf("pending = %d, want 0 (excluded entry already matches the alert's state)", got.Status.PendingDeliveries)
	}
	if got.Status.LastSuccessAt != nil {
		t.Errorf("lastSuccessAt = %v, want nil (excluded entry never sent anything)", got.Status.LastSuccessAt)
	}
}

func TestSubscriptionStatusKeepsLastSuccessAfterAlertsArePurged(t *testing.T) {
	sub := subscription("neura", "http://x", framev1beta1.AlertFilter{})
	sub.Status.LastSuccessAt = at(3)
	sub.Status.ComputedAt = at(0)
	sub.Status.ObservedGeneration = 1
	// No FrameAlerts at all (all purged)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameAlertSubscription{}).WithObjects(sub).Build()
	ck := &clock{t: time.Date(2026, 9, 15, 12, 10, 0, 0, time.UTC)}
	r := &SubscriptionStatusReconciler{Client: c, Namespace: ns, Now: ck.now}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "neura"}}); err != nil {
		t.Fatal(err)
	}
	var got framev1beta1.FrameAlertSubscription
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "neura"}, &got)
	if got.Status.LastSuccessAt == nil || !got.Status.LastSuccessAt.Equal(at(3)) {
		t.Errorf("lastSuccessAt = %v, want 12:03 (preserved from previous state)", got.Status.LastSuccessAt)
	}
	if got.Status.PendingDeliveries != 0 {
		t.Errorf("pending = %d, want 0 (no alerts)", got.Status.PendingDeliveries)
	}
}
