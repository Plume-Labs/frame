// Package inference provisions llama.cpp model servers.
//
// llama.cpp is the only backend this package supports, and that is a hardware
// fact rather than a preference: the cluster's Tesla P4 is Pascal, compute
// capability 6.1, and vLLM and KubeAI both need sm_7.0 or newer. The choice is
// internal to this provider, so a newer card means a new implementation behind
// the same spec.type, not an API change.
package inference

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	"github.com/rmocq/frame/internal/services/provider"
)

const (
	// defaultContextLength is what llama.cpp itself defaults to.
	defaultContextLength = 4096
	// bytesPerMiB converts the KV cache arithmetic into the unit the catalog
	// and the resource requests both speak.
	bytesPerMiB = 1024 * 1024
	// containerPort is the port llama.cpp's server binds and the Service
	// forwards to. Fixed rather than configurable: nothing in spec.parameters
	// needs to reach it, and the Deployment and Service must always agree.
	containerPort = 8080
	// serviceClassLabel is the label the FrameNode controller already writes
	// on every node (see internal/controller/frame/framenode_controller.go).
	// Reusing it as a nodeSelector is what places the pod on a node of the
	// requested class without this package ever naming a node: Frame decides
	// placement, not the provider.
	serviceClassLabel = "frame.plume-labs.io/service-class"
)

// Provider serves models with llama.cpp.
type Provider struct {
	// gpuMemoryMiB is the memory of the card an instance will land on. Injected
	// rather than probed so the arithmetic is testable and so a heterogeneous
	// cluster can size against the smallest card rather than the largest.
	gpuMemoryMiB int64
	// client creates the Deployment and Service a FrameService needs. Reconcile
	// and Bind are the only methods that touch it; Type, ParameterSchema and
	// Size never do, so a caller that only validates and sizes — the webhook —
	// can pass nil.
	client client.Client
}

// New builds the provider for a card of the given size, using c to create and
// converge the workload. c may be nil for a caller that only ever calls Size
// and ParameterSchema — the webhook is the only such caller today — since
// Reconcile and Bind are the sole methods that dereference it.
func New(gpuMemoryMiB int64, c client.Client) *Provider {
	return &Provider{gpuMemoryMiB: gpuMemoryMiB, client: c}
}

func (p *Provider) Type() string { return "inference" }

func (p *Provider) ParameterSchema() *provider.Schema {
	return &apiextensionsv1.JSONSchemaProps{
		Type:     "object",
		Required: []string{"model"},
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"model": {
				Type:        "string",
				Description: "Model to serve. Must be one of: " + strings.Join(knownModels(), ", "),
				Enum:        enumOf(knownModels()),
			},
			"contextLength": {
				Type: "string",
				Description: fmt.Sprintf(
					"Context window in tokens, as a string. Defaults to %d. Sized against the "+
						"GPU: an oversized window is refused here rather than exhausting the KV "+
						"cache at runtime.", defaultContextLength),
				Pattern: `^[0-9]+$`,
			},
		},
	}
}

func enumOf(values []string) []apiextensionsv1.JSON {
	out := make([]apiextensionsv1.JSON, 0, len(values))
	for _, v := range values {
		out = append(out, apiextensionsv1.JSON{Raw: []byte(strconv.Quote(v))})
	}
	return out
}

// Size derives the footprint from the model and the requested context window,
// and refuses anything that will not fit the card.
func (p *Provider) Size(params map[string]string) (provider.Sizing, error) {
	name := params["model"]
	m, ok := catalog[name]
	if !ok {
		return provider.Sizing{}, fmt.Errorf(
			"parameters.model %q is not a model this provider serves: known models are %s",
			name, strings.Join(knownModels(), ", "))
	}

	ctx, err := resolveContextLength(params)
	if err != nil {
		return provider.Sizing{}, err
	}

	// The weights are the floor: no context window makes an oversized model fit,
	// so say that rather than blaming the context the operator can change.
	if m.WeightsMiB >= p.gpuMemoryMiB {
		return provider.Sizing{}, fmt.Errorf(
			"model %q needs %dMi for weights alone, and the GPU has %dMi",
			name, m.WeightsMiB, p.gpuMemoryMiB)
	}

	// maxTokens is the largest context this card's remaining memory could ever
	// hold, derived by division so computing it can never overflow — unlike
	// ctx times the per-token KV cost below, which can, for a contextLength
	// large enough. The subtraction is positive: the weights-alone case above
	// already returned otherwise.
	maxTokens := (p.gpuMemoryMiB - m.WeightsMiB) * bytesPerMiB / m.kvBytesPerToken()

	// Past this point ctx*kvBytesPerToken, or the ceiling division's
	// +(bytesPerMiB-1) immediately below, would wrap int64 silently, which
	// can turn a request that plainly does not fit into one whose (wrapped,
	// possibly negative) footprint looks like it does. The bound covers the
	// whole expression that follows, not just the multiplication: bounding
	// the product alone still leaves a band of ctx values whose sum with
	// (bytesPerMiB-1) overflows on its own. Refuse before ever forming
	// either, naming the context that would actually have fit rather than
	// just the verdict.
	if ctx > (math.MaxInt64-(bytesPerMiB-1))/m.kvBytesPerToken() {
		return provider.Sizing{}, fmt.Errorf(
			"parameters.contextLength %d is too large to size for model %q: the %dMi GPU "+
				"holds at most %d tokens of context alongside the %dMi weights",
			ctx, name, p.gpuMemoryMiB, maxTokens, m.WeightsMiB)
	}

	kvMiB := (m.kvBytesPerToken()*ctx + bytesPerMiB - 1) / bytesPerMiB
	totalMiB := m.WeightsMiB + kvMiB
	if totalMiB > p.gpuMemoryMiB {
		return provider.Sizing{}, fmt.Errorf(
			"model %q at contextLength %d needs %dMi (%dMi weights + %dMi KV cache) "+
				"and the GPU has %dMi: lower contextLength or pick a smaller model",
			name, ctx, totalMiB, m.WeightsMiB, kvMiB, p.gpuMemoryMiB)
	}

	return provider.Sizing{
		GPU:       "1",
		GPUMemory: fmt.Sprintf("%dMi", totalMiB),
		// Host resources scale off the model rather than being guessed: llama.cpp
		// needs enough RAM to load and mmap the weights before they reach the GPU.
		CPU:    "4",
		Memory: fmt.Sprintf("%dMi", m.WeightsMiB*2),
	}, nil
}

// resolveContextLength parses parameters.contextLength the same way Size
// does — same default, same validation — so Reconcile starts llama.cpp with
// exactly the context window Size costed. Deriving it twice, independently,
// would risk the two silently drifting apart the next time either is edited.
func resolveContextLength(params map[string]string) (int64, error) {
	ctx := int64(defaultContextLength)
	if raw, set := params["contextLength"]; set && raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			return 0, fmt.Errorf(
				"parameters.contextLength %q is not a positive whole number of tokens", raw)
		}
		ctx = parsed
	}
	return ctx, nil
}

// modelPath is where llama.cpp is told to find the weights inside the
// container.
//
// NOTE: nothing in this package, or in the plan this provider was built
// against, arranges for the file to actually be there — there is no volume,
// no init container, and no download step wired up. This path is a
// placeholder naming convention only; a pod scheduled from today's Deployment
// will crash-loop looking for it. Getting the weights onto the node (a
// pre-populated PVC, an init container pulling from a model registry, a
// hostPath, ...) is a real design decision — it decides whether the object
// holding them counts as "data" under the ownership contract in provider.go —
// and is left for a follow-up task rather than guessed here.
func modelPath(model string) string {
	return fmt.Sprintf("/models/%s.gguf", model)
}

// Reconcile creates or converges the Deployment and Service a FrameService
// needs. Both are exposing objects — a consumer reaches the instance through
// the Service, and the Deployment is what makes it exist at all — so both
// carry SetControllerReference per the ownership contract on Provisioner.
// Reconcile: deleting the FrameService must garbage-collect them regardless
// of spec.deletionPolicy, which only ever governs data objects, and this
// provider creates none.
func (p *Provider) Reconcile(ctx context.Context, svc *servicesv1alpha1.FrameService) (provider.Result, error) {
	sizing, err := p.Size(svc.Spec.Parameters)
	if err != nil {
		return provider.Result{}, err
	}
	contextLength, err := resolveContextLength(svc.Spec.Parameters)
	if err != nil {
		return provider.Result{}, err
	}

	labels := map[string]string{"app": svc.Name}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: svc.Name, Namespace: svc.Namespace},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, p.client, deployment, func() error {
		replicas := int32(1)
		deployment.Spec.Replicas = &replicas
		deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deployment.Spec.Template.Labels = labels
		// Frame decides placement: the selector reaches the label the
		// FrameNode controller already writes rather than this provider ever
		// naming a node.
		deployment.Spec.Template.Spec.NodeSelector = map[string]string{
			serviceClassLabel: svc.Spec.ServiceClass,
		}
		deployment.Spec.Template.Spec.Containers = []corev1.Container{{
			Name:  "llama-cpp",
			Image: "ghcr.io/ggml-org/llama.cpp:server-cuda",
			Args: []string{
				"--host", "0.0.0.0",
				"--port", strconv.Itoa(containerPort),
				"-m", modelPath(svc.Spec.Parameters["model"]),
				"-c", strconv.FormatInt(contextLength, 10),
			},
			Ports: []corev1.ContainerPort{{ContainerPort: containerPort}},
			// The GPU is requested as a resource, never as a nodeName, so the
			// scheduler finds the card on its own and the same spec keeps
			// working the day a second one arrives. CPU and memory come from
			// Size rather than being recomputed here, so the resources a pod
			// actually gets are the ones the operator's request was costed
			// against.
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					"nvidia.com/gpu":      resource.MustParse("1"),
					corev1.ResourceCPU:    resource.MustParse(sizing.CPU),
					corev1.ResourceMemory: resource.MustParse(sizing.Memory),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(sizing.CPU),
					corev1.ResourceMemory: resource.MustParse(sizing.Memory),
				},
			},
		}}
		return controllerutil.SetControllerReference(svc, deployment, p.client.Scheme())
	}); err != nil {
		return provider.Result{}, fmt.Errorf("reconciling Deployment %s/%s: %w", svc.Namespace, svc.Name, err)
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: svc.Name, Namespace: svc.Namespace},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, p.client, service, func() error {
		service.Spec.Selector = labels
		service.Spec.Ports = []corev1.ServicePort{{
			Port:       containerPort,
			TargetPort: intstr.FromInt32(int32(containerPort)),
		}}
		return controllerutil.SetControllerReference(svc, service, p.client.Scheme())
	}); err != nil {
		return provider.Result{}, fmt.Errorf("reconciling Service %s/%s: %w", svc.Namespace, svc.Name, err)
	}

	return provider.Result{
		Ready:   true,
		Reason:  "Provisioned",
		Message: "Deployment and Service created",
		Provisioned: []servicesv1alpha1.ProvisionedRef{
			{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name, Namespace: deployment.Namespace},
			{APIVersion: "v1", Kind: "Service", Name: service.Name, Namespace: service.Namespace},
		},
	}, nil
}

// Bind returns the in-cluster DNS name of the Service Reconcile created.
// Nothing here can carry a credential — the Service exposes a bare HTTP
// endpoint — so Data only echoes the same endpoint under a key a consumer can
// look up without special-casing Binding.Endpoint.
func (p *Provider) Bind(_ context.Context, svc *servicesv1alpha1.FrameService) (provider.Binding, error) {
	endpoint := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", svc.Name, svc.Namespace, containerPort)
	return provider.Binding{
		Endpoint: endpoint,
		Data:     map[string][]byte{"endpoint": []byte(endpoint)},
	}, nil
}
