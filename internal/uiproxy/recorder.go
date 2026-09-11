package uiproxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

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

func verbForMethod(method string) string {
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

// verbForRequest is verbForMethod plus the one case where the HTTP method is
// not what the apiserver authorizes: an exec arrives from a browser as a GET
// (`new WebSocket()` can issue nothing else) and is authorized as `create
// pods/exec`. The record says `create` so it can be read against the RBAC rule
// that allowed it.
func verbForRequest(r *http.Request, ref framev1beta1.ObjectRef) string {
	if ref.Subresource == "exec" && isExecUpgrade(r) {
		return framev1beta1.TaskVerbCreate
	}
	return verbForMethod(r.Method)
}

// maxAction is FrameTaskSpec.Action's CRD cap. Past it the apiserver refuses
// the FrameTask create, the recorder logs the error, and the action proceeds
// with no record — the cost of overflowing is a silent hole in the trail.
const maxAction = 200

// execAction names the session the way a person would. The console cannot
// supply an X-Frame-Action here: `new WebSocket()` takes a URL and a
// subprotocol list, and no header, so the label every other write carries has
// nowhere to travel and the recorder builds it instead.
func execAction(ref framev1beta1.ObjectRef, q url.Values) string {
	s := fmt.Sprintf("open a shell in %s/%s", ref.Namespace, ref.Name)
	if c := q.Get("container"); c != "" {
		s = fmt.Sprintf("%s (%s)", s, c)
	}
	return boundAction(s)
}

// boundAction holds a label inside FrameTaskSpec.Action's cap, counting
// characters rather than bytes because that is what a CRD's maxLength counts.
// A byte bound would cut an accented label at half its allowance, and a byte
// slice can land mid-rune and store invalid UTF-8 — a worse record than a
// short one.
//
// It lives here, where every label passes through, rather than at the call
// sites that compose one. About twenty of those exist in the console and they
// compose from values carrying no bound of their own — a node name, a
// namespace and a pod name, for instance. A site that forgets costs a silent
// hole in the trail rather than a visible error, so the bound belongs at the
// choke point and not at the memory of whoever adds the next site.
func boundAction(s string) string {
	if utf8.RuneCountInString(s) <= maxAction {
		return s
	}
	return string([]rune(s)[:maxAction])
}

// isDryRun reports whether the request asked the apiserver to validate without
// storing. A dry run changes nothing, so it is not a write to record: the
// manifest editor makes one before every real edit, to learn what would be
// stored, and recording both would put two rows in the trail for one action.
func isDryRun(q url.Values) bool {
	for _, v := range q["dryRun"] {
		if v != "" {
			return true
		}
	}
	return false
}

// parsePath turns a Kubernetes request path into a reference.
//
//	/api/v1/nodes/w2
//	/api/v1/namespaces/{ns}/{resource}[/{name}[/{subresource}]]
//	/apis/{group}/{version}/namespaces/{ns}/{resource}[/{name}[/{sub}]]
//
// A create has no name in its path; the record still needs one field to
// print, so it gets "-".
//
// It used to return the resource as a second value, which every caller
// discarded — the reference already carries it.
func parsePath(p string) (framev1beta1.ObjectRef, bool) {
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
		return framev1beta1.ObjectRef{}, false
	}
	if len(rest) >= 2 && rest[0] == "namespaces" && len(rest) > 2 {
		ref.Namespace = rest[1]
		rest = rest[2:]
	}
	if len(rest) == 0 {
		return framev1beta1.ObjectRef{}, false
	}
	ref.Resource = rest[0]
	ref.Name = "-"
	if len(rest) >= 2 {
		ref.Name = rest[1]
	}
	if len(rest) >= 3 {
		ref.Subresource = rest[2]
	}
	return ref, true
}

func (t *TaskRecorder) Start(ctx context.Context, id Identity, r *http.Request) string {
	ref, ok := parsePath(r.URL.Path)
	if !ok {
		return ""
	}
	q := r.URL.Query()
	if isDryRun(q) {
		return ""
	}
	verb := verbForRequest(r, ref)
	if verb == "" {
		return ""
	}
	action := boundAction(r.Header.Get("X-Frame-Action"))
	if action == "" && ref.Subresource == "exec" && isExecUpgrade(r) {
		action = execAction(ref, q)
	}
	task := &framev1beta1.FrameTask{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "task-", Namespace: t.ns},
		Spec: framev1beta1.FrameTaskSpec{
			User:   id.User,
			Verb:   verb,
			Target: ref,
			Action: action,
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
	// 101 is what a hijacked upgrade leaves behind (statusRecorder.Hijack): the
	// session opened. It is below 200, so a bare 2xx test would file every
	// successful shell as a failure.
	if httpCode != http.StatusSwitchingProtocols && (httpCode < 200 || httpCode >= 300) {
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
