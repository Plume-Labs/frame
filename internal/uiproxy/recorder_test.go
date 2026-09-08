package uiproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func TestParsePath(t *testing.T) {
	cases := []struct {
		path string
		want framev1beta1.ObjectRef
		ok   bool
	}{
		{"/api/v1/nodes/w2", framev1beta1.ObjectRef{Resource: "nodes", Name: "w2"}, true},
		{"/api/v1/namespaces/neura/pods/api-0/eviction",
			framev1beta1.ObjectRef{Resource: "pods", Namespace: "neura", Name: "api-0"}, true},
		{"/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/framejobs/j-1",
			framev1beta1.ObjectRef{Group: "frame.plume-labs.io", Resource: "framejobs", Namespace: "frame-system", Name: "j-1"}, true},
		{"/apis/apps/v1/namespaces/neura/deployments/api/scale",
			framev1beta1.ObjectRef{Group: "apps", Resource: "deployments", Namespace: "neura", Name: "api"}, true},
		// A create has no name in the path; the record still has to exist.
		{"/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/framejobs",
			framev1beta1.ObjectRef{Group: "frame.plume-labs.io", Resource: "framejobs", Namespace: "frame-system", Name: "-"}, true},
		{"/healthz", framev1beta1.ObjectRef{}, false},
	}
	for _, tc := range cases {
		got, _, ok := parsePath(tc.path)
		if ok != tc.ok {
			t.Fatalf("%s: ok = %v", tc.path, ok)
		}
		if ok && got != tc.want {
			t.Fatalf("%s: got %+v, want %+v", tc.path, got, tc.want)
		}
	}
}

func newRecorderFixture(t *testing.T) (*TaskRecorder, client.Client) {
	t.Helper()
	s := scheme.Scheme
	if err := framev1beta1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&framev1beta1.FrameTask{}).Build()
	return NewRecorder(c, "frame-system", nil), c
}

func TestStartRecordsTheUserAndAction(t *testing.T) {
	rec, c := newRecorderFixture(t)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/nodes/w2", nil)
	req.Header.Set("X-Frame-Action", "cordon node w2")

	name := rec.Start(context.Background(), Identity{User: "alice@example.com", Groups: []string{"operators"}}, req)
	if name == "" {
		t.Fatal("Start recorded nothing")
	}
	var task framev1beta1.FrameTask
	if err := c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "frame-system"}, &task); err != nil {
		t.Fatal(err)
	}
	if task.Spec.User != "alice@example.com" {
		t.Fatalf("User = %q", task.Spec.User)
	}
	if task.Spec.Verb != framev1beta1.TaskVerbPatch {
		t.Fatalf("Verb = %q", task.Spec.Verb)
	}
	if task.Spec.Action != "cordon node w2" {
		t.Fatalf("Action = %q", task.Spec.Action)
	}
	if task.Status.Phase != framev1beta1.TaskPhaseRunning {
		t.Fatalf("Phase = %q", task.Status.Phase)
	}
}

// A viewer's refused write is exactly the trace worth keeping.
func TestFinishRecordsFailure(t *testing.T) {
	rec, c := newRecorderFixture(t)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/nodes/w2", nil)
	name := rec.Start(context.Background(), Identity{User: "bob@example.com", Groups: []string{"viewers"}}, req)

	rec.Finish(context.Background(), name, http.StatusForbidden)

	var task framev1beta1.FrameTask
	if err := c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "frame-system"}, &task); err != nil {
		t.Fatal(err)
	}
	if task.Status.Phase != framev1beta1.TaskPhaseFailed {
		t.Fatalf("Phase = %q, want Failed", task.Status.Phase)
	}
	if task.Status.HTTPCode != 403 {
		t.Fatalf("HTTPCode = %d", task.Status.HTTPCode)
	}
	if task.Status.FinishedAt == nil {
		t.Fatal("FinishedAt not set")
	}
}

func TestPurgeKeepsRunningAndRecentTasks(t *testing.T) {
	rec, c := newRecorderFixture(t)
	ctx := context.Background()
	old := metav1.NewTime(time.Now().Add(-8 * 24 * time.Hour))
	recent := metav1.NewTime(time.Now().Add(-time.Hour))

	mk := func(name, phase string, finished *metav1.Time) {
		task := &framev1beta1.FrameTask{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "frame-system"},
			Spec: framev1beta1.FrameTaskSpec{
				User: "a@b.c", Verb: framev1beta1.TaskVerbPatch,
				Target: framev1beta1.ObjectRef{Resource: "nodes", Name: "w2"},
			},
		}
		if err := c.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		task.Status = framev1beta1.FrameTaskStatus{Phase: phase, FinishedAt: finished}
		if err := c.Status().Update(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	mk("old-done", framev1beta1.TaskPhaseSucceeded, &old)
	mk("recent-done", framev1beta1.TaskPhaseSucceeded, &recent)
	mk("still-running", framev1beta1.TaskPhaseRunning, nil)

	if err := rec.Purge(ctx, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var list framev1beta1.FrameTaskList
	if err := c.List(ctx, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("kept %d tasks, want 2", len(list.Items))
	}
	for _, it := range list.Items {
		if it.Name == "old-done" {
			t.Fatal("purge kept a finished task older than the window")
		}
	}
}
