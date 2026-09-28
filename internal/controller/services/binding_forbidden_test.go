package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	servicesv1beta1 "github.com/gothshoot/frame/api/services/v1beta1"
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
