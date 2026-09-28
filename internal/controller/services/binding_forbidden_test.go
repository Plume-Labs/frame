package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	servicesv1beta1 "github.com/gothshoot/frame/api/services/v1beta1"
	"github.com/gothshoot/frame/internal/services/provider"
)

// The manager holds nothing on Secrets cluster-wide: in a namespace where
// no RoleBinding gives it frame-manager-secret-writer, the binding degrades
// with a reason naming the missing grant, instead of error-looping.
func TestForbiddenSecretAccessDegradesWithTheMissingGrant(t *testing.T) {
	sch := runtime.NewScheme()
	_ = corev1.AddToScheme(sch)
	_ = servicesv1beta1.AddToScheme(sch)
	forbid := func(obj client.Object) error {
		return apierrors.NewForbidden(corev1.Resource("secrets"), obj.GetName(), errors.New("RBAC: no binding"))
	}
	c := interceptor.NewClient(fake.NewClientBuilder().WithScheme(sch).Build(), interceptor.Funcs{
		Get: func(_ context.Context, _ client.WithWatch, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
			obj.SetName(key.Name)
			return forbid(obj)
		},
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			return forbid(obj)
		},
	})
	r := &FrameServiceReconciler{Client: c, Scheme: sch}
	svc := &servicesv1beta1.FrameService{ObjectMeta: metav1.ObjectMeta{Name: "llm", Namespace: "apps"}}
	coord := servicesv1beta1.ProjectedSecretRef{Namespace: "team-a", Name: "llm-credentials"}

	for name, err := range map[string]error{
		"claim": r.claimNewCoordinates(context.Background(), svc, []servicesv1beta1.ProjectedSecretRef{coord}),
		"write": r.writeSecret(context.Background(), svc, coord, map[string][]byte{"k": []byte("v")}),
	} {
		var d *bindingDegradation
		if !errors.As(err, &d) || d.Reason != "SecretAccessForbidden" || !strings.Contains(d.Message, `"team-a"`) || !strings.Contains(d.Message, "frame-manager-secret-writer") {
			t.Errorf("%s: %v, want a SecretAccessForbidden degradation naming the namespace and the ClusterRole", name, err)
		}
	}
}

// stubProvisioner is Ready at once and whose Bind meets a Forbidden
// Secret, the way the inference provider's readAPIKey does.
type stubProvisioner struct{}

func (stubProvisioner) Type() string                      { return "stub" }
func (stubProvisioner) ParameterSchema() *provider.Schema { return &provider.Schema{Type: "object"} }
func (stubProvisioner) Size(map[string]string) (provider.Sizing, error) {
	return provider.Sizing{}, nil
}
func (stubProvisioner) Reconcile(context.Context, *servicesv1beta1.FrameService) (provider.Result, error) {
	return provider.Result{Ready: true, Reason: "Ready"}, nil
}
func (stubProvisioner) Bind(_ context.Context, svc *servicesv1beta1.FrameService) (provider.Binding, error) {
	return provider.Binding{}, &provider.SecretAccessForbidden{Namespace: svc.Namespace, Action: "read"}
}

func forbiddenFixture(t *testing.T, svc *servicesv1beta1.FrameService, fns interceptor.Funcs) (*FrameServiceReconciler, client.Client) {
	t.Helper()
	sch := runtime.NewScheme()
	_ = corev1.AddToScheme(sch)
	_ = servicesv1beta1.AddToScheme(sch)
	inner := fake.NewClientBuilder().WithScheme(sch).WithObjects(svc).WithStatusSubresource(&servicesv1beta1.FrameService{}).Build()
	c := interceptor.NewClient(inner, fns)
	return &FrameServiceReconciler{Client: c, Scheme: sch, Recorder: record.NewFakeRecorder(16), Registry: provider.NewRegistry(stubProvisioner{})}, c
}

func fetchReady(t *testing.T, c client.Client, key client.ObjectKey) (*metav1.Condition, *servicesv1beta1.FrameService) {
	t.Helper()
	var got servicesv1beta1.FrameService
	if err := c.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	return meta.FindStatusCondition(got.Status.Conditions, "Ready"), &got
}

// Bind's Forbidden (the API key Secret, unreadable) degrades on the
// degraded requeue instead of erroring with backoff under an old reason.
func TestForbiddenBindDegrades(t *testing.T) {
	svc := &servicesv1beta1.FrameService{
		ObjectMeta: metav1.ObjectMeta{Name: "llm", Namespace: "apps", Finalizers: []string{frameServiceFinalizer}},
		Spec:       servicesv1beta1.FrameServiceSpec{Type: "stub"},
	}
	r, c := forbiddenFixture(t, svc, interceptor.Funcs{})
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(svc)})
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("Reconcile: %+v, %v; want a degraded requeue, no error", res, err)
	}
	if cond, _ := fetchReady(t, c, client.ObjectKeyFromObject(svc)); cond == nil || cond.Reason != provider.ReasonSecretAccessForbidden || !strings.Contains(cond.Message, `"apps"`) {
		t.Fatalf("Ready condition %+v, want SecretAccessForbidden naming apps", cond)
	}
}

// Deleting a FrameService whose projectTo namespace is no longer granted:
// the finalizer stays (the projected Secret would otherwise be orphaned),
// and status names the grant to add, instead of an error loop.
func TestForbiddenProjectionDeleteKeepsTheFinalizer(t *testing.T) {
	svc := &servicesv1beta1.FrameService{
		ObjectMeta: metav1.ObjectMeta{Name: "llm", Namespace: "apps", Finalizers: []string{frameServiceFinalizer}},
		Spec:       servicesv1beta1.FrameServiceSpec{Type: "stub"},
	}
	r, c := forbiddenFixture(t, svc, interceptor.Funcs{
		Delete: func(ctx context.Context, w client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return apierrors.NewForbidden(corev1.Resource("secrets"), obj.GetName(), errors.New("RBAC: no binding"))
			}
			return w.Delete(ctx, obj, opts...)
		},
	})
	ctx := context.Background()
	key := client.ObjectKeyFromObject(svc)
	_, got := fetchReady(t, c, key)
	got.Status.Binding.Projected = []servicesv1beta1.ProjectedSecretRef{{Namespace: "team-a", Name: "llm-credentials"}}
	if err := c.Status().Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, got); err != nil {
		t.Fatal(err)
	}
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("Reconcile: %+v, %v; want a degraded requeue, no error", res, err)
	}
	cond, after := fetchReady(t, c, key)
	if !controllerutil.ContainsFinalizer(after, frameServiceFinalizer) {
		t.Fatal("finalizer removed: the projected Secret in team-a would be orphaned")
	}
	if cond == nil || cond.Reason != provider.ReasonSecretAccessForbidden || !strings.Contains(cond.Message, "team-a/llm-credentials") || !strings.Contains(cond.Message, "frame-manager-secret-writer") || !strings.Contains(cond.Message, "finalizer kept") {
		t.Fatalf("Ready condition %+v, want SecretAccessForbidden naming the Secret, the ClusterRole and the kept finalizer", cond)
	}
}
