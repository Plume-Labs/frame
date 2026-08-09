package inference_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	servicesv1beta1 "github.com/rmocq/frame/api/services/v1beta1"
	"github.com/rmocq/frame/internal/services/provider"
	"github.com/rmocq/frame/internal/services/provider/inference"
)

// defaultModelCachePVC is the PVC name inference.Provider mounts when
// spec.parameters.modelCache is unset.
const defaultModelCachePVC = "model-cache-pvc"

// frameServiceKind is the owner-reference Kind every object this provider
// creates must carry, asserted by every ownership test in this file.
const frameServiceKind = "FrameService"

// rolloutInProgress is the provider's fallback degrade reason — the one that
// means "a replica is missing and nothing more specific was found". Several
// tests turn on whether a failure reports this or something that actually
// explains itself, so it is named once.
const rolloutInProgress = "RolloutInProgress"

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
func newFixture(t *testing.T, seedModelCache bool) (*inference.Provider, client.Client, *servicesv1beta1.FrameService) {
	t.Helper()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = servicesv1beta1.AddToScheme(scheme)

	builder := fake.NewClientBuilder().WithScheme(scheme)
	if seedModelCache {
		builder = builder.WithObjects(&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: defaultModelCachePVC, Namespace: "research"},
		})
	}
	c := builder.Build()

	// c stands in for both the cached client and the uncached APIReader: the
	// fake client satisfies both interfaces, and Reconcile's model-cache
	// check is exercised the same way either way in these tests.
	p := inference.New(7680, c, c)
	svc := &servicesv1beta1.FrameService{
		ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "research"},
		Spec: servicesv1beta1.FrameServiceSpec{
			Type:         "inference",
			ServiceClass: "HIGH",
			Parameters: map[string]framev1beta1.ParameterValue{
				"model":         "llama-3.1-8b-instruct",
				"contextLength": "8192",
			},
		},
	}
	return p, c, svc
}

// newReconcileFixture is newFixture with the default model cache PVC already
// present — the common case every test but the missing-cache one wants.
func newReconcileFixture(t *testing.T) (*inference.Provider, client.Client, *servicesv1beta1.FrameService) {
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
	sizing, err := p.Size(provider.Params(svc.Spec.Parameters))
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
	if len(owners) != 1 || owners[0].Name != svc.Name || owners[0].Kind != frameServiceKind {
		t.Fatalf("Deployment owner references = %v, want a single FrameService owner", owners)
	}

	var s corev1.Service
	if err := c.Get(context.Background(),
		types.NamespacedName{Name: "llama", Namespace: "research"}, &s); err != nil {
		t.Fatalf("Service not created: %v", err)
	}
	svcOwners := s.GetOwnerReferences()
	if len(svcOwners) != 1 || svcOwners[0].Name != svc.Name || svcOwners[0].Kind != frameServiceKind {
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
	// ContainerPort.Protocol defaults to TCP exactly like ServicePort.Protocol
	// does — this is the field the previous fix round's version of this test
	// never seeded, which is why a wholesale-replaced Ports slice one level
	// inside the container shipped undetected.
	d.Spec.Template.Spec.Containers[0].Ports[0].Protocol = corev1.ProtocolTCP
	// Kubernetes defaults a missing Requests entry for an extended resource
	// from its Limits entry when only Limits is set, so a real apiserver adds
	// this key on its own; nothing in this provider ever writes it directly.
	d.Spec.Template.Spec.Containers[0].Resources.Requests["nvidia.com/gpu"] = resource.MustParse("1")
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
	if c2.Ports[0].Protocol != corev1.ProtocolTCP {
		t.Fatalf("ContainerPort.Protocol = %q, want it to survive a second Reconcile", c2.Ports[0].Protocol)
	}
	if c2.Resources.Requests["nvidia.com/gpu"] != resource.MustParse("1") {
		t.Fatalf("Requests[nvidia.com/gpu] = %v, want the apiserver-defaulted value to survive a second Reconcile",
			c2.Resources.Requests["nvidia.com/gpu"])
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
	if result.Reason != rolloutInProgress {
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

// TestReconcileDegradesWhenModelCacheCheckFails pins the fix for this
// branch's own Critical: any error other than NotFound while checking for
// the model-cache PVC — Forbidden included — must degrade with its own named
// reason instead of returning a bare error. A bare error takes
// FrameServiceReconciler's error path, which never calls setStatus, so the
// FrameService would be left with empty status forever: nothing for
// `kubectl describe` to show, indistinguishable from a resource still being
// admitted. Every other test in this file seeds the PVC the way a cluster
// with correct RBAC would, so none of them would catch a regression here — a
// refactor that quietly restored `return provider.Result{}, err` on this
// branch would pass every one of them while silently reintroducing the bug.
func TestReconcileDegradesWhenModelCacheCheckFails(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = servicesv1beta1.AddToScheme(scheme)

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	// Only the PVC lookup is intercepted: everything else — the Deployment
	// and Service CreateOrUpdate calls Reconcile would reach next, if this
	// branch failed to stop it from getting there — goes through the real
	// fake client unchanged.
	apiReader := interceptor.NewClient(c, interceptor.Funcs{
		Get: func(ctx context.Context, inner client.WithWatch, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
				return apierrors.NewInternalError(errors.New("etcd unavailable"))
			}
			return inner.Get(ctx, key, obj, opts...)
		},
	})

	p := inference.New(7680, c, apiReader)
	svc := &servicesv1beta1.FrameService{
		ObjectMeta: metav1.ObjectMeta{Name: "llama", Namespace: "research"},
		Spec: servicesv1beta1.FrameServiceSpec{
			Type:         "inference",
			ServiceClass: "HIGH",
			Parameters: map[string]framev1beta1.ParameterValue{
				"model":         "llama-3.1-8b-instruct",
				"contextLength": "8192",
			},
		},
	}

	result, err := p.Reconcile(context.Background(), svc)
	if err != nil {
		t.Fatalf("Reconcile returned an error %v, want a degraded Result instead", err)
	}
	if result.Ready {
		t.Fatal("Ready = true despite the model-cache check failing")
	}
	if result.Reason != "ModelCacheCheckFailed" {
		t.Fatalf("Reason = %q, want ModelCacheCheckFailed", result.Reason)
	}
	for _, want := range []string{defaultModelCachePVC, "research"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("Message %q does not name %q", result.Message, want)
		}
	}

	var deployments appsv1.DeploymentList
	if err := c.List(context.Background(), &deployments); err != nil {
		t.Fatalf("listing Deployments: %v", err)
	}
	if len(deployments.Items) != 0 {
		t.Fatalf("a Deployment was created despite the model-cache check failing: %v", deployments.Items)
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
	if strings.Contains(b.Endpoint, "token") || strings.Contains(b.Endpoint, "@") || strings.Contains(b.Endpoint, "key") {
		t.Fatalf("endpoint %q looks like it carries a credential", b.Endpoint)
	}
}

// apiKeySecretNameForTest mirrors the provider's own apiKeySecretName, which
// is unexported: this file is package inference_test and can only observe
// the Secret it produces, not call the naming helper directly.
func apiKeySecretNameForTest(svcName string) string {
	return svcName + "-inference-key"
}

// TestReconcileGeneratesAnAPIKeyWhenNoneExists pins the first half of the
// stability contract: a fresh instance gets a real, non-empty token minted
// from crypto/rand, not an empty or placeholder value.
func TestReconcileGeneratesAnAPIKeyWhenNoneExists(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}

	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Name: apiKeySecretNameForTest(svc.Name), Namespace: svc.Namespace}, &secret); err != nil {
		t.Fatalf("API key Secret not created: %v", err)
	}
	token := secret.Data["apiKey"]
	if len(token) == 0 {
		t.Fatal("API key Secret has no apiKey data")
	}
	// 32 bytes of entropy, base64url-encoded without padding, is 43
	// characters — long enough that a placeholder or truncated value would
	// be caught here.
	if len(token) < 40 {
		t.Fatalf("apiKey %q looks too short to be 32 bytes of entropy", token)
	}

	// Ownership: the API key Secret carries a credential, so — like the
	// Deployment and Service — it must be owner-referenced to the
	// FrameService and garbage-collected with it.
	owners := secret.GetOwnerReferences()
	if len(owners) != 1 || owners[0].Name != svc.Name || owners[0].Kind != frameServiceKind {
		t.Fatalf("API key Secret owner references = %v, want a single FrameService owner", owners)
	}
}

// TestReconcileReusesTheSameAPIKeyOnASecondPass pins the stability contract
// that actually matters: Reconcile runs repeatedly, and a fresh token on
// every pass would rewrite the Deployment (and invalidate every client
// holding the old value) forever.
func TestReconcileReusesTheSameAPIKeyOnASecondPass(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	var first corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Name: apiKeySecretNameForTest(svc.Name), Namespace: svc.Namespace}, &first); err != nil {
		t.Fatalf("API key Secret not created: %v", err)
	}
	firstToken := string(first.Data["apiKey"])

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	var second corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Name: apiKeySecretNameForTest(svc.Name), Namespace: svc.Namespace}, &second); err != nil {
		t.Fatalf("API key Secret missing after second Reconcile: %v", err)
	}
	if string(second.Data["apiKey"]) != firstToken {
		t.Fatalf("apiKey changed between reconciles: %q -> %q", firstToken, string(second.Data["apiKey"]))
	}
}

// TestReconcileWiresTheAPIKeyIntoTheContainerAsAnEnvVar pins the exposure
// decision: the token reaches llama.cpp through LLAMA_API_KEY sourced from
// the Secret, never as a --api-key argument, which kubectl describe pod and
// Events would expose to a far wider audience than whoever can read the
// Secret.
func TestReconcileWiresTheAPIKeyIntoTheContainerAsAnEnvVar(t *testing.T) {
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

	var found *corev1.EnvVar
	for i := range container.Env {
		if container.Env[i].Name == "LLAMA_API_KEY" {
			found = &container.Env[i]
		}
	}
	if found == nil {
		t.Fatalf("no LLAMA_API_KEY env var: %v", container.Env)
	}
	if found.Value != "" {
		t.Fatalf("LLAMA_API_KEY.Value = %q, want it sourced from a Secret, not set inline", found.Value)
	}
	if found.ValueFrom == nil || found.ValueFrom.SecretKeyRef == nil {
		t.Fatal("LLAMA_API_KEY has no ValueFrom.SecretKeyRef")
	}
	if got := found.ValueFrom.SecretKeyRef.Name; got != apiKeySecretNameForTest(svc.Name) {
		t.Fatalf("SecretKeyRef.Name = %q, want %q", got, apiKeySecretNameForTest(svc.Name))
	}
	if got := found.ValueFrom.SecretKeyRef.Key; got != "apiKey" {
		t.Fatalf("SecretKeyRef.Key = %q, want apiKey", got)
	}

	// The whole point: nothing about the key ever appears in Args, which is
	// visible to anyone who can kubectl describe the pod.
	for _, a := range container.Args {
		if strings.Contains(strings.ToLower(a), "api-key") {
			t.Fatalf("args %v mention the API key flag; it must only reach the container via env", container.Args)
		}
	}
}

// TestBindReturnsTheAPIKeyAlongsideTheEndpoint pins the last leg: Bind must
// hand the same token back to the controller so it lands in the credentials
// Secret, and it must be the exact value ensureAPIKey already committed to
// during Reconcile — not a second, independently generated one.
func TestBindReturnsTheAPIKeyAlongsideTheEndpoint(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Name: apiKeySecretNameForTest(svc.Name), Namespace: svc.Namespace}, &secret); err != nil {
		t.Fatalf("API key Secret not created: %v", err)
	}
	wantToken := string(secret.Data["apiKey"])

	b, err := p.Bind(ctx, svc)
	if err != nil {
		t.Fatalf("Bind returned %v", err)
	}
	gotToken, ok := b.Data["apiKey"]
	if !ok {
		t.Fatalf("Bind Data = %v, want an apiKey entry", b.Data)
	}
	if string(gotToken) != wantToken {
		t.Fatalf("Bind's apiKey = %q, want the same token Reconcile committed to the Secret (%q)", gotToken, wantToken)
	}
	if string(b.Data["endpoint"]) != b.Endpoint {
		t.Fatalf("Bind Data[endpoint] = %q, want it to match Endpoint %q", b.Data["endpoint"], b.Endpoint)
	}
}

// TestReconcileDegradesOnCreateContainerConfigError pins the second half of
// the Deployment-before-Secret ordering fix: ensureAPIKey closes the
// ordinary race, but a pod can still end up unable to read its Secret for
// reasons outside this provider's control (an operator deleting it by hand,
// RBAC narrowed after the fact). That must surface as a named, actionable
// degrade rather than reading like an ordinary rollout that never finishes.
func TestReconcileDegradesOnCreateContainerConfigError(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "llama-pod",
			Namespace: svc.Namespace,
			Labels:    map[string]string{"app": svc.Name},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "llama-cpp",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "CreateContainerConfigError",
							Message: `secret "llama-inference-key" not found`,
						},
					},
				},
			},
		},
	}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatalf("seeding the failing Pod: %v", err)
	}
	if err := c.Status().Update(ctx, pod); err != nil {
		t.Fatalf("setting Pod status: %v", err)
	}

	result, err := p.Reconcile(ctx, svc)
	if err != nil {
		t.Fatalf("Reconcile returned an error %v, want a degraded Result instead", err)
	}
	if result.Ready {
		t.Fatal("Ready = true with a pod stuck in CreateContainerConfigError")
	}
	if result.Reason != "CreateContainerConfigError" {
		t.Fatalf("Reason = %q, want CreateContainerConfigError", result.Reason)
	}
	if !strings.Contains(result.Message, "llama-pod") || !strings.Contains(result.Message, "llama-cpp") {
		t.Fatalf("Message %q does not name the pod and container", result.Message)
	}
}

// seedFailingPod creates a Pod carrying containerStatus and returns it, so the
// crash-loop tests below differ only in the status they are proving.
func seedFailingPod(t *testing.T, ctx context.Context, c client.Client,
	svc *servicesv1beta1.FrameService, name string, cs corev1.ContainerStatus) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: svc.Namespace,
			Labels:    map[string]string{"app": svc.Name},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{cs}},
	}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatalf("seeding the failing Pod: %v", err)
	}
	if err := c.Status().Update(ctx, pod); err != nil {
		t.Fatalf("setting Pod status: %v", err)
	}
}

// TestReconcileDegradesOnCrashLoopWithTheTerminationReason is the test for the
// one way the pod hardening can itself break a user.
//
// runAsUser 65532 against a model cache that is not world-readable makes
// llama-server take EACCES on the GGUF and exit non-zero, so the pod goes
// CrashLoopBackOff — it starts and dies, rather than failing to start. Before
// this was handled, the pod-inspection loop matched only
// CreateContainerConfigError, so a crash loop fell through to the generic
// fallback and the FrameService sat at Degraded/RolloutInProgress forever
// while the pod restarted behind it. The cause was only ever in kubectl logs.
//
// The specific trap this pins: CrashLoopBackOff's own Waiting.Message says
// nothing useful ("back-off 5m0s restarting failed container"). Reporting only
// that would technically name the reason while still explaining nothing, so
// the assertion below requires the *termination* detail to reach status too.
func TestReconcileDegradesOnCrashLoopWithTheTerminationReason(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}

	seedFailingPod(t, ctx, c, svc, "llama-pod", corev1.ContainerStatus{
		Name: "llama-cpp",
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason:  "CrashLoopBackOff",
				Message: "back-off 5m0s restarting failed container=llama-cpp",
			},
		},
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				Reason:   "Error",
				ExitCode: 1,
				Message:  "error loading model /models/llama-3.1-8b-instruct.gguf: permission denied",
			},
		},
	})

	result, err := p.Reconcile(ctx, svc)
	if err != nil {
		t.Fatalf("Reconcile returned an error %v, want a degraded Result instead", err)
	}
	if result.Ready {
		t.Fatal("Ready = true with a pod in CrashLoopBackOff")
	}
	if result.Reason == rolloutInProgress {
		t.Fatal("a crash-looping pod reported as RolloutInProgress: this is the undiagnosable state the pod List exists to prevent")
	}
	if result.Reason != "CrashLoopBackOff" {
		t.Fatalf("Reason = %q, want CrashLoopBackOff", result.Reason)
	}
	if !strings.Contains(result.Message, "llama-pod") || !strings.Contains(result.Message, "llama-cpp") {
		t.Fatalf("Message %q does not name the pod and container", result.Message)
	}
	// The whole point: the actual cause, not just the back-off notice.
	if !strings.Contains(result.Message, "permission denied") {
		t.Fatalf("Message %q does not carry the termination message, so status still does not explain the failure", result.Message)
	}
	if !strings.Contains(result.Message, "exited with code 1") {
		t.Fatalf("Message %q does not carry the exit code", result.Message)
	}
}

// TestReconcileTruncatesARunawayTerminationMessage guards the other direction.
// A container's termination message is attacker- and accident-influenced
// output that lands in the CR's status, which kubectl describe prints and
// every watcher of the object receives on every event. It has to be bounded.
func TestReconcileTruncatesARunawayTerminationMessage(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}

	seedFailingPod(t, ctx, c, svc, "llama-pod", corev1.ContainerStatus{
		Name: "llama-cpp",
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
		},
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 2,
				Message:  strings.Repeat("stack trace line\n", 4000),
			},
		},
	})

	result, err := p.Reconcile(ctx, svc)
	if err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}
	if len(result.Message) > 1024 {
		t.Fatalf("degrade message is %d bytes; an unbounded termination message reaches every watcher of the CR", len(result.Message))
	}
	if !strings.Contains(result.Message, "stack trace line") {
		t.Fatalf("Message %q truncated away all of the cause", result.Message)
	}
}

// TestReconcileStillReportsRolloutInProgressWhileStarting keeps the widened
// reason list from swallowing an ordinary rollout. ContainerCreating is what a
// healthy pod passes through on its way up; reporting that as a named degrade
// would make every fresh instance look broken for its first few seconds.
func TestReconcileStillReportsRolloutInProgressWhileStarting(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}

	seedFailingPod(t, ctx, c, svc, "llama-pod", corev1.ContainerStatus{
		Name: "llama-cpp",
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"},
		},
	})

	result, err := p.Reconcile(ctx, svc)
	if err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}
	if result.Reason != rolloutInProgress {
		t.Fatalf("Reason = %q for a pod that is merely still starting, want RolloutInProgress", result.Reason)
	}
}

// apiKeyDigestAnnotationForTest mirrors the provider's own unexported
// apiKeyDigestAnnotation, for the same reason apiKeySecretNameForTest does:
// this file is package inference_test and can only observe what the
// provider produces.
const apiKeyDigestAnnotationForTest = "frame.plume-labs.io/api-key-sha256"

// TestReconcileRollsThePodOntoARegeneratedAPIKey pins the fix for a silent
// authentication outage: deleting the API key Secret out from under a
// running instance used to mint a new token and republish it through Bind
// while the already-running pod kept the old value in its process
// environment forever, because Kubernetes never live-updates an env var
// sourced from a secretKeyRef. A SHA-256 digest of the token on the pod
// template turns a regenerated token into a real template diff, so
// CreateOrUpdate updates the Deployment and Kubernetes rolls the pod. This
// test proves both halves: a new token is minted, and the template actually
// changed — the second is what proves a rollout will follow, since nothing
// here can observe a real rollout against a fake client.
func TestReconcileRollsThePodOntoARegeneratedAPIKey(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	var firstSecret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Name: apiKeySecretNameForTest(svc.Name), Namespace: svc.Namespace}, &firstSecret); err != nil {
		t.Fatalf("API key Secret not created: %v", err)
	}
	firstToken := string(firstSecret.Data["apiKey"])

	var d appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: "research"}, &d); err != nil {
		t.Fatalf("Deployment not created: %v", err)
	}
	firstDigest := d.Spec.Template.Annotations[apiKeyDigestAnnotationForTest]
	if firstDigest == "" {
		t.Fatal("pod template has no api-key digest annotation after the first Reconcile")
	}
	// Mark the instance running, the way TestReconcileReportsNotReadyUntilThePodIsServing does.
	d.Status.ReadyReplicas = 1
	if err := c.Status().Update(ctx, &d); err != nil {
		t.Fatalf("marking the Deployment ready: %v", err)
	}

	// The operator (or anything else) deletes the Secret out from under the
	// running instance.
	if err := c.Delete(ctx, &firstSecret); err != nil {
		t.Fatalf("deleting the API key Secret: %v", err)
	}

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}

	var secondSecret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Name: apiKeySecretNameForTest(svc.Name), Namespace: svc.Namespace}, &secondSecret); err != nil {
		t.Fatalf("API key Secret missing after second Reconcile: %v", err)
	}
	secondToken := string(secondSecret.Data["apiKey"])
	if secondToken == "" {
		t.Fatal("no token minted after the Secret was deleted")
	}
	if secondToken == firstToken {
		t.Fatal("token unchanged after the Secret was deleted, want a freshly minted one")
	}

	var d2 appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: "research"}, &d2); err != nil {
		t.Fatalf("Deployment missing after second Reconcile: %v", err)
	}
	secondDigest := d2.Spec.Template.Annotations[apiKeyDigestAnnotationForTest]
	if secondDigest == "" {
		t.Fatal("pod template lost its api-key digest annotation")
	}
	if secondDigest == firstDigest {
		t.Fatal("pod template digest unchanged after the token was regenerated: no rollout would follow")
	}
}

// hardenedRunAsUser is the uid inference.Provider runs llama.cpp as. Asserted
// as an unprivileged uid rather than pinned to a magic number for its own
// sake: what matters is that it is not 0.
const hardenedRunAsUser = int64(65532)

// TestReconcileHardensTheInferencePod pins the securityContext on the pod this
// provider creates. Before this existed the Deployment carried none at all, so
// every inference pod ran as root, with a writable root filesystem, the full
// default capability set including NET_RAW, no seccomp profile, and the
// namespace default ServiceAccount's token mounted. llama.cpp parses untrusted
// input in C++, so that combination turned any memory-safety bug in it into
// immediate lateral movement.
//
// Each assertion below is a distinct containment property, so they are checked
// individually rather than by comparing whole structs: a struct comparison
// would fail as one opaque diff, and would also break the moment a field this
// provider does not own is added by something else.
func TestReconcileHardensTheInferencePod(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}

	var d appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: "research"}, &d); err != nil {
		t.Fatalf("Deployment not created: %v", err)
	}
	pod := d.Spec.Template.Spec

	// No ServiceAccount token in the pod: this container never calls the
	// Kubernetes API, so mounting one only ever helps an attacker.
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Error("AutomountServiceAccountToken is not false: a compromised llama.cpp would inherit the namespace default SA")
	}

	if pod.SecurityContext == nil {
		t.Fatal("pod has no securityContext at all")
	}
	if pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
		t.Error("pod securityContext.runAsNonRoot is not true")
	}
	if pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser != hardenedRunAsUser {
		t.Errorf("pod securityContext.runAsUser = %v, want %d", pod.SecurityContext.RunAsUser, hardenedRunAsUser)
	}
	if pod.SecurityContext.SeccompProfile == nil ||
		pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod seccompProfile = %v, want RuntimeDefault", pod.SecurityContext.SeccompProfile)
	}

	container := pod.Containers[0]
	if container.SecurityContext == nil {
		t.Fatal("container has no securityContext at all")
	}
	sc := container.SecurityContext
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("container allowPrivilegeEscalation is not false")
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Error("container readOnlyRootFilesystem is not true")
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("container securityContext.runAsNonRoot is not true")
	}
	if sc.RunAsUser == nil || *sc.RunAsUser != hardenedRunAsUser {
		t.Errorf("container securityContext.runAsUser = %v, want %d", sc.RunAsUser, hardenedRunAsUser)
	}
	// Dropping ALL is the whole point; dropping a named subset would leave
	// NET_RAW and CHOWN behind, which is most of what is worth having.
	if sc.Capabilities == nil ||
		len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != corev1.Capability("ALL") {
		t.Errorf("container capabilities.drop = %v, want exactly [ALL]", sc.Capabilities)
	}
	if len(sc.Capabilities.Add) != 0 {
		t.Errorf("container capabilities.add = %v, want nothing added back", sc.Capabilities.Add)
	}

}

// TestReconcileGivesTheReadOnlyRootFilesystemSomewhereWritable is the other
// half of TestReconcileHardensTheInferencePod, split out because the two
// together exceed the repo's cyclomatic-complexity limit. readOnlyRootFilesystem
// is only survivable if the process has one writable path; this pins that the
// path exists, that it is a per-pod emptyDir rather than anything shared or
// host-backed, and that adding it did not quietly make the shared model cache
// writable too.
func TestReconcileGivesTheReadOnlyRootFilesystemSomewhereWritable(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("Reconcile returned %v", err)
	}

	var d appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: "research"}, &d); err != nil {
		t.Fatalf("Deployment not created: %v", err)
	}
	pod := d.Spec.Template.Spec

	var tmp *corev1.Volume
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == "tmp" {
			tmp = &pod.Volumes[i]
		}
	}
	if tmp == nil {
		t.Fatal("no tmp volume: a read-only root filesystem with no writable path is a crash waiting for the first write")
	}
	if tmp.EmptyDir == nil {
		t.Errorf("tmp volume is %v, want an emptyDir", tmp.VolumeSource)
	}
	if tmp.HostPath != nil {
		t.Errorf("tmp volume is a hostPath (%v), which would be a node escape hatch", tmp.HostPath)
	}

	var tmpMount, modelMount *corev1.VolumeMount
	for i := range pod.Containers[0].VolumeMounts {
		switch pod.Containers[0].VolumeMounts[i].Name {
		case "tmp":
			tmpMount = &pod.Containers[0].VolumeMounts[i]
		case "model-cache":
			modelMount = &pod.Containers[0].VolumeMounts[i]
		}
	}
	if tmpMount == nil || tmpMount.MountPath != "/tmp" {
		t.Errorf("tmp mount = %v, want it mounted at /tmp", tmpMount)
	}
	// The hardening must not have quietly made the shared model cache
	// writable: several instances share it, and none may corrupt it.
	if modelMount == nil || !modelMount.ReadOnly {
		t.Errorf("model-cache mount = %v, want it still mounted read-only", modelMount)
	}
}

// TestReconcilePreservesSecurityContextFieldsItDoesNotOwn pins why the
// securityContext helpers set individual fields on an existing struct instead
// of assigning a fresh one. A mutating admission webhook — a policy engine, a
// service mesh — may add fields to this pod template. If Reconcile replaced
// the whole SecurityContext each pass it would drop them, CreateOrUpdate would
// see a diff and Update, and that Update would re-trigger a reconcile: exactly
// the hot loop this file already had to close twice, in a third field.
//
// Same technique as TestReconcileDoesNotFightApiserverDefaults: apply the
// foreign fields by hand between two Reconcile calls, then assert both that
// they survived and that the owned fields are still right.
func TestReconcilePreservesSecurityContextFieldsItDoesNotOwn(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	ctx := context.Background()

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}

	var d appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: "research"}, &d); err != nil {
		t.Fatalf("Deployment not created: %v", err)
	}
	fsGroup := int64(2000)
	runAsGroup := int64(3000)
	d.Spec.Template.Spec.SecurityContext.FSGroup = &fsGroup
	d.Spec.Template.Spec.Containers[0].SecurityContext.RunAsGroup = &runAsGroup
	if err := c.Update(ctx, &d); err != nil {
		t.Fatalf("simulating a mutating webhook on the Deployment: %v", err)
	}

	if _, err := p.Reconcile(ctx, svc); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}

	var d2 appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: "research"}, &d2); err != nil {
		t.Fatalf("Deployment missing after second Reconcile: %v", err)
	}
	pod := d2.Spec.Template.Spec
	if pod.SecurityContext.FSGroup == nil || *pod.SecurityContext.FSGroup != fsGroup {
		t.Errorf("pod securityContext.fsGroup = %v, want the foreign value %d to survive",
			pod.SecurityContext.FSGroup, fsGroup)
	}
	sc := pod.Containers[0].SecurityContext
	if sc.RunAsGroup == nil || *sc.RunAsGroup != runAsGroup {
		t.Errorf("container securityContext.runAsGroup = %v, want the foreign value %d to survive",
			sc.RunAsGroup, runAsGroup)
	}

	// And the fields this provider does own are still what it set.
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Error("container readOnlyRootFilesystem lost on the second pass")
	}
	if pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser != hardenedRunAsUser {
		t.Errorf("pod securityContext.runAsUser = %v after the second pass, want %d",
			pod.SecurityContext.RunAsUser, hardenedRunAsUser)
	}
}

// TestReconcileSetsPriorityClassFromServiceClass pins F10: a FrameService's
// scheduling priority is derived from spec.serviceClass through the same
// mapping the FrameJob controller uses, not from a field of its own.
func TestReconcileSetsPriorityClassFromServiceClass(t *testing.T) {
	for serviceClass, want := range map[framev1beta1.ServiceClass]string{
		framev1beta1.ServiceClassHigh:   "frame-high",
		framev1beta1.ServiceClassMedium: "frame-medium",
		framev1beta1.ServiceClassLow:    "frame-low",
	} {
		t.Run(string(serviceClass), func(t *testing.T) {
			// newReconcileFixture and newFixture are the helpers this file
			// already uses for every other Deployment assertion; reuse them
			// rather than building a second fixture shape.
			p, c, svc := newReconcileFixture(t)
			svc.Spec.ServiceClass = serviceClass

			if _, err := p.Reconcile(context.Background(), svc); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			var d appsv1.Deployment
			key := types.NamespacedName{Name: svc.Name, Namespace: svc.Namespace}
			if err := c.Get(context.Background(), key, &d); err != nil {
				t.Fatalf("get Deployment: %v", err)
			}
			if got := d.Spec.Template.Spec.PriorityClassName; got != want {
				t.Fatalf("priorityClassName = %q, want %q", got, want)
			}
		})
	}
}

// TestReconcileOmitsPriorityClassForAnUnrecognisedServiceClass pins the other
// half of PriorityClassForServiceClass's contract: an unrecognised
// serviceClass must not propagate a garbage priorityClassName onto the pod
// spec, which the apiserver would reject outright (a pod naming a
// PriorityClass that does not exist fails admission — unlike an empty
// priorityClassName, which just leaves the pod at the cluster's implicit
// default). The webhook layer is expected to reject an unrecognised
// serviceClass before this is ever reached; this test pins Reconcile's own
// behaviour in isolation, independent of that layer.
func TestReconcileOmitsPriorityClassForAnUnrecognisedServiceClass(t *testing.T) {
	p, c, svc := newReconcileFixture(t)
	svc.Spec.ServiceClass = "NOT-A-REAL-CLASS"

	if _, err := p.Reconcile(context.Background(), svc); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var d appsv1.Deployment
	key := types.NamespacedName{Name: svc.Name, Namespace: svc.Namespace}
	if err := c.Get(context.Background(), key, &d); err != nil {
		t.Fatalf("get Deployment: %v", err)
	}
	if got := d.Spec.Template.Spec.PriorityClassName; got != "" {
		t.Fatalf("priorityClassName = %q, want empty for an unrecognised serviceClass", got)
	}
}
