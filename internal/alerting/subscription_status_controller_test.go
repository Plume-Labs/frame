package alerting

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
