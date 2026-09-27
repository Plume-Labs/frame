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

// Reader turns dashboard reads into FrameStorage status, cached.
type Reader struct {
	// Load returns the current account (re-read each interval, so a
	// rotated Secret takes effect), or ErrNotConfigured.
	Load     func(ctx context.Context) (Config, error)
	Interval time.Duration
	Now      func() time.Time
	// newClient is New, replaceable in tests.
	newClient func(Config) (*Client, error)

	mu     sync.Mutex
	last   *framev1beta1.CephPlacement
	lastAt time.Time
	client *Client
	cfgKey string
}

// Placement returns the current placement status, reading the dashboard at
// most once per Interval. It never returns nil and never an error: a
// failure is a State, and a failed read keeps the last good lists.
func (r *Reader) Placement(ctx context.Context) *framev1beta1.CephPlacement {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if r.last != nil && now.Sub(r.lastAt) < r.interval() {
		return r.last.DeepCopy()
	}
	r.lastAt = now
	r.last = r.read(ctx, now)
	return r.last.DeepCopy()
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

func (r *Reader) failed(msg string) *framev1beta1.CephPlacement {
	out := &framev1beta1.CephPlacement{State: "Error", Message: clip(msg, 256)}
	if r.last != nil {
		out.CollectedAt = r.last.CollectedAt
		out.Pools = r.last.Pools
		out.OSDs = r.last.OSDs
	}
	return out
}

func (r *Reader) read(ctx context.Context, now time.Time) *framev1beta1.CephPlacement {
	cfg, err := r.Load(ctx)
	if errors.Is(err, ErrNotConfigured) {
		return &framev1beta1.CephPlacement{State: "NotConfigured", Message: "no dashboard account Secret"}
	}
	if err != nil {
		return r.failed(err.Error())
	}
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%x", cfg.BaseURL, cfg.Username, cfg.Password, cfg.ServerName, cfg.CAPEM)
	if r.client == nil || key != r.cfgKey {
		mk := r.newClient
		if mk == nil {
			mk = New
		}
		c, err := mk(cfg)
		if err != nil {
			r.client = nil
			return r.failed(err.Error())
		}
		r.client, r.cfgKey = c, key
	}
	snap, err := r.client.Read(ctx)
	if err != nil {
		return r.failed(err.Error())
	}
	at := metav1.NewTime(now)
	return toStatus(snap, &at)
}

// CRD bounds (api/frame/v1beta1/framestorage_types.go).
const (
	maxPools = 64
	maxOSDs  = 256
)

func toStatus(s *Snapshot, at *metav1.Time) *framev1beta1.CephPlacement {
	out := &framev1beta1.CephPlacement{State: "Collected", CollectedAt: at}
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
	return out
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
