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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// Argo Workflows is optional: the manager must start without its CRD, and a
// pipeline job must say why it cannot run instead of looping on an error.

func TestArgoWorkflowsServed(t *testing.T) {
	empty := meta.NewDefaultRESTMapper(nil)
	if argoWorkflowsServed(empty) {
		t.Fatal("a mapper without the Workflow kind must report Argo as not served")
	}
	withArgo := meta.NewDefaultRESTMapper(nil)
	withArgo.Add(argoWorkflowGVK, meta.RESTScopeNamespace)
	if !argoWorkflowsServed(withArgo) {
		t.Fatal("a mapper that knows the Workflow kind must report Argo as served")
	}
}

func TestIsArgoMissing(t *testing.T) {
	noMatch := &meta.NoKindMatchError{GroupKind: argoWorkflowGVK.GroupKind()}
	if !isArgoMissing(noMatch) {
		t.Error("NoKindMatchError must count as Argo missing")
	}
	if isArgoMissing(errors.New("connection refused")) || isArgoMissing(nil) {
		t.Error("transient errors and nil must not count as Argo missing")
	}
	if !isArgoMissing(runtime.NewNotRegisteredErrForKind("s", argoWorkflowGVK)) {
		t.Error("a kind unknown to the scheme must count as Argo missing")
	}
}

func TestReportArgoMissingIsStableAndNonFailing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := framev1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	job := &framev1beta1.FrameJob{ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: "ns", Generation: 1}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(job).WithStatusSubresource(job).Build()
	rec := record.NewFakeRecorder(5)
	r := &FrameJobReconciler{Client: c, Scheme: scheme, Recorder: rec}

	res, err := r.reportArgoMissing(context.Background(), job)
	if err != nil {
		t.Fatalf("must not return an error (it would trigger backoff): %v", err)
	}
	if res.RequeueAfter != argoMissingRetry {
		t.Errorf("requeue = %v, want %v", res.RequeueAfter, argoMissingRetry)
	}
	if got := readyReason(job.Status.Conditions); got != jobPhaseArgoMissing {
		t.Errorf("Ready reason = %q, want %q", got, jobPhaseArgoMissing)
	}
	if len(rec.Events) != 1 {
		t.Fatalf("want exactly one warning event, got %d", len(rec.Events))
	}

	// Second pass on an unchanged job: no new event, no status write.
	if _, err := r.reportArgoMissing(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if len(rec.Events) != 1 {
		t.Errorf("repeat reconcile must not re-emit the event, got %d", len(rec.Events))
	}
	_ = corev1.EventTypeWarning
}
