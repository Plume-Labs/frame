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

package updater

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

const ns = "frame-system"

func deployment(namespace, name string, ready int32, containers ...corev1.Container) *appsv1.Deployment {
	one := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: containers}}},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: ready},
	}
}

func release(version string, verified bool, over func(*framev1beta1.FrameRelease)) *framev1beta1.FrameRelease {
	r := &framev1beta1.FrameRelease{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "r-" + strings.ReplaceAll(version, ".", "-")},
		Spec:       framev1beta1.FrameReleaseSpec{Version: version, Channel: "stable", Images: map[string]string{"ui": "ui@" + version, "uiproxy": "proxy@" + version}},
		Status:     framev1beta1.FrameReleaseStatus{Verified: verified},
	}
	if over != nil {
		over(r)
	}
	return r
}

func updateReq(name string, ts time.Time, spec framev1beta1.FrameUpdateSpec, status framev1beta1.FrameUpdateStatus) *framev1beta1.FrameUpdate {
	return &framev1beta1.FrameUpdate{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, CreationTimestamp: metav1.NewTime(ts)},
		Spec:       spec, Status: status,
	}
}

type recFixture struct {
	r *Reconciler
	c client.Client
}

// newFixture runs 1.0.0 (ui, uiproxy in one Deployment), with the given extra objects.
func newRecFixture(t *testing.T, uiReady int32, extra ...client.Object) recFixture {
	t.Helper()
	s := runtime.NewScheme()
	_ = appsv1.AddToScheme(s)
	_ = framev1beta1.AddToScheme(s)
	ui := deployment("cluster-control", "cluster-control-ui", uiReady,
		corev1.Container{Name: "ui", Image: "ui@1.0.0"}, corev1.Container{Name: "uiproxy", Image: "proxy@1.0.0"})
	objs := append([]client.Object{ui}, extra...)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(ui, &framev1beta1.FrameUpdate{}, &framev1beta1.FrameRelease{}).Build()
	w := map[string]Workload{
		"uiproxy": DefaultWorkloads()["uiproxy"],
		"ui":      DefaultWorkloads()["ui"],
	}
	k := &KubeCluster{Client: c, Workloads: w, ReadyTimeout: 100 * time.Millisecond, PollEvery: 5 * time.Millisecond}
	return recFixture{c: c, r: &Reconciler{Client: c, Cluster: k, Namespace: ns, Now: func() time.Time { return time.Unix(1700000000, 0) }}}
}

func (f recFixture) status(t *testing.T) framev1beta1.FrameUpdateStatus {
	t.Helper()
	var u framev1beta1.FrameUpdate
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "u1"}, &u); err != nil {
		t.Fatal(err)
	}
	return u.Status
}

func (f recFixture) uiImage(t *testing.T) string {
	t.Helper()
	var d appsv1.Deployment
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "cluster-control", Name: "cluster-control-ui"}, &d); err != nil {
		t.Fatal(err)
	}
	return d.Spec.Template.Spec.Containers[0].Image + "," + d.Spec.Template.Spec.Containers[1].Image
}

var t0 = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

func TestReconcileAppliesAVerifiedUpdate(t *testing.T) {
	f := newRecFixture(t, 1, release("1.0.0", true, nil), release("1.1.0", true, nil),
		updateReq("u1", t0, framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"}, framev1beta1.FrameUpdateStatus{}))
	if _, err := f.r.Reconcile(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	st := f.status(t)
	if st.Phase != framev1beta1.UpdatePhaseSucceeded || st.FromVersion != "1.0.0" {
		t.Fatalf("status = %+v", st)
	}
	if got := f.uiImage(t); got != "ui@1.1.0,proxy@1.1.0" {
		t.Fatalf("images = %s", got)
	}
	for _, c := range st.Components {
		if c.State != "Ready" {
			t.Fatalf("component %+v", c)
		}
	}
}

func TestReconcileRefusesAnUnverifiedReleaseAndTouchesNothing(t *testing.T) {
	f := newRecFixture(t, 1, release("1.0.0", true, nil), release("1.1.0", false, nil),
		updateReq("u1", t0, framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0", Force: true}, framev1beta1.FrameUpdateStatus{}))
	if _, err := f.r.Reconcile(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	st := f.status(t)
	if st.Phase != framev1beta1.UpdatePhaseRefused || !strings.Contains(st.Message, RefusedUnverified) {
		t.Fatalf("status = %+v", st)
	}
	if got := f.uiImage(t); got != "ui@1.0.0,proxy@1.0.0" {
		t.Fatalf("a refused update changed %s", got)
	}
}

func TestReconcileRevertsWhenTheHealthGateFails(t *testing.T) {
	// The Deployment never becomes ready (fake clusters do not roll out).
	f := newRecFixture(t, 0, release("1.0.0", true, nil), release("1.1.0", true, nil),
		updateReq("u1", t0, framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0", Force: true}, framev1beta1.FrameUpdateStatus{}))
	if _, err := f.r.Reconcile(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	st := f.status(t)
	// The revert cannot get healthy either, so the honest outcome is Failed with a message.
	if st.Phase != framev1beta1.UpdatePhaseFailed || !strings.Contains(st.Message, "manual intervention") {
		t.Fatalf("status = %+v", st)
	}
	if got := f.uiImage(t); got != "ui@1.0.0,proxy@1.0.0" {
		t.Fatalf("images were not put back: %s", got)
	}
}

func TestReconcileWaitsBehindAnUpdateInProgress(t *testing.T) {
	f := newRecFixture(t, 1, release("1.0.0", true, nil), release("1.1.0", true, nil),
		updateReq("u0", t0, framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"}, framev1beta1.FrameUpdateStatus{Phase: framev1beta1.UpdatePhaseApplying}),
		updateReq("u1", t0.Add(time.Minute), framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"}, framev1beta1.FrameUpdateStatus{}))
	after, err := f.r.Reconcile(context.Background(), "u1")
	if err != nil || after == 0 {
		t.Fatalf("after=%v err=%v", after, err)
	}
	if f.status(t).Phase != "" {
		t.Fatal("a queued request was started")
	}
}

func TestReconcileServesTheOldestRequestFirst(t *testing.T) {
	f := newRecFixture(t, 1, release("1.0.0", true, nil), release("1.1.0", true, nil),
		updateReq("u0", t0, framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"}, framev1beta1.FrameUpdateStatus{}),
		updateReq("u1", t0.Add(time.Minute), framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"}, framev1beta1.FrameUpdateStatus{}))
	after, _ := f.r.Reconcile(context.Background(), "u1")
	if after == 0 || f.status(t).Phase != "" {
		t.Fatal("the newer request jumped the queue")
	}
}

func TestReconcileLeavesDecidedRequestsAlone(t *testing.T) {
	f := newRecFixture(t, 1, release("1.0.0", true, nil), release("1.1.0", true, nil),
		updateReq("u1", t0, framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"}, framev1beta1.FrameUpdateStatus{Phase: framev1beta1.UpdatePhaseRefused, Message: "x"}))
	if _, err := f.r.Reconcile(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if f.uiImage(t) != "ui@1.0.0,proxy@1.0.0" || f.status(t).Message != "x" {
		t.Fatal("a decided request was acted on again")
	}
}

func TestReconcileRollsBackToThePreviousVersion(t *testing.T) {
	// 1.0.0 -> 1.1.0 succeeded earlier; the cluster now runs 1.1.0.
	f := newRecFixture(t, 1,
		release("1.0.0", true, nil), release("1.1.0", true, func(r *framev1beta1.FrameRelease) { r.Spec.RollbackSafeTo = "1.0.0" }),
		updateReq("u0", t0, framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"}, framev1beta1.FrameUpdateStatus{Phase: framev1beta1.UpdatePhaseSucceeded, FromVersion: "1.0.0"}),
		updateReq("u1", t0.Add(time.Hour), framev1beta1.FrameUpdateSpec{Rollback: true}, framev1beta1.FrameUpdateStatus{}))
	// Put the cluster on 1.1.0 first.
	var d appsv1.Deployment
	_ = f.c.Get(context.Background(), client.ObjectKey{Namespace: "cluster-control", Name: "cluster-control-ui"}, &d)
	d.Spec.Template.Spec.Containers[0].Image, d.Spec.Template.Spec.Containers[1].Image = "ui@1.1.0", "proxy@1.1.0"
	if err := f.c.Update(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.Reconcile(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if st := f.status(t); st.Phase != framev1beta1.UpdatePhaseSucceeded {
		t.Fatalf("status = %+v", st)
	}
	if got := f.uiImage(t); got != "ui@1.0.0,proxy@1.0.0" {
		t.Fatalf("images = %s", got)
	}
}

func TestDetectCurrentDoesNotGuessBetweenEqualMatches(t *testing.T) {
	rels := map[string]Release{
		"1.1.0": {Images: map[string]string{"ui": "ui@b"}},
		"1.2.0": {Images: map[string]string{"ui": "ui@b"}},
	}
	if v := DetectCurrent([]Running{{Component: "ui", Image: "ui@b"}}, rels); v != "" {
		t.Fatalf("guessed %q", v)
	}
	rels["1.2.0"] = Release{Images: map[string]string{"ui": "ui@b", "uiproxy": "p@c"}}
	if v := DetectCurrent([]Running{{Component: "ui", Image: "ui@b"}, {Component: "uiproxy", Image: "p@c"}}, rels); v != "1.2.0" {
		t.Fatalf("got %q", v)
	}
}

func TestReconcileResumesAnInterruptedUpdate(t *testing.T) {
	// ui was done, uiproxy not: the journal says so and the cluster agrees.
	f := newRecFixture(t, 1, release("1.0.0", true, nil), release("1.1.0", true, nil),
		updateReq("u1", t0, framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"}, framev1beta1.FrameUpdateStatus{
			Phase: framev1beta1.UpdatePhaseApplying, FromVersion: "1.0.0",
			Components: []framev1beta1.UpdateComponentStatus{
				{Name: "uiproxy", From: "proxy@1.0.0", To: "proxy@1.1.0", State: "Applying"},
				{Name: "ui", From: "ui@1.0.0", To: "ui@1.1.0", State: "Ready"},
			},
		}))
	if _, err := f.r.Reconcile(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if st := f.status(t); st.Phase != framev1beta1.UpdatePhaseSucceeded {
		t.Fatalf("status = %+v", st)
	}
	if got := f.uiImage(t); got != "ui@1.0.0,proxy@1.1.0" {
		// ui was journalled Ready, so it is not touched again.
		t.Fatalf("images = %s", got)
	}
}

func TestReconcileDoesNotReportRefusedWhenAnInterruptedUpdateCannotResume(t *testing.T) {
	// The target lost its verification during the restart.
	f := newRecFixture(t, 1, release("1.0.0", true, nil), release("1.1.0", false, nil),
		updateReq("u1", t0, framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"}, framev1beta1.FrameUpdateStatus{
			Phase: framev1beta1.UpdatePhaseApplying, FromVersion: "1.0.0",
		}))
	if _, err := f.r.Reconcile(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	st := f.status(t)
	if st.Phase != framev1beta1.UpdatePhaseFailed || !strings.Contains(st.Message, "by hand") {
		t.Fatalf("status = %+v", st)
	}
}

func TestReconcileFailsHonestlyAfterAnInterruptedRevert(t *testing.T) {
	f := newRecFixture(t, 1, release("1.0.0", true, nil), release("1.1.0", true, nil),
		updateReq("u1", t0, framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"}, framev1beta1.FrameUpdateStatus{Phase: framev1beta1.UpdatePhaseRollingBack}))
	if _, err := f.r.Reconcile(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if st := f.status(t); st.Phase != framev1beta1.UpdatePhaseFailed {
		t.Fatalf("status = %+v", st)
	}
}

func TestReconcileLeavesAnInterruptedRunForTheNextStart(t *testing.T) {
	f := newRecFixture(t, 1, release("1.0.0", true, nil), release("1.1.0", true, nil),
		updateReq("u1", t0, framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"}, framev1beta1.FrameUpdateStatus{}))
	ctx, cancel := context.WithCancel(context.Background())
	f.r.Cluster = &stopsOnSetImage{WorkCluster: f.r.Cluster, stop: cancel} // the updater is being stopped
	if _, err := f.r.Reconcile(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if st := f.status(t); st.Phase != framev1beta1.UpdatePhaseApplying {
		t.Fatalf("an interrupted update was decided: %+v", st)
	}
}

// stopsOnSetImage cancels the context once the first image is patched, and
// waits like the real cluster does: by looking at the context.
type stopsOnSetImage struct {
	WorkCluster
	stop func()
}

func (s *stopsOnSetImage) SetImage(ctx context.Context, c, img string) error {
	err := s.WorkCluster.SetImage(ctx, c, img)
	s.stop()
	return err
}

func (s *stopsOnSetImage) WaitReady(ctx context.Context, c string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.WorkCluster.WaitReady(ctx, c)
}

const newUpdaterImage = "ghcr.io/plume-labs/hearthrack-updater@sha256:2222222222222222222222222222222222222222222222222222222222222222"

func TestReconcileAsksTheOperatorToApplyTheUpdaterImage(t *testing.T) {
	withUpdater := func(r *framev1beta1.FrameRelease) { r.Spec.Images[UpdaterComponent] = newUpdaterImage }
	f := newRecFixture(t, 1, release("1.0.0", true, nil), release("1.1.0", true, withUpdater),
		updateReq("u1", t0, framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"}, framev1beta1.FrameUpdateStatus{}))
	if _, err := f.r.Reconcile(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	st := f.status(t)
	if st.Phase != framev1beta1.UpdatePhaseSucceeded {
		t.Fatalf("status = %+v", st)
	}
	if st.Updater == nil || st.Updater.To != newUpdaterImage || st.Updater.State != framev1beta1.UpdaterPending {
		t.Fatalf("updater request = %+v", st.Updater)
	}
	// The updater is asked for, never patched by the updater itself.
	if got := f.uiImage(t); got != "ui@1.1.0,proxy@1.1.0" {
		t.Fatalf("images = %s", got)
	}
}

func TestReconcileAsksNothingWhenTheReleaseHasNoUpdaterImage(t *testing.T) {
	f := newRecFixture(t, 1, release("1.0.0", true, nil), release("1.1.0", true, nil),
		updateReq("u1", t0, framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"}, framev1beta1.FrameUpdateStatus{}))
	if _, err := f.r.Reconcile(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if st := f.status(t); st.Updater != nil {
		t.Fatalf("updater request = %+v", st.Updater)
	}
}
