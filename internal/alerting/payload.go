// Package alerting receives Alertmanager webhooks into FrameAlert objects and
// relays them to FrameAlertSubscriptions. See
// docs/superpowers/specs/2026-09-15-alert-relay-design.md.
package alerting

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxBodyBytes        = 1 << 20
	MaxAlertsPerRequest = 100

	maxMapEntries = 64
	maxKeyBytes   = 256
	maxValueBytes = 4096
)

var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

// Payload is Alertmanager's webhook body, version 4. Frame both reads it and
// writes it (one alert per request) to tenants.
type Payload struct {
	Version  string  `json:"version"`
	Status   string  `json:"status,omitempty"`
	Receiver string  `json:"receiver,omitempty"`
	Alerts   []Alert `json:"alerts"`
}

type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

func Validate(p Payload) error {
	if p.Version != "4" {
		return fmt.Errorf("unsupported payload version %q", p.Version)
	}
	if len(p.Alerts) == 0 {
		return errors.New("no alerts")
	}
	if len(p.Alerts) > MaxAlertsPerRequest {
		return fmt.Errorf("%d alerts, at most %d", len(p.Alerts), MaxAlertsPerRequest)
	}
	for i, a := range p.Alerts {
		if !fingerprintPattern.MatchString(a.Fingerprint) {
			return fmt.Errorf("alert %d: fingerprint %q", i, a.Fingerprint)
		}
		if a.Status != "firing" && a.Status != "resolved" {
			return fmt.Errorf("alert %d: status %q", i, a.Status)
		}
	}
	return nil
}

func AlertObjectName(fingerprint string) string { return "fa-" + fingerprint }

// BoundMap keeps the 64 smallest keys (sorted, so two replicas bound the
// same alert identically) and truncates keys and values on rune boundaries.
// If two distinct keys truncate to the same value, the first in sorted order wins.
func BoundMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > maxMapEntries {
		keys = keys[:maxMapEntries]
	}
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		truncatedKey := truncate(k, maxKeyBytes)
		// Only insert if this truncated key is not already present.
		if _, exists := out[truncatedKey]; !exists {
			out[truncatedKey] = truncate(m[k], maxValueBytes)
		}
	}
	return out
}

func truncate(s string, max int) string {
	// Sanitize invalid UTF-8 first.
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= max {
		return s
	}
	s = s[:max]
	// Back off to rune boundary if needed.
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
