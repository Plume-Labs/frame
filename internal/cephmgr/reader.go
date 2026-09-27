/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cephmgr

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/gothshoot/frame/api/frame/v1beta1"
)

// ErrNotConfigured means no dashboard account has been provided (no
// Secret): the normal state until an operator creates one.
var ErrNotConfigured = errors.New("no ceph-mgr dashboard account configured")

// DefaultInterval is how often the dashboard is actually read. Every
// FrameStorage reconcile (every 2 minutes, per entry) asks; they share one
// read per interval.
const DefaultInterval = 5 * time.Minute

// Authentication backoff: after a refused login, the next attempt with the
// same Secret waits authBackoffStart, doubling up to authBackoffMax. The
// dashboard disables an account after repeated failed logins (10 by
// default); retried every 5 minutes, a wrong password would lock the
// account within the hour. A changed Secret is tried at once.
const (
	authBackoffStart = 5 * time.Minute
	authBackoffMax   = 6 * time.Hour
)

// readDeadline bounds one background read (login and three GETs).
const readDeadline = 60 * time.Second

// Reader turns dashboard reads into FrameStorage status, cached, and never
// makes its caller wait on the network: Placement returns the cached result
// at once and, when it is due, starts one refresh in the background (a
// second caller during that refresh does not start another).
type Reader struct {
	// Load returns the current account (re-read each interval, so a
	// rotated Secret takes effect), or ErrNotConfigured.
	Load     func(ctx context.Context) (Config, error)
	Interval time.Duration
	Now      func() time.Time
	// newClient is New, replaceable in tests.
	newClient func(Config) (*Client, error)

	mu       sync.Mutex
	last     *framev1beta1.CephPlacement
	lastAt   time.Time
	inflight bool
	done     chan struct{} // closed when the in-flight refresh ends (tests)

	// Only touched by the refresh goroutine (one at a time).
	client      *Client
	cfgKey      string
	authKey     string // cfgKey of the account last refused
	authRetryAt time.Time
	authBackoff time.Duration
}

// Placement returns the current placement status without blocking: the
// cached result (nil before the first read has completed), a refresh being
// started in the background when the cache is older than Interval. A
// failure is a State, never an error, and a failed list keeps its last
// good value.
func (r *Reader) Placement(_ context.Context) *framev1beta1.CephPlacement {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if !r.inflight && (r.last == nil || now.Sub(r.lastAt) >= r.interval()) {
		r.inflight = true
		r.done = make(chan struct{})
		prev := r.last.DeepCopy()
		done := r.done
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), readDeadline)
			defer cancel()
			res := r.refresh(ctx, now, prev)
			r.mu.Lock()
			r.last, r.lastAt, r.inflight = res, now, false
			r.mu.Unlock()
			close(done)
		}()
	}
	if r.last == nil {
		return nil
	}
	return r.last.DeepCopy()
}

// wait blocks until the in-flight refresh, if any, has ended (tests).
func (r *Reader) wait() {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (r *Reader) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reader) interval() time.Duration {
	if r.Interval > 0 {
		return r.Interval
	}
	return DefaultInterval
}

// withLast fills out's missing lists from prev, the last result.
func withLast(out *framev1beta1.CephPlacement, prev *framev1beta1.CephPlacement) *framev1beta1.CephPlacement {
	if prev != nil {
		if out.Pools == nil {
			out.Pools = prev.Pools
		}
		if out.OSDs == nil {
			out.OSDs = prev.OSDs
		}
		if out.CollectedAt == nil {
			out.CollectedAt = prev.CollectedAt
		}
	}
	return out
}

func (r *Reader) refresh(ctx context.Context, now time.Time, prev *framev1beta1.CephPlacement) *framev1beta1.CephPlacement {
	cfg, err := r.Load(ctx)
	if errors.Is(err, ErrNotConfigured) {
		return &framev1beta1.CephPlacement{State: "NotConfigured", Message: "no dashboard account Secret"}
	}
	if err != nil {
		return withLast(&framev1beta1.CephPlacement{State: "Error", Message: clip(err.Error(), 256)}, prev)
	}
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%x\x00%s", cfg.BaseURL, cfg.Username, cfg.Password, cfg.ServerName, cfg.CAPEM, cfg.Version)
	if key == r.authKey && now.Before(r.authRetryAt) {
		return withLast(&framev1beta1.CephPlacement{
			State:   "AuthRefused",
			Message: clip(fmt.Sprintf("the dashboard refused the account; next attempt %s, or as soon as the Secret changes", r.authRetryAt.UTC().Format(time.RFC3339)), 256),
		}, prev)
	}
	if r.client == nil || key != r.cfgKey {
		mk := r.newClient
		if mk == nil {
			mk = New
		}
		c, err := mk(cfg)
		if err != nil {
			r.client = nil
			return withLast(&framev1beta1.CephPlacement{State: "Error", Message: clip(err.Error(), 256)}, prev)
		}
		r.client, r.cfgKey = c, key
	}
	snap, err := r.client.Read(ctx)
	if errors.Is(err, ErrAuthRefused) {
		if key == r.authKey {
			r.authBackoff = min(r.authBackoff*2, authBackoffMax)
		} else {
			r.authKey, r.authBackoff = key, authBackoffStart
		}
		r.authRetryAt = now.Add(r.authBackoff)
		return withLast(&framev1beta1.CephPlacement{
			State:   "AuthRefused",
			Message: clip(fmt.Sprintf("the dashboard refused the account; next attempt %s, or as soon as the Secret changes", r.authRetryAt.UTC().Format(time.RFC3339)), 256),
		}, prev)
	}
	r.authKey, r.authBackoff = "", 0
	if err != nil {
		return withLast(&framev1beta1.CephPlacement{State: "Error", Message: clip(err.Error(), 256)}, prev)
	}
	at := metav1.NewTime(now)
	out := toStatus(snap, &at)
	if len(snap.Errors) > 0 {
		var parts []string
		for _, p := range []string{PathPools, PathOSDs, PathCrushRules} {
			if e, ok := snap.Errors[p]; ok {
				parts = append(parts, e.Error())
			}
		}
		out.State = "Error"
		out.Message = clip("partial read: "+strings.Join(parts, "; "), 256)
		if out.Pools == nil && out.OSDs == nil {
			out.CollectedAt = nil // nothing fresh: keep the last read's date
		}
		withLast(out, prev)
	}
	return out
}

// CRD bounds (api/frame/v1beta1/framestorage_types.go).
const (
	maxPools = 64
	maxOSDs  = 256
)

// toStatus maps a snapshot; a list the snapshot lacks (its GET failed)
// stays nil, for withLast to fill.
func toStatus(s *Snapshot, at *metav1.Time) *framev1beta1.CephPlacement {
	out := &framev1beta1.CephPlacement{State: "Collected", CollectedAt: at}
	if s.Pools != nil {
		out.Pools = []framev1beta1.CephPoolPlacement{}
		pools := append([]Pool(nil), s.Pools...)
		sort.Slice(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
		for _, p := range pools {
			if len(out.Pools) == maxPools {
				out.Message = fmt.Sprintf("%d pool(s) not listed (status size limit)", len(pools)-maxPools)
				break
			}
			out.Pools = append(out.Pools, framev1beta1.CephPoolPlacement{
				Name: clip(p.Name, 128), Size: p.Size, MinSize: p.MinSize, CrushRule: clip(p.CrushRule, 160),
			})
		}
	}
	if s.OSDs != nil {
		out.OSDs = []framev1beta1.CephOSDWeight{}
		osds := append([]OSD(nil), s.OSDs...)
		sort.Slice(osds, func(i, j int) bool { return osds[i].ID < osds[j].ID })
		for _, o := range osds {
			if len(out.OSDs) == maxOSDs {
				break
			}
			out.OSDs = append(out.OSDs, framev1beta1.CephOSDWeight{
				ID: o.ID, CrushWeight: fmt.Sprintf("%.4f", o.CrushWeight), DeviceClass: clip(o.DeviceClass, 32),
			})
		}
	}
	return out
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
