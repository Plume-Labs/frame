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
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

const (
	oldUpdaterImage = "ghcr.io/plume-labs/hearthrack-updater@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	newUpdaterImage = "ghcr.io/plume-labs/hearthrack-updater@sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

type selfFixture struct {
	r   *FrameUpdateUpdaterReconciler
	c   client.Client
	now time.Time
}

func newSelfFixture(t *testing.T, ready int32, st *framev1beta1.UpdaterStatus) *selfFixture {
	t.Helper()
	s := runtime.NewScheme()
	_ = appsv1.AddToScheme(s)
	_ = framev1beta1.AddToScheme(s)
	one := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "frame-system", Name: "frame-updater", Generation: 1},
		Spec: appsv1.DeploymentSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "updater", Image: oldUpdaterImage}}}}},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: ready, AvailableReplicas: ready},
	}
	rel := &framev1beta1.FrameRelease{
		ObjectMeta: metav1.ObjectMeta{Namespace: "frame-system", Name: "r-1-1-0"},
		Spec: framev1beta1.FrameReleaseSpec{Version: "1.1.0", Channel: "stable",
			Images: map[string]string{"updater": newUpdaterImage}},
		Status: framev1beta1.FrameReleaseStatus{Verified: true},
	}
	upd := &framev1beta1.FrameUpdate{
		ObjectMeta: metav1.ObjectMeta{Namespace: "frame-system", Name: "u1"},
		Spec:       framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"},
		Status:     framev1beta1.FrameUpdateStatus{Phase: framev1beta1.UpdatePhaseSucceeded, Updater: st},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(dep, upd, rel).
		WithStatusSubresource(upd, rel).Build()
	f := &selfFixture{c: c, now: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)}
	f.r = &FrameUpdateUpdaterReconciler{Client: c, Namespace: "frame-system", Now: func() time.Time { return f.now }}
	return f
}

func (f *selfFixture) step(t *testing.T) ctrl.Result {
	t.Helper()
	res, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: "frame-system", Name: "u1"}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (f *selfFixture) state(t *testing.T) (*framev1beta1.UpdaterStatus, string) {
	t.Helper()
	var u framev1beta1.FrameUpdate
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "frame-system", Name: "u1"}, &u); err != nil {
		t.Fatal(err)
	}
	var d appsv1.Deployment
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "frame-system", Name: "frame-updater"}, &d); err != nil {
		t.Fatal(err)
	}
	return u.Status.Updater, d.Spec.Template.Spec.Containers[0].Image
}

func TestUpdaterSelfUpdateAppliesThenWaitsThenReports(t *testing.T) {
	f := newSelfFixture(t, 0, &framev1beta1.UpdaterStatus{To: newUpdaterImage, State: framev1beta1.UpdaterPending})
	if res := f.step(t); res.RequeueAfter == 0 {
		t.Fatal("it must look again while the new updater starts")
	}
	st, image := f.state(t)
	if image != newUpdaterImage || st.State != framev1beta1.UpdaterApplying || st.From != oldUpdaterImage {
		t.Fatalf("after applying: %+v image=%s", st, image)
	}

	// Not ready yet, deadline far: keep waiting.
	f.now = f.now.Add(time.Minute)
	f.step(t)
	if st, _ = f.state(t); st.State != framev1beta1.UpdaterApplying {
		t.Fatalf("state = %+v", st)
	}

	// Ready: done, and the previous image is still on record.
	var d appsv1.Deployment
	_ = f.c.Get(context.Background(), client.ObjectKey{Namespace: "frame-system", Name: "frame-updater"}, &d)
	d.Status.ReadyReplicas, d.Status.AvailableReplicas = 1, 1
	if err := f.c.Status().Update(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	// First sight of it ready starts the stability window; ready is not enough.
	f.step(t)
	if st, _ = f.state(t); st.State != framev1beta1.UpdaterApplying || st.ReadySince == nil {
		t.Fatalf("not yet stable: %+v", st)
	}
	f.now = f.now.Add(updaterStableFor + time.Second)
	f.step(t)
	if st, image = f.state(t); st.State != framev1beta1.UpdaterReady || image != newUpdaterImage || st.From != oldUpdaterImage {
		t.Fatalf("final: %+v image=%s", st, image)
	}
}

func TestUpdaterSelfUpdatePutsThePreviousImageBackWhenItNeverGetsReady(t *testing.T) {
	f := newSelfFixture(t, 0, &framev1beta1.UpdaterStatus{To: newUpdaterImage, State: framev1beta1.UpdaterPending})
	f.step(t)
	f.now = f.now.Add(updaterReadyDeadline + time.Second)
	f.step(t)
	if st, _ := f.state(t); st.State != framev1beta1.UpdaterRollingBack {
		t.Fatalf("the intent to roll back is journalled first: %+v", st)
	}
	f.step(t)
	st, image := f.state(t)
	if st.State != framev1beta1.UpdaterRolledBack || image != oldUpdaterImage {
		t.Fatalf("after the deadline: %+v image=%s", st, image)
	}
	// A decided request is left alone.
	f.step(t)
	if _, image = f.state(t); image != oldUpdaterImage {
		t.Fatalf("image changed again: %s", image)
	}
}

func TestUpdaterSelfUpdateResumesAnApplyCutShortByARestart(t *testing.T) {
	// The journal says Applying, but the image was never changed.
	since := metav1.NewTime(time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC))
	f := newSelfFixture(t, 0, &framev1beta1.UpdaterStatus{
		To: newUpdaterImage, From: oldUpdaterImage, State: framev1beta1.UpdaterApplying, Since: &since})
	f.step(t)
	if _, image := f.state(t); image != newUpdaterImage {
		t.Fatalf("image = %s", image)
	}
}

func TestUpdaterSelfUpdateRefusesAnImageThatIsNotADigest(t *testing.T) {
	f := newSelfFixture(t, 1, &framev1beta1.UpdaterStatus{To: "ghcr.io/plume-labs/hearthrack-updater:latest", State: framev1beta1.UpdaterPending})
	f.step(t)
	st, image := f.state(t)
	if st.State != framev1beta1.UpdaterFailed || image != oldUpdaterImage {
		t.Fatalf("%+v image=%s", st, image)
	}
}

func TestUpdaterSelfUpdateIgnoresOtherNamespacesAndRequestsWithoutAnUpdater(t *testing.T) {
	f := newSelfFixture(t, 1, nil)
	f.step(t)
	f.r.Namespace = "elsewhere"
	f.step(t)
	if _, image := f.state(t); image != oldUpdaterImage {
		t.Fatalf("image = %s", image)
	}
}

func TestUpdaterSelfUpdateRollsBackToATagImage(t *testing.T) {
	// The very first self-update of an install: the Deployment ships with a tag.
	f := newSelfFixture(t, 0, &framev1beta1.UpdaterStatus{To: newUpdaterImage, State: framev1beta1.UpdaterPending})
	var d appsv1.Deployment
	_ = f.c.Get(context.Background(), client.ObjectKey{Namespace: "frame-system", Name: "frame-updater"}, &d)
	d.Spec.Template.Spec.Containers[0].Image = "frame-updater:latest"
	if err := f.c.Update(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	f.now = f.now.Add(updaterReadyDeadline + time.Second)
	f.step(t)
	f.step(t)
	if st, image := f.state(t); st.State != framev1beta1.UpdaterRolledBack || image != "frame-updater:latest" {
		t.Fatalf("%+v image=%s", st, image)
	}
}

func TestUpdaterSelfUpdateDoesNotFightSomeoneElseChangingTheImage(t *testing.T) {
	since := metav1.NewTime(time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC))
	f := newSelfFixture(t, 0, &framev1beta1.UpdaterStatus{
		To: newUpdaterImage, From: "frame-updater:old", State: framev1beta1.UpdaterApplying, Since: &since})
	f.step(t) // the Deployment holds oldUpdaterImage, which is neither From nor To
	st, image := f.state(t)
	if st.State != framev1beta1.UpdaterFailed || image != oldUpdaterImage {
		t.Fatalf("%+v image=%s", st, image)
	}
}

func TestUpdaterSelfUpdateRefusesAnImageNoVerifiedReleaseNames(t *testing.T) {
	other := "ghcr.io/plume-labs/hearthrack-updater@sha256:3333333333333333333333333333333333333333333333333333333333333333"
	f := newSelfFixture(t, 1, &framev1beta1.UpdaterStatus{To: other, State: framev1beta1.UpdaterPending})
	f.step(t)
	st, image := f.state(t)
	if st.State != framev1beta1.UpdaterFailed || image != oldUpdaterImage {
		t.Fatalf("%+v image=%s", st, image)
	}
}

func TestUpdaterSelfUpdateOnlyTheNewestRequestActs(t *testing.T) {
	f := newSelfFixture(t, 0, &framev1beta1.UpdaterStatus{To: newUpdaterImage, State: framev1beta1.UpdaterPending})
	later := &framev1beta1.FrameUpdate{
		ObjectMeta: metav1.ObjectMeta{Namespace: "frame-system", Name: "u2", CreationTimestamp: metav1.NewTime(f.now.Add(time.Hour))},
		Status:     framev1beta1.FrameUpdateStatus{Updater: &framev1beta1.UpdaterStatus{To: newUpdaterImage, State: framev1beta1.UpdaterPending}},
	}
	if err := f.c.Create(context.Background(), later); err != nil {
		t.Fatal(err)
	}
	f.step(t)
	st, image := f.state(t)
	if st.State != framev1beta1.UpdaterFailed || image != oldUpdaterImage {
		t.Fatalf("%+v image=%s", st, image)
	}
}
