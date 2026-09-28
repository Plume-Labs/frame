package inference_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	servicesv1beta1 "github.com/gothshoot/frame/api/services/v1beta1"
	"github.com/gothshoot/frame/internal/services/provider"
	"github.com/gothshoot/frame/internal/services/provider/inference"
)

// With no RoleBinding for the manager in the FrameService's namespace, the
// API key Secret is Forbidden: Reconcile degrades with SecretAccessForbidden
// (not a bare error, which would go to ProvisionFailed with backoff and
// leave the old reason in status), and creates no Deployment reading it.
// Bind's read of the same Secret returns the typed error the controller
// turns into the same degrade.
func TestForbiddenAPIKeySecretDegrades(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = servicesv1beta1.AddToScheme(scheme)
	inner := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: defaultModelCachePVC, Namespace: "research"},
	}).Build()
	forbid := func(name string) error {
		return apierrors.NewForbidden(corev1.Resource("secrets"), name, errors.New("RBAC: no binding"))
	}
	c := interceptor.NewClient(inner, interceptor.Funcs{
		Get: func(ctx context.Context, w client.WithWatch, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return forbid(key.Name)
			}
			return w.Get(ctx, key, obj, opts...)
		},
	})
	p := inference.New(7680, c, c)
	_, _, svc := newFixture(t, true)

	result, err := p.Reconcile(context.Background(), svc)
	if err != nil {
		t.Fatalf("Reconcile returned %v, want a degraded Result", err)
	}
	if result.Ready || result.Reason != provider.ReasonSecretAccessForbidden || !strings.Contains(result.Message, `"research"`) || !strings.Contains(result.Message, "frame-manager-secret-writer") {
		t.Fatalf("Reconcile: %+v, want a SecretAccessForbidden degrade naming the namespace and the ClusterRole", result)
	}
	var deployments appsv1.DeploymentList
	if err := inner.List(context.Background(), &deployments); err != nil || len(deployments.Items) != 0 {
		t.Fatalf("Deployments %v (err %v), want none without its API key Secret", deployments.Items, err)
	}

	_, err = p.Bind(context.Background(), svc)
	var forbidden *provider.SecretAccessForbidden
	if !errors.As(err, &forbidden) || forbidden.Namespace != "research" {
		t.Fatalf("Bind: %v, want *provider.SecretAccessForbidden for research", err)
	}
}
