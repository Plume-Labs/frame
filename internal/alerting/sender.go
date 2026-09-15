package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

type Outcome int

const (
	Delivered Outcome = iota
	Retry
	Permanent
)

func (o Outcome) String() string { return [...]string{"delivered", "retry", "permanent"}[o] }

type Sender struct{ HTTP *http.Client }

// NewSender refuses redirects: following one would send the tenant's token
// to wherever the redirect points.
func NewSender() *Sender {
	return &Sender{HTTP: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *Sender) Send(ctx context.Context, url, token, receiver string, a *framev1beta1.FrameAlert, state string) (Outcome, error) {
	body, err := json.Marshal(buildPayload(receiver, a, state))
	if err != nil {
		return Permanent, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Permanent, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return Retry, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	switch c := resp.StatusCode; {
	case c >= 200 && c < 300:
		return Delivered, nil
	case c >= 500, c == http.StatusRequestTimeout, c == http.StatusTooManyRequests:
		return Retry, fmt.Errorf("HTTP %d", c)
	default:
		return Permanent, fmt.Errorf("HTTP %d", c)
	}
}

func buildPayload(receiver string, a *framev1beta1.FrameAlert, state string) Payload {
	status := "firing"
	var endsAt time.Time
	if state == framev1beta1.AlertStateResolved {
		status = "resolved"
		if a.Spec.EndsAt != nil {
			endsAt = a.Spec.EndsAt.Time
		}
	}
	labels := a.Spec.Labels
	if labels == nil {
		labels = make(map[string]string)
	}
	annotations := a.Spec.Annotations
	if annotations == nil {
		annotations = make(map[string]string)
	}
	return Payload{Version: "4", Status: status, Receiver: receiver, Alerts: []Alert{{
		Status: status, Fingerprint: a.Spec.Fingerprint,
		Labels: labels, Annotations: annotations,
		StartsAt: a.Spec.StartsAt.Time, EndsAt: endsAt, GeneratorURL: a.Spec.GeneratorURL,
	}}}
}

func Backoff(attempts int32) time.Duration {
	if attempts < 1 {
		return 0
	}
	d := 5 * time.Second
	for i := int32(1); i < attempts && d < 10*time.Minute; i++ {
		d *= 2
	}
	return min(d, 10*time.Minute)
}
