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

// defaultModelCachePVC is the PVC name inference.Provider mounts when
// spec.parameters.modelCache is unset.
const defaultModelCachePVC = "model-cache-pvc"

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

// newFixture builds a fake client seeded with the schemes Reconcile and Bind
// touch, the provider under test, and the FrameService every test in this
// file reconciles against. seedModelCache controls whether the default model
// cache PVC exists in the fake client — every test wants it present except
// the one proving what happens when it is missing.
func newFixture(t *testing.T, seedModelCache bool) (*inference.Provider, client.Client, *servicesv1alpha1.FrameService) {
	t.Helper()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = servicesv1alpha1.AddToScheme(scheme)

	builder := fake.NewClientBuilder().WithScheme(scheme)
	if seedModelCache {
		builder = builder.WithObjects(&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: defaultModelCachePVC, Namespace: "research"},
		})
	}
	c := builder.Build()

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

// newReconcileFixture is newFixture with the default model cache PVC already
// present — the common case every test but the missing-cache one wants.
func newReconcileFixture(t *testing.T) (*inference.Provider, client.Client, *servicesv1alpha1.FrameService) {
	return newFixture(t, true)
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

	// The Service has to reach the pods the Deployment actually creates: a
	// selector that silently drifted from the pod template labels would
	// leave the Service with no endpoints, and nothing would ever say why.
	if len(s.Spec.Selector) == 0 {
		t.Fatal("Service selector is empty")
	}
	for k, v := range s.Spec.Selector {
		if d.Spec.Template.Labels[k] != v {
			t.Fatalf("Service selector %s=%s does not match the Deployment's pod template labels %v",
				k, v, d.Spec.Template.Labels)
		}
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
	if err := c.List(ctx, &deployments); err != nil {
		t.Fatalf("listing Deployments: %v", err)
	}
	if len(deployments.Items) != 1 {
		t.Fatalf("reconciling twice created %d Deployments", len(deployments.Items))
	}

	var services corev1.ServiceList
	if err := c.List(ctx, &services); err != nil {
		t.Fatalf("listing Services: %v", err)
	}
	if len(services.Items) != 1 {
		t.Fatalf("reconciling twice created %d Services", len(services.Items))
	}
}

// TestReconcileDoesNotFightApiserverDefaults pins the fix for a hot loop: a
// real apiserver defaults fields (ImagePullPolicy, the container's
// termination-message fields, a Service port's Protocol) that Reconcile's
// literals never set. If Reconcile replaced the whole Containers or Ports
// slice on every pass, those defaults would be dropped every time,
// CreateOrUpdate would see a diff and Update, and — because the controller
// Owns() both kinds — that Update would re-trigger a FrameService reconcile,
// forever. A fake client does no such defaulting on its own, so this test
// applies the defaults by hand between two Reconcile calls — the closest a
// fake client gets to the real failure — and asserts they survive the second
// pass rather than being silently dropped.
func TestReconcileDoesNotFightApiserverDefaults(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}

	var d appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: "research"}, &d); err != nil {
		t.Fatalf("Deployment not created: %v", err)
	}
	d.Spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	d.Spec.Template.Spec.Containers[0].TerminationMessagePath = "/dev/termination-log"
	d.Spec.Template.Spec.Containers[0].TerminationMessagePolicy = corev1.TerminationMessageReadFile
	if err := c.Update(ctx, &d); err != nil {
		t.Fatalf("simulating apiserver defaults on the Deployment: %v", err)
	}

	var s corev1.Service
	if err := c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: "research"}, &s); err != nil {
		t.Fatalf("Service not created: %v", err)
	}
	s.Spec.Ports[0].Protocol = corev1.ProtocolTCP
	if err := c.Update(ctx, &s); err != nil {
		t.Fatalf("simulating apiserver defaults on the Service: %v", err)
	}

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}

	var d2 appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: "research"}, &d2); err != nil {
		t.Fatalf("Deployment missing after second Reconcile: %v", err)
	}
	c2 := d2.Spec.Template.Spec.Containers[0]
	if c2.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Fatalf("ImagePullPolicy = %q, want it to survive a second Reconcile", c2.ImagePullPolicy)
	}
	if c2.TerminationMessagePath != "/dev/termination-log" {
		t.Fatalf("TerminationMessagePath = %q, want it to survive a second Reconcile", c2.TerminationMessagePath)
	}
	if c2.TerminationMessagePolicy != corev1.TerminationMessageReadFile {
		t.Fatalf("TerminationMessagePolicy = %q, want it to survive a second Reconcile", c2.TerminationMessagePolicy)
	}

	var s2 corev1.Service
	if err := c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: "research"}, &s2); err != nil {
		t.Fatalf("Service missing after second Reconcile: %v", err)
	}
	if s2.Spec.Ports[0].Protocol != corev1.ProtocolTCP {
		t.Fatalf("Service port Protocol = %q, want it to survive a second Reconcile", s2.Spec.Ports[0].Protocol)
	}
}

// TestReconcileReportsNotReadyUntilThePodIsServing pins Ready coming from the
// Deployment's actual status rather than from CreateOrUpdate merely
// succeeding. Publishing an endpoint (and, once Task 8 lands, minting
// credentials) for a Deployment with zero ready replicas would be
// advertising a pod that has not started.
func TestReconcileReportsNotReadyUntilThePodIsServing(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	result, err := p.Reconcile(ctx, svc)
	if err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}
	if result.Ready {
		t.Fatal("Ready = true immediately after creating the Deployment, want false until it has a ready replica")
	}
	if result.Reason != "RolloutInProgress" {
		t.Fatalf("Reason = %q, want RolloutInProgress", result.Reason)
	}
	if len(result.Provisioned) != 2 {
		t.Fatalf("Provisioned = %v, want the Deployment and Service to still be reported while rolling out", result.Provisioned)
	}

	var d appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: "research"}, &d); err != nil {
		t.Fatalf("Deployment not created: %v", err)
	}
	d.Status.ReadyReplicas = 1
	// Deployment is one of the fake client's built-in status-subresource
	// types, so a plain Update silently discards a Status change — the
	// status subresource writer is what actually persists it, same as a
	// real apiserver requires the /status endpoint for this field.
	if err := c.Status().Update(ctx, &d); err != nil {
		t.Fatalf("marking the Deployment ready: %v", err)
	}

	result, err = p.Reconcile(ctx, svc)
	if err != nil {
		t.Fatalf("Reconcile after the replica became ready: %v", err)
	}
	if !result.Ready {
		t.Fatalf("Ready = false with a ready replica, want true; Reason=%q Message=%q", result.Reason, result.Message)
	}
	if result.Reason != "Provisioned" {
		t.Fatalf("Reason = %q, want Provisioned", result.Reason)
	}
}

func TestReconcileMountsTheDefaultModelCacheReadOnly(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}

	var d appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: "research"}, &d); err != nil {
		t.Fatalf("Deployment not created: %v", err)
	}

	container := d.Spec.Template.Spec.Containers[0]
	if !argsContain(container.Args, "-m", "/models/llama-3.1-8b-instruct.gguf") {
		t.Fatalf("args %v do not point -m at the mounted cache", container.Args)
	}

	var mount *corev1.VolumeMount
	for i := range container.VolumeMounts {
		if container.VolumeMounts[i].MountPath == "/models" {
			mount = &container.VolumeMounts[i]
		}
	}
	if mount == nil {
		t.Fatalf("no volume mount at /models: %v", container.VolumeMounts)
	}
	if !mount.ReadOnly {
		t.Fatal("model cache mount is writable, want read-only: several instances share one cache")
	}

	var vol *corev1.Volume
	for i := range d.Spec.Template.Spec.Volumes {
		if d.Spec.Template.Spec.Volumes[i].Name == mount.Name {
			vol = &d.Spec.Template.Spec.Volumes[i]
		}
	}
	if vol == nil || vol.PersistentVolumeClaim == nil {
		t.Fatalf("volume %q is not backed by a PersistentVolumeClaim: %v", mount.Name, vol)
	}
	if vol.PersistentVolumeClaim.ClaimName != defaultModelCachePVC {
		t.Fatalf("ClaimName = %q, want the default %q used when parameters.modelCache is absent",
			vol.PersistentVolumeClaim.ClaimName, defaultModelCachePVC)
	}
	if !vol.PersistentVolumeClaim.ReadOnly {
		t.Fatal("PersistentVolumeClaimVolumeSource.ReadOnly = false, want true")
	}
}

func TestReconcileMountsTheNamedModelCacheWhenParameterSet(t *testing.T) {
	p, c, svc := newFixture(t, false)
	svc.Spec.Parameters["modelCache"] = "custom-cache-pvc"
	ctx := context.Background()

	if err := c.Create(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-cache-pvc", Namespace: "research"},
	}); err != nil {
		t.Fatalf("seeding the named PVC: %v", err)
	}

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}

	var d appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: "research"}, &d); err != nil {
		t.Fatalf("Deployment not created: %v", err)
	}
	var claimName string
	for _, v := range d.Spec.Template.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			claimName = v.PersistentVolumeClaim.ClaimName
		}
	}
	if claimName != "custom-cache-pvc" {
		t.Fatalf("ClaimName = %q, want the parameters.modelCache override custom-cache-pvc", claimName)
	}
}

// TestReconcileDegradesWhenModelCacheMissing pins the decision to check for
// the cache before creating anything: an operator reads why nothing started
// from status, instead of a pod stuck failing to mount a volume that was
// never going to appear.
func TestReconcileDegradesWhenModelCacheMissing(t *testing.T) {
	p, c, svc := newFixture(t, false)
	ctx := context.Background()

	result, err := p.Reconcile(ctx, svc)
	if err != nil {
		t.Fatalf("Reconcile returned an error %v, want a degraded Result instead", err)
	}
	if result.Ready {
		t.Fatal("Ready = true with no model cache PVC")
	}
	if result.Reason != "ModelCacheMissing" {
		t.Fatalf("Reason = %q, want ModelCacheMissing", result.Reason)
	}
	for _, want := range []string{defaultModelCachePVC, "research"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("Message %q does not name %q", result.Message, want)
		}
	}

	var deployments appsv1.DeploymentList
	if err := c.List(ctx, &deployments); err != nil {
		t.Fatalf("listing Deployments: %v", err)
	}
	if len(deployments.Items) != 0 {
		t.Fatalf("a Deployment was created despite the missing model cache: %v", deployments.Items)
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
