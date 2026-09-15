package alerting

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

const lastReceivedRefresh = 15 * time.Minute

// Receiver is the Alertmanager webhook endpoint. It only records: the relay
// to tenants happens in RelayReconciler, never inside this request, so a
// tenant outage cannot fail Alertmanager's delivery.
type Receiver struct {
	Client      client.Client
	TokenReader client.Reader // mgr.GetAPIReader(): no cluster-wide Secret cache
	Namespace   string
	TokenSecret string
	Addr        string
	Now         func() time.Time
	Log         logr.Logger
}

// NeedLeaderElection is false: Alertmanager reaches whichever replica the
// Service picks, and writes are idempotent by object name.
func (r *Receiver) NeedLeaderElection() bool { return false }

func (r *Receiver) Start(ctx context.Context) error {
	srv := &http.Server{Addr: r.Addr, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-errc:
		return err
	}
}

func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	code := r.serve(req)
	receiverRequests.WithLabelValues(strconv.Itoa(code)).Inc()
	w.WriteHeader(code)
}

func (r *Receiver) serve(req *http.Request) int {
	if req.URL.Path != "/alertmanager" || req.Method != http.MethodPost {
		return http.StatusNotFound
	}
	if code := r.authorize(req); code != 0 {
		return code
	}
	// Size (413) is checked before format (400): io.ReadAll drains the
	// MaxBytesReader fully, so an oversized body always trips the byte
	// limit first, regardless of whether its content happens to be valid
	// JSON. A json.Decoder.Decode over the same reader would instead fail
	// on the first malformed byte before the limit is reached, answering
	// 400 for a body that is both oversized and non-JSON.
	body, err := io.ReadAll(http.MaxBytesReader(nil, req.Body, MaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return http.StatusRequestEntityTooLarge
		}
		return http.StatusBadRequest
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return http.StatusBadRequest
	}
	if err := Validate(p); err != nil {
		return http.StatusBadRequest
	}
	for _, a := range p.Alerts {
		if err := r.record(req.Context(), a); err != nil {
			r.Log.Error(err, "recording alert", "fingerprint", a.Fingerprint)
			return http.StatusServiceUnavailable
		}
	}
	return http.StatusOK
}

// authorize returns 0 when the bearer token matches, otherwise the HTTP
// status to answer with. A missing/wrong/unconfigured token (including the
// Secret genuinely not existing) is 401. Any other failure to read the
// Secret — an apiserver/kine hiccup — is 503, so Alertmanager retries
// instead of treating a transient error as "unconfigured" and dropping the
// alert for good within its flush window.
func (r *Receiver) authorize(req *http.Request) int {
	got, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok || got == "" {
		return http.StatusUnauthorized
	}
	var s corev1.Secret
	key := types.NamespacedName{Namespace: r.Namespace, Name: r.TokenSecret}
	if err := r.TokenReader.Get(req.Context(), key, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return http.StatusUnauthorized
		}
		return http.StatusServiceUnavailable
	}
	want := s.Data["token"]
	if len(want) == 0 || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
		return http.StatusUnauthorized
	}
	return 0
}

func (r *Receiver) record(ctx context.Context, a Alert) error {
	now := metav1.NewTime(r.Now())
	state := framev1beta1.AlertStateFiring
	var endsAt *metav1.Time
	if a.Status == "resolved" {
		state = framev1beta1.AlertStateResolved
		t := metav1.NewTime(a.EndsAt.Truncate(time.Second))
		endsAt = &t
	}
	alertsReceived.WithLabelValues(state).Inc()

	// StartsAt/EndsAt are truncated to the second: Alertmanager sends
	// sub-second precision, but metav1.Time only round-trips whole seconds
	// through JSON, so the object read back from a real apiserver never
	// carries the original nanoseconds. Without truncating here first, the
	// DeepEqual comparison below never matches on a resend and every single
	// notification would issue an empty PATCH.
	spec := framev1beta1.FrameAlertSpec{
		Fingerprint:  a.Fingerprint,
		AlertName:    truncate(a.Labels["alertname"], 256),
		Severity:     truncate(a.Labels["severity"], 64),
		Namespace:    truncate(a.Labels["namespace"], 63),
		Labels:       BoundMap(a.Labels),
		Annotations:  BoundMap(a.Annotations),
		StartsAt:     metav1.NewTime(a.StartsAt.Truncate(time.Second)),
		EndsAt:       endsAt,
		GeneratorURL: truncate(a.GeneratorURL, 2048),
	}

	var fa framev1beta1.FrameAlert
	key := types.NamespacedName{Namespace: r.Namespace, Name: AlertObjectName(a.Fingerprint)}
	err := r.Client.Get(ctx, key, &fa)
	if apierrors.IsNotFound(err) {
		fa = framev1beta1.FrameAlert{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}, Spec: spec}
		if err := r.Client.Create(ctx, &fa); err != nil {
			return err
		}
		return r.patchStatus(ctx, &fa, state, now)
	}
	if err != nil {
		return err
	}

	if !equality.Semantic.DeepEqual(fa.Spec, spec) {
		orig := fa.DeepCopy()
		fa.Spec = spec
		if err := r.Client.Patch(ctx, &fa, client.MergeFrom(orig)); err != nil {
			return err
		}
	}
	stale := fa.Status.LastReceivedAt == nil || now.Sub(fa.Status.LastReceivedAt.Time) >= lastReceivedRefresh
	if fa.Status.State != state || stale {
		return r.patchStatus(ctx, &fa, state, now)
	}
	return nil
}

// patchStatus touches only state and lastReceivedAt; deliveries belong to
// the relay and must not be sent back in this patch.
func (r *Receiver) patchStatus(ctx context.Context, fa *framev1beta1.FrameAlert, state string, now metav1.Time) error {
	orig := fa.DeepCopy()
	fa.Status.State = state
	fa.Status.LastReceivedAt = &now
	return r.Client.Status().Patch(ctx, fa, client.MergeFrom(orig))
}
