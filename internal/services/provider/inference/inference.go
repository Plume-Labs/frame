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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	"github.com/rmocq/frame/internal/scheduling"
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
	// tmpVolumeName / tmpMountPath give llama.cpp one writable directory
	// while the root filesystem stays read-only. An emptyDir, so it is
	// per-pod, never shared, and gone when the pod is.
	//
	// Verified against ghcr.io/ggml-org/llama.cpp:server-cuda that the
	// serving path does not need it: booted under exactly the securityContext
	// set below — uid 65532, read-only rootfs, all capabilities dropped,
	// no-new-privileges — the server loaded a model, served a completion, and
	// wrote nothing at all to its root filesystem. This mount is kept anyway
	// for the one path that could not be exercised here: on a GPU node the
	// CUDA runtime may JIT-compile kernels and want somewhere to cache them.
	// CUDA degrades to not caching rather than failing when it cannot write,
	// so this is insurance, not a dependency — but it costs nothing, and the
	// alternative is discovering it on a GPU node instead.
	tmpVolumeName = "tmp"
	tmpMountPath  = "/tmp"
	// runAsUser is the unprivileged uid llama.cpp runs as. The same uid the
	// project's own images already use (Dockerfile.controller:23,
	// Dockerfile.authd:25), so there is one non-root identity across Frame
	// rather than a second convention.
	//
	// The image itself declares no USER and would otherwise run as root.
	// Verified this uid can actually run it: every file in the image's /app
	// is mode 0755 root:root (llama-server, and all of libggml*/libllama*),
	// so a non-root uid reads and executes them.
	//
	// One deployment precondition follows from this and is worth knowing
	// before it bites: the model cache PVC must be readable by uid 65532.
	// Weights fetched with a normal umask are world-readable and fine, and
	// hostPath-style provisioners (local-path among them) create the volume
	// root 0777. A cache populated by a job with a restrictive umask, or a
	// CSI driver that creates the root 0700, is not.
	//
	// The symptom when it is not: llama-server takes EACCES opening the GGUF
	// and exits non-zero, so the pod goes CrashLoopBackOff rather than
	// failing to start. That reason is in blockedWaitingReasons and the
	// underlying exit is surfaced by containerBlockedMessage, so the
	// FrameService reports it in status instead of sitting at
	// RolloutInProgress forever — which is what it did before that was
	// handled, and is the only reason this precondition was ever hard to
	// diagnose.
	//
	// No fsGroup is set to force the issue, deliberately — fsGroup triggers a
	// recursive chown, and this volume is a large cache shared read-only
	// between instances, which is the wrong thing to rewrite the ownership of.
	runAsUser = int64(65532)
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
	// apiKeySecretSuffix names the Secret this provider owns to hold the
	// generated llama.cpp API key, appended to the FrameService's name.
	// Deliberately distinct from the credentials Secret the controller
	// writes at (svc.Namespace, spec.binding.secretName or svc.Name): see
	// ensureAPIKey for why writing to that exact coordinate before the
	// controller's own binding reconciler has recorded it in
	// status.binding.projected would trip its ownership-conflict check.
	//
	// This is reachable, not just theoretical: an operator who sets
	// spec.binding.secretName to exactly "<name>-inference-key" collides
	// with this Secret. claimNewCoordinates
	// (internal/controller/services/binding.go) then finds a Secret already
	// sitting at that coordinate that status.binding.projected has never
	// recorded, and permanently degrades the instance with BindingConflict,
	// naming a Secret "not written by" this FrameService — when it was,
	// just outside reconcileBinding's own bookkeeping. Deliberately not
	// validated against: it is a confusing message to land on, but a rare
	// and recoverable naming collision, not a security question. Left here
	// so whoever hits it finds the explanation next to the cause.
	apiKeySecretSuffix = "-inference-key"
	// apiKeyDataKey is the key this provider stores the generated token
	// under, both in the Secret it owns and in the Data map Bind returns —
	// the same key a consumer looks up in the credentials Secret.
	apiKeyDataKey = "apiKey"
	// apiKeyDigestAnnotation carries a SHA-256 hex digest of the current API
	// key on the Deployment's pod template — the digest, never the token
	// itself, since a pod template is visible the same way Args would be.
	// Its only purpose is making a changed token a changed template: see the
	// comment where it is set in Reconcile for why that has to be true.
	apiKeyDigestAnnotation = "frame.plume-labs.io/api-key-sha256"
	// apiKeyEnvVar is the environment variable llama.cpp's server reads its
	// API key from. Confirmed against tools/server/README.md: --api-key has
	// a documented environment-variable equivalent (LLAMA_API_KEY), unlike
	// --api-key-file, which does not. Using the env var instead of the flag
	// keeps the key out of the pod spec: args are visible to anyone who can
	// `kubectl describe pod` or read Events, which is a much wider audience
	// than those who can read the Secret an env var's secretKeyRef points at.
	apiKeyEnvVar = "LLAMA_API_KEY"
	// apiKeyBytes is the entropy encoded into every generated API key: 32
	// bytes (256 bits) of crypto/rand output, base64url-encoded without
	// padding below, is far past the point where guessing it is easier than
	// reaching the Service and asking nicely.
	apiKeyBytes = 32
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
	// apiReader is the manager's uncached reader (mgr.GetAPIReader()), used
	// solely for the model-cache PVC existence check in Reconcile. That check
	// is a single Get by name, and controller-runtime's cached client cannot
	// serve a Get without first opening an informer for the whole GVK — a
	// cluster-scoped List+Watch across every PersistentVolumeClaim in the
	// cluster, held in the manager's memory for as long as it runs, just to
	// answer one named lookup. The uncached reader talks straight to the
	// apiserver instead, so the RBAC this provider needs stays a single `get`
	// on one object rather than list+watch on the whole resource type
	// cluster-wide. May be nil for a caller that only ever calls Size and
	// ParameterSchema, exactly like client above.
	apiReader client.Reader
}

// New builds the provider for a card of the given size, using c to create and
// converge the workload and apiReader for the uncached model-cache PVC check
// (see the field comment on apiReader for why it must not go through c's
// cache). Both may be nil for a caller that only ever calls Size and
// ParameterSchema — the webhook is the only such caller today — since
// Reconcile and Bind are the sole methods that dereference either.
func New(gpuMemoryMiB int64, c client.Client, apiReader client.Reader) *Provider {
	return &Provider{gpuMemoryMiB: gpuMemoryMiB, client: c, apiReader: apiReader}
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

// apiKeySecretName names the Secret this provider owns to hold the
// generated llama.cpp API key. See apiKeySecretSuffix for why it is never
// the same coordinate as the credentials Secret the controller writes.
func apiKeySecretName(svcName string) string {
	return svcName + apiKeySecretSuffix
}

// generateAPIKey mints a fresh token from crypto/rand — never math/rand,
// which is predictable and unfit for anything a client authenticates with.
// 32 bytes of entropy, base64url-encoded without padding, keeps the value
// safely outside guessing range while remaining a plain string llama.cpp's
// --api-key/LLAMA_API_KEY accepts verbatim.
func generateAPIKey() (string, error) {
	buf := make([]byte, apiKeyBytes)
	if _, err := rand.Read(buf); err != nil {
		// A silent failure here would produce an instance with an empty or
		// predictable token, which is worse than no token at all, so the
		// read error is wrapped and surfaced rather than ignored.
		return "", fmt.Errorf("reading random bytes for API key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// apiKeyDigest fingerprints token for apiKeyDigestAnnotation: a one-way hash
// lets the pod template change whenever the token does — the actual
// requirement, see where the annotation is set — without the template itself
// ever carrying the credential.
func apiKeyDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ensureAPIKey makes the Secret backing the FrameService's llama.cpp API key
// exist and returns the token in it, generating one only the first time.
//
// The Secret is the source of truth, not a field this provider recomputes:
// Reconcile runs repeatedly, and Reconcile and Bind are separate calls that
// both need the exact same value, so something has to persist it between
// calls and across reconciles. Nothing in this provider's own state survives
// a restart, and the token must never reach svc.Status (conditions and
// status are logged and displayed; a credential must not be). A Secret is
// the only object already in this design meant to hold a credential, so it
// is where this one lives too — read back here on every call rather than
// generated fresh, which is what keeps it stable across reconciles instead
// of rewriting the Deployment (and breaking every client holding the old
// value) on every pass.
//
// This also fixes the Deployment-before-Secret ordering the controller's own
// credentials Secret has: that one is only written by reconcileBinding after
// Reconcile has already reported Ready, so a Deployment reading its API key
// from that Secret could never start — the pod would sit in
// CreateContainerConfigError forever, Reconcile would never see a ready
// replica, and the controller would never reach the code that creates the
// Secret the pod is waiting on. Owning a separate Secret here, and creating
// it before the Deployment below, breaks that deadlock: this Secret exists
// before anything reads it, on every pass including the first.
func (p *Provider) ensureAPIKey(ctx context.Context, svc *servicesv1alpha1.FrameService) (string, error) {
	name := apiKeySecretName(svc.Name)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: svc.Namespace}}

	var genErr error
	if _, err := controllerutil.CreateOrUpdate(ctx, p.client, secret, func() error {
		if len(secret.Data[apiKeyDataKey]) > 0 {
			// Already generated on an earlier pass: reuse it rather than
			// mint a second value that would invalidate every client
			// holding the first.
			return controllerutil.SetControllerReference(svc, secret, p.client.Scheme())
		}
		token, err := generateAPIKey()
		if err != nil {
			genErr = err
			return err
		}
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[apiKeyDataKey] = []byte(token)
		return controllerutil.SetControllerReference(svc, secret, p.client.Scheme())
	}); err != nil {
		if genErr != nil {
			return "", fmt.Errorf("generating API key for %s/%s: %w", svc.Namespace, svc.Name, genErr)
		}
		return "", fmt.Errorf("reconciling API key Secret %s/%s: %w", svc.Namespace, name, err)
	}
	return string(secret.Data[apiKeyDataKey]), nil
}

// readAPIKey reads back the token ensureAPIKey wrote, for Bind to hand to a
// consumer. Bind runs after Reconcile has already reported Ready, so the
// Secret ensureAPIKey depends on is guaranteed to exist by the time this is
// called — but the Get is real rather than assumed, so a Secret deleted out
// from under a Ready instance surfaces as an error here instead of Bind
// quietly returning an empty credential.
func (p *Provider) readAPIKey(ctx context.Context, svc *servicesv1alpha1.FrameService) (string, error) {
	name := apiKeySecretName(svc.Name)
	var secret corev1.Secret
	if err := p.client.Get(ctx, types.NamespacedName{Name: name, Namespace: svc.Namespace}, &secret); err != nil {
		return "", fmt.Errorf("reading API key Secret %s/%s: %w", svc.Namespace, name, err)
	}
	token := secret.Data[apiKeyDataKey]
	if len(token) == 0 {
		return "", fmt.Errorf("API key Secret %s/%s has no %q key", svc.Namespace, name, apiKeyDataKey)
	}
	return string(token), nil
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
	// Deliberately p.apiReader, not p.client: see the field comment on
	// apiReader for why this single named lookup must not go through the
	// cached client.
	if err := p.apiReader.Get(ctx, types.NamespacedName{Name: cacheName, Namespace: svc.Namespace}, &pvc); err != nil {
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
		// Any other error — Forbidden included — means Frame could not
		// determine whether the cache exists, which is not the same claim as
		// ModelCacheMissing and must not be reported as it. Degrading with
		// its own named reason, rather than returning a bare error here, is
		// deliberate: a hard error takes FrameServiceReconciler's error path,
		// which never calls setStatus, so the FrameService is left with
		// empty status forever — nothing for kubectl describe to show,
		// indistinguishable from a resource still being admitted. Returning
		// a Result instead (nil error) keeps this on the same degradedRequeue
		// cadence as every other degrade, so a permission fix, once granted,
		// is picked up on the next scheduled pass without needing a restart
		// or an edit to the object to force one.
		return provider.Result{
			Ready:  false,
			Reason: "ModelCacheCheckFailed",
			Message: fmt.Sprintf(
				"checking for PersistentVolumeClaim %q in namespace %q: %v",
				cacheName, svc.Namespace, err),
		}, nil
	}

	// The API key Secret must exist before the Deployment that reads it: see
	// ensureAPIKey for why creating it after, the way the controller's own
	// credentials Secret is written, would deadlock the instance forever.
	apiKeySecret := apiKeySecretName(svc.Name)
	apiKey, err := p.ensureAPIKey(ctx, svc)
	if err != nil {
		return provider.Result{}, err
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
		// Scheduling priority is derived from serviceClass, not from a
		// field of its own (F10): a long-lived instance's tier is its
		// urgency. The mapping is shared with the FrameJob controller so
		// the two cannot drift, and it only ever names a PriorityClass
		// SchedulingPolicy's controller created — never a system one.
		//
		// PriorityClassName is a scalar on the PodSpec, so setContainer's
		// partial-update discipline does not apply: it is assigned
		// wholesale. An unrecognised serviceClass yields "", which leaves
		// the pod at the cluster's implicit default rather than failing.
		deployment.Spec.Template.Spec.PriorityClassName =
			scheduling.PriorityClassForServiceClass(svc.Spec.ServiceClass)
		// A digest of the token, never the token itself, rides on the pod
		// template so a changed token is a changed template: if the API key
		// Secret is ever deleted and ensureAPIKey mints a new value, the
		// Deployment's env reference to the Secret is unchanged and
		// CreateOrUpdate would otherwise see no diff and roll nothing —
		// leaving the running pod holding the old value in its process
		// environment forever, since Kubernetes never live-updates an env
		// var sourced from a secretKeyRef. Putting the digest here makes
		// that rewrite a real template diff, so CreateOrUpdate updates the
		// Deployment and Kubernetes rolls the pods onto the new value on its
		// own — no detection logic needed, and the same mechanism covers
		// rotation whenever that arrives.
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = map[string]string{}
		}
		deployment.Spec.Template.Annotations[apiKeyDigestAnnotation] = apiKeyDigest(apiKey)
		setPodSecurityContext(&deployment.Spec.Template.Spec)
		setModelCacheVolume(&deployment.Spec.Template.Spec, cacheName)
		setTmpVolume(&deployment.Spec.Template.Spec)
		// Read-only: several instances can share one cache, and none of them
		// should be able to corrupt it. /tmp is the one writable path, and it
		// is an emptyDir, not the container's own root filesystem.
		mounts := []corev1.VolumeMount{
			{Name: modelCacheVolumeName, MountPath: modelMountPath, ReadOnly: true},
			{Name: tmpVolumeName, MountPath: tmpMountPath},
		}
		args := []string{
			"--host", "0.0.0.0",
			"--port", strconv.Itoa(containerPort),
			"-m", modelPath(svc.Spec.Parameters["model"]),
			"-c", strconv.FormatInt(contextLength, 10),
		}
		// The API key reaches llama.cpp through the environment, sourced
		// from the Secret ensureAPIKey just converged — never as a --api-key
		// argument, which kubectl describe pod and Events would expose to
		// anyone who can read the pod spec, a far wider audience than
		// whoever can read the Secret.
		env := []corev1.EnvVar{
			{
				Name: apiKeyEnvVar,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: apiKeySecret},
						Key:                  apiKeyDataKey,
					},
				},
			},
		}
		// The GPU is requested as a resource, never as a nodeName, so the
		// scheduler finds the card on its own and the same spec keeps working
		// the day a second one arrives. CPU and memory come from Size rather
		// than being recomputed here, so the resources a pod actually gets
		// are the ones the operator's request was costed against.
		setContainer(&deployment.Spec.Template.Spec, containerName,
			"ghcr.io/ggml-org/llama.cpp:server-cuda", args, containerPort, mounts, env, sizing)
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
		// A replica is actually missing, so it's worth asking why: this is
		// the only situation the pod List below exists to explain, and
		// listing pods on every reconcile of every already-healthy instance
		// is work nobody needs — and, on a real cluster, an RBAC-gated call
		// that would otherwise run constantly instead of only when it has
		// something to look for.
		//
		// ensureAPIKey closes the ordinary Deployment-before-Secret deadlock
		// — the Secret exists before this Deployment does — but it cannot
		// cover every way a pod can still end up unable to start. Those
		// failures surface on the pod, not as a Deployment condition, and
		// left unchecked they would all read as an ordinary, permanent
		// RolloutInProgress: a degrade that never explains itself and never
		// clears. Naming them here turns a silent crash loop into a status
		// message an operator can act on.
		//
		// Two distinct shapes have to be recognised, because they present
		// differently and only one of them was originally handled:
		//
		//   - The container never starts at all — a missing or unreadable
		//     Secret gives CreateContainerConfigError, which sits in
		//     State.Waiting with the cause in Waiting.Message.
		//   - The container starts and then exits — which is what an
		//     unreadable model cache does. llama-server gets EACCES opening
		//     the GGUF and exits non-zero, so the pod lands in
		//     CrashLoopBackOff. Waiting.Message for CrashLoopBackOff says
		//     only "back-off 5m0s restarting failed container", which names
		//     no cause at all; the actual cause is in the *previous*
		//     container's termination record. That is why
		//     LastTerminationState is read below rather than trusting
		//     Waiting.Message.
		//
		// This second case is the one failure mode the pod hardening above
		// can itself introduce (runAsUser 65532 against a model cache that
		// is not world-readable), so leaving it undiagnosable would mean
		// shipping a change whose one plausible breakage is invisible in the
		// place an operator looks first.
		var pods corev1.PodList
		if err := p.client.List(ctx, &pods, client.InNamespace(svc.Namespace), client.MatchingLabels(labels)); err != nil {
			return provider.Result{}, fmt.Errorf("listing pods for %s/%s: %w", svc.Namespace, svc.Name, err)
		}
		for i := range pods.Items {
			for _, cs := range pods.Items[i].Status.ContainerStatuses {
				if cs.State.Waiting == nil || !blockedWaitingReasons[cs.State.Waiting.Reason] {
					continue
				}
				return provider.Result{
					Ready:  false,
					Reason: cs.State.Waiting.Reason,
					Message: fmt.Sprintf("Pod %s container %s cannot start: %s",
						pods.Items[i].Name, cs.Name, containerBlockedMessage(cs)),
					Provisioned: provisioned,
				}, nil
			}
		}

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

// blockedWaitingReasons are the pod waiting reasons that mean "this container
// is not going to start on its own, and an operator needs to know why" — as
// opposed to the ordinary transient ones (ContainerCreating, PodInitializing)
// that a rollout passes through and that must keep reading as
// RolloutInProgress rather than as a named degrade.
//
// CrashLoopBackOff and Error cover the container-started-then-exited shape;
// CreateContainerConfigError and CreateContainerError cover
// never-started. ImagePullBackOff and ErrImagePull are included because they
// are the same class of permanent, operator-actionable stall: a bad image
// reference or a missing pull secret will never resolve by waiting.
var blockedWaitingReasons = map[string]bool{
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"CrashLoopBackOff":           true,
	"Error":                      true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
}

// terminationMessageLimit caps how much of a dead container's output reaches
// the FrameService's status. Status is displayed by kubectl describe and
// carried in every watch event for the object, so an unbounded stack trace
// there is both unreadable and a real payload on every consumer of the CR.
const terminationMessageLimit = 512

// containerBlockedMessage explains why a container is stuck, preferring the
// most specific evidence available.
//
// For a container that never started, Waiting.Message carries the cause
// directly. For CrashLoopBackOff it does not — it says only "back-off 5m0s
// restarting failed container", naming nothing — so the useful information is
// in the previous run's termination record: its exit code, and whatever the
// process wrote to the termination message path. Both are reported when
// present, because an exit code alone ("exited with code 1") is rarely enough
// and the message alone loses the distinction between a clean refusal and a
// signal.
func containerBlockedMessage(cs corev1.ContainerStatus) string {
	waiting := ""
	if cs.State.Waiting != nil {
		waiting = cs.State.Waiting.Message
	}

	term := cs.LastTerminationState.Terminated
	if term == nil {
		return waiting
	}

	detail := fmt.Sprintf("previous run exited with code %d", term.ExitCode)
	if term.Reason != "" {
		detail += " (" + term.Reason + ")"
	}
	if msg := strings.TrimSpace(term.Message); msg != "" {
		if len(msg) > terminationMessageLimit {
			msg = msg[:terminationMessageLimit] + "…"
		}
		detail += ": " + msg
	}
	if waiting == "" {
		return detail
	}
	return waiting + "; " + detail
}

// setContainer finds the container named name inside spec and overwrites
// only the fields this provider owns — Image, Args, VolumeMounts, Env, plus
// the port and resources handled below — leaving anything the apiserver
// defaulted (ImagePullPolicy, TerminationMessagePath, TerminationMessagePolicy,
// ...) exactly as fetched. If no such container exists yet, one is appended;
// the apiserver then defaults the remaining fields on Create, and the next
// Reconcile pass finds this same container by name and preserves them.
//
// VolumeMounts and Env are each assigned wholesale rather than merged
// entry-by-entry like Ports and Resources below, but this is provably safe
// rather than merely convenient: this provider ever mounts exactly two
// VolumeMounts (the model cache and the writable /tmp emptyDir) and sets
// exactly one EnvVar (the API key), neither VolumeMount nor
// EnvVar/EnvVarSource/SecretKeySelector has any apiserver-defaulted subfield
// (unlike ContainerPort.Protocol or a Requests key), and both slices this
// provider sets are byte-for-byte identical on every pass. There is nothing
// here for a real apiserver to add that a wholesale replacement could then
// drop.
func setContainer(spec *corev1.PodSpec, name, image string, args []string, port int32, mounts []corev1.VolumeMount, env []corev1.EnvVar, sizing provider.Sizing) {
	for i := range spec.Containers {
		if spec.Containers[i].Name != name {
			continue
		}
		c := &spec.Containers[i]
		c.Image = image
		c.Args = args
		c.VolumeMounts = mounts
		c.Env = env
		setContainerPort(c, port)
		setContainerResources(c, sizing)
		setContainerSecurityContext(c)
		return
	}
	c := corev1.Container{Name: name, Image: image, Args: args, VolumeMounts: mounts, Env: env}
	setContainerPort(&c, port)
	setContainerResources(&c, sizing)
	setContainerSecurityContext(&c)
	spec.Containers = append(spec.Containers, c)
}

// setPodSecurityContext hardens the pod llama.cpp runs in. Without this the
// pod inherits every default there is: root, an automounted ServiceAccount
// token, and an unconfined seccomp profile. llama.cpp parses untrusted input
// (GGUF weights, HTTP bodies, chat templates) in C++ and has a real CVE
// history, so the question is not whether a memory-safety bug is reachable
// but what it yields when it is. With these set it yields an unprivileged
// process that cannot talk to the apiserver.
//
// Fields are set individually on an existing struct rather than by replacing
// it, matching setContainerResources rather than setModelCacheVolume. That is
// deliberate: a mutating admission webhook (a policy engine, a service mesh)
// may add fsGroup or seLinuxOptions to this pod template, and wholesale
// replacement would drop whatever it added on every pass — the same
// CreateOrUpdate update-loop shape this file has already had to close twice.
// Only the fields this provider owns are touched.
func setPodSecurityContext(spec *corev1.PodSpec) {
	// No ServiceAccount token in the pod. This provider's container never
	// calls the Kubernetes API; the only credential it needs is the llama.cpp
	// API key, which arrives through the environment from its own Secret.
	// Leaving the default token mounted would hand any code execution in this
	// container the namespace default SA's rights for free.
	spec.AutomountServiceAccountToken = new(false)

	if spec.SecurityContext == nil {
		spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	spec.SecurityContext.RunAsNonRoot = new(true)
	spec.SecurityContext.RunAsUser = new(runAsUser)
	spec.SecurityContext.SeccompProfile = &corev1.SeccompProfile{
		Type: corev1.SeccompProfileTypeRuntimeDefault,
	}
}

// setContainerSecurityContext hardens llama.cpp's own container, for the same
// reasons and with the same set-fields-not-the-struct discipline as
// setPodSecurityContext.
//
// readOnlyRootFilesystem is safe here and was checked rather than assumed:
// booted under exactly this configuration, the real server-cuda image loaded
// a model, served a completion, and wrote nothing whatsoever to its root
// filesystem. The writable path it gets is the /tmp emptyDir — see
// tmpVolumeName.
func setContainerSecurityContext(c *corev1.Container) {
	if c.SecurityContext == nil {
		c.SecurityContext = &corev1.SecurityContext{}
	}
	c.SecurityContext.AllowPrivilegeEscalation = new(false)
	c.SecurityContext.ReadOnlyRootFilesystem = new(true)
	c.SecurityContext.RunAsNonRoot = new(true)
	c.SecurityContext.RunAsUser = new(runAsUser)
	// Dropping ALL and adding nothing back: llama.cpp binds a port above 1024
	// and reads files, neither of which needs a capability. NET_RAW in
	// particular, which the default set grants, is what turns code execution
	// in this container into a packet-crafting position on the pod network.
	if c.SecurityContext.Capabilities == nil {
		c.SecurityContext.Capabilities = &corev1.Capabilities{}
	}
	c.SecurityContext.Capabilities.Drop = []corev1.Capability{"ALL"}
}

// setTmpVolume finds or appends the emptyDir backing the container's one
// writable path. Addressed by name and replaced wholesale for the same reason
// setModelCacheVolume is: an EmptyDirVolumeSource this provider leaves zeroed
// has no apiserver-defaulted subfield for a replacement to drop.
func setTmpVolume(spec *corev1.PodSpec) {
	vol := corev1.Volume{
		Name:         tmpVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}
	for i := range spec.Volumes {
		if spec.Volumes[i].Name == vol.Name {
			spec.Volumes[i] = vol
			return
		}
	}
	spec.Volumes = append(spec.Volumes, vol)
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

// Bind returns the in-cluster DNS name of the Service Reconcile created,
// together with the API key that Service now requires. Endpoint itself
// carries no credential — it is published in status.binding.endpoint, in
// plain sight — so the token only ever travels in Data, which the controller
// writes into the credentials Secret and never into status. The token is
// read back from the Secret ensureAPIKey wrote during Reconcile rather than
// generated here: Bind and Reconcile are separate calls, and both have to
// agree on the same value.
func (p *Provider) Bind(ctx context.Context, svc *servicesv1alpha1.FrameService) (provider.Binding, error) {
	endpoint := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", svc.Name, svc.Namespace, containerPort)
	token, err := p.readAPIKey(ctx, svc)
	if err != nil {
		return provider.Binding{}, err
	}
	return provider.Binding{
		Endpoint: endpoint,
		Data: map[string][]byte{
			"endpoint":    []byte(endpoint),
			apiKeyDataKey: []byte(token),
		},
	}, nil
}
