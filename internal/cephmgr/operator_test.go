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
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// opDashboard answers the operator routes with bodies shaped like Ceph
// v20.2.2's, and records method, URI, Accept and body of every request.
type opDashboard struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []string
}

const (
	snapshotBody = `{"fsid":"x","health":{"status":"HEALTH_WARN","checks":{"BLUESTORE_SLOW_OP_ALERT":{"severity":"HEALTH_WARN","summary":{"message":"1 OSD(s) experiencing slow operations in BlueStore","count":1},"muted":false}},"mutes":[]},
"osdmap":{"in":9,"up":9,"num_osds":9},"pgmap":{"pgs_by_state":[{"state_name":"active+clean","count":210},{"state_name":"active+remapped+backfilling","count":7}],"num_pools":3,"num_pgs":217}}`
	osdListBody = `[{"osd":2,"up":1,"in":1,"state":["exists","up"],"host":{"name":"neura-k3s-r320","children":[2,7,8,9]},"tree":{"device_class":"t320"}},
{"osd":0,"up":1,"in":0,"state":["exists","up"],"host":{"name":"neura-k3s-cp"},"tree":{"device_class":"t320"}}]`
	osdGetBody = `{"osd_map":{"osd":8,"up":1,"in":1},"osd_metadata":{"hostname":"neura-k3s-r320","device_ids":"sdc=ST500DM002-1BD142_Z2AW5NWA","bluestore_bdev_dev_node":"/dev/sdc"},"operational_status":"unmanaged"}`
)

func newOpDashboard(t *testing.T) *opDashboard {
	d := &opDashboard{}
	d.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		d.mu.Lock()
		if r.URL.Path != pathAuth {
			d.reqs = append(d.reqs, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Accept")+" "+string(b)))
		}
		d.mu.Unlock()
		if r.URL.Path == pathAuth {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"tok"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/health/snapshot":
			_, _ = w.Write([]byte(snapshotBody))
		case "GET /api/osd":
			_, _ = w.Write([]byte(osdListBody))
		case "GET /api/osd/8":
			_, _ = w.Write([]byte(osdGetBody))
		case "GET /api/osd/flags":
			_, _ = w.Write([]byte(`["sortbitwise","recovery_deletes","noout"]`))
		case "GET /api/osd/safe_to_destroy":
			if r.URL.Query().Get("ids") == "8" {
				_, _ = w.Write([]byte(`{"safe_to_destroy":[8],"active":[],"missing_stats":[],"stored_pgs":[],"is_safe_to_destroy":true}`))
				return
			}
			_, _ = w.Write([]byte(`{"safe_to_destroy":[],"active":[],"missing_stats":[],"stored_pgs":[7],"is_safe_to_destroy":false}`))
		case "GET /api/pool":
			_, _ = w.Write([]byte(`[{"pool_name":"replicapool","size":3,"min_size":2,"crush_rule":"hote_x3"}]`))
		case "PUT /api/osd/8/mark", "POST /api/osd/8/purge":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`null`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"detail":"osd.99 does not exist","code":"500"}`))
		}
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *opDashboard) operator(t *testing.T) *Operator {
	t.Helper()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d.srv.Certificate().Raw})
	op, err := NewOperator(Config{BaseURL: d.srv.URL, Username: "frame-osd-operator", Password: "p", CAPEM: ca, ServerName: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func (d *opDashboard) last() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.reqs) == 0 {
		return ""
	}
	return d.reqs[len(d.reqs)-1]
}

func TestOperatorSendsExactlyTheListedRequests(t *testing.T) {
	d := newOpDashboard(t)
	op := d.operator(t)
	ctx := context.Background()
	cases := []struct {
		call func() error
		want string
	}{
		{func() error { _, err := op.Status(ctx); return err }, "GET /api/health/snapshot application/vnd.ceph.api.v1.0+json"},
		{func() error { _, err := op.OSDs(ctx); return err }, "GET /api/osd?limit=-1 application/vnd.ceph.api.v1.1+json"},
		{func() error { _, err := op.Metadata(ctx, 8); return err }, "GET /api/osd/8 application/vnd.ceph.api.v1.0+json"},
		{func() error { _, err := op.Flags(ctx); return err }, "GET /api/osd/flags application/vnd.ceph.api.v1.0+json"},
		{func() error { _, err := op.PoolSizes(ctx); return err }, "GET /api/pool application/vnd.ceph.api.v1.0+json"},
		{func() error { _, _, err := op.SafeToDestroy(ctx, 8); return err }, "GET /api/osd/safe_to_destroy?ids=8 application/vnd.ceph.api.v1.0+json"},
		{func() error { return op.MarkOut(ctx, 8) }, `PUT /api/osd/8/mark application/vnd.ceph.api.v1.0+json {"action":"out"}`},
		{func() error { return op.MarkIn(ctx, 8) }, `PUT /api/osd/8/mark application/vnd.ceph.api.v1.0+json {"action":"in"}`},
		{func() error { return op.Purge(ctx, 8) }, `POST /api/osd/8/purge application/vnd.ceph.api.v1.0+json {}`},
	}
	for _, tc := range cases {
		if err := tc.call(); err != nil {
			t.Fatalf("%s: %v", tc.want, err)
		}
		if got := d.last(); got != tc.want {
			t.Fatalf("sent %q, want %q", got, tc.want)
		}
	}
}

func TestOperatorRefusesAnOutOfRangeID(t *testing.T) {
	d := newOpDashboard(t)
	op := d.operator(t)
	for _, id := range []int32{-1, MaxOSDID + 1} {
		if err := op.Purge(context.Background(), id); err == nil {
			t.Fatalf("purge %d accepted", id)
		}
		if err := op.MarkOut(context.Background(), id); err == nil {
			t.Fatalf("out %d accepted", id)
		}
	}
	if len(d.reqs) != 0 {
		t.Fatalf("sent %v", d.reqs)
	}
}

func TestOperatorParsesCephAnswers(t *testing.T) {
	d := newOpDashboard(t)
	op := d.operator(t)
	ctx := context.Background()
	st, err := op.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Health != "HEALTH_WARN" || st.NumPGs != 217 || st.ActiveClean() != 210 || st.AllActiveClean() ||
		len(st.Checks) != 1 || st.Checks[0].Name != "BLUESTORE_SLOW_OP_ALERT" {
		t.Fatalf("status = %+v", st)
	}
	osds, err := op.OSDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(osds) != 2 || osds[0].ID != 0 || osds[0].In || osds[1].Host != "neura-k3s-r320" || !osds[1].Up {
		t.Fatalf("osds = %+v", osds)
	}
	m, err := op.Metadata(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if m.Hostname != "neura-k3s-r320" || !slices.Equal(m.Serials, []string{"Z2AW5NWA"}) || m.DevNode != "/dev/sdc" {
		t.Fatalf("metadata = %+v", m)
	}
	if ok, _, err := op.SafeToDestroy(ctx, 8); err != nil || !ok {
		t.Fatalf("safe 8 = %t %v", ok, err)
	}
	if ok, why, err := op.SafeToDestroy(ctx, 7); err != nil || ok || !strings.Contains(why, "stored PGs [7]") {
		t.Fatalf("safe 7 = %t %q %v", ok, why, err)
	}
	if _, err := op.Metadata(ctx, 99); err == nil || !strings.Contains(err.Error(), "osd.99 does not exist") {
		t.Fatalf("metadata 99 err = %v", err)
	}
}

func TestSerialsOf(t *testing.T) {
	cases := map[string][]string{
		"sdc=ST500DM002-1BD142_Z2AW5NWA":                         {"Z2AW5NWA"},
		"sda=SEAGATE_ST600MM0006_S0M1ABCD,sdb=INTEL_SSD_BTWL123": {"S0M1ABCD", "BTWL123"},
		"":             nil,
		"sdc=NOSERIAL": nil,
		"sdc=trail_":   nil,
	}
	for in, want := range cases {
		if got := SerialsOf(in); !slices.Equal(got, want) {
			t.Fatalf("SerialsOf(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestOKToStopVerdict(t *testing.T) {
	clean := &ClusterStatus{Health: "HEALTH_OK", NumPGs: 10, PGsByState: map[string]int64{"active+clean": 10}}
	dirty := &ClusterStatus{Health: "HEALTH_OK", NumPGs: 10, PGsByState: map[string]int64{"active+clean": 9}}
	up := []OSDState{{ID: 1, Up: true, In: true}, {ID: 2, Up: true, In: true}}
	pools := []Pool{{Name: "p", Size: 3, MinSize: 2}}
	if ok, why := OKToStopVerdict(clean, up, pools); !ok {
		t.Fatalf("refused: %s", why)
	}
	for name, tc := range map[string]struct {
		st    *ClusterStatus
		osds  []OSDState
		pools []Pool
	}{
		"unclean":    {dirty, up, pools},
		"nil status": {nil, up, pools},
		"osd out":    {clean, []OSDState{{ID: 1, Up: true}}, pools},
		"osd down":   {clean, []OSDState{{ID: 1, In: true}}, pools},
		"min=size":   {clean, up, []Pool{{Name: "p", Size: 2, MinSize: 2}}},
		"min 0":      {clean, up, []Pool{{Name: "p", Size: 3, MinSize: 0}}},
		"no pools":   {clean, up, nil},
		"no pgs":     {&ClusterStatus{PGsByState: map[string]int64{}}, up, pools},
	} {
		if ok, _ := OKToStopVerdict(tc.st, tc.osds, tc.pools); ok {
			t.Fatalf("%s: accepted", name)
		}
	}
}
