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

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	"github.com/Plume-Labs/frame/internal/cephmgr"
)

// fakeCeph is a small Ceph: OSDs with a host, an up/in state and a serial,
// PG counts, health. Every call that changes it is recorded.
type fakeCeph struct {
	health   string
	checks   []cephmgr.HealthCheck
	numPGs   int64
	clean    int64
	osds     []cephmgr.OSDState
	serials  map[int32]string
	flags    []string
	pools    []cephmgr.Pool
	safe     bool
	purgeErr error
	calls    []string
}

func newFakeCeph() *fakeCeph {
	f := &fakeCeph{health: "HEALTH_OK", numPGs: 217, clean: 217, safe: true,
		pools:   []cephmgr.Pool{{Name: "replicapool", Size: 3, MinSize: 2}},
		serials: map[int32]string{}}
	add := func(id int32, host, serial string) {
		f.osds = append(f.osds, cephmgr.OSDState{ID: id, Up: true, In: true, Host: host})
		f.serials[id] = serial
	}
	add(0, "cp", "QM0001")
	add(1, "g9", "G9A")
	add(2, "r320", "W3TF2C5A")
	add(7, "r320", "S2AYL9RE")
	add(8, "r320", "Z2AW5NWA")
	return f
}

func (f *fakeCeph) osd(id int32) *cephmgr.OSDState { return findOSD(f.osds, id) }

func (f *fakeCeph) Status(context.Context) (*cephmgr.ClusterStatus, error) {
	st := &cephmgr.ClusterStatus{Health: f.health, Checks: f.checks, NumPGs: f.numPGs, PGsByState: map[string]int64{"active+clean": f.clean}}
	if f.clean < f.numPGs {
		st.PGsByState["active+remapped+backfilling"] = f.numPGs - f.clean
	}
	return st, nil
}
func (f *fakeCeph) OSDs(context.Context) ([]cephmgr.OSDState, error) {
	return slices.Clone(f.osds), nil
}
func (f *fakeCeph) Metadata(_ context.Context, id int32) (*cephmgr.OSDMetadata, error) {
	o := f.osd(id)
	if o == nil {
		return nil, cephmgr.ErrOSDNotFound
	}
	return &cephmgr.OSDMetadata{ID: id, Hostname: o.Host, Serials: []string{f.serials[id]}}, nil
}
func (f *fakeCeph) Flags(context.Context) ([]string, error) { return f.flags, nil }
func (f *fakeCeph) PoolSizes(context.Context) ([]cephmgr.Pool, error) {
	return f.pools, nil
}
func (f *fakeCeph) SafeToDestroy(context.Context, int32) (bool, string, error) {
	return f.safe, "pgs stored", nil
}
func (f *fakeCeph) MarkOut(_ context.Context, id int32) error {
	f.calls = append(f.calls, "out")
	f.osd(id).In = false
	return nil
}
func (f *fakeCeph) MarkIn(_ context.Context, id int32) error {
	f.calls = append(f.calls, "in")
	f.osd(id).In = true
	return nil
}
func (f *fakeCeph) Purge(_ context.Context, id int32) error {
	f.calls = append(f.calls, "purge")
	if f.purgeErr != nil {
		return f.purgeErr
	}
	f.osds = slices.DeleteFunc(f.osds, func(o cephmgr.OSDState) bool { return o.ID == id })
	delete(f.serials, id)
	return nil
}

func (f *fakeCeph) count(call string) int {
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

type osdHarness struct {
	t    *testing.T
	c    client.Client
	ceph *fakeCeph
	r    *FrameOSDReplacementReconciler
	now  time.Time
	key  types.NamespacedName
}

const (
	oldByID = "/dev/disk/by-id/wwn-0x5000c50000000008"
	newByID = "/dev/disk/by-id/wwn-0x5000cca000000099"
)

func r320Disks(withOld, withNew bool) []framev1beta1.ObservedDisk {
	d := []framev1beta1.ObservedDisk{
		{Path: "/dev/sda", SerialNumber: "W3TF2C5A", ByIDPath: "/dev/disk/by-id/wwn-0x5000c50000000002", Occupancy: "lvm-pv", LogicalSectorBytes: 512},
		{Path: "/dev/sdb", SerialNumber: "S2AYL9RE", ByIDPath: "/dev/disk/by-id/wwn-0x5000c50000000007", Occupancy: "lvm-pv", LogicalSectorBytes: 512},
		{Path: "/dev/md0", SerialNumber: "ROOT1", ByIDPath: "", Occupancy: "mounted"},
	}
	if withOld {
		d = append(d, framev1beta1.ObservedDisk{Path: "/dev/sdc", SerialNumber: "Z2AW5NWA", ByIDPath: oldByID, Occupancy: "lvm-pv", LogicalSectorBytes: 512})
	}
	if withNew {
		d = append(d, framev1beta1.ObservedDisk{Path: "/dev/sdc", SerialNumber: "SAS600NEW", ByIDPath: newByID, Occupancy: "free", LogicalSectorBytes: 512, SizeGB: 600, Model: "ST600MM0006"})
	}
	return d
}

func newOSDHarness(t *testing.T) *osdHarness {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := framev1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	h := &osdHarness{t: t, ceph: newFakeCeph(), now: time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)}
	at := metav1.NewTime(h.now)
	m := &framev1beta1.FrameMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "neu-srv-11", Namespace: "frame-system"},
		Spec:       framev1beta1.FrameMachineSpec{NodeRef: "r320"},
		Status: framev1beta1.FrameMachineStatus{Storage: &framev1beta1.MachineStorage{
			ObservedAt: &at, Observed: r320Disks(true, false)}},
	}
	cc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ceph.rook.io/v1", "kind": "CephCluster",
		"metadata": map[string]any{"name": "rook-ceph", "namespace": "rook-ceph"},
		"spec": map[string]any{"storage": map[string]any{
			"useAllDevices": false,
			"devices": []any{
				map[string]any{"name": "/dev/disk/by-id/wwn-0x5000c50000000002", "config": map[string]any{"deviceClass": "hdd"}},
				map[string]any{"name": "/dev/disk/by-id/wwn-0x5000c50000000007", "config": map[string]any{"deviceClass": "hdd"}},
				map[string]any{"name": oldByID, "config": map[string]any{"deviceClass": "hdd"}},
			},
		}},
	}}
	one := int32(1)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "rook-ceph-osd-8", Namespace: "rook-ceph"}, Spec: appsv1.DeploymentSpec{Replicas: &one}}
	opdep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "rook-ceph-operator", Namespace: "rook-ceph"}}
	repl := &framev1beta1.FrameOSDReplacement{
		ObjectMeta: metav1.ObjectMeta{Name: "osd-8", Namespace: "frame-system", UID: "0a1b2c3d-0000-0000-0000-000000000008", CreationTimestamp: at, Generation: 1},
		Spec:       framev1beta1.FrameOSDReplacementSpec{OSDID: 8, Serial: "Z2AW5NWA"},
	}
	h.c = fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(m, cc, dep, opdep, repl, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "r320"}}).
		WithStatusSubresource(&framev1beta1.FrameOSDReplacement{}, &framev1beta1.FrameMachine{}).
		Build()
	h.key = types.NamespacedName{Namespace: "frame-system", Name: "osd-8"}
	h.r = &FrameOSDReplacementReconciler{
		Client: h.c, Scheme: scheme,
		Ceph: func(context.Context) (CephOperator, error) { return h.ceph, nil },
		Now:  func() time.Time { return h.now },
	}
	return h
}

func (h *osdHarness) reconcile() ctrl.Result {
	h.t.Helper()
	res, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: h.key})
	if err != nil {
		h.t.Fatalf("reconcile: %v", err)
	}
	return res
}

func (h *osdHarness) get() *framev1beta1.FrameOSDReplacement {
	h.t.Helper()
	var o framev1beta1.FrameOSDReplacement
	if err := h.c.Get(context.Background(), h.key, &o); err != nil {
		h.t.Fatal(err)
	}
	return &o
}

func (h *osdHarness) expectPhase(want string) {
	h.t.Helper()
	if o := h.get(); o.Status.Phase != want {
		h.t.Fatalf("phase = %s (%s), want %s", o.Status.Phase, o.Status.Message, want)
	}
}

func (h *osdHarness) patchSpec(f func(*framev1beta1.FrameOSDReplacement)) {
	h.t.Helper()
	o := h.get()
	base := o.DeepCopy()
	f(o)
	o.Generation++ // what the apiserver does on a spec change; the fake client does not
	if err := h.c.Patch(context.Background(), o, client.MergeFrom(base)); err != nil {
		h.t.Fatal(err)
	}
}

func (h *osdHarness) setDisks(d []framev1beta1.ObservedDisk) {
	h.t.Helper()
	var m framev1beta1.FrameMachine
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "frame-system", Name: "neu-srv-11"}, &m); err != nil {
		h.t.Fatal(err)
	}
	at := metav1.NewTime(h.now)
	m.Status.Storage.Observed, m.Status.Storage.ObservedAt = d, &at
	if err := h.c.Status().Update(context.Background(), &m); err != nil {
		h.t.Fatal(err)
	}
}

func (h *osdHarness) advance(d time.Duration) { h.now = h.now.Add(d) }

func (h *osdHarness) devices() []string {
	h.t.Helper()
	names, err := h.r.deviceNames(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return names
}

func (h *osdHarness) node() *corev1.Node {
	var n corev1.Node
	if err := h.c.Get(context.Background(), types.NamespacedName{Name: "r320"}, &n); err != nil {
		h.t.Fatal(err)
	}
	return &n
}

func (h *osdHarness) agentAnswers(ok, ran bool, msg string) {
	h.t.Helper()
	n := h.node()
	var req framev1beta1.DiskWipeRequest
	if err := json.Unmarshal([]byte(n.Annotations[framev1beta1.DiskWipeRequestAnnotation]), &req); err != nil {
		h.t.Fatalf("no wipe request on the node: %v", err)
	}
	res, _ := json.Marshal(framev1beta1.DiskWipeResult{ID: req.ID, OK: ok, Ran: ran, Message: msg, At: metav1.NewTime(h.now)})
	base := n.DeepCopy()
	n.Annotations[framev1beta1.DiskWipeResultAnnotation] = string(res)
	if err := h.c.Patch(context.Background(), n, client.MergeFrom(base)); err != nil {
		h.t.Fatal(err)
	}
}

// driveToPurgeApproval runs the check and the drain.
func (h *osdHarness) driveToPurgeApproval() {
	h.t.Helper()
	h.reconcile() // Checking → Draining
	h.expectPhase(framev1beta1.OSDReplacementDraining)
	h.reconcile() // out
	if h.ceph.count("out") != 1 {
		h.t.Fatalf("calls = %v", h.ceph.calls)
	}
	h.ceph.clean = 200 // data moving
	h.advance(time.Minute)
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementDraining)
	h.ceph.clean = 217
	h.advance(10 * time.Minute)
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementAwaitingPurgeApproval)
	h.reconcile() // arms approval no. 1
	if h.get().Status.ApprovalArmedGeneration == 0 {
		h.t.Fatal("approval no. 1 not armed")
	}
}

func TestOSDReplacementHappyPath(t *testing.T) {
	h := newOSDHarness(t)
	h.driveToPurgeApproval()

	o := h.get()
	if o.Status.Host != "r320" || o.Status.OldDisk.ByIDPath != oldByID || *o.Status.DeviceIndex != 2 || o.Status.HostOSDsBefore != 3 {
		t.Fatalf("check record: %+v", o.Status)
	}

	// No approval: nothing happens.
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementAwaitingPurgeApproval)
	if h.ceph.count("purge") != 0 {
		t.Fatal("purged without approval")
	}
	// A wrong serial is refused, and can be retyped.
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
		o.Spec.PurgeApproval = &framev1beta1.SerialApproval{Serial: "Z2AW5NWB"}
	})
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementAwaitingPurgeApproval)
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
		o.Spec.PurgeApproval = &framev1beta1.SerialApproval{Serial: "Z2AW5NWA"}
	})
	h.reconcile() // → Purging
	h.expectPhase(framev1beta1.OSDReplacementPurging)
	h.ceph.osd(8).Up = false // the deployment went to 0
	h.reconcile()            // scale, purge
	if h.ceph.count("purge") != 1 {
		t.Fatalf("calls = %v", h.ceph.calls)
	}
	var d appsv1.Deployment
	_ = h.c.Get(context.Background(), types.NamespacedName{Namespace: "rook-ceph", Name: "rook-ceph-osd-8"}, &d)
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 0 {
		t.Fatalf("deployment not scaled to 0: %+v", d.Spec.Replicas)
	}
	h.reconcile() // purged → deployment deleted → AwaitingDisk
	h.expectPhase(framev1beta1.OSDReplacementAwaitingDisk)
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "rook-ceph", Name: "rook-ceph-osd-8"}, &d); err == nil {
		t.Fatal("osd deployment not deleted")
	}

	// The old disk is still there: wait.
	h.advance(time.Minute)
	h.setDisks(r320Disks(true, false))
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementAwaitingDisk)
	if !strings.Contains(h.get().Status.Message, "still there") {
		t.Fatalf("message = %s", h.get().Status.Message)
	}
	// Swapped.
	h.advance(time.Minute)
	h.setDisks(r320Disks(false, true))
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementAwaitingWipeApproval)
	if h.get().Status.NewDisk.Serial != "SAS600NEW" {
		t.Fatalf("new disk = %+v", h.get().Status.NewDisk)
	}
	if n := h.node(); n.Annotations[framev1beta1.DiskWipeRequestAnnotation] != "" {
		t.Fatal("wipe requested without approval")
	}
	h.reconcile() // arms approval no. 2
	// The new disk's serial is read on its label, never on the screen.
	if o := h.get(); strings.Contains(o.Status.Message, "SAS600NEW") || slices.ContainsFunc(o.Status.Events, func(e framev1beta1.OSDReplacementEvent) bool {
		return strings.Contains(e.Message, "SAS600NEW")
	}) {
		t.Fatalf("the new disk's serial is shown before approval no. 2: %q / %+v", o.Status.Message, o.Status.Events)
	}
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
		o.Spec.WipeApproval = &framev1beta1.SerialApproval{Serial: "SAS600NEW"}
	})
	h.reconcile() // → Wiping
	h.reconcile() // request written
	h.expectPhase(framev1beta1.OSDReplacementWiping)
	var req framev1beta1.DiskWipeRequest
	if err := json.Unmarshal([]byte(h.node().Annotations[framev1beta1.DiskWipeRequestAnnotation]), &req); err != nil {
		t.Fatal(err)
	}
	if req.ByIDPath != newByID || req.Serial != "SAS600NEW" {
		t.Fatalf("request = %+v", req)
	}
	h.agentAnswers(true, true, "wiped")
	h.reconcile() // → Declaring
	h.reconcile() // patch
	h.expectPhase(framev1beta1.OSDReplacementAwaitingOSD)
	devs := h.devices()
	if devs[2] != newByID || slices.Contains(devs, oldByID) || len(devs) != 3 {
		t.Fatalf("devices = %v", devs)
	}
	// The other entries are untouched, deviceClass included.
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(cephClusterGVK)
	_ = h.c.Get(context.Background(), types.NamespacedName{Namespace: "rook-ceph", Name: "rook-ceph"}, u)
	list, _, _ := unstructured.NestedSlice(u.Object, "spec", "storage", "devices")
	if cls := list[2].(map[string]any)["config"].(map[string]any)["deviceClass"]; cls != "hdd" {
		t.Fatalf("entry's config changed: %v", list[2])
	}

	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementAwaitingOSD)
	h.ceph.osds = append(h.ceph.osds, cephmgr.OSDState{ID: 10, Up: true, In: true, Host: "r320"})
	h.ceph.serials[10] = "SAS600NEW"
	h.reconcile() // → Backfilling
	h.expectPhase(framev1beta1.OSDReplacementBackfilling)
	h.ceph.clean = 190
	h.advance(time.Minute)
	h.reconcile()
	h.ceph.clean = 217
	h.advance(time.Minute)
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementSucceeded)
	if h.ceph.count("purge") != 1 || h.ceph.count("out") != 1 {
		t.Fatalf("calls = %v", h.ceph.calls)
	}
}

func TestOSDReplacementCheckRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *osdHarness)
		want  string // phase
		msg   string
	}{
		{"wrong serial", func(h *osdHarness) {
			h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) { o.Spec.Serial = "ZZZ" })
		}, framev1beta1.OSDReplacementFailed, "not osd.8's disk"},
		{"health err", func(h *osdHarness) { h.ceph.health = "HEALTH_ERR" }, framev1beta1.OSDReplacementPaused, "HEALTH_ERR"},
		{"untolerated warning", func(h *osdHarness) {
			h.ceph.health = "HEALTH_WARN"
			h.ceph.checks = []cephmgr.HealthCheck{{Name: "PG_DEGRADED", Message: "x"}}
		}, framev1beta1.OSDReplacementPaused, "PG_DEGRADED"},
		{"tolerated warning", func(h *osdHarness) {
			h.ceph.health = "HEALTH_WARN"
			h.ceph.checks = []cephmgr.HealthCheck{{Name: "BLUESTORE_SLOW_OP_ALERT", Message: "osd.0"}}
		}, framev1beta1.OSDReplacementDraining, ""},
		{"pgs not clean", func(h *osdHarness) { h.ceph.clean = 216 }, framev1beta1.OSDReplacementPaused, "216 of 217"},
		{"another osd down", func(h *osdHarness) { h.ceph.osd(1).Up = false }, framev1beta1.OSDReplacementPaused, "osd.1 is down"},
		{"noout", func(h *osdHarness) { h.ceph.flags = []string{"noout"} }, framev1beta1.OSDReplacementPaused, "noout"},
		{"min_size", func(h *osdHarness) { h.ceph.pools[0].MinSize = 3 }, framev1beta1.OSDReplacementPaused, "min_size"},
		{"undeclared sibling", func(h *osdHarness) {
			h.setDisks(append(r320Disks(true, false), framev1beta1.ObservedDisk{Path: "/dev/sdd", SerialNumber: "Z6EGEL3W", ByIDPath: "/dev/disk/by-id/wwn-0x5000c50000000009", Occupancy: "lvm-pv"}))
			h.ceph.osds = append(h.ceph.osds, cephmgr.OSDState{ID: 9, Up: true, In: true, Host: "r320"})
			h.ceph.serials[9] = "Z6EGEL3W"
		}, framev1beta1.OSDReplacementPaused, "osd.9"},
		{"unknown osd", func(h *osdHarness) {
			h.ceph.osds = slices.DeleteFunc(h.ceph.osds, func(o cephmgr.OSDState) bool { return o.ID == 8 })
		}, framev1beta1.OSDReplacementFailed, "does not exist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOSDHarness(t)
			tc.setup(h)
			h.reconcile()
			o := h.get()
			if o.Status.Phase != tc.want || !strings.Contains(o.Status.Message, tc.msg) {
				t.Fatalf("phase %s message %q, want %s containing %q", o.Status.Phase, o.Status.Message, tc.want, tc.msg)
			}
			if len(h.ceph.calls) != 0 {
				t.Fatalf("Ceph changed during the check: %v", h.ceph.calls)
			}
		})
	}
}

func TestOSDReplacementOneAtATime(t *testing.T) {
	h := newOSDHarness(t)
	older := &framev1beta1.FrameOSDReplacement{
		ObjectMeta: metav1.ObjectMeta{Name: "osd-2", Namespace: "frame-system", CreationTimestamp: metav1.NewTime(h.now.Add(-time.Hour))},
		Spec:       framev1beta1.FrameOSDReplacementSpec{OSDID: 2, Serial: "W3TF2C5A"},
	}
	if err := h.c.Create(context.Background(), older); err != nil {
		t.Fatal(err)
	}
	older.Status.Phase = framev1beta1.OSDReplacementPaused
	if err := h.c.Status().Update(context.Background(), older); err != nil {
		t.Fatal(err)
	}
	h.reconcile()
	if p := h.get(); p.Status.Phase != framev1beta1.OSDReplacementChecking || !strings.Contains(p.Status.Message, "osd-2") {
		t.Fatalf("phase %s message %s", p.Status.Phase, p.Status.Message)
	}
	if len(h.ceph.calls) != 0 {
		t.Fatal("acted while another replacement holds the turn")
	}
}

func TestOSDReplacementWaitsForACephActionInFlight(t *testing.T) {
	h := newOSDHarness(t)
	at := metav1.NewTime(h.now)
	id := int32(1)
	act := &framev1beta1.FrameCephAction{
		ObjectMeta: metav1.ObjectMeta{Name: "restart-1", Namespace: "frame-system"},
		Spec:       framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDRestart, OSDID: &id},
		Status:     framev1beta1.FrameCephActionStatus{Phase: framev1beta1.CephActionPending, SentAt: &at},
	}
	if err := h.c.Create(context.Background(), act); err != nil {
		t.Fatal(err)
	}
	h.reconcile()
	if p := h.get(); p.Status.Phase != framev1beta1.OSDReplacementChecking || !strings.Contains(p.Status.Message, "restart-1") {
		t.Fatalf("phase %s message %s", p.Status.Phase, p.Status.Message)
	}
	if len(h.ceph.calls) != 0 {
		t.Fatal("acted while a Ceph action is in flight")
	}
}

func TestOSDReplacementPurgeNeverResent(t *testing.T) {
	h := newOSDHarness(t)
	h.driveToPurgeApproval()
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
		o.Spec.PurgeApproval = &framev1beta1.SerialApproval{Serial: "Z2AW5NWA"}
	})
	h.reconcile()
	h.ceph.osd(8).Up = false
	h.ceph.purgeErr = errors.New("connection reset")
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementPaused)
	// A resume while the OSD still exists does not send the purge again.
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) { o.Spec.Resume = 1 })
	h.advance(5 * time.Minute)
	h.reconcile() // resumed
	h.reconcile() // the step
	h.expectPhase(framev1beta1.OSDReplacementPaused)
	if h.ceph.count("purge") != 1 {
		t.Fatalf("purge sent %d times", h.ceph.count("purge"))
	}
	// Purged by hand, then resumed: the replacement goes on.
	h.ceph.osds = slices.DeleteFunc(h.ceph.osds, func(o cephmgr.OSDState) bool { return o.ID == 8 })
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) { o.Spec.Resume = 2 })
	h.reconcile()
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementAwaitingDisk)
	if h.ceph.count("purge") != 1 {
		t.Fatalf("purge sent %d times", h.ceph.count("purge"))
	}
}

func TestOSDReplacementCancelBeforePurgeMarksIn(t *testing.T) {
	h := newOSDHarness(t)
	h.driveToPurgeApproval()
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) { o.Spec.Cancel = true })
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementCancelled)
	if !h.ceph.osd(8).In || h.ceph.count("in") != 1 || h.ceph.count("purge") != 0 {
		t.Fatalf("calls = %v", h.ceph.calls)
	}
}

func TestOSDReplacementNewDiskGuards(t *testing.T) {
	toDisk := func(h *osdHarness) {
		h.driveToPurgeApproval()
		h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
			o.Spec.PurgeApproval = &framev1beta1.SerialApproval{Serial: "Z2AW5NWA"}
		})
		h.reconcile()
		h.ceph.osd(8).Up = false
		h.reconcile()
		h.reconcile()
		h.expectPhase(framev1beta1.OSDReplacementAwaitingDisk)
		h.advance(time.Minute)
	}
	t.Run("two new disks", func(t *testing.T) {
		h := newOSDHarness(t)
		toDisk(h)
		d := r320Disks(false, true)
		d = append(d, framev1beta1.ObservedDisk{Path: "/dev/sdd", SerialNumber: "OTHER", ByIDPath: "/dev/disk/by-id/wwn-0x1", Occupancy: "free", LogicalSectorBytes: 512})
		h.setDisks(d)
		h.reconcile()
		h.expectPhase(framev1beta1.OSDReplacementPaused)
	})
	t.Run("520-byte sectors", func(t *testing.T) {
		h := newOSDHarness(t)
		toDisk(h)
		d := r320Disks(false, true)
		d[len(d)-1].LogicalSectorBytes = 520
		h.setDisks(d)
		h.reconcile()
		h.expectPhase(framev1beta1.OSDReplacementPaused)
	})
	t.Run("partitioned", func(t *testing.T) {
		h := newOSDHarness(t)
		toDisk(h)
		d := r320Disks(false, true)
		d[len(d)-1].Occupancy = "partitioned"
		h.setDisks(d)
		h.reconcile()
		h.expectPhase(framev1beta1.OSDReplacementPaused)
	})
	t.Run("wrong wipe serial then agent refusal pauses", func(t *testing.T) {
		h := newOSDHarness(t)
		toDisk(h)
		h.setDisks(r320Disks(false, true))
		h.reconcile()
		h.reconcile() // arms approval no. 2
		h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
			o.Spec.WipeApproval = &framev1beta1.SerialApproval{Serial: "nope"}
		})
		h.reconcile()
		h.expectPhase(framev1beta1.OSDReplacementAwaitingWipeApproval)
		h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
			o.Spec.WipeApproval = &framev1beta1.SerialApproval{Serial: "SAS600NEW"}
		})
		h.reconcile()
		h.reconcile()
		h.agentAnswers(false, false, "refused: held")
		h.reconcile()
		h.expectPhase(framev1beta1.OSDReplacementPaused)
		if h.node().Annotations[framev1beta1.DiskWipeRequestAnnotation] != "" {
			t.Fatal("the refused request was not withdrawn")
		}
		first := h.get().Status.WipeRequestID
		// Resume: back to approval no. 2; the old approval does not count.
		h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) { o.Spec.Resume = 1 })
		h.reconcile()
		h.reconcile() // arms
		h.expectPhase(framev1beta1.OSDReplacementAwaitingWipeApproval)
		if h.get().Spec.WipeApproval != nil {
			t.Fatal("the approval given before the pause was kept")
		}
		h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
			o.Spec.WipeApproval = &framev1beta1.SerialApproval{Serial: "SAS600NEW"}
		})
		h.reconcile()
		h.reconcile()
		// A new request id.
		if id := h.get().Status.WipeRequestID; id == first || id == "" {
			t.Fatalf("request id after resume = %q (first %q)", id, first)
		}
		if devs := h.devices(); slices.Contains(devs, newByID) {
			t.Fatal("declared without a wipe")
		}
	})
}

func TestOSDReplacementApprovalGivenInAdvanceDoesNotCount(t *testing.T) {
	h := newOSDHarness(t)
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
		o.Spec.PurgeApproval = &framev1beta1.SerialApproval{Serial: "Z2AW5NWA"}
		o.Spec.WipeApproval = &framev1beta1.SerialApproval{Serial: "SAS600NEW"}
	})
	h.driveToPurgeApproval()
	h.ceph.osd(8).Up = false
	h.reconcile()
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementAwaitingPurgeApproval)
	if o := h.get(); o.Spec.PurgeApproval != nil || h.ceph.count("purge") != 0 {
		t.Fatalf("approval given in advance: spec %+v, calls %v", o.Spec.PurgeApproval, h.ceph.calls)
	}
	// Given now, it counts; the wipe approval written in advance does not.
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
		o.Spec.PurgeApproval = &framev1beta1.SerialApproval{Serial: "Z2AW5NWA"}
	})
	h.reconcile()
	h.reconcile()
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementAwaitingDisk)
	h.advance(time.Minute)
	h.setDisks(r320Disks(false, true))
	h.reconcile()
	h.reconcile()
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementAwaitingWipeApproval)
	if o := h.get(); o.Spec.WipeApproval != nil || h.node().Annotations[framev1beta1.DiskWipeRequestAnnotation] != "" {
		t.Fatal("wipe approval given in advance was not cleared")
	}
}

func TestOSDReplacementPauseBeforePurgeAsksApprovalAgain(t *testing.T) {
	h := newOSDHarness(t)
	h.driveToPurgeApproval()
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
		o.Spec.PurgeApproval = &framev1beta1.SerialApproval{Serial: "Z2AW5NWA"}
	})
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementPurging)
	h.ceph.osd(8).Up, h.ceph.osd(8).In = false, true // someone marked it in
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementPaused)
	h.ceph.osd(8).In = false
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) { o.Spec.Resume = 1 })
	h.reconcile()
	h.reconcile()
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementAwaitingPurgeApproval)
	if o := h.get(); o.Spec.PurgeApproval != nil || h.ceph.count("purge") != 0 {
		t.Fatalf("stale approval kept: spec %+v, calls %v", o.Spec.PurgeApproval, h.ceph.calls)
	}
}

func TestOSDReplacementCancelWithdrawsTheWipeRequest(t *testing.T) {
	h := newOSDHarness(t)
	h.driveToPurgeApproval()
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
		o.Spec.PurgeApproval = &framev1beta1.SerialApproval{Serial: "Z2AW5NWA"}
	})
	h.reconcile()
	h.ceph.osd(8).Up = false
	h.reconcile()
	h.reconcile()
	h.advance(time.Minute)
	h.setDisks(r320Disks(false, true))
	h.reconcile()
	h.reconcile()
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
		o.Spec.WipeApproval = &framev1beta1.SerialApproval{Serial: "SAS600NEW"}
	})
	h.reconcile()
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementWiping)
	if h.node().Annotations[framev1beta1.DiskWipeRequestAnnotation] == "" {
		t.Fatal("no wipe request")
	}
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) { o.Spec.Cancel = true })
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementCancelled)
	if h.node().Annotations[framev1beta1.DiskWipeRequestAnnotation] != "" {
		t.Fatal("the wipe request outlived the cancel")
	}
}

func TestOSDReplacementCancelWhenTheOSDIsGone(t *testing.T) {
	h := newOSDHarness(t)
	h.driveToPurgeApproval()
	h.ceph.osds = slices.DeleteFunc(h.ceph.osds, func(o cephmgr.OSDState) bool { return o.ID == 8 })
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) { o.Spec.Cancel = true })
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementCancelled)
	if h.ceph.count("in") != 0 || !strings.Contains(h.get().Status.Message, "no longer exists") {
		t.Fatalf("calls %v, message %q", h.ceph.calls, h.get().Status.Message)
	}
}

func TestOSDReplacementCancelReportsALateWipeAnswer(t *testing.T) {
	h := newOSDHarness(t)
	h.driveToPurgeApproval()
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
		o.Spec.PurgeApproval = &framev1beta1.SerialApproval{Serial: "Z2AW5NWA"}
	})
	h.reconcile()
	h.ceph.osd(8).Up = false
	h.reconcile()
	h.reconcile()
	h.advance(time.Minute)
	h.setDisks(r320Disks(false, true))
	h.reconcile()
	h.reconcile()
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) {
		o.Spec.WipeApproval = &framev1beta1.SerialApproval{Serial: "SAS600NEW"}
	})
	h.reconcile()
	h.reconcile()
	// The agent wiped and answered; the cancel comes before the controller
	// read the answer.
	h.agentAnswers(true, true, "wiped")
	h.patchSpec(func(o *framev1beta1.FrameOSDReplacement) { o.Spec.Cancel = true })
	h.reconcile()
	h.expectPhase(framev1beta1.OSDReplacementCancelled)
	if msg := h.get().Status.Message; !strings.Contains(msg, "wiped (the agent's answer came after the request)") {
		t.Fatalf("message %q", msg)
	}
}
