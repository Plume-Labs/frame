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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	// containerName identifies llama.cpp's container within the pod, so
	// Reconcile can find and update it in place on every pass rather than
	// replacing the whole Containers slice — see the comment on Reconcile.
	containerName = "llama-cpp"
	// modelCacheVolumeName is the Volume/VolumeMount name for the shared
	// model cache, distinct from any name an operator might reuse elsewhere
	// in a future container spec.
	modelCacheVolumeName = "model-cache"
	// modelMountPath is where the cache is mounted, and where modelPath looks
	// for the weights.
	modelMountPath = "/models"
	// defaultModelCache is the PersistentVolumeClaim this provider mounts
	// when spec.parameters.modelCache is unset. It is the same cache
	// deploy/jobs/speculative-decoding.yaml and deploy/jobs/pipeline-parallelism.yaml
	// already mount for their own inference workloads — this provider does
	// not create storage, it shares what the cluster already provisions.
	defaultModelCache = "model-cache-pvc"
	// desiredReplicas is how many pods this provider ever asks for. A
	// constant, not spec.replicas, because nothing in the spec asks for more
	// than one instance; Reconcile and the readiness check both reference it
	// so they can never disagree about what "ready" means.
	desiredReplicas = 1
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
			"modelCache": {
				Type: "string",
				Description: fmt.Sprintf(
					"Name of the PersistentVolumeClaim, already present in this namespace, that "+
						"holds cached GGUF weights. Defaults to %q. This provider mounts it "+
						"read-only rather than creating or populating it.", defaultModelCache),
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

// resolveModelCache names the PersistentVolumeClaim Reconcile mounts,
// defaulting when spec.parameters.modelCache is unset.
func resolveModelCache(params map[string]string) string {
	if name, set := params["modelCache"]; set && name != "" {
		return name
	}
	return defaultModelCache
}

// modelPath is where llama.cpp is told to find the weights inside the
// container: on the model cache volume, mounted at modelMountPath.
func modelPath(model string) string {
	return fmt.Sprintf("%s/%s.gguf", modelMountPath, model)
}

// Reconcile creates or converges the Deployment and Service a FrameService
// needs. Both are exposing objects — a consumer reaches the instance through
// the Service, and the Deployment is what makes it exist at all — so both
// carry SetControllerReference per the ownership contract on Provisioner.
// Reconcile: deleting the FrameService must garbage-collect them regardless
// of spec.deletionPolicy, which only ever governs data objects, and this
// provider creates none.
//
// The mutate closures below, and the setContainer/setContainerPort/
// setContainerResources/setModelCacheVolume/setServicePort helpers they call,
// set only the fields this provider owns and leave everything else on the
// fetched object untouched — down to individual slice entries and resource
// map keys, not just the top-level Containers/Ports slices. controller-runtime's
// CreateOrUpdate compares the object before and after the closure with
// equality.Semantic.DeepEqual and issues an Update on any difference; a real
// apiserver defaults fields these literals never set on their own
// (ImagePullPolicy, TerminationMessagePath/Policy on a container, Protocol on
// both a container port and a Service port, and a Requests entry mirrored
// from Limits for an extended resource like nvidia.com/gpu), so replacing any
// of those wholesale — the whole Containers slice, or a Ports/Resources field
// one level inside a single container — would make every Reconcile after the
// first see a diff, Update, and, because the controller Owns() both kinds,
// re-trigger itself. Forever. A fake client models none of that defaulting,
// which is why this failure mode is invisible to a naive test and had to be
// closed twice: first at the Containers-slice level, then again one level
// down inside a single container's Ports and Resources.
func (p *Provider) Reconcile(ctx context.Context, svc *servicesv1alpha1.FrameService) (provider.Result, error) {
	sizing, err := p.Size(svc.Spec.Parameters)
	if err != nil {
		return provider.Result{}, err
	}
	contextLength, err := resolveContextLength(svc.Spec.Parameters)
	if err != nil {
		return provider.Result{}, err
	}

	cacheName := resolveModelCache(svc.Spec.Parameters)
	var pvc corev1.PersistentVolumeClaim
	if err := p.client.Get(ctx, types.NamespacedName{Name: cacheName, Namespace: svc.Namespace}, &pvc); err != nil {
		if apierrors.IsNotFound(err) {
			// Not provisioning anything is the point: an operator reads why
			// nothing started here, instead of finding a pod stuck failing to
			// mount a volume that was never going to appear.
			return provider.Result{
				Ready:  false,
				Reason: "ModelCacheMissing",
				Message: fmt.Sprintf(
					"PersistentVolumeClaim %q not found in namespace %q: create the model cache "+
						"(or set parameters.modelCache to an existing one) before this instance can start",
					cacheName, svc.Namespace),
			}, nil
		}
		return provider.Result{}, fmt.Errorf("checking model cache PVC %s/%s: %w", svc.Namespace, cacheName, err)
	}

	labels := map[string]string{"app": svc.Name}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: svc.Name, Namespace: svc.Namespace},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, p.client, deployment, func() error {
		replicas := int32(desiredReplicas)
		deployment.Spec.Replicas = &replicas
		deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deployment.Spec.Template.Labels = labels
		// Frame decides placement: the selector reaches the label the
		// FrameNode controller already writes rather than this provider ever
		// naming a node.
		deployment.Spec.Template.Spec.NodeSelector = map[string]string{
			serviceClassLabel: svc.Spec.ServiceClass,
		}
		setModelCacheVolume(&deployment.Spec.Template.Spec, cacheName)
		// Read-only: several instances can share one cache, and none of them
		// should be able to corrupt it.
		mounts := []corev1.VolumeMount{
			{Name: modelCacheVolumeName, MountPath: modelMountPath, ReadOnly: true},
		}
		args := []string{
			"--host", "0.0.0.0",
			"--port", strconv.Itoa(containerPort),
			"-m", modelPath(svc.Spec.Parameters["model"]),
			"-c", strconv.FormatInt(contextLength, 10),
		}
		// The GPU is requested as a resource, never as a nodeName, so the
		// scheduler finds the card on its own and the same spec keeps working
		// the day a second one arrives. CPU and memory come from Size rather
		// than being recomputed here, so the resources a pod actually gets
		// are the ones the operator's request was costed against.
		setContainer(&deployment.Spec.Template.Spec, containerName,
			"ghcr.io/ggml-org/llama.cpp:server-cuda", args, containerPort, mounts, sizing)
		return controllerutil.SetControllerReference(svc, deployment, p.client.Scheme())
	}); err != nil {
		return provider.Result{}, fmt.Errorf("reconciling Deployment %s/%s: %w", svc.Namespace, svc.Name, err)
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: svc.Name, Namespace: svc.Namespace},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, p.client, service, func() error {
		service.Spec.Selector = labels
		setServicePort(&service.Spec, containerPort, intstr.FromInt32(int32(containerPort)))
		return controllerutil.SetControllerReference(svc, service, p.client.Scheme())
	}); err != nil {
		return provider.Result{}, fmt.Errorf("reconciling Service %s/%s: %w", svc.Namespace, svc.Name, err)
	}

	provisioned := []servicesv1alpha1.ProvisionedRef{
		{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name, Namespace: deployment.Namespace},
		{APIVersion: "v1", Kind: "Service", Name: service.Name, Namespace: service.Namespace},
	}

	// Ready means serving, not merely "the objects exist": CreateOrUpdate
	// succeeding says the Deployment was created or converged, not that its
	// pod ever started. deployment.Status here is whatever CreateOrUpdate's
	// own Get last observed — untouched by the mutate closure above — so a
	// freshly created Deployment (ReadyReplicas defaults to zero) or one
	// still rolling out correctly reports not-yet-ready rather than lying
	// about an endpoint nothing is serving on. The controller already
	// degrades and requeues on Ready: false, which is exactly right for a
	// rollout in progress.
	if deployment.Status.ReadyReplicas < desiredReplicas {
		return provider.Result{
			Ready:       false,
			Reason:      "RolloutInProgress",
			Message:     fmt.Sprintf("Deployment %s has %d/%d ready replicas", deployment.Name, deployment.Status.ReadyReplicas, desiredReplicas),
			Provisioned: provisioned,
		}, nil
	}

	return provider.Result{
		Ready:       true,
		Reason:      "Provisioned",
		Message:     "Deployment and Service created",
		Provisioned: provisioned,
	}, nil
}

// setContainer finds the container named name inside spec and overwrites
// only the fields this provider owns — Image, Args, VolumeMounts, plus the
// port and resources handled below — leaving anything the apiserver
// defaulted (ImagePullPolicy, TerminationMessagePath, TerminationMessagePolicy,
// ...) exactly as fetched. If no such container exists yet, one is appended;
// the apiserver then defaults the remaining fields on Create, and the next
// Reconcile pass finds this same container by name and preserves them.
//
// VolumeMounts is assigned wholesale rather than merged entry-by-entry like
// Ports and Resources below, but this is provably safe rather than merely
// convenient: this provider ever mounts exactly one VolumeMount (the model
// cache), VolumeMount has no apiserver-defaulted subfields (unlike
// ContainerPort.Protocol or a Requests key), and the slice this provider
// sets is byte-for-byte identical on every pass. There is nothing here for a
// real apiserver to add that a wholesale replacement could then drop.
func setContainer(spec *corev1.PodSpec, name, image string, args []string, port int32, mounts []corev1.VolumeMount, sizing provider.Sizing) {
	for i := range spec.Containers {
		if spec.Containers[i].Name != name {
			continue
		}
		c := &spec.Containers[i]
		c.Image = image
		c.Args = args
		c.VolumeMounts = mounts
		setContainerPort(c, port)
		setContainerResources(c, sizing)
		return
	}
	c := corev1.Container{Name: name, Image: image, Args: args, VolumeMounts: mounts}
	setContainerPort(&c, port)
	setContainerResources(&c, sizing)
	spec.Containers = append(spec.Containers, c)
}

// setContainerPort finds or appends a ContainerPort by its port number. This
// provider sets nothing about a container port beyond the number itself, so
// — mirroring setServicePort below — an existing entry is left untouched in
// full, preserving whatever the apiserver defaulted (Protocol: TCP, the same
// field setServicePort preserves on the Service side), and a new entry is
// appended only when no entry with that port number exists yet.
//
// This exists because the previous fix for the update loop stopped
// replacing the whole Containers slice and then replaced this one instead:
// ContainerPort.Protocol defaults to TCP exactly like ServicePort.Protocol
// does, and a rebuilt []corev1.ContainerPort{{ContainerPort: port}} literal
// silently dropped it on every pass after the first.
func setContainerPort(c *corev1.Container, port int32) {
	for i := range c.Ports {
		if c.Ports[i].ContainerPort == port {
			return
		}
	}
	c.Ports = append(c.Ports, corev1.ContainerPort{ContainerPort: port})
}

// setContainerResources sets only the resource keys this provider computes —
// CPU and memory in both Limits and Requests, and the GPU in Limits — leaving
// any other key already present untouched.
//
// This matters for the same reason setContainerPort does: Kubernetes
// defaults a missing Requests entry for an extended resource (such as
// nvidia.com/gpu) from its Limits entry when only Limits is set, so the
// stored container ends up with requests["nvidia.com/gpu"] = "1" that this
// provider never wrote. Wholesale-replacing ResourceRequirements — as the
// original submission did — would drop that defaulted key on every pass
// after the first, which is the identical failure shape as
// ContainerPort.Protocol, just in a field the review's own test could not
// see because the fake client does not model extended-resource defaulting
// either.
func setContainerResources(c *corev1.Container, sizing provider.Sizing) {
	if c.Resources.Limits == nil {
		c.Resources.Limits = corev1.ResourceList{}
	}
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{}
	}
	c.Resources.Limits["nvidia.com/gpu"] = resource.MustParse("1")
	c.Resources.Limits[corev1.ResourceCPU] = resource.MustParse(sizing.CPU)
	c.Resources.Limits[corev1.ResourceMemory] = resource.MustParse(sizing.Memory)
	c.Resources.Requests[corev1.ResourceCPU] = resource.MustParse(sizing.CPU)
	c.Resources.Requests[corev1.ResourceMemory] = resource.MustParse(sizing.Memory)
}

// setModelCacheVolume finds or appends the Volume backing the model cache
// mount, addressing the existing entry in place by index for the same
// reason setContainer does.
//
// Unlike setContainerPort and setContainerResources, this replaces the found
// entry wholesale rather than merging field by field — deliberately, not
// because it was missed. A Volume backed by PersistentVolumeClaim has
// exactly two meaningful fields (ClaimName, ReadOnly), both fully owned and
// set by this provider, and neither is a field Kubernetes' pod defaulting
// ever populates when absent (unlike ContainerPort.Protocol or a Requests
// key) — there is nothing here for a wholesale replacement to drop. It is
// also the behavior actually wanted: if an operator changes
// parameters.modelCache, the volume's ClaimName must change to match, which
// leave-alone-if-present (the Port/Resources pattern) would not do.
//
// The PVC itself is a data object under the ownership contract on
// Provisioner.Reconcile — it holds the instance's data (the cached weights)
// rather than exposing the instance — but this provider does not create it
// and never will: it is provisioned once, out of band, and shared read-only
// across every inference instance. There is deliberately no owner reference
// question to answer here, unlike the Deployment and Service below: nothing
// in this package ever creates or deletes the PVC, so nothing in this
// package can leak or prematurely collect it.
func setModelCacheVolume(spec *corev1.PodSpec, pvcName string) {
	vol := corev1.Volume{
		Name: modelCacheVolumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcName,
				ReadOnly:  true,
			},
		},
	}
	for i := range spec.Volumes {
		if spec.Volumes[i].Name == vol.Name {
			spec.Volumes[i] = vol
			return
		}
	}
	spec.Volumes = append(spec.Volumes, vol)
}

// setServicePort finds or appends the Service port by port number, setting
// only Port and TargetPort and leaving anything the apiserver defaulted
// (Protocol: TCP) exactly as fetched — same reasoning as setContainer.
func setServicePort(spec *corev1.ServiceSpec, port int32, targetPort intstr.IntOrString) {
	for i := range spec.Ports {
		if spec.Ports[i].Port == port {
			spec.Ports[i].TargetPort = targetPort
			return
		}
	}
	spec.Ports = append(spec.Ports, corev1.ServicePort{Port: port, TargetPort: targetPort})
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
