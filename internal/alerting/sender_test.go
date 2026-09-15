package alerting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func sampleAlert() *framev1beta1.FrameAlert {
	end := metav1.NewTime(time.Date(2026, 9, 15, 12, 5, 0, 0, time.UTC))
	return &framev1beta1.FrameAlert{Spec: framev1beta1.FrameAlertSpec{
		Fingerprint: "ab12", AlertName: "X",
		Labels:   map[string]string{"alertname": "X"},
		StartsAt: metav1.NewTime(time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC)),
		EndsAt:   &end,
	}}
}

func TestSendDeliversAnAlertmanagerV4BodyWithOneAlert(t *testing.T) {
	var got Payload
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()

	out, err := NewSender().Send(context.Background(), srv.URL, "tok", "neura", sampleAlert(), framev1beta1.AlertStateResolved)
	if out != Delivered || err != nil {
		t.Fatalf("got %v %v", out, err)
	}
	if auth != "Bearer tok" {
		t.Errorf("Authorization = %q", auth)
	}
	if got.Version != "4" || got.Receiver != "neura" || got.Status != "resolved" || len(got.Alerts) != 1 {
		t.Fatalf("payload: %+v", got)
	}
	if got.Alerts[0].Fingerprint != "ab12" || got.Alerts[0].Status != "resolved" || got.Alerts[0].EndsAt.IsZero() {
		t.Fatalf("alert: %+v", got.Alerts[0])
	}
}

func TestSendFiringOmitsEndsAtEvenIfTheObjectHasOne(t *testing.T) {
	var got Payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	_, _ = NewSender().Send(context.Background(), srv.URL, "tok", "neura", sampleAlert(), framev1beta1.AlertStateFiring)
	if got.Alerts[0].Status != "firing" || !got.Alerts[0].EndsAt.IsZero() {
		t.Fatalf("firing replay of a resolved alert: %+v", got.Alerts[0])
	}
}

func TestSendClassifiesAnswers(t *testing.T) {
	for _, tc := range []struct {
		code int
		want Outcome
	}{
		{200, Delivered}, {204, Delivered},
		{500, Retry}, {503, Retry}, {408, Retry}, {429, Retry},
		{400, Permanent}, {401, Permanent}, {404, Permanent},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code) }))
		out, _ := NewSender().Send(context.Background(), srv.URL, "tok", "neura", sampleAlert(), framev1beta1.AlertStateFiring)
		srv.Close()
		if out != tc.want {
			t.Errorf("HTTP %d: got %v, want %v", tc.code, out, tc.want)
		}
	}
}

func TestSendRefusesRedirects(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked = true
		}
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer redirector.Close()

	out, _ := NewSender().Send(context.Background(), redirector.URL, "tok", "neura", sampleAlert(), framev1beta1.AlertStateFiring)
	if out != Permanent || leaked {
		t.Fatalf("outcome %v, token leaked to redirect target: %v", out, leaked)
	}
}

func TestSendUnreachableIsRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if out, err := NewSender().Send(context.Background(), url, "tok", "neura", sampleAlert(), framev1beta1.AlertStateFiring); out != Retry || err == nil {
		t.Fatalf("got %v %v", out, err)
	}
}

func TestBackoff(t *testing.T) {
	for attempts, want := range map[int32]time.Duration{1: 5 * time.Second, 2: 10 * time.Second, 4: 40 * time.Second, 8: 10 * time.Minute, 60: 10 * time.Minute} {
		if got := Backoff(attempts); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", attempts, got, want)
		}
	}
}
