package uiproxy

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// TaskRecorder writes one FrameTask per mutating request.
//
// It writes with the pod ServiceAccount rather than through impersonation
// on purpose: a viewer must be able to leave the trace of their own 403,
// and a viewer cannot create FrameTasks.
type TaskRecorder struct {
	c   client.Client
	ns  string
	log *slog.Logger
}

func NewRecorder(c client.Client, namespace string, log *slog.Logger) *TaskRecorder {
	if log == nil {
		log = slog.Default()
	}
	return &TaskRecorder{c: c, ns: namespace, log: log}
}

func verbFor(method string) string {
	switch method {
	case http.MethodPost:
		return framev1beta1.TaskVerbCreate
	case http.MethodPut:
		return framev1beta1.TaskVerbUpdate
	case http.MethodPatch:
		return framev1beta1.TaskVerbPatch
	case http.MethodDelete:
		return framev1beta1.TaskVerbDelete
	}
	return ""
}

// parsePath turns a Kubernetes request path into a reference.
//
//	/api/v1/nodes/w2
//	/api/v1/namespaces/{ns}/{resource}[/{name}[/{subresource}]]
//	/apis/{group}/{version}/namespaces/{ns}/{resource}[/{name}[/{sub}]]
//
// A create has no name in its path; the record still needs one field to
// print, so it gets "-".
func parsePath(p string) (framev1beta1.ObjectRef, string, bool) {
	seg := strings.Split(strings.Trim(p, "/"), "/")
	var ref framev1beta1.ObjectRef
	var rest []string
	switch {
	case len(seg) >= 3 && seg[0] == "api":
		rest = seg[2:]
	case len(seg) >= 4 && seg[0] == "apis":
		ref.Group = seg[1]
		rest = seg[3:]
	default:
		return framev1beta1.ObjectRef{}, "", false
	}
	if len(rest) >= 2 && rest[0] == "namespaces" && len(rest) > 2 {
		ref.Namespace = rest[1]
		rest = rest[2:]
	}
	if len(rest) == 0 {
		return framev1beta1.ObjectRef{}, "", false
	}
	ref.Resource = rest[0]
	ref.Name = "-"
	if len(rest) >= 2 {
		ref.Name = rest[1]
	}
	return ref, rest[0], true
}

func (t *TaskRecorder) Start(ctx context.Context, id Identity, r *http.Request) string {
	verb := verbFor(r.Method)
	ref, _, ok := parsePath(r.URL.Path)
	if verb == "" || !ok {
		return ""
	}
	task := &framev1beta1.FrameTask{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "task-", Namespace: t.ns},
		Spec: framev1beta1.FrameTaskSpec{
			User:   id.User,
			Verb:   verb,
			Target: ref,
			Action: r.Header.Get("X-Frame-Action"),
		},
	}
	if err := t.c.Create(ctx, task); err != nil {
		// A missing trace must not cost the user their action.
		t.log.Error("could not record task", "err", err, "user", id.User, "path", r.URL.Path)
		return ""
	}
	task.Status = framev1beta1.FrameTaskStatus{
		Phase:     framev1beta1.TaskPhaseRunning,
		StartedAt: ptr(metav1.Now()),
	}
	if err := t.c.Status().Update(ctx, task); err != nil {
		t.log.Error("could not set task status", "err", err, "task", task.Name)
	}
	return task.Name
}

func (t *TaskRecorder) Finish(ctx context.Context, name string, httpCode int) {
	if name == "" {
		return
	}
	var task framev1beta1.FrameTask
	if err := t.c.Get(ctx, client.ObjectKey{Name: name, Namespace: t.ns}, &task); err != nil {
		t.log.Error("could not close task", "err", err, "task", name)
		return
	}
	phase := framev1beta1.TaskPhaseSucceeded
	if httpCode < 200 || httpCode >= 300 {
		phase = framev1beta1.TaskPhaseFailed
	}
	task.Status.Phase = phase
	task.Status.HTTPCode = int32(httpCode)
	task.Status.FinishedAt = ptr(metav1.Now())
	if err := t.c.Status().Update(ctx, &task); err != nil {
		t.log.Error("could not close task", "err", err, "task", name)
	}
}

// Purge deletes finished tasks older than the window. Running tasks are
// never deleted: one that never finished is a bug worth seeing.
func (t *TaskRecorder) Purge(ctx context.Context, olderThan time.Duration) error {
	var list framev1beta1.FrameTaskList
	if err := t.c.List(ctx, &list, client.InNamespace(t.ns)); err != nil {
		return err
	}
	cutoff := time.Now().Add(-olderThan)
	for i := range list.Items {
		f := list.Items[i].Status.FinishedAt
		if f == nil || f.Time.After(cutoff) {
			continue
		}
		if err := t.c.Delete(ctx, &list.Items[i]); err != nil {
			t.log.Error("could not purge task", "err", err, "task", list.Items[i].Name)
		}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }
