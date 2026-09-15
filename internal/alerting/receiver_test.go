package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

const ns = "frame-system"

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := framev1beta1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func tokenSecret(value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "frame-alert-receiver-token", Namespace: ns},
		Data:       map[string][]byte{"token": []byte(value)},
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newReceiver(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) (*Receiver, client.Client, *clock) {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameAlert{}).WithObjects(objs...)
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	c := b.Build()
	ck := &clock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	return &Receiver{Client: c, TokenReader: c, Namespace: ns, TokenSecret: "frame-alert-receiver-token",
		Now: ck.now, Log: logr.Discard()}, c, ck
}

func post(r http.Handler, token string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	switch b := body.(type) {
	case []byte:
		buf.Write(b)
	default:
		_ = json.NewEncoder(&buf).Encode(b)
	}
	req := httptest.NewRequest(http.MethodPost, "/alertmanager", &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func firing(fp string) Payload {
	return Payload{Version: "4", Alerts: []Alert{{
		Status: "firing", Fingerprint: fp,
		Labels:   map[string]string{"alertname": "KubeCPUOvercommit", "severity": "warning", "namespace": "kube-system"},
		StartsAt: time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC),
	}}}
}

func TestReceiverRejectsMissingWrongOrUnconfiguredToken(t *testing.T) {
	r, _, _ := newReceiver(t, nil, tokenSecret("s3cret"))
	if code := post(r, "", firing("ab")).Code; code != http.StatusUnauthorized {
		t.Errorf("no token: got %d", code)
	}
	if code := post(r, "wrong", firing("ab")).Code; code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d", code)
	}
	unconfigured, _, _ := newReceiver(t, nil)
	if code := post(unconfigured, "anything", firing("ab")).Code; code != http.StatusUnauthorized {
		t.Errorf("secret absent: got %d", code)
	}
	empty, _, _ := newReceiver(t, nil, tokenSecret(""))
	if code := post(empty, "", firing("ab")).Code; code != http.StatusUnauthorized {
		t.Errorf("empty secret must not match an empty bearer: got %d", code)
	}
}

func TestReceiverRejectsOtherPathsAndMethods(t *testing.T) {
	r, _, _ := newReceiver(t, nil, tokenSecret("s3cret"))
	for _, tc := range []struct{ method, path string }{{http.MethodGet, "/alertmanager"}, {http.MethodPost, "/"}} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer s3cret")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: got %d", tc.method, tc.path, rec.Code)
		}
	}
}

func TestReceiverRejectsOversizedAndInvalidBodies(t *testing.T) {
	r, _, _ := newReceiver(t, nil, tokenSecret("s3cret"))
	if code := post(r, "s3cret", bytes.Repeat([]byte("a"), MaxBodyBytes+1)).Code; code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized: got %d", code)
	}
	if code := post(r, "s3cret", []byte("{not json")).Code; code != http.StatusBadRequest {
		t.Errorf("bad json: got %d", code)
	}
	bad := firing("AB")
	if code := post(r, "s3cret", bad).Code; code != http.StatusBadRequest {
		t.Errorf("bad fingerprint: got %d", code)
	}
}

func TestReceiverCreatesAFiringAlert(t *testing.T) {
	r, c, ck := newReceiver(t, nil, tokenSecret("s3cret"))
	if code := post(r, "s3cret", firing("ab12")).Code; code != http.StatusOK {
		t.Fatalf("got %d", code)
	}
	var fa framev1beta1.FrameAlert
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "fa-ab12"}, &fa); err != nil {
		t.Fatal(err)
	}
	if fa.Spec.AlertName != "KubeCPUOvercommit" || fa.Spec.Severity != "warning" || fa.Spec.Namespace != "kube-system" {
		t.Errorf("spec not projected from labels: %+v", fa.Spec)
	}
	if fa.Status.State != framev1beta1.AlertStateFiring || !fa.Status.LastReceivedAt.Time.Equal(ck.t) {
		t.Errorf("status: %+v", fa.Status)
	}
	if fa.Spec.EndsAt != nil {
		t.Error("a firing alert carries endsAt")
	}
}

func TestReceiverResolvesThenReopens(t *testing.T) {
	r, c, _ := newReceiver(t, nil, tokenSecret("s3cret"))
	post(r, "s3cret", firing("ab12"))
	res := firing("ab12")
	res.Alerts[0].Status = "resolved"
	res.Alerts[0].EndsAt = time.Date(2026, 9, 15, 12, 5, 0, 0, time.UTC)
	post(r, "s3cret", res)

	var fa framev1beta1.FrameAlert
	key := types.NamespacedName{Namespace: ns, Name: "fa-ab12"}
	_ = c.Get(context.Background(), key, &fa)
	if fa.Status.State != framev1beta1.AlertStateResolved || fa.Spec.EndsAt == nil {
		t.Fatalf("not resolved: state=%s endsAt=%v", fa.Status.State, fa.Spec.EndsAt)
	}

	post(r, "s3cret", firing("ab12"))
	_ = c.Get(context.Background(), key, &fa)
	if fa.Status.State != framev1beta1.AlertStateFiring || fa.Spec.EndsAt != nil {
		t.Fatalf("not reopened: state=%s endsAt=%v", fa.Status.State, fa.Spec.EndsAt)
	}
}

func TestReceiverDoesNotRewriteAnIdenticalResendWithin15Minutes(t *testing.T) {
	writes := 0
	funcs := &interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, o ...client.PatchOption) error {
			writes++
			return c.Patch(ctx, obj, p, o...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, o ...client.SubResourcePatchOption) error {
			writes++
			return c.SubResource(sub).Patch(ctx, obj, p, o...)
		},
	}
	r, _, ck := newReceiver(t, funcs, tokenSecret("s3cret"))
	post(r, "s3cret", firing("ab12"))
	before := writes

	ck.t = ck.t.Add(5 * time.Minute)
	post(r, "s3cret", firing("ab12"))
	if writes != before {
		t.Fatalf("identical resend after 5 min wrote %d times", writes-before)
	}

	ck.t = ck.t.Add(11 * time.Minute)
	post(r, "s3cret", firing("ab12"))
	if writes == before {
		t.Fatal("resend after 16 min did not refresh lastReceivedAt")
	}
}

func TestReceiverBoundsLabelsBeforeWriting(t *testing.T) {
	r, c, _ := newReceiver(t, nil, tokenSecret("s3cret"))
	p := firing("ab12")
	p.Alerts[0].Annotations = map[string]string{"description": strings.Repeat("x", 10000)}
	post(r, "s3cret", p)
	var fa framev1beta1.FrameAlert
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "fa-ab12"}, &fa)
	if len(fa.Spec.Annotations["description"]) != 4096 {
		t.Fatalf("annotation not truncated: %d bytes", len(fa.Spec.Annotations["description"]))
	}
}

func TestReceiverAnswers503WhenAWriteFails(t *testing.T) {
	funcs := &interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, o ...client.CreateOption) error {
			return errors.New("etcdserver: request timed out")
		},
	}
	r, _, _ := newReceiver(t, funcs, tokenSecret("s3cret"))
	if code := post(r, "s3cret", firing("ab12")).Code; code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 so Alertmanager retries", code)
	}
}

func TestReceiverDoesNotNeedLeaderElection(t *testing.T) {
	if (&Receiver{}).NeedLeaderElection() {
		t.Fatal("receiver would only run on the leader; Alertmanager hits every replica")
	}
}
