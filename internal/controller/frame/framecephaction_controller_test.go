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
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	"github.com/Plume-Labs/frame/internal/cephmgr"
)

// actCeph is a fake operator account recording every write.
type actCeph struct {
	osds    []cephmgr.OSDState
	clean   int64
	flags   []string
	calls   []string
	sendErr error
	// raceFlag is set by "someone else" just before Frame's SetFlag.
	raceFlag string
}

func newActCeph() *actCeph {
	return &actCeph{
		osds: []cephmgr.OSDState{
			{ID: 0, Up: true, In: true, Host: "cp"},
			{ID: 2, Up: true, In: true, Host: "r320"},
			{ID: 8, Up: true, In: true, Host: "r320"},
			{ID: 1, Up: true, In: true, Host: "g9"},
		},
		clean: 100,
		flags: []string{"sortbitwise", "recovery_deletes"},
	}
}

func (f *actCeph) Status(context.Context) (*cephmgr.ClusterStatus, error) {
	return &cephmgr.ClusterStatus{Health: "HEALTH_OK", NumPGs: 100, PGsByState: map[string]int64{"active+clean": f.clean}}, nil
}
func (f *actCeph) OSDs(context.Context) ([]cephmgr.OSDState, error) { return slices.Clone(f.osds), nil }
func (f *actCeph) PoolSizes(context.Context) ([]cephmgr.Pool, error) {
	return []cephmgr.Pool{{Name: "replicapool", Size: 3, MinSize: 2}}, nil
}
func (f *actCeph) Flags(context.Context) ([]string, error) { return slices.Clone(f.flags), nil }
func (f *actCeph) record(c string) error {
	f.calls = append(f.calls, c)
	return f.sendErr
}
func (f *actCeph) MarkOut(_ context.Context, id int32) error {
	if err := f.record(fmt.Sprintf("out %d", id)); err != nil {
		return err
	}
	findOSD(f.osds, id).In = false
	return nil
}
func (f *actCeph) MarkIn(_ context.Context, id int32) error {
	if err := f.record(fmt.Sprintf("in %d", id)); err != nil {
		return err
	}
	findOSD(f.osds, id).In = true
	return nil
}
func (f *actCeph) Scrub(_ context.Context, id int32, deep bool) error {
	return f.record(fmt.Sprintf("scrub %d %t", id, deep))
}
func (f *actCeph) SetFlag(_ context.Context, flag string, on bool) (bool, error) {
	if f.raceFlag == flag && !slices.Contains(f.flags, flag) {
		f.flags = append(f.flags, flag)
	}
	was := slices.Contains(f.flags, flag)
	if err := f.record(fmt.Sprintf("flag %s %t", flag, on)); err != nil {
		return was, err
	}
	f.flags = slices.DeleteFunc(f.flags, func(s string) bool { return s == flag })
	if on {
		f.flags = append(f.flags, flag)
	}
	return was, nil
}

type actHarness struct {
	t    *testing.T
	c    client.Client
	r    *FrameCephActionReconciler
	ceph *actCeph
	now  time.Time
}

func newActHarness(t *testing.T) *actHarness {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := framev1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	one := int32(1)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "rook-ceph-osd-8", Namespace: "rook-ceph", Generation: 1},
		Spec:   appsv1.DeploymentSpec{Replicas: &one},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 1, AvailableReplicas: 1}}
	h := &actHarness{t: t, ceph: newActCeph(), now: time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)}
	h.c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(dep).
		WithStatusSubresource(&framev1beta1.FrameCephAction{}, &appsv1.Deployment{}).Build()
	h.r = &FrameCephActionReconciler{Client: h.c, Scheme: scheme,
		Ceph: func(context.Context) (CephActionOperator, error) { return h.ceph, nil },
		Now:  func() time.Time { return h.now }}
	return h
}

func (h *actHarness) create(name string, spec framev1beta1.FrameCephActionSpec) types.NamespacedName {
	h.t.Helper()
	a := &framev1beta1.FrameCephAction{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "frame-system", UID: types.UID("uid-" + name)}, Spec: spec}
	if err := h.c.Create(context.Background(), a); err != nil {
		h.t.Fatal(err)
	}
	return client.ObjectKeyFromObject(a)
}

func (h *actHarness) reconcile(k types.NamespacedName) ctrl.Result {
	h.t.Helper()
	res, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: k})
	if err != nil {
		h.t.Fatalf("reconcile: %v", err)
	}
	return res
}

func (h *actHarness) get(k types.NamespacedName) *framev1beta1.FrameCephAction {
	h.t.Helper()
	var a framev1beta1.FrameCephAction
	if err := h.c.Get(context.Background(), k, &a); err != nil {
		h.t.Fatal(err)
	}
	return &a
}

func (h *actHarness) expect(k types.NamespacedName, phase string) *framev1beta1.FrameCephAction {
	h.t.Helper()
	a := h.get(k)
	if a.Status.Phase != phase {
		h.t.Fatalf("%s: phase %s (%s), want %s", k.Name, a.Status.Phase, a.Status.Message, phase)
	}
	return a
}

func osdRef(id int32) *int32 { return new(id) }

func TestCephActionOutRefusedUnlessOKToStop(t *testing.T) {
	cases := map[string]func(*actCeph){
		"PGs not clean":           func(c *actCeph) { c.clean = 97 },
		"another host degraded":   func(c *actCeph) { findOSD(c.osds, 1).Up = false },
		"another OSD already out": func(c *actCeph) { findOSD(c.osds, 2).In = false },
		"norecover set":           func(c *actCeph) { c.flags = append(c.flags, "norecover") },
		"pause set":               func(c *actCeph) { c.flags = append(c.flags, "pauserd") },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			h := newActHarness(t)
			mut(h.ceph)
			k := h.create("out", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDOut, OSDID: osdRef(8)})
			h.reconcile(k)
			h.expect(k, framev1beta1.CephActionRefused)
			if len(h.ceph.calls) != 0 {
				t.Fatalf("sent %v", h.ceph.calls)
			}
		})
	}
}

func TestCephActionOutThenInAndOneOutAtATime(t *testing.T) {
	h := newActHarness(t)
	k := h.create("out", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDOut, OSDID: osdRef(8)})
	h.reconcile(k)
	a := h.expect(k, framev1beta1.CephActionDone)
	if a.Status.SentAt == nil || !slices.Equal(h.ceph.calls, []string{"out 8"}) {
		t.Fatalf("status %+v, calls %v", a.Status, h.ceph.calls)
	}
	// A second out while osd.8 is out is refused.
	k2 := h.create("out2", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDOut, OSDID: osdRef(2)})
	h.reconcile(k2)
	h.expect(k2, framev1beta1.CephActionRefused)
	// In is always allowed.
	k3 := h.create("in", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDIn, OSDID: osdRef(8)})
	h.reconcile(k3)
	h.expect(k3, framev1beta1.CephActionDone)
	if !slices.Equal(h.ceph.calls, []string{"out 8", "in 8"}) {
		t.Fatalf("calls %v", h.ceph.calls)
	}
}

func TestCephActionSentOnce(t *testing.T) {
	h := newActHarness(t)
	h.ceph.sendErr = errors.New("connection reset")
	k := h.create("scrub", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDDeepScrub, OSDID: osdRef(8)})
	h.reconcile(k)
	h.expect(k, framev1beta1.CephActionFailed)
	h.reconcile(k)
	if !slices.Equal(h.ceph.calls, []string{"scrub 8 true"}) {
		t.Fatalf("calls %v", h.ceph.calls)
	}
	// A crash between the record and the send: never sent afterwards.
	h2 := newActHarness(t)
	k2 := h2.create("in", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDIn, OSDID: osdRef(8)})
	a := h2.get(k2)
	at := metav1.NewTime(h2.now)
	a.Status.Phase, a.Status.SentAt = framev1beta1.CephActionPending, &at
	if err := h2.c.Status().Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	findOSD(h2.ceph.osds, 8).In = false
	h2.reconcile(k2)
	h2.expect(k2, framev1beta1.CephActionFailed)
	if len(h2.ceph.calls) != 0 {
		t.Fatalf("resent: %v", h2.ceph.calls)
	}
}

func TestCephActionRestart(t *testing.T) {
	h := newActHarness(t)
	k := h.create("restart", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDRestart, OSDID: osdRef(8)})
	h.reconcile(k)
	h.expect(k, framev1beta1.CephActionPending)
	var d appsv1.Deployment
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "rook-ceph", Name: "rook-ceph-osd-8"}, &d); err != nil {
		t.Fatal(err)
	}
	if d.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] == "" {
		t.Fatal("deployment not restarted")
	}
	// Rolling: not done yet.
	d.Generation = 2
	findOSD(h.ceph.osds, 8).Up = false
	if err := h.c.Update(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	h.reconcile(k)
	h.expect(k, framev1beta1.CephActionPending)
	// Rolled and up.
	d.Status.ObservedGeneration = d.Generation
	if err := h.c.Status().Update(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	findOSD(h.ceph.osds, 8).Up = true
	h.reconcile(k)
	h.expect(k, framev1beta1.CephActionDone)

	// Not ok to stop: nothing touched.
	h2 := newActHarness(t)
	h2.ceph.clean = 90
	k2 := h2.create("restart", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDRestart, OSDID: osdRef(8)})
	h2.reconcile(k2)
	h2.expect(k2, framev1beta1.CephActionRefused)
	var d2 appsv1.Deployment
	_ = h2.c.Get(context.Background(), types.NamespacedName{Namespace: "rook-ceph", Name: "rook-ceph-osd-8"}, &d2)
	if len(d2.Spec.Template.Annotations) != 0 {
		t.Fatal("restarted while not ok to stop")
	}
}

func TestCephActionFlagExpires(t *testing.T) {
	h := newActHarness(t)
	k := h.create("noout", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionSetFlag, Flag: "noout", DurationMinutes: 30})
	h.reconcile(k)
	a := h.expect(k, framev1beta1.CephActionActive)
	if !slices.Contains(h.ceph.flags, "noout") || !a.Status.ExpiresAt.Time.Equal(h.now.Add(30*time.Minute)) {
		t.Fatalf("flags %v, status %+v", h.ceph.flags, a.Status)
	}
	// A second holder for the same flag is refused.
	k2 := h.create("noout2", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionSetFlag, Flag: "noout"})
	h.reconcile(k2)
	h.expect(k2, framev1beta1.CephActionRefused)
	h.now = h.now.Add(29 * time.Minute)
	h.reconcile(k)
	h.expect(k, framev1beta1.CephActionActive)
	h.now = h.now.Add(2 * time.Minute)
	h.reconcile(k)
	h.expect(k, framev1beta1.CephActionDone)
	if slices.Contains(h.ceph.flags, "noout") {
		t.Fatalf("noout still set: %v", h.ceph.flags)
	}
}

func TestCephActionFlagReleaseAndHandSetFlag(t *testing.T) {
	h := newActHarness(t)
	k := h.create("nobackfill", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionSetFlag, Flag: "nobackfill"})
	h.reconcile(k)
	a := h.expect(k, framev1beta1.CephActionActive)
	base := a.DeepCopy()
	a.Spec.Release = true
	if err := h.c.Patch(context.Background(), a, client.MergeFrom(base)); err != nil {
		t.Fatal(err)
	}
	h.reconcile(k)
	h.expect(k, framev1beta1.CephActionDone)
	if slices.Contains(h.ceph.flags, "nobackfill") {
		t.Fatal("not unset on release")
	}

	// Set by hand before: left set at the end.
	h2 := newActHarness(t)
	h2.ceph.flags = append(h2.ceph.flags, "noscrub")
	k2 := h2.create("noscrub", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionSetFlag, Flag: "noscrub", DurationMinutes: 5})
	h2.reconcile(k2)
	h2.expect(k2, framev1beta1.CephActionActive)
	h2.now = h2.now.Add(6 * time.Minute)
	h2.reconcile(k2)
	a2 := h2.expect(k2, framev1beta1.CephActionDone)
	if !slices.Contains(h2.ceph.flags, "noscrub") || !strings.Contains(a2.Status.Message, "left set") {
		t.Fatalf("flags %v, message %q", h2.ceph.flags, a2.Status.Message)
	}
	for _, c := range h2.ceph.calls {
		if strings.HasPrefix(c, "flag noscrub") {
			t.Fatalf("touched a flag set by hand: %v", h2.ceph.calls)
		}
	}
}

func TestCephActionFlagUnsetIsRetried(t *testing.T) {
	h := newActHarness(t)
	k := h.create("noout", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionSetFlag, Flag: "noout", DurationMinutes: 5})
	h.reconcile(k)
	h.now = h.now.Add(6 * time.Minute)
	h.ceph.sendErr = errors.New("timeout")
	h.reconcile(k)
	h.expect(k, framev1beta1.CephActionActive)
	h.ceph.sendErr = nil
	h.reconcile(k)
	h.expect(k, framev1beta1.CephActionDone)
	if slices.Contains(h.ceph.flags, "noout") {
		t.Fatal("noout still set")
	}
}

func TestCephActionWaitsForOtherWork(t *testing.T) {
	ctx := context.Background()
	h := newActHarness(t)
	// A running replacement: only scrubs go out.
	rep := &framev1beta1.FrameOSDReplacement{ObjectMeta: metav1.ObjectMeta{Name: "osd-2", Namespace: "frame-system"},
		Status: framev1beta1.FrameOSDReplacementStatus{Phase: framev1beta1.OSDReplacementPaused}}
	if err := h.c.Create(ctx, rep); err != nil {
		t.Fatal(err)
	}
	for _, act := range []string{framev1beta1.CephActionOSDOut, framev1beta1.CephActionOSDIn, framev1beta1.CephActionOSDRestart} {
		k := h.create("during-"+strings.ToLower(act), framev1beta1.FrameCephActionSpec{Action: act, OSDID: osdRef(8)})
		h.reconcile(k)
		if a := h.expect(k, framev1beta1.CephActionRefused); !strings.Contains(a.Status.Message, "replacement") {
			t.Fatalf("%s: %q", act, a.Status.Message)
		}
	}
	kf := h.create("during-flag", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionSetFlag, Flag: "noout"})
	h.reconcile(kf)
	h.expect(kf, framev1beta1.CephActionRefused)
	ks := h.create("during-scrub", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDScrub, OSDID: osdRef(8)})
	h.reconcile(ks)
	h.expect(ks, framev1beta1.CephActionDone)
	if !slices.Equal(h.ceph.calls, []string{"scrub 8 false"}) {
		t.Fatalf("calls %v", h.ceph.calls)
	}
	rep.Status.Phase = framev1beta1.OSDReplacementSucceeded
	if err := h.c.Update(ctx, rep); err != nil {
		t.Fatal(err)
	}

	// A restart in flight holds back an out, and so does one that ended
	// less than cephActionSettle ago, even with every PG clean.
	kr := h.create("restart", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDRestart, OSDID: osdRef(8)})
	h.reconcile(kr)
	h.expect(kr, framev1beta1.CephActionPending)
	ko := h.create("out-in-flight", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDOut, OSDID: osdRef(2)})
	h.reconcile(ko)
	if a := h.expect(ko, framev1beta1.CephActionRefused); !strings.Contains(a.Status.Message, "in progress") {
		t.Fatalf("message %q", a.Status.Message)
	}
	h.reconcile(kr) // rolled (the fake deployment does not move) and up: done
	h.expect(kr, framev1beta1.CephActionDone)
	h.now = h.now.Add(5 * time.Minute)
	ko2 := h.create("out-too-soon", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDOut, OSDID: osdRef(2)})
	h.reconcile(ko2)
	if a := h.expect(ko2, framev1beta1.CephActionRefused); !strings.Contains(a.Status.Message, "settle") {
		t.Fatalf("message %q", a.Status.Message)
	}
	h.now = h.now.Add(6 * time.Minute)
	ko3 := h.create("out-later", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionOSDOut, OSDID: osdRef(2)})
	h.reconcile(ko3)
	h.expect(ko3, framev1beta1.CephActionDone)
}

func TestCephActionFlagFinalizer(t *testing.T) {
	ctx := context.Background()
	h := newActHarness(t)
	k := h.create("noout", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionSetFlag, Flag: "noout"})
	h.reconcile(k)
	a := h.expect(k, framev1beta1.CephActionActive)
	if !slices.Contains(a.Finalizers, cephActionFinalizer) {
		t.Fatalf("finalizers %v", a.Finalizers)
	}
	// Deleted while holding noout: unset, then gone.
	if err := h.c.Delete(ctx, a); err != nil {
		t.Fatal(err)
	}
	h.ceph.sendErr = errors.New("timeout")
	if _, err := h.r.Reconcile(ctx, ctrl.Request{NamespacedName: k}); err == nil {
		t.Fatal("finalizer dropped although the unset failed")
	}
	h.get(k)
	h.ceph.sendErr = nil
	h.reconcile(k)
	if slices.Contains(h.ceph.flags, "noout") {
		t.Fatal("noout left set by a deleted action")
	}
	if err := h.c.Get(ctx, k, &framev1beta1.FrameCephAction{}); err == nil {
		t.Fatal("object still there")
	}

	// Ended normally: the finalizer goes with it.
	k2 := h.create("noscrub", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionSetFlag, Flag: "noscrub", DurationMinutes: 5})
	h.reconcile(k2)
	h.now = h.now.Add(6 * time.Minute)
	h.reconcile(k2)
	if a2 := h.expect(k2, framev1beta1.CephActionDone); len(a2.Finalizers) != 0 {
		t.Fatalf("finalizers %v", a2.Finalizers)
	}
}

func TestCephActionFlagSetByOtherMeanwhileIsLeftSet(t *testing.T) {
	h := newActHarness(t)
	h.ceph.raceFlag = "norebalance"
	k := h.create("norebalance", framev1beta1.FrameCephActionSpec{Action: framev1beta1.CephActionSetFlag, Flag: "norebalance", DurationMinutes: 5})
	h.reconcile(k)
	if a := h.expect(k, framev1beta1.CephActionActive); !a.Status.FlagWasSet {
		t.Fatalf("status %+v", a.Status)
	}
	h.now = h.now.Add(6 * time.Minute)
	h.reconcile(k)
	h.expect(k, framev1beta1.CephActionDone)
	if !slices.Contains(h.ceph.flags, "norebalance") {
		t.Fatal("unset a flag someone else set")
	}
}
