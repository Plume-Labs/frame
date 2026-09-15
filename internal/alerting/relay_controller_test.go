package alerting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// tenant records what it received and answers with the next scripted code.
type tenant struct {
	mu       sync.Mutex
	codes    []int
	received []string // alert statuses, in order
	srv      *httptest.Server
}

func newTenant(codes ...int) *tenant {
	tn := &tenant{codes: codes}
	tn.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Payload
		_ = json.NewDecoder(r.Body).Decode(&p)
		tn.mu.Lock()
		defer tn.mu.Unlock()
		tn.received = append(tn.received, p.Alerts[0].Status)
		code := 200
		if len(tn.codes) > 0 {
			code, tn.codes = tn.codes[0], tn.codes[1:]
		}
		w.WriteHeader(code)
	}))
	return tn
}

func subscription(name, url string, f framev1beta1.AlertFilter) *framev1beta1.FrameAlertSubscription {
	return &framev1beta1.FrameAlertSubscription{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: framev1beta1.FrameAlertSubscriptionSpec{
			URL: url, TokenSecretRef: framev1beta1.AlertTokenRef{Name: name + "-token", Key: "token"}, Filter: f,
		},
	}
}

func subToken(name string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-token", Namespace: ns,
		Labels: map[string]string{AlertTokenLabel: "true"}},
		Data: map[string][]byte{"token": []byte("tok")}}
}

func storedAlert(state string, endsAt *metav1.Time) *framev1beta1.FrameAlert {
	return &framev1beta1.FrameAlert{
		ObjectMeta: metav1.ObjectMeta{Name: "fa-ab12", Namespace: ns},
		Spec: framev1beta1.FrameAlertSpec{Fingerprint: "ab12", AlertName: "KubeCPUOvercommit", Severity: "warning",
			StartsAt: metav1.NewTime(time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC)), EndsAt: endsAt},
		Status: framev1beta1.FrameAlertStatus{State: state},
	}
}

func newRelay(t *testing.T, objs ...client.Object) (*RelayReconciler, client.Client, *clock) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameAlert{}, &framev1beta1.FrameAlertSubscription{}).
		WithObjects(objs...).Build()
	ck := &clock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	return &RelayReconciler{Client: c, TokenReader: c, Namespace: ns, Sender: NewSender(),
		Retention: 14 * 24 * time.Hour, Now: ck.now}, c, ck
}

func reconcileAlert(t *testing.T, r *RelayReconciler) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "fa-ab12"}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func getAlert(t *testing.T, c client.Client) *framev1beta1.FrameAlert {
	t.Helper()
	var fa framev1beta1.FrameAlert
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "fa-ab12"}, &fa); err != nil {
		t.Fatal(err)
	}
	return &fa
}

func delivery(fa *framev1beta1.FrameAlert, sub string) *framev1beta1.AlertDelivery {
	for i := range fa.Status.Deliveries {
		if fa.Status.Deliveries[i].Subscription == sub {
			return &fa.Status.Deliveries[i]
		}
	}
	return nil
}

func TestRelayDeliversAFiringAlertOnce(t *testing.T) {
	tn := newTenant()
	defer tn.srv.Close()
	r, c, _ := newRelay(t, storedAlert("Firing", nil), subscription("neura", tn.srv.URL, framev1beta1.AlertFilter{}), subToken("neura"))

	reconcileAlert(t, r)
	reconcileAlert(t, r)

	if len(tn.received) != 1 || tn.received[0] != "firing" {
		t.Fatalf("tenant received %v", tn.received)
	}
	d := delivery(getAlert(t, c), "neura")
	if d == nil || d.DeliveredState != "Firing" || d.Attempts != 0 || d.LastDeliveredAt == nil {
		t.Fatalf("delivery: %+v", d)
	}
}

func TestRelaySendsFiringThenResolvedForAnAlertNeverDelivered(t *testing.T) {
	tn := newTenant()
	defer tn.srv.Close()
	end := metav1.NewTime(time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC))
	fa := storedAlert("Resolved", &end)
	fa.CreationTimestamp = metav1.NewTime(time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC))
	// A pre-existing subscription: it was there before the alert ever fired,
	// so it must still get the firing-then-resolved replay.
	sub := subscription("neura", tn.srv.URL, framev1beta1.AlertFilter{})
	sub.CreationTimestamp = metav1.NewTime(time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC))
	r, c, _ := newRelay(t, fa, sub, subToken("neura"))

	reconcileAlert(t, r)

	if len(tn.received) != 2 || tn.received[0] != "firing" || tn.received[1] != "resolved" {
		t.Fatalf("tenant received %v, want [firing resolved]", tn.received)
	}
	if d := delivery(getAlert(t, c), "neura"); d.DeliveredState != "Resolved" {
		t.Fatalf("delivery: %+v", d)
	}
}

// Spec §5.3: a subscription created after an alert already resolved must not
// see a firing incident it never subscribed to.
func TestRelayDoesNotReplayResolvedAlertsToANewSubscription(t *testing.T) {
	tn := newTenant()
	defer tn.srv.Close()
	end := metav1.NewTime(time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC))
	fa := storedAlert("Resolved", &end)
	fa.CreationTimestamp = metav1.NewTime(time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC))
	sub := subscription("neura", tn.srv.URL, framev1beta1.AlertFilter{})
	sub.CreationTimestamp = metav1.NewTime(time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	r, c, _ := newRelay(t, fa, sub, subToken("neura"))

	reconcileAlert(t, r)

	if len(tn.received) != 0 {
		t.Fatalf("tenant received %v, want nothing", tn.received)
	}
	if d := delivery(getAlert(t, c), "neura"); d == nil || d.DeliveredState != "Resolved" || d.LastDeliveredAt != nil {
		t.Fatalf("delivery: %+v", d)
	}
}

func TestRelayRetriesA5xxWithBackoffThenDelivers(t *testing.T) {
	tn := newTenant(503)
	defer tn.srv.Close()
	r, c, ck := newRelay(t, storedAlert("Firing", nil), subscription("neura", tn.srv.URL, framev1beta1.AlertFilter{}), subToken("neura"))

	res := reconcileAlert(t, r)
	d := delivery(getAlert(t, c), "neura")
	if d.Attempts != 1 || d.DeliveredState != "" || d.LastError == "" || res.RequeueAfter != 5*time.Second {
		t.Fatalf("after 503: %+v requeue=%v", d, res.RequeueAfter)
	}

	ck.t = ck.t.Add(2 * time.Second)
	reconcileAlert(t, r)
	if len(tn.received) != 1 {
		t.Fatalf("retried before backoff elapsed: %v", tn.received)
	}

	ck.t = ck.t.Add(4 * time.Second)
	reconcileAlert(t, r)
	if d := delivery(getAlert(t, c), "neura"); d.DeliveredState != "Firing" || d.Attempts != 0 {
		t.Fatalf("not delivered after backoff: %+v", d)
	}
}

func TestRelayStopsOnAPermanentFailureUntilTheSubscriptionChanges(t *testing.T) {
	tn := newTenant(401)
	defer tn.srv.Close()
	sub := subscription("neura", tn.srv.URL, framev1beta1.AlertFilter{})
	r, c, ck := newRelay(t, storedAlert("Firing", nil), sub, subToken("neura"))

	reconcileAlert(t, r)
	ck.t = ck.t.Add(time.Hour)
	reconcileAlert(t, r)
	if len(tn.received) != 1 {
		t.Fatalf("401 retried: %v", tn.received)
	}
	if d := delivery(getAlert(t, c), "neura"); !d.PermanentFailure {
		t.Fatalf("not marked permanent: %+v", d)
	}

	var s framev1beta1.FrameAlertSubscription
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "neura"}, &s)
	s.Generation = 2 // the fake client does not bump generation; the token was fixed
	_ = c.Update(context.Background(), &s)
	reconcileAlert(t, r)
	if len(tn.received) != 2 {
		t.Fatalf("generation change did not lift the failure: %v", tn.received)
	}
}

func TestRelayLiftsAPermanentFailureWhenTheAlertChangesState(t *testing.T) {
	tn := newTenant(401)
	defer tn.srv.Close()
	sub := subscription("neura", tn.srv.URL, framev1beta1.AlertFilter{})
	r, c, ck := newRelay(t, storedAlert("Firing", nil), sub, subToken("neura"))

	reconcileAlert(t, r)
	if d := delivery(getAlert(t, c), "neura"); !d.PermanentFailure || d.FailedState != "Firing" {
		t.Fatalf("not marked permanent for Firing: %+v", d)
	}

	var fa framev1beta1.FrameAlert
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "fa-ab12"}, &fa)
	end := metav1.NewTime(ck.t)
	fa.Spec.EndsAt = &end
	_ = c.Update(context.Background(), &fa)
	fa.Status.State = framev1beta1.AlertStateResolved
	_ = c.Status().Update(context.Background(), &fa)

	reconcileAlert(t, r)
	if d := delivery(getAlert(t, c), "neura"); d.PermanentFailure || d.DeliveredState != "Resolved" {
		t.Fatalf("permanent failure not lifted on state change: %+v", d)
	}
	if got := tn.received[len(tn.received)-1]; got != "resolved" {
		t.Fatalf("tenant did not get resolved after the lift: %v", tn.received)
	}
}

func TestRelayHoldsDeliveriesWhilePaused(t *testing.T) {
	tn := newTenant()
	defer tn.srv.Close()
	sub := subscription("neura", tn.srv.URL, framev1beta1.AlertFilter{})
	sub.Spec.Paused = true
	r, c, _ := newRelay(t, storedAlert("Firing", nil), sub, subToken("neura"))
	reconcileAlert(t, r)
	if len(tn.received) != 0 {
		t.Fatal("paused subscription received an alert")
	}
	if d := delivery(getAlert(t, c), "neura"); d == nil || d.DeliveredState != "" {
		t.Fatalf("paused delivery must stay pending: %+v", d)
	}
}

func TestRelayDropsEntriesForDeletedOrNonMatchingSubscriptions(t *testing.T) {
	fa := storedAlert("Firing", nil)
	fa.Status.Deliveries = []framev1beta1.AlertDelivery{{Subscription: "gone", DeliveredState: "Firing"}, {Subscription: "strict"}}
	strict := subscription("strict", "http://unused.invalid", framev1beta1.AlertFilter{Severities: []string{"critical"}})
	r, c, _ := newRelay(t, fa, strict, subToken("strict"))
	reconcileAlert(t, r)
	if got := getAlert(t, c).Status.Deliveries; len(got) != 0 {
		t.Fatalf("stale entries kept: %+v", got)
	}
}

func TestRelayPurgesOnlyDeliveredAlertsPastRetention(t *testing.T) {
	end := metav1.NewTime(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	pending := storedAlert("Resolved", &end)
	pending.Status.Deliveries = []framev1beta1.AlertDelivery{{Subscription: "neura", DeliveredState: "Firing", Attempts: 3,
		LastAttemptAt: &metav1.Time{Time: time.Date(2026, 9, 15, 11, 59, 59, 0, time.UTC)}}}
	sub := subscription("neura", "http://unreachable.invalid:1", framev1beta1.AlertFilter{})
	r, c, _ := newRelay(t, pending, sub, subToken("neura"))

	reconcileAlert(t, r)
	getAlert(t, c) // still there: resolution not delivered yet

	var fa framev1beta1.FrameAlert
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "fa-ab12"}, &fa)
	fa.Status.Deliveries[0].DeliveredState = "Resolved"
	_ = c.Status().Update(context.Background(), &fa)
	reconcileAlert(t, r)
	err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "fa-ab12"}, &fa)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("delivered alert past retention not purged: %v", err)
	}
}

// I1: a frame-admin has no Secret access, but could still create a
// subscription pointing tokenSecretRef at a Secret it does not own (e.g. the
// receiver's own token). Without the label check, the relay would send that
// value as a Bearer token to the subscription's URL: Secret exfiltration.
func TestRelayRefusesAnUnlabelledTokenSecretAndRecordsAPermanentFailure(t *testing.T) {
	tn := newTenant()
	defer tn.srv.Close()
	unlabelled := subToken("neura")
	unlabelled.Labels = nil
	r, c, _ := newRelay(t, storedAlert("Firing", nil), subscription("neura", tn.srv.URL, framev1beta1.AlertFilter{}), unlabelled)

	reconcileAlert(t, r)

	if len(tn.received) != 0 {
		t.Fatalf("tenant received %v, want nothing", tn.received)
	}
	want := "token Secret neura-token is not labelled " + AlertTokenLabel + "=true"
	d := delivery(getAlert(t, c), "neura")
	if d == nil || !d.PermanentFailure || d.LastError != want {
		t.Fatalf("delivery: %+v, want PermanentFailure with lastError %q", d, want)
	}
}

func TestRelayRequeuesAResolvedAlertAtItsPurgeTime(t *testing.T) {
	end := metav1.NewTime(time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC))
	fa := storedAlert("Resolved", &end)
	fa.Status.Deliveries = []framev1beta1.AlertDelivery{{Subscription: "neura", DeliveredState: "Resolved"}}
	r, _, _ := newRelay(t, fa, subscription("neura", "http://unused.invalid", framev1beta1.AlertFilter{}), subToken("neura"))
	res := reconcileAlert(t, r)
	want := end.Add(14 * 24 * time.Hour).Sub(time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	if res.RequeueAfter != want {
		t.Fatalf("requeue %v, want %v", res.RequeueAfter, want)
	}
}
