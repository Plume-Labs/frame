package uiproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
		// The subresource is the difference between draining a pod and
		// deleting one, and between opening a shell and creating a pod.
		{"/api/v1/namespaces/neura/pods/api-0/eviction",
			framev1beta1.ObjectRef{Resource: "pods", Namespace: "neura", Name: "api-0", Subresource: "eviction"}, true},
		{"/api/v1/namespaces/neura/pods/api-0/exec",
			framev1beta1.ObjectRef{Resource: "pods", Namespace: "neura", Name: "api-0", Subresource: "exec"}, true},
		{"/api/v1/namespaces/neura/pods/api-0/log",
			framev1beta1.ObjectRef{Resource: "pods", Namespace: "neura", Name: "api-0", Subresource: "log"}, true},
		{"/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/framejobs/j-1",
			framev1beta1.ObjectRef{Group: "frame.plume-labs.io", Resource: "framejobs", Namespace: "frame-system", Name: "j-1"}, true},
		{"/apis/apps/v1/namespaces/neura/deployments/api/scale",
			framev1beta1.ObjectRef{Group: "apps", Resource: "deployments", Namespace: "neura", Name: "api", Subresource: "scale"}, true},
		// A create has no name in the path; the record still has to exist.
		{"/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/framejobs",
			framev1beta1.ObjectRef{Group: "frame.plume-labs.io", Resource: "framejobs", Namespace: "frame-system", Name: "-"}, true},
		{"/healthz", framev1beta1.ObjectRef{}, false},
	}
	for _, tc := range cases {
		got, ok := parsePath(tc.path)
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

// A hijacked upgrade leaves 101 behind, which is below 200 and would read as
// a failure under a plain 2xx test. Every shell that opened successfully would
// be recorded as failed — the opposite of what the Tasks screen is for.
func TestFinishTreats101AsASuccessfulSession(t *testing.T) {
	rec, c := newRecorderFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/neura/pods/api-0/exec", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	name := rec.Start(context.Background(),
		Identity{User: "alice@example.com", Groups: []string{"admins"}}, req)
	if name == "" {
		t.Fatal("an exec session left no record")
	}

	rec.Finish(context.Background(), name, http.StatusSwitchingProtocols)

	var task framev1beta1.FrameTask
	if err := c.Get(context.Background(),
		client.ObjectKey{Name: name, Namespace: "frame-system"}, &task); err != nil {
		t.Fatal(err)
	}
	if task.Status.Phase != framev1beta1.TaskPhaseSucceeded {
		t.Fatalf("Phase = %q, want Succeeded — 101 is a session that opened", task.Status.Phase)
	}
}

// The one action in the product that most deserves a record. A browser opens
// an exec as a WebSocket upgrade, which `new WebSocket()` can only issue as a
// GET — so the mutating-method test that gates every other record says no, and
// without this rule a shell leaves nothing behind at all.
func TestStartRecordsAnExecOpenedOverAWebSocket(t *testing.T) {
	rec, c := newRecorderFixture(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/namespaces/neura/pods/api-0/exec?container=api&stdin=true&stdout=true&tty=true", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	name := rec.Start(context.Background(),
		Identity{User: "alice@example.com", Groups: []string{"admins"}}, req)
	if name == "" {
		t.Fatal("an exec session left no record")
	}
	var task framev1beta1.FrameTask
	if err := c.Get(context.Background(),
		client.ObjectKey{Name: name, Namespace: "frame-system"}, &task); err != nil {
		t.Fatal(err)
	}
	// `create` is the verb the apiserver authorizes for pods/exec whatever
	// method carries it. Recording "get" would make the trail disagree with
	// the RBAC rule that allowed it.
	if task.Spec.Verb != framev1beta1.TaskVerbCreate {
		t.Fatalf("Verb = %q, want create", task.Spec.Verb)
	}
	if task.Spec.Target.Subresource != "exec" {
		t.Fatalf("Subresource = %q, want exec", task.Spec.Target.Subresource)
	}
	if task.Spec.Action != "open a shell in neura/api-0 (api)" {
		t.Fatalf("Action = %q", task.Spec.Action)
	}
	if task.Status.StartedAt == nil {
		t.Fatal("StartedAt not set — a session with no start has no duration")
	}
}

// A plain GET must stay unrecorded. Reads are not recorded by design, and the
// exec rule is the narrowest possible exception to that: an upgrade, on the
// exec subresource. Widening it to "any GET on pods" would put a row in the
// trail for every screen refresh in the console.
func TestStartStillIgnoresOrdinaryReads(t *testing.T) {
	rec, _ := newRecorderFixture(t)
	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{"plain list", httptest.NewRequest(http.MethodGet, "/api/v1/pods", nil)},
		{"pod log", httptest.NewRequest(http.MethodGet,
			"/api/v1/namespaces/neura/pods/api-0/log?follow=true", nil)},
		{"exec path with no upgrade", httptest.NewRequest(http.MethodGet,
			"/api/v1/namespaces/neura/pods/api-0/exec", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if name := rec.Start(context.Background(), Identity{User: "a@b.c"}, tc.req); name != "" {
				t.Fatalf("recorded %q for a read", name)
			}
		})
	}
}

// The manifest editor PUTs the edited object once with ?dryRun=All to learn
// what the apiserver would store — that is how it computes the changed field
// paths for the real write's label. A dry run changes nothing, so recording it
// would put two rows in the trail for one edit, one of which did nothing.
func TestStartIgnoresADryRun(t *testing.T) {
	rec, _ := newRecorderFixture(t)
	req := httptest.NewRequest(http.MethodPut,
		"/apis/apps/v1/namespaces/neura/deployments/api?dryRun=All", nil)
	if name := rec.Start(context.Background(), Identity{User: "a@b.c"}, req); name != "" {
		t.Fatalf("recorded %q for a dry run", name)
	}
}

// The same PUT without the parameter is a real write and must be recorded —
// otherwise "skip dry runs" is indistinguishable from "skip PUTs".
func TestStartRecordsTheSamePutWithoutDryRun(t *testing.T) {
	rec, _ := newRecorderFixture(t)
	req := httptest.NewRequest(http.MethodPut,
		"/apis/apps/v1/namespaces/neura/deployments/api", nil)
	if rec.Start(context.Background(), Identity{User: "a@b.c"}, req) == "" {
		t.Fatal("a real update left no record")
	}
}

func TestExecActionNamesThePodAndContainer(t *testing.T) {
	ref := framev1beta1.ObjectRef{Resource: "pods", Namespace: "neura", Name: "api-0", Subresource: "exec"}
	if got := execAction(ref, url.Values{"container": []string{"api"}}); got != "open a shell in neura/api-0 (api)" {
		t.Fatalf("got %q", got)
	}
	if got := execAction(ref, url.Values{}); got != "open a shell in neura/api-0" {
		t.Fatalf("got %q", got)
	}
	// FrameTaskSpec.Action is capped at 200 characters by the CRD, and a
	// container name arrives from a URL. Over the cap the apiserver refuses the
	// FrameTask create outright, the recorder logs it, and the session runs
	// with no record at all — the failure is silence, not an error the user
	// sees.
	long := execAction(ref, url.Values{"container": []string{strings.Repeat("x", 400)}})
	if len(long) > 200 {
		t.Fatalf("action is %d characters, over the CRD's 200-character cap", len(long))
	}
}

// The console builds X-Frame-Action itself for every write that is not an exec,
// and about twenty of those call sites compose it from values with no bound of
// their own — a node name, a namespace and a pod name, for instance. Past the
// CRD's cap the apiserver refuses the FrameTask create, the recorder logs it,
// and the write proceeds with no record. The failure is a silent hole in the
// trail, not an error anyone sees, so the bound belongs here rather than at
// each site that could forget it.
func TestStartBoundsAnOverlongActionHeader(t *testing.T) {
	rec, c := newRecorderFixture(t)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/nodes/w2", nil)
	req.Header.Set("X-Frame-Action", strings.Repeat("x", 400))

	name := rec.Start(context.Background(), Identity{User: "alice@example.com"}, req)
	if name == "" {
		t.Fatal("Start recorded nothing")
	}
	var task framev1beta1.FrameTask
	if err := c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "frame-system"}, &task); err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(task.Spec.Action); n > maxAction {
		t.Fatalf("Action is %d characters, over the CRD's %d-character cap", n, maxAction)
	}
}

// A CRD's maxLength counts characters, not bytes, and these labels carry French
// copy. Bounding by byte length would cut an accented label at half its allowed
// size, and a byte slice can land mid-rune and produce invalid UTF-8 — which is
// a worse record than a short one.
func TestBoundActionCountsCharactersNotBytes(t *testing.T) {
	accented := strings.Repeat("é", 250)
	got := boundAction(accented)

	if n := utf8.RuneCountInString(got); n != maxAction {
		t.Fatalf("kept %d characters, want exactly %d — a byte bound would have kept 100", n, maxAction)
	}
	if !utf8.ValidString(got) {
		t.Fatal("bounded label is not valid UTF-8 — the cut landed mid-rune")
	}
}

// A label already within the cap must come back untouched, accents and all.
func TestBoundActionLeavesAShortLabelAlone(t *testing.T) {
	const s = "redémarrage du déploiement neura/api"
	if got := boundAction(s); got != s {
		t.Fatalf("got %q, want it unchanged", got)
	}
}

// execAction builds its own label from a container name that arrives in a URL,
// so it needs the same bound and the same rune-awareness.
func TestExecActionBoundIsRuneAware(t *testing.T) {
	ref := framev1beta1.ObjectRef{Resource: "pods", Namespace: "neura", Name: "api-0", Subresource: "exec"}
	got := execAction(ref, url.Values{"container": []string{strings.Repeat("é", 300)}})
	if n := utf8.RuneCountInString(got); n > maxAction {
		t.Fatalf("action is %d characters, over the cap", n)
	}
	if !utf8.ValidString(got) {
		t.Fatal("action is not valid UTF-8 — the cut landed mid-rune")
	}
}
