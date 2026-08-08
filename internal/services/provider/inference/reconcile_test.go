package inference_test

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	"github.com/rmocq/frame/internal/services/provider/inference"
)

// argsContain reports whether args holds flag immediately followed by value,
// the way a []string of CLI arguments carries a "-c 8192" pair.
func argsContain(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// newReconcileFixture builds a fake client seeded with the schemes Reconcile
// and Bind touch, the provider under test, and the FrameService every test in
// this file reconciles against.
func newReconcileFixture(t *testing.T) (*inference.Provider, client.Client, *servicesv1alpha1.FrameService) {
	t.Helper()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = servicesv1alpha1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	p := inference.New(7680, c)
	svc := &servicesv1alpha1.FrameService{
		ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "research"},
		Spec: servicesv1alpha1.FrameServiceSpec{
			Type:         "inference",
			ServiceClass: "HIGH",
			Parameters: map[string]string{
				"model":         "llama-3.1-8b-instruct",
				"contextLength": "8192",
			},
		},
	}
	return p, c, svc
}

func TestReconcileCreatesADeploymentSizedFromTheModel(t *testing.T) {
	p, c, svc := newReconcileFixture(t)

	result, err := p.Reconcile(context.Background(), svc)
	if err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}
	if len(result.Provisioned) != 2 {
		t.Fatalf("Provisioned = %v, want a Deployment and a Service", result.Provisioned)
	}

	var d appsv1.Deployment
	if err := c.Get(context.Background(),
		types.NamespacedName{Name: "llama", Namespace: "research"}, &d); err != nil {
		t.Fatalf("Deployment not created: %v", err)
	}

	container := d.Spec.Template.Spec.Containers[0]
	// The GPU is requested as a resource so the scheduler finds the card: this
	// is what stands in for a nodeName, and it keeps working when a second card
	// arrives.
	if container.Resources.Limits["nvidia.com/gpu"] != resource.MustParse("1") {
		t.Fatalf("no GPU requested: %v", container.Resources.Limits)
	}
	// The context window reaches llama.cpp, and it is the one that was sized.
	if !argsContain(container.Args, "-c", "8192") {
		t.Fatalf("args %v do not carry the sized context length", container.Args)
	}

	// CPU and memory come from Size, not recomputed independently: they must
	// be the exact numbers the operator's request was costed against.
	sizing, err := p.Size(svc.Spec.Parameters)
	if err != nil {
		t.Fatalf("Size returned %v", err)
	}
	if container.Resources.Limits[corev1.ResourceMemory] != resource.MustParse(sizing.Memory) {
		t.Fatalf("Memory limit = %v, want %v", container.Resources.Limits[corev1.ResourceMemory], sizing.Memory)
	}
	if container.Resources.Requests[corev1.ResourceCPU] != resource.MustParse(sizing.CPU) {
		t.Fatalf("CPU request = %v, want %v", container.Resources.Requests[corev1.ResourceCPU], sizing.CPU)
	}

	// serviceClass reaches the pod as placement through the label the
	// FrameNode controller already writes on every node. Frame decides
	// placement: this provider must never emit a nodeName.
	if got := d.Spec.Template.Spec.NodeSelector["frame.plume-labs.io/service-class"]; got != "HIGH" {
		t.Fatalf("nodeSelector[frame.plume-labs.io/service-class] = %q, want HIGH", got)
	}
	if d.Spec.Template.Spec.NodeName != "" {
		t.Fatalf("NodeName = %q, want empty: Frame decides placement, not the provider", d.Spec.Template.Spec.NodeName)
	}

	// Ownership: the Deployment exposes the instance (it is what makes it
	// exist at all), so it must be owner-referenced to the FrameService —
	// deleting one has to garbage-collect the other.
	owners := d.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Name != svc.Name || owners[0].Kind != "FrameService" {
		t.Fatalf("Deployment owner references = %v, want a single FrameService owner", owners)
	}

	var s corev1.Service
	if err := c.Get(context.Background(),
		types.NamespacedName{Name: "llama", Namespace: "research"}, &s); err != nil {
		t.Fatalf("Service not created: %v", err)
	}
	svcOwners := s.GetOwnerReferences()
	if len(svcOwners) != 1 || svcOwners[0].Name != svc.Name || svcOwners[0].Kind != "FrameService" {
		t.Fatalf("Service owner references = %v, want a single FrameService owner", svcOwners)
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}

	var deployments appsv1.DeploymentList
	_ = c.List(ctx, &deployments)
	if len(deployments.Items) != 1 {
		t.Fatalf("reconciling twice created %d Deployments", len(deployments.Items))
	}

	var services corev1.ServiceList
	_ = c.List(ctx, &services)
	if len(services.Items) != 1 {
		t.Fatalf("reconciling twice created %d Services", len(services.Items))
	}
}

func TestBindReturnsTheClusterEndpoint(t *testing.T) {
	p, _, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}

	b, err := p.Bind(ctx, svc)
	if err != nil {
		t.Fatalf("Bind returned %v", err)
	}
	if b.Endpoint != "http://llama.research.svc.cluster.local:8080" {
		t.Fatalf("Endpoint = %q", b.Endpoint)
	}
	// The endpoint is published in status, so it must never carry a credential.
	if strings.Contains(b.Endpoint, "token") || strings.Contains(b.Endpoint, "@") {
		t.Fatalf("endpoint %q looks like it carries a credential", b.Endpoint)
	}
}
