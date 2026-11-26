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

// Package selfupdate plays the two halves of an update against a real API
// server (envtest): the updater applies every component but itself, and the
// operator applies the updater, watches it, and puts the previous image back
// when it never gets ready (spec section 4.1, cross supervision). The CRDs, the
// status subresource and the CEL rules are the real ones; what is stood in for
// is the kubelet, which marks a Deployment rolled out, or not.
//
// It is not a Kind run: no image is pulled and no pod starts. What it proves is
// the choreography of the two reconcilers on the objects they share.
package selfupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	controller "github.com/Plume-Labs/frame/internal/controller/frame"
	"github.com/Plume-Labs/frame/internal/updater"
)

const digestFmt = "registry.invalid/hearthrack-%s@sha256:%064d"

func image(name string, n int) string { return fmt.Sprintf(digestFmt, name, n) }

var (
	admin client.Client
	seq   atomic.Int64
)

func TestMain(m *testing.M) {
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	env := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join(root, "config", "crd", "bases", "frame.plume-labs.io_frameupdates.yaml"),
			filepath.Join(root, "config", "crd", "bases", "frame.plume-labs.io_framereleases.yaml"),
		},
		ErrorIfCRDPathMissing: true,
	}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if matches, _ := filepath.Glob(filepath.Join(root, "bin", "k8s", "*")); len(matches) > 0 {
			env.BinaryAssetsDirectory = matches[0]
		}
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "envtest start failed: %v (run `make setup-envtest` or `make test`)\n", err)
		os.Exit(1)
	}
	_ = framev1beta1.AddToScheme(scheme.Scheme)
	code := 1
	if admin, err = client.New(cfg, client.Options{Scheme: scheme.Scheme}); err == nil {
		code = m.Run()
	} else {
		fmt.Fprintln(os.Stderr, err)
	}
	_ = env.Stop()
	os.Exit(code)
}

// world is one install in its own namespace: the operator and the updater
// Deployments, the two releases, and the two reconcilers.
type world struct {
	t   *testing.T
	ns  string
	upd *updater.Reconciler
	op  *controller.FrameUpdateUpdaterReconciler

	mu     sync.Mutex
	broken map[string]bool // Deployments the kubelet never rolls out
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	w := &world{t: t, ns: fmt.Sprintf("su-%d", seq.Add(1)), broken: map[string]bool{}}
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: w.ns}}); err != nil {
		t.Fatal(err)
	}
	w.deployment("operator", "manager", image("controller", 1))
	w.deployment("frame-updater", "updater", image("updater", 1))
	w.release("1.0.0", image("controller", 1), image("updater", 1))
	w.release("1.1.0", image("controller", 2), image("updater", 2), "1.0.0")

	workloads := map[string]updater.Workload{
		"controller": {Kind: "Deployment", Namespace: w.ns, Name: "operator", Container: "manager"},
	}
	w.upd = &updater.Reconciler{
		Client:    admin,
		Namespace: w.ns,
		Cluster: &updater.KubeCluster{Client: admin, Workloads: workloads,
			ReadyTimeout: 20 * time.Second, PollEvery: 50 * time.Millisecond},
	}
	w.op = &controller.FrameUpdateUpdaterReconciler{
		Client: admin, Namespace: w.ns,
		ReadyDeadline: 3 * time.Second, StableFor: 300 * time.Millisecond, Poll: 100 * time.Millisecond,
	}
	return w
}

func (w *world) deployment(name, container, img string) {
	w.t.Helper()
	one := int32(1)
	labels := map[string]string{"app": name}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: w.ns, Name: name},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: container, Image: img}}},
			},
		},
	}
	if err := admin.Create(context.Background(), d); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) release(version, controllerImg, updaterImg string, rollbackSafeTo ...string) {
	w.t.Helper()
	r := &framev1beta1.FrameRelease{
		ObjectMeta: metav1.ObjectMeta{Namespace: w.ns, Name: "r-" + version},
		Spec: framev1beta1.FrameReleaseSpec{Version: version, Channel: "stable",
			Images: map[string]string{"controller": controllerImg, updater.UpdaterComponent: updaterImg}},
	}
	if len(rollbackSafeTo) > 0 {
		r.Spec.RollbackSafeTo = rollbackSafeTo[0]
	}
	ctx := context.Background()
	if err := admin.Create(ctx, r); err != nil {
		w.t.Fatal(err)
	}
	// Only the signature check writes this; the test plays it.
	r.Status.Verified = true
	if err := admin.Status().Update(ctx, r); err != nil {
		w.t.Fatal(err)
	}
}

// kubelet marks every Deployment rolled out, as a node would, except the ones
// named in broken, until ctx ends.
func (w *world) kubelet(ctx context.Context) {
	go func() {
		for ctx.Err() == nil {
			var l appsv1.DeploymentList
			if err := admin.List(ctx, &l, client.InNamespace(w.ns)); err == nil {
				for i := range l.Items {
					d := &l.Items[i]
					w.mu.Lock()
					broken := w.broken[d.Name]
					w.mu.Unlock()
					ready := int32(1)
					if broken && d.Spec.Template.Spec.Containers[0].Image != image("updater", 1) {
						ready = 0
					}
					st := &d.Status
					if st.ObservedGeneration != d.Generation || st.ReadyReplicas != ready || st.UpdatedReplicas != 1 {
						st.ObservedGeneration, st.Replicas, st.UpdatedReplicas = d.Generation, 1, 1
						st.ReadyReplicas, st.AvailableReplicas = ready, ready
						_ = admin.Status().Update(ctx, d)
					}
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
}

func (w *world) breakDeployment(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.broken[name] = true
}

// request creates a FrameUpdate and runs both reconcilers until it ends.
func (w *world) request(name string, spec framev1beta1.FrameUpdateSpec) framev1beta1.FrameUpdateStatus {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	w.waitRolledOut(ctx, "operator")
	u := &framev1beta1.FrameUpdate{ObjectMeta: metav1.ObjectMeta{Namespace: w.ns, Name: name}, Spec: spec}
	if err := admin.Create(ctx, u); err != nil {
		w.t.Fatal(err)
	}
	req := ctrl.Request{}
	req.Namespace, req.Name = w.ns, name
	for ctx.Err() == nil {
		if _, err := w.upd.Reconcile(ctx, name); err != nil {
			w.t.Fatalf("updater: %v", err)
		}
		if _, err := w.op.Reconcile(ctx, req); err != nil {
			w.t.Fatalf("operator: %v", err)
		}
		var cur framev1beta1.FrameUpdate
		if err := admin.Get(ctx, client.ObjectKeyFromObject(u), &cur); err != nil {
			w.t.Fatal(err)
		}
		switch cur.Status.Phase {
		case framev1beta1.UpdatePhaseSucceeded, framev1beta1.UpdatePhaseRolledBack,
			framev1beta1.UpdatePhaseFailed, framev1beta1.UpdatePhaseRefused:
			return cur.Status
		}
		time.Sleep(100 * time.Millisecond)
	}
	w.t.Fatalf("update %s did not end", name)
	return framev1beta1.FrameUpdateStatus{}
}

// waitRolledOut waits for the stand-in kubelet to have reported a Deployment
// ready: an update is refused while a component is not.
func (w *world) waitRolledOut(ctx context.Context, name string) {
	w.t.Helper()
	for ctx.Err() == nil {
		var d appsv1.Deployment
		if err := admin.Get(ctx, client.ObjectKey{Namespace: w.ns, Name: name}, &d); err == nil &&
			d.Status.ReadyReplicas == 1 && d.Status.ObservedGeneration >= d.Generation {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	w.t.Fatalf("%s never reported ready", name)
}

func (w *world) image(deployment string) string {
	w.t.Helper()
	var d appsv1.Deployment
	if err := admin.Get(context.Background(), client.ObjectKey{Namespace: w.ns, Name: deployment}, &d); err != nil {
		w.t.Fatal(err)
	}
	return d.Spec.Template.Spec.Containers[0].Image
}

func TestUpdateThenRollbackLeavesTheUpdaterAlone(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.kubelet(ctx)

	st := w.request("up", framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"})
	if st.Phase != framev1beta1.UpdatePhaseSucceeded || st.Message != "" {
		t.Fatalf("update: %+v", st)
	}
	if st.Updater == nil || st.Updater.State != framev1beta1.UpdaterReady {
		t.Fatalf("updater: %+v", st.Updater)
	}
	if w.image("operator") != image("controller", 2) || w.image("frame-updater") != image("updater", 2) {
		t.Fatalf("images: operator %s, updater %s", w.image("operator"), w.image("frame-updater"))
	}

	// Going back returns the components the updater applies, and leaves the
	// updater at the newer image: the request names no version to check one
	// against, and the newer updater reads what the older components wrote.
	st = w.request("back", framev1beta1.FrameUpdateSpec{Rollback: true})
	if st.Phase != framev1beta1.UpdatePhaseSucceeded || st.Updater != nil {
		t.Fatalf("rollback: %+v", st)
	}
	if w.image("operator") != image("controller", 1) || w.image("frame-updater") != image("updater", 2) {
		t.Fatalf("images: operator %s, updater %s", w.image("operator"), w.image("frame-updater"))
	}
}

func TestAnUpdaterThatNeverGetsReadyIsRolledBackAndTheUpdateStillEnds(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.kubelet(ctx)
	w.breakDeployment("frame-updater")

	st := w.request("up", framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"})
	// The other components are updated: that is a success, with the reason.
	if st.Phase != framev1beta1.UpdatePhaseSucceeded || st.Message == "" {
		t.Fatalf("update: %+v", st)
	}
	if st.Updater == nil || st.Updater.State != framev1beta1.UpdaterRolledBack {
		t.Fatalf("updater: %+v", st.Updater)
	}
	if w.image("operator") != image("controller", 2) {
		t.Fatalf("the operator should have been updated: %s", w.image("operator"))
	}
	if w.image("frame-updater") != image("updater", 1) {
		t.Fatalf("the previous updater should be back: %s", w.image("frame-updater"))
	}
}

func TestAnImageNoVerifiedReleaseNamesIsNeverInstalled(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.kubelet(ctx)

	// Someone with the updater's token writes a request for an image of their own.
	u := &framev1beta1.FrameUpdate{
		ObjectMeta: metav1.ObjectMeta{Namespace: w.ns, Name: "forged"},
		Spec:       framev1beta1.FrameUpdateSpec{TargetVersion: "1.1.0"},
	}
	if err := admin.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	u.Status.Phase = framev1beta1.UpdatePhaseApplying
	u.Status.Updater = &framev1beta1.UpdaterStatus{To: image("updater", 99), State: framev1beta1.UpdaterPending}
	if err := admin.Status().Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	req := ctrl.Request{}
	req.Namespace, req.Name = w.ns, "forged"
	if _, err := w.op.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	var cur framev1beta1.FrameUpdate
	if err := admin.Get(ctx, client.ObjectKeyFromObject(u), &cur); err != nil {
		t.Fatal(err)
	}
	if cur.Status.Updater.State != framev1beta1.UpdaterFailed || w.image("frame-updater") != image("updater", 1) {
		t.Fatalf("%+v, updater image %s", cur.Status.Updater, w.image("frame-updater"))
	}
}
