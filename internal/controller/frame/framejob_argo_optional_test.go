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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// Argo Workflows is optional: the manager must start without its CRD, and a
// pipeline job must say why it cannot run instead of looping on an error.

func TestArgoWorkflowsServed(t *testing.T) {
	served, err := argoWorkflowsServed(meta.NewDefaultRESTMapper(nil))
	if err != nil || served {
		t.Fatalf("a mapper without the Workflow kind: served=%v err=%v, want false/nil", served, err)
	}
	withArgo := meta.NewDefaultRESTMapper(nil)
	withArgo.Add(argoWorkflowGVK, meta.RESTScopeNamespace)
	if served, err := argoWorkflowsServed(withArgo); err != nil || !served {
		t.Fatalf("a mapper that knows the Workflow kind: served=%v err=%v, want true/nil", served, err)
	}
}

// failingMapper stands in for an unreachable API server during discovery.
type failingMapper struct{ meta.RESTMapper }

func (failingMapper) RESTMapping(_ schema.GroupKind, _ ...string) (*meta.RESTMapping, error) {
	return nil, errors.New("connection refused")
}

func TestArgoDiscoveryErrorIsNotTreatedAsAbsent(t *testing.T) {
	served, err := argoWorkflowsServed(failingMapper{})
	if err == nil || served {
		t.Fatalf("a transient discovery failure must be returned, got served=%v err=%v", served, err)
	}
}

func TestIsArgoMissing(t *testing.T) {
	if !isArgoMissing(&meta.NoKindMatchError{GroupKind: argoWorkflowGVK.GroupKind()}) {
		t.Error("NoKindMatchError must count as Argo missing")
	}
	if isArgoMissing(errors.New("connection refused")) || isArgoMissing(nil) {
		t.Error("transient errors and nil must not count as Argo missing")
	}
}

// noArgoClient behaves like a cluster without the Workflow CRD: every access to
// the Workflow kind answers NoKindMatch, as the real client does.
func noArgoClient(t *testing.T, objs ...client.Object) (client.Client, *record.FakeRecorder, *runtime.Scheme) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := framev1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	noMatch := &meta.NoKindMatchError{GroupKind: argoWorkflowGVK.GroupKind()}
	isWorkflow := func(o client.Object) bool {
		u, ok := o.(*unstructured.Unstructured)
		return ok && u.GroupVersionKind().Kind == argoWorkflowGVK.Kind
	}
	b := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&framev1beta1.FrameJob{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object,
				opts ...client.GetOption) error {
				if isWorkflow(o) {
					return noMatch
				}
				return c.Get(ctx, k, o, opts...)
			},
			Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				if isWorkflow(o) {
					return noMatch
				}
				return c.Create(ctx, o, opts...)
			},
			Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
				if isWorkflow(o) {
					return noMatch
				}
				return c.Delete(ctx, o, opts...)
			},
		})
	if len(objs) > 0 {
		b = b.WithObjects(objs...)
	}
	return b.Build(), record.NewFakeRecorder(10), scheme
}

func pipelineJob() *framev1beta1.FrameJob {
	return &framev1beta1.FrameJob{
		ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: "ns", Generation: 1,
			Finalizers: []string{frameJobFinalizer}},
		Spec: framev1beta1.FrameJobSpec{Pipeline: "training-dag", ServiceClass: "HIGH", GPUCount: 1},
	}
}

func TestReconcileWithoutArgoReportsAndDoesNotError(t *testing.T) {
	c, rec, scheme := noArgoClient(t, pipelineJob())
	r := &FrameJobReconciler{Client: c, Scheme: scheme, Recorder: rec}
	key := client.ObjectKey{Namespace: "ns", Name: "j"}

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatalf("a missing Argo CRD must not be an error (it would back off and flood the log): %v", err)
	}
	if res.RequeueAfter != argoMissingRetry {
		t.Errorf("requeue = %v, want %v", res.RequeueAfter, argoMissingRetry)
	}

	var got framev1beta1.FrameJob
	if err := c.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	if reason := readyReason(got.Status.Conditions); reason != jobPhaseArgoMissing {
		t.Errorf("stored Ready reason = %q, want %q", reason, jobPhaseArgoMissing)
	}
	if len(rec.Events) != 1 {
		t.Fatalf("want exactly one event, got %d", len(rec.Events))
	}
	if ev := <-rec.Events; !strings.HasPrefix(ev, "Warning "+jobPhaseArgoMissing) {
		t.Errorf("event = %q, want a Warning %s", ev, jobPhaseArgoMissing)
	}

	// A second pass on the unchanged job writes no new event.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if len(rec.Events) != 0 {
		t.Errorf("repeat reconcile re-emitted an event: %d", len(rec.Events))
	}
}

func TestDeleteWithoutArgoReleasesTheFinalizer(t *testing.T) {
	job := pipelineJob()
	c, rec, scheme := noArgoClient(t, job)
	r := &FrameJobReconciler{Client: c, Scheme: scheme, Recorder: rec}
	key := client.ObjectKey{Namespace: "ns", Name: "j"}

	if err := c.Delete(context.Background(), job); err != nil { // sets deletionTimestamp: the finalizer holds it
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("deleting a pipeline job without Argo must succeed: %v", err)
	}
	var got framev1beta1.FrameJob
	err := c.Get(context.Background(), key, &got)
	if err == nil && controllerutil.ContainsFinalizer(&got, frameJobFinalizer) {
		t.Fatal("the finalizer was kept: the job could never be deleted")
	}
}
