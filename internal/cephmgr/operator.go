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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// The operator account: the second dashboard account, held by the manager
// only, that the OSD replacement (FrameOSDReplacement) uses to change Ceph.
// The console's own account (SecretName) stays read-only and is never used
// here, and this one is never used for the Storage screen.
//
// Everything it may send is one of the methods below, each a constant path
// with at most one OSD id rendered from an integer, a constant API version,
// and a body built from constants. There is no generic call: an endpoint
// this file does not name cannot be reached through the operator account,
// whatever its role would allow.
//
// The routes and their versions are those of Ceph v20.2.2
// (src/pybind/mgr/dashboard/controllers/osd.py and health.py):
//
//	GET  /api/health/snapshot          1.0  `ceph status`, reduced
//	GET  /api/osd?limit=-1             1.1  every OSD: up, in, host
//	GET  /api/osd/<id>                 1.0  one OSD's map entry and metadata
//	GET  /api/osd/flags                1.0  cluster-wide OSD flags
//	GET  /api/osd/safe_to_destroy?ids  1.0  `ceph osd safe-to-destroy`
//	GET  /api/pool                     1.0  size and min_size of every pool
//	PUT  /api/osd/<id>/mark {out|in}   1.0  `ceph osd out|in` (osd: update)
//	POST /api/osd/<id>/purge           1.0  `ceph osd purge-actual` (osd: create)
//
// There is no `ok-to-stop` route in the dashboard; see OKToStop.

// OperatorSecretName is the Secret holding the operator account, in the
// manager's namespace, with the same keys as SecretName. No console tier
// may read it (test/manifests pins this).
const OperatorSecretName = "frame-ceph-operator"

// MaxOSDID bounds an OSD id the operator client will put in a path.
const MaxOSDID = 65535

// ErrOSDNotFound: the dashboard does not know this OSD (it was purged, or
// never existed).
var ErrOSDNotFound = errors.New("osd not found")

// Operator is the manager's handle on the operator account.
type Operator struct {
	c *Client
}

// NewOperator builds the operator client on cfg; gate is the same
// write-ahead login gate as the reader's (logins of both accounts are
// recorded in one state Secret, keyed per account).
func NewOperator(cfg Config, gate LoginGate) (*Operator, error) {
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}
	c.gate = gate
	return &Operator{c: c}, nil
}

// Reset starts a new pass: a login refused in an earlier pass is tried
// again only through the gate, which refuses it while it is recorded.
func (o *Operator) Reset() {
	o.c.mu.Lock()
	o.c.loginFailed = nil
	o.c.mu.Unlock()
}

func osdPath(id int32, suffix string) (string, error) {
	if id < 0 || id > MaxOSDID {
		return "", fmt.Errorf("refusing OSD id %d", id)
	}
	p := "/api/osd/" + strconv.FormatInt(int64(id), 10)
	if suffix != "" {
		p += "/" + suffix
	}
	return p, nil
}

// HealthCheck is one active health check.
type HealthCheck struct {
	Name     string
	Severity string // HEALTH_WARN, HEALTH_ERR
	Message  string
	Muted    bool
}

// ClusterStatus is the part of `ceph status` the replacement reads.
type ClusterStatus struct {
	Health     string // HEALTH_OK, HEALTH_WARN, HEALTH_ERR
	Checks     []HealthCheck
	NumPGs     int64
	PGsByState map[string]int64
}

// ActiveClean is the number of PGs whose state is exactly active+clean.
func (s *ClusterStatus) ActiveClean() int64 { return s.PGsByState["active+clean"] }

// AllActiveClean: every PG is active+clean, and there is at least one.
func (s *ClusterStatus) AllActiveClean() bool {
	return s.NumPGs > 0 && s.ActiveClean() == s.NumPGs
}

// Status reads GET /api/health/snapshot.
func (o *Operator) Status(ctx context.Context) (*ClusterStatus, error) {
	var raw struct {
		Health struct {
			Status string `json:"status"`
			Checks map[string]struct {
				Severity string `json:"severity"`
				Summary  struct {
					Message string `json:"message"`
				} `json:"summary"`
				Muted bool `json:"muted"`
			} `json:"checks"`
		} `json:"health"`
		PGMap *struct {
			NumPGs     *int64 `json:"num_pgs"`
			PGsByState []struct {
				StateName string `json:"state_name"`
				Count     int64  `json:"count"`
			} `json:"pgs_by_state"`
		} `json:"pgmap"`
	}
	if err := o.c.send(ctx, http.MethodGet, "/api/health/snapshot", "", "1.0", nil, &raw); err != nil {
		return nil, err
	}
	if raw.Health.Status == "" {
		return nil, errors.New("GET /api/health/snapshot: no health status in the answer")
	}
	// Without the osd read scope the dashboard leaves pgmap out: that is
	// "could not look", never "no PG".
	if raw.PGMap == nil || raw.PGMap.NumPGs == nil {
		return nil, errors.New("GET /api/health/snapshot: no pgmap in the answer (does the account read the osd scope?)")
	}
	out := &ClusterStatus{Health: raw.Health.Status, NumPGs: *raw.PGMap.NumPGs, PGsByState: map[string]int64{}}
	for _, s := range raw.PGMap.PGsByState {
		out.PGsByState[s.StateName] += s.Count
	}
	for name, c := range raw.Health.Checks {
		out.Checks = append(out.Checks, HealthCheck{Name: name, Severity: c.Severity, Message: c.Summary.Message, Muted: c.Muted})
	}
	slices.SortFunc(out.Checks, func(a, b HealthCheck) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// OSDState is one OSD as GET /api/osd lists it.
type OSDState struct {
	ID          int32
	Up          bool
	In          bool
	Host        string // CRUSH host; "" when the OSD is under no host
	DeviceClass string
}

// OSDs reads GET /api/osd?limit=-1.
func (o *Operator) OSDs(ctx context.Context) ([]OSDState, error) {
	var raw []struct {
		OSD  *int32 `json:"osd"`
		Up   int    `json:"up"`
		In   int    `json:"in"`
		Host *struct {
			Name string `json:"name"`
		} `json:"host"`
		Tree struct {
			DeviceClass string `json:"device_class"`
		} `json:"tree"`
	}
	if err := o.c.send(ctx, http.MethodGet, PathOSDs, "limit=-1", "1.1", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]OSDState, 0, len(raw))
	for _, r := range raw {
		if r.OSD == nil {
			return nil, errors.New("GET /api/osd: an entry has no osd id")
		}
		s := OSDState{ID: *r.OSD, Up: r.Up == 1, In: r.In == 1, DeviceClass: r.Tree.DeviceClass}
		if r.Host != nil {
			s.Host = r.Host.Name
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b OSDState) int { return int(a.ID - b.ID) })
	return out, nil
}

// OSDMetadata is what the replacement needs of one OSD's metadata.
type OSDMetadata struct {
	ID       int32
	Hostname string
	// Serials are the serial numbers of the devices behind the OSD, from
	// device_ids ("sdc=ST500DM002-1BD142_Z2AW5NWA" → "Z2AW5NWA"), in order.
	Serials []string
	DevNode string // bluestore_bdev_dev_node, e.g. /dev/sdc (display only)
}

// Metadata reads GET /api/osd/<id>. An id the dashboard does not know is
// ErrOSDNotFound — but callers that must tell "purged" from "not found by
// mistake" read the list (OSDs) instead.
func (o *Operator) Metadata(ctx context.Context, id int32) (*OSDMetadata, error) {
	p, err := osdPath(id, "")
	if err != nil {
		return nil, err
	}
	var raw struct {
		Meta *struct {
			Hostname  string `json:"hostname"`
			DeviceIDs string `json:"device_ids"`
			DevNode   string `json:"bluestore_bdev_dev_node"`
		} `json:"osd_metadata"`
	}
	if err := o.c.send(ctx, http.MethodGet, p, "", "1.0", nil, &raw); err != nil {
		var he *HTTPError
		if errors.As(err, &he) && (he.Status == http.StatusNotFound || he.Status == http.StatusBadRequest) {
			return nil, fmt.Errorf("%w: %w", ErrOSDNotFound, err)
		}
		return nil, err
	}
	if raw.Meta == nil || raw.Meta.Hostname == "" {
		return nil, fmt.Errorf("GET %s: no metadata for osd.%d (is it running?)", p, id)
	}
	return &OSDMetadata{ID: id, Hostname: raw.Meta.Hostname, Serials: SerialsOf(raw.Meta.DeviceIDs), DevNode: raw.Meta.DevNode}, nil
}

// SerialsOf parses Ceph's device_ids metadata: comma-separated
// "<kernel name>=<vendor/model>_<serial>". The serial is what follows the
// last underscore; an entry without one is skipped.
func SerialsOf(deviceIDs string) []string {
	var out []string
	for part := range strings.SplitSeq(deviceIDs, ",") {
		part = strings.TrimSpace(part)
		if i := strings.IndexByte(part, '='); i >= 0 {
			part = part[i+1:]
		}
		i := strings.LastIndexByte(part, '_')
		if i < 0 || i == len(part)-1 {
			continue
		}
		out = append(out, part[i+1:])
	}
	return out
}

// Flags reads GET /api/osd/flags.
func (o *Operator) Flags(ctx context.Context) ([]string, error) {
	var out []string
	if err := o.c.send(ctx, http.MethodGet, "/api/osd/flags", "", "1.0", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PoolSizes reads size and min_size of every pool, GET /api/pool.
func (o *Operator) PoolSizes(ctx context.Context) ([]Pool, error) {
	var raw []rawPool
	if err := o.c.send(ctx, http.MethodGet, PathPools, "", "1.0", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Pool, 0, len(raw))
	for _, p := range raw {
		out = append(out, Pool{Name: p.PoolName, Size: p.Size, MinSize: p.MinSize})
	}
	return out, nil
}

// SafeToDestroy asks `ceph osd safe-to-destroy <id>` through the dashboard.
// The dashboard turns a command error into is_safe_to_destroy false with a
// message, which is returned as the reason.
func (o *Operator) SafeToDestroy(ctx context.Context, id int32) (bool, string, error) {
	if _, err := osdPath(id, ""); err != nil {
		return false, "", err
	}
	var raw struct {
		Safe    *bool   `json:"is_safe_to_destroy"`
		Message string  `json:"message"`
		Active  []int32 `json:"active"`
		Missing []int32 `json:"missing_stats"`
		Stored  []int32 `json:"stored_pgs"`
	}
	q := "ids=" + strconv.FormatInt(int64(id), 10)
	if err := o.c.send(ctx, http.MethodGet, "/api/osd/safe_to_destroy", q, "1.0", nil, &raw); err != nil {
		return false, "", err
	}
	if raw.Safe == nil {
		return false, "", errors.New("GET /api/osd/safe_to_destroy: no is_safe_to_destroy in the answer")
	}
	why := raw.Message
	if !*raw.Safe && why == "" {
		why = fmt.Sprintf("active %v, missing stats %v, stored PGs %v", raw.Active, raw.Missing, raw.Stored)
	}
	return *raw.Safe, clip(why, 200), nil
}

// MarkOut sends `ceph osd out <id>`.
func (o *Operator) MarkOut(ctx context.Context, id int32) error { return o.mark(ctx, id, "out") }

// MarkIn sends `ceph osd in <id>`.
func (o *Operator) MarkIn(ctx context.Context, id int32) error { return o.mark(ctx, id, "in") }

func (o *Operator) mark(ctx context.Context, id int32, action string) error {
	if action != "out" && action != "in" {
		return fmt.Errorf("refusing mark %q", action)
	}
	p, err := osdPath(id, "mark")
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"action": action})
	return o.c.send(ctx, http.MethodPut, p, "", "1.0", body, nil)
}

// Purge sends `ceph osd purge-actual <id> --yes-i-really-mean-it`. It does
// not check anything: the caller has asked safe-to-destroy, seen the OSD
// down, and recorded its intention before calling.
func (o *Operator) Purge(ctx context.Context, id int32) error {
	p, err := osdPath(id, "purge")
	if err != nil {
		return err
	}
	return o.c.send(ctx, http.MethodPost, p, "", "1.0", []byte("{}"), nil)
}

// Scrub sends `ceph osd scrub <id>`, or `ceph osd deep-scrub <id>`.
func (o *Operator) Scrub(ctx context.Context, id int32, deep bool) error {
	p, err := osdPath(id, "scrub")
	if err != nil {
		return err
	}
	return o.c.send(ctx, http.MethodPost, p, "deep="+strconv.FormatBool(deep), "1.0", []byte("{}"), nil)
}

// SettableFlags are the cluster flags the console sets and unsets, each
// for a while (FrameCephAction). pause, noup, nodown, noin and the others
// are left to the command line.
var SettableFlags = []string{"noout", "norebalance", "nobackfill", "norecover", "noscrub", "nodeep-scrub"}

// SetFlag sets (on) or unsets one cluster flag. The dashboard's PUT
// /api/osd/flags takes the whole list and sends `osd set`/`osd unset` for
// what differs from the cluster's: the list sent is the one just read with
// only this flag added or removed, so nothing else changes. It returns
// whether the flag was set before.
func (o *Operator) SetFlag(ctx context.Context, flag string, on bool) (bool, error) {
	if !slices.Contains(SettableFlags, flag) {
		return false, fmt.Errorf("refusing flag %q", flag)
	}
	cur, err := o.Flags(ctx)
	if err != nil {
		return false, err
	}
	was := slices.Contains(cur, flag)
	if was == on {
		return was, nil
	}
	next := slices.DeleteFunc(slices.Clone(cur), func(f string) bool { return f == flag })
	if on {
		next = append(next, flag)
	}
	body, _ := json.Marshal(map[string][]string{"flags": next})
	if err := o.c.send(ctx, http.MethodPut, "/api/osd/flags", "", "1.0", body, nil); err != nil {
		return was, err
	}
	// The PUT takes the whole list: a flag someone else set or unset
	// between the read and the PUT was overwritten. Read back and say so.
	after, err := o.Flags(ctx)
	if err != nil {
		return was, nil //nolint:nilerr // the PUT was accepted; the check is best effort
	}
	if slices.Contains(after, flag) != on {
		return was, fmt.Errorf("ceph accepted the change but %s is still %s", flag, map[bool]string{true: "unset", false: "set"}[on])
	}
	if d := flagDrift(next, after, flag); len(d) > 0 {
		return was, &FlagDriftError{Changed: d}
	}
	return was, nil
}

// FlagDriftError says that SetFlag did its job but other flags differ from
// what it sent: they changed between its read and its write, and its write
// may have undone that change.
type FlagDriftError struct{ Changed []string }

func (e *FlagDriftError) Error() string {
	return fmt.Sprintf("other flags changed at the same time (%s): check `ceph osd dump | grep flags`", strings.Join(e.Changed, ", "))
}

// flagDrift lists the flags, other than the one changed, that are in only
// one of sent and now (as +flag or -flag).
func flagDrift(sent, now []string, flag string) []string {
	var d []string
	for _, f := range now {
		if f != flag && !slices.Contains(sent, f) {
			d = append(d, "+"+f)
		}
	}
	for _, f := range sent {
		if f != flag && !slices.Contains(now, f) {
			d = append(d, "-"+f)
		}
	}
	return d
}

// OKToStopVerdict is the replacement's stand-in for `ceph osd ok-to-stop`,
// which the dashboard does not expose. It is stricter than Ceph's: it
// answers yes only when every PG is active+clean, every OSD is up and in,
// and every pool keeps min_size copies with one copy fewer. In that state a
// PG has exactly `size` copies on `size` distinct OSDs, so stopping any one
// OSD leaves it `size-1 >= min_size` copies: active, which is all ok-to-stop
// asks. Anything less is a refusal with its reason, never a guess.
func OKToStopVerdict(st *ClusterStatus, osds []OSDState, pools []Pool) (bool, string) {
	if st == nil || !st.AllActiveClean() {
		if st == nil {
			return false, "cluster status unknown"
		}
		return false, fmt.Sprintf("%d of %d PGs active+clean", st.ActiveClean(), st.NumPGs)
	}
	for _, o := range osds {
		if !o.Up || !o.In {
			return false, fmt.Sprintf("osd.%d is %s", o.ID, upIn(o))
		}
	}
	if len(pools) == 0 {
		return false, "no pool read"
	}
	for _, p := range pools {
		if p.MinSize < 1 || p.MinSize > p.Size-1 {
			return false, fmt.Sprintf("pool %s: min_size %d with size %d leaves no copy to lose", p.Name, p.MinSize, p.Size)
		}
	}
	return true, ""
}

func upIn(o OSDState) string {
	s := "down"
	if o.Up {
		s = "up"
	}
	if o.In {
		return s + "/in"
	}
	return s + "/out"
}

// OperatorSecretLoader reads the operator account, like SecretLoader.
func OperatorSecretLoader(r secretReader, namespace string) func(context.Context) (Config, error) {
	return secretLoader(r, namespace, OperatorSecretName)
}

// OperatorProvider hands out the operator client, rebuilt when the account
// in the Secret changes (re-read on every call, so a rotation takes effect).
type OperatorProvider struct {
	Load func(ctx context.Context) (Config, error)
	// Gate is required: no login is sent without it.
	Gate LoginGate

	mu  sync.Mutex
	op  *Operator
	key string
}

// Get returns the client for the current account. A login refused in an
// earlier call stays refused through the gate.
func (p *OperatorProvider) Get(ctx context.Context) (*Operator, error) {
	if p.Gate == nil {
		return nil, errors.New("no login gate (state Secret) configured")
	}
	cfg, err := p.Load(ctx)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%x", cfg.BaseURL, cfg.Username, cfg.Password, cfg.ServerName, cfg.CAPEM)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.op == nil || p.key != key {
		op, err := NewOperator(cfg, p.Gate)
		if err != nil {
			return nil, err
		}
		p.op, p.key = op, key
	}
	p.op.Reset()
	return p.op, nil
}
