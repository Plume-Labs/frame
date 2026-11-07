// Package cache provides FrameService type "cache": one Valkey server, run by
// Frame itself as a single-replica StatefulSet. docs/superpowers/specs/
// 2026-11-07-frameservice-cache-valkey-design.md says why no operator.
//
// Two profiles, fixed at creation:
//
//   - cache: evicts with LRU and persists nothing. A restart empties it.
//   - queue: AOF on a volume, noeviction. What BullMQ needs: a job is never
//     evicted, and a restart loses a second or two of writes, more on a disk
//     that stalls.
//
// The rules follow from a queue being data:
//
//   - Nothing is taken over. A StatefulSet, Service, Secret or volume under
//     these names that this FrameService did not create degrades with
//     NotOwned, and nothing is written.
//   - A queue that lost its volume says so, and stays degraded until a person
//     does (InstanceLost). Frame cannot always prevent the empty start: with its
//     pod gone, the StatefulSet controller makes a new claim under the same name
//     on its own. So the volume's UID is recorded on the StatefulSet, and a
//     different one is what gives it away.
//   - The volume carries no owner reference and is not in status.provisioned,
//     and the manager has no delete verb on volumes or StatefulSets, so
//     deletionPolicy never reaches it. A FrameService recreated under the same
//     name finds its data.
package cache

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	servicesv1beta1 "github.com/Plume-Labs/frame/api/services/v1beta1"
	"github.com/Plume-Labs/frame/internal/scheduling"
	"github.com/Plume-Labs/frame/internal/services/provider"
)

// No delete on statefulsets: garbage collection removes them through the owner
// reference, and a grant cannot tell Frame's from anyone else's.
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update
// patch grows a queue's volume. Only this code keeps that to volumes labelled as
// the instance's: the grant itself is cluster-wide.
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;patch

const (
	// ProfileCache and ProfileQueue are the profile parameter's values.
	ProfileCache = "cache"
	ProfileQueue = "queue"

	// OwnerLabel selects the instance's pods, and marks its volume: the
	// StatefulSet controller copies the selector onto the claims it creates.
	OwnerLabel = "services.plume-labs.io/frameservice"
	// profileAnnotation records the profile a StatefulSet was created with.
	profileAnnotation = "services.plume-labs.io/cache-profile"
	// volumeUIDAnnotation is the UID of the volume a queue first ran on.
	volumeUIDAnnotation = "services.plume-labs.io/volume-uid"
	// templateHashAnnotation is the hash of the pod template this provider
	// last wrote. Comparing it rather than the live template is what keeps a
	// field the apiserver defaulted from reading as a difference on every pass.
	templateHashAnnotation = "services.plume-labs.io/template-hash"
	// authDigestAnnotation rides on the pod template so a new password is a
	// new template: a pod never rereads its environment.
	authDigestAnnotation = "services.plume-labs.io/auth-digest"
	serviceClassLabel    = "frame.plume-labs.io/service-class"

	// ponytail: one image per Frame release, upgraded with it. Add a parameter
	// the day two instances need different versions.
	image         = "docker.io/valkey/valkey:9.1.3-alpine"
	containerName = "valkey"
	port          = 6379
	// The image's valkey user, which owns nothing else.
	runAsUser  int64 = 999
	runAsGroup int64 = 1000

	dataVolume = "data"
	dataPath   = "/data"
	authVolume = "auth"
	authPath   = "/etc/valkey-auth"

	authSecretSuffix = "-valkey-auth"
	passwordKey      = "password"
	// saltKey keys the HMAC whose sum rides on the pod template. A bare hash
	// of the password there would let anyone who reads pods test guesses
	// against a password edited by hand.
	saltKey     = "digest-salt"
	authConfKey = "auth.conf"

	// Memory a server needs besides its dataset: clients, buffers, the process.
	overheadMiB = 64
	cpuRequest  = "100m"
	// A FrameResourceQuota with maxCPU refuses a pod without a CPU limit. Two
	// cores never throttle a server whose commands run on one thread.
	cpuLimit = "2"
	// The controller-revision-hash label is the pod's name and 11 characters more.
	maxNameLength = 52
)

// Degrade reasons.
const (
	ReasonInvalidName  = "InvalidName"
	ReasonNotOwned     = "NotOwned"
	ReasonSpecDrift    = "SpecDrift"
	ReasonInstanceLost = "InstanceLost"
	ReasonVolumeShrink = "VolumeShrinkRefused"
	ReasonVolumeGrow   = "VolumeGrowRefused"
	ReasonAuthInvalid  = "AuthSecretInvalid"
	ReasonProvisioning = "Provisioning"
	ReasonRunning      = "Running"
)

// Provider implements provider.Provisioner.
type Provider struct {
	client client.Client
	// apiReader reads volumes: through the cached client, one named Get would
	// open a cluster-wide informer over every PersistentVolumeClaim.
	apiReader client.Reader
}

// New builds the provider.
func New(c client.Client, apiReader client.Reader) *Provider {
	return &Provider{client: c, apiReader: apiReader}
}

// Type is the spec.type value.
func (p *Provider) Type() string { return "cache" }

// ParameterSchema is what the webhook enforces.
func (p *Provider) ParameterSchema() *provider.Schema {
	return &apiextensionsv1.JSONSchemaProps{
		Type:     "object",
		Required: []string{"profile", "maxMemory"},
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"profile": {
				Type: "string",
				Description: "cache evicts with LRU and persists nothing; queue keeps an AOF on a volume and never " +
					"evicts, for BullMQ. Fixed once the instance exists.",
				Enum: []apiextensionsv1.JSON{{Raw: []byte(`"cache"`)}, {Raw: []byte(`"queue"`)}},
			},
			"maxMemory": {
				Type:        "string",
				Description: "Dataset ceiling, e.g. 512Mi. Moving it restarts the server, which empties a cache.",
				Pattern:     `^[1-9][0-9]{0,6}(Mi|Gi)$`,
			},
			"volumeSize": {
				Type:        "string",
				Description: "Size of a queue's volume, at least three times maxMemory. Required for queue, refused for cache. Can grow, never shrink.",
				Pattern:     `^[1-9][0-9]{0,6}(Mi|Gi|Ti)$`,
			},
			"storageClass": {
				Type:        "string",
				Description: "StorageClass of a queue's volume. Fixed once the instance exists; empty is the cluster default.",
				Pattern:     `^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`,
			},
			"restoredVolumeUID": {
				Type: "string",
				Description: "UID of the volume a queue now runs on, after InstanceLost: says the data on it is the " +
					"data wanted. Compared on every pass, so it answers for that volume and no other.",
				Pattern: `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
			},
		},
	}
}

// Size checks what the schema cannot and derives the pod's memory: the dataset,
// and for a queue room to double it, since an AOF rewrite forks and every page
// written during the fork is copied.
func (p *Provider) Size(params map[string]string) (provider.Sizing, error) {
	maxMemory, err := resource.ParseQuantity(params["maxMemory"])
	if err != nil {
		return provider.Sizing{}, fmt.Errorf("maxMemory: %w", err)
	}
	mib := maxMemory.Value() >> 20
	if params["profile"] == ProfileQueue {
		if params["volumeSize"] == "" {
			return provider.Sizing{}, errors.New("a queue needs volumeSize")
		}
		volume, err := resource.ParseQuantity(params["volumeSize"])
		if err != nil {
			return provider.Sizing{}, fmt.Errorf("volumeSize: %w", err)
		}
		if volume.Value() < 3*maxMemory.Value() {
			return provider.Sizing{}, fmt.Errorf(
				"volumeSize %s is under three times maxMemory %s: an AOF rewrite holds the old base, its log and the new base at once",
				params["volumeSize"], params["maxMemory"])
		}
		return provider.Sizing{CPU: cpuRequest, Memory: fmt.Sprintf("%dMi", 2*mib+overheadMiB)}, nil
	}
	if params["volumeSize"] != "" || params["storageClass"] != "" || params["restoredVolumeUID"] != "" {
		return provider.Sizing{}, errors.New("volumeSize, storageClass and restoredVolumeUID belong to the queue profile: a cache has no volume")
	}
	return provider.Sizing{CPU: cpuRequest, Memory: fmt.Sprintf("%dMi", mib+mib/4+overheadMiB)}, nil
}

func degraded(reason, format string, args ...any) provider.Result {
	return provider.Result{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

func authSecretName(svc string) string { return svc + authSecretSuffix }

// volumeName is the claim the StatefulSet controller makes for its one pod.
func volumeName(svc string) string { return dataVolume + "-" + svc + "-0" }

// Reconcile converges the instance, or says why it will not. A write that lost
// to the cached read it was based on is no failure: the next pass reads again.
func (p *Provider) Reconcile(ctx context.Context, svc *servicesv1beta1.FrameService) (provider.Result, error) {
	res, err := p.converge(ctx, svc)
	if apierrors.IsAlreadyExists(err) || apierrors.IsConflict(err) {
		return degraded(ReasonProvisioning, "%s changed during this pass, reading it again", svc.Name), nil
	}
	return res, err
}

func (p *Provider) converge(ctx context.Context, svc *servicesv1beta1.FrameService) (provider.Result, error) {
	// The Service needs a DNS-1035 label, which a FrameService's name need not be.
	if errs := validation.IsDNS1035Label(svc.Name); len(errs) > 0 || len(svc.Name) > maxNameLength {
		return degraded(ReasonInvalidName, "%q cannot name this instance: at most %d characters, a DNS label starting with a letter",
			svc.Name, maxNameLength), nil
	}
	params := provider.Params(svc.Spec.Parameters)
	sizing, err := p.Size(params)
	if err != nil {
		return provider.Result{}, err
	}

	var sts appsv1.StatefulSet
	exists, res, stop, err := p.findOwned(ctx, svc, &sts, "StatefulSet")
	if stop || err != nil {
		return res, err
	}
	if exists {
		if drift := driftOf(&sts, params); drift != "" {
			return degraded(ReasonSpecDrift, "nothing written: %s", drift), nil
		}
	}
	serviceExists, res, stop, err := p.findOwned(ctx, svc, &corev1.Service{}, "Service")
	if stop || err != nil {
		return res, err
	}

	var volume *corev1.PersistentVolumeClaim
	if params["profile"] == ProfileQueue {
		if volume, res, stop, err = p.checkVolume(ctx, svc, params); stop || err != nil {
			return res, err
		}
		if volume != nil && exists {
			if res, stop, err := p.checkVolumeIdentity(ctx, &sts, volume, params); stop || err != nil {
				return res, err
			}
		}
	}

	digest, res, stop, err := p.ensureAuth(ctx, svc)
	if stop || err != nil {
		return res, err
	}

	if !serviceExists {
		if err := p.createService(ctx, svc); err != nil {
			return provider.Result{}, err
		}
	}

	// Before the template: a refused growth must not restart the server for the
	// other half of the same edit.
	if volume != nil {
		if res, stop, err := p.growVolume(ctx, volume, params["volumeSize"]); stop || err != nil {
			return res, err
		}
	}

	template, err := podTemplate(svc, params, sizing, digest)
	if err != nil {
		return provider.Result{}, err
	}
	hash, err := hashOf(template)
	if err != nil {
		return provider.Result{}, err
	}
	if !exists {
		if err := p.createStatefulSet(ctx, svc, params, template, hash); err != nil {
			return provider.Result{}, err
		}
		return degraded(ReasonProvisioning, "StatefulSet %s created, waiting for its pod", svc.Name), nil
	}
	return p.rollOut(ctx, &sts, svc, params, template, hash)
}

// rollOut writes a template that moved, or a replica count someone moved, and
// otherwise reads where the instance stands.
func (p *Provider) rollOut(
	ctx context.Context, sts *appsv1.StatefulSet, svc *servicesv1beta1.FrameService, params map[string]string,
	template corev1.PodTemplateSpec, hash string,
) (provider.Result, error) {
	if sts.Annotations[templateHashAnnotation] == hash && sts.Spec.Replicas != nil && *sts.Spec.Replicas == 1 {
		return readiness(sts, svc, params), nil
	}
	sts.Spec.Template = template
	sts.Spec.Replicas = new(int32(1))
	sts.Annotations[templateHashAnnotation] = hash
	if err := p.client.Update(ctx, sts); err != nil {
		return provider.Result{}, fmt.Errorf("updating the StatefulSet: %w", err)
	}
	return degraded(ReasonProvisioning, "StatefulSet %s updated, the server restarts", svc.Name), nil
}

// readiness reads the StatefulSet. A pod the apiserver refuses, for a quota say,
// leaves only a FailedCreate event: the StatefulSet writes no condition for it.
func readiness(sts *appsv1.StatefulSet, svc *servicesv1beta1.FrameService, params map[string]string) provider.Result {
	if sts.Status.ObservedGeneration >= sts.Generation && sts.Status.ReadyReplicas >= 1 && sts.Status.UpdatedReplicas >= 1 {
		return provider.Result{Ready: true, Reason: ReasonRunning, Message: fmt.Sprintf("%s running, profile %s", svc.Name, params["profile"])}
	}
	return degraded(ReasonProvisioning, "waiting for the pod of %s; if none appears, kubectl describe statefulset says why", svc.Name)
}

// findOwned reads the object named after the instance, and stops on one this
// FrameService does not control.
func (p *Provider) findOwned(
	ctx context.Context, svc *servicesv1beta1.FrameService, obj client.Object, kind string,
) (bool, provider.Result, bool, error) {
	err := p.client.Get(ctx, client.ObjectKeyFromObject(svc), obj)
	switch {
	case apierrors.IsNotFound(err):
		return false, provider.Result{}, false, nil
	case err != nil:
		return false, provider.Result{}, false, fmt.Errorf("reading the %s: %w", kind, err)
	case !metav1.IsControlledBy(obj, svc):
		return true, degraded(ReasonNotOwned, "a %s %s exists and this FrameService did not create it: nothing written", kind, svc.Name), true, nil
	}
	return true, provider.Result{}, false, nil
}

// driftOf names a fixed setting the parameters state differently from the live
// StatefulSet. volumeClaimTemplates cannot change, and a profile cannot change
// without losing or stranding data.
func driftOf(sts *appsv1.StatefulSet, params map[string]string) string {
	if live := sts.Annotations[profileAnnotation]; live != params["profile"] {
		return fmt.Sprintf("profile is %q, the instance was created as %q", params["profile"], live)
	}
	if params["profile"] != ProfileQueue || len(sts.Spec.VolumeClaimTemplates) == 0 {
		return ""
	}
	live := ""
	if c := sts.Spec.VolumeClaimTemplates[0].Spec.StorageClassName; c != nil {
		live = *c
	}
	if want := params["storageClass"]; want != live {
		return fmt.Sprintf("storageClass is %q, the instance has %q", want, live)
	}
	return ""
}

// checkVolume is the queue's guard on its data. A nil volume that does not stop
// means there is none yet, and none was expected.
func (p *Provider) checkVolume(
	ctx context.Context, svc *servicesv1beta1.FrameService, params map[string]string,
) (*corev1.PersistentVolumeClaim, provider.Result, bool, error) {
	var pvc corev1.PersistentVolumeClaim
	err := p.apiReader.Get(ctx, client.ObjectKey{Namespace: svc.Namespace, Name: volumeName(svc.Name)}, &pvc)
	if apierrors.IsNotFound(err) {
		if svc.Status.Binding.Endpoint != "" {
			return nil, degraded(ReasonInstanceLost,
				"queue %s was serving and its volume %s has disappeared: not started on an empty one; "+
					"restore it under that name, labelled %s=%s",
				svc.Name, volumeName(svc.Name), OwnerLabel, svc.Name), true, nil
		}
		return nil, provider.Result{}, false, nil
	}
	if err != nil {
		return nil, provider.Result{}, false, fmt.Errorf("reading the volume: %w", err)
	}
	if pvc.Labels[OwnerLabel] != svc.Name {
		return nil, degraded(ReasonNotOwned, "a volume %s exists and is not labelled as this FrameService's: nothing written", pvc.Name), true, nil
	}
	// A retained volume is not rewritten: the template's class would say otherwise.
	if want := params["storageClass"]; want != "" && pvc.Spec.StorageClassName != nil && want != *pvc.Spec.StorageClassName {
		return nil, degraded(ReasonSpecDrift, "nothing written: storageClass is %q, the volume %s has %q",
			want, pvc.Name, *pvc.Spec.StorageClassName), true, nil
	}
	live := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if want := resource.MustParse(params["volumeSize"]); want.Cmp(live) < 0 {
		return nil, degraded(ReasonVolumeShrink, "volumeSize %s is under the volume's %s: a volume cannot shrink",
			params["volumeSize"], live.String()), true, nil
	}
	return &pvc, provider.Result{}, false, nil
}

// checkVolumeIdentity notices a volume that is not the one the queue ran on. The
// StatefulSet controller replaces a lost claim by itself, under the same name and
// the same label: only the UID differs.
func (p *Provider) checkVolumeIdentity(
	ctx context.Context, sts *appsv1.StatefulSet, volume *corev1.PersistentVolumeClaim, params map[string]string,
) (provider.Result, bool, error) {
	uid := string(volume.UID)
	recorded := sts.Annotations[volumeUIDAnnotation]
	switch {
	case recorded == uid:
		return provider.Result{}, false, nil
	case recorded != "" && params["restoredVolumeUID"] != uid:
		return degraded(ReasonInstanceLost,
			"the volume of queue %s is no longer the one it ran on (UID %s, now %s): its data may be gone; "+
				"restore what is needed, then set restoredVolumeUID to the volume's UID. "+
				"Deleting the StatefulSet would forget this record",
			sts.Name, recorded, uid), true, nil
	}
	sts.Annotations[volumeUIDAnnotation] = uid
	if err := p.client.Update(ctx, sts); err != nil {
		return provider.Result{}, false, fmt.Errorf("recording the volume's UID: %w", err)
	}
	return provider.Result{}, false, nil
}

// growVolume stops the pass while a growth is under way, or refused: a class
// that cannot expand answers Forbidden, and an error would leave status as it was.
func (p *Provider) growVolume(ctx context.Context, pvc *corev1.PersistentVolumeClaim, size string) (provider.Result, bool, error) {
	want := resource.MustParse(size)
	// A claim not bound yet cannot be resized, and its pod may be waiting on the
	// other half of this very edit.
	if want.Cmp(pvc.Spec.Resources.Requests[corev1.ResourceStorage]) == 0 || pvc.Status.Phase != corev1.ClaimBound {
		return provider.Result{}, false, nil
	}
	body, err := json.Marshal(map[string]any{"spec": map[string]any{
		"resources": map[string]any{"requests": map[string]any{"storage": size}},
	}})
	if err != nil {
		return provider.Result{}, false, err
	}
	err = p.client.Patch(ctx, pvc, client.RawPatch("application/merge-patch+json", body))
	switch {
	case apierrors.IsForbidden(err) || apierrors.IsInvalid(err):
		return degraded(ReasonVolumeGrow, "volume %s cannot grow to %s: %v", pvc.Name, size, err), true, nil
	case err != nil:
		return provider.Result{}, false, fmt.Errorf("growing the volume: %w", err)
	}
	return degraded(ReasonProvisioning, "volume %s growing to %s", pvc.Name, size), true, nil
}

// ensureAuth returns the instance's auth digest (an HMAC of the password,
// keyed by a random salt kept in the same Secret), generating the password and
// the salt on the first pass.
// The Secret exists before the pod that mounts it, and is separate from the
// binding Secret: the controller writes that one only after Ready, which a pod
// waiting on it would never reach. Valkey takes the password from auth.conf, so
// it appears in no argument and no pod spec.
func (p *Provider) ensureAuth(ctx context.Context, svc *servicesv1beta1.FrameService) (string, provider.Result, bool, error) {
	name := authSecretName(svc.Name)
	forbidden := func(action string) (string, provider.Result, bool, error) {
		return "", provider.Result{
			Reason:  provider.ReasonSecretAccessForbidden,
			Message: (&provider.SecretAccessForbidden{Namespace: svc.Namespace, Action: action}).Error(),
		}, true, nil
	}

	var secret corev1.Secret
	err := p.client.Get(ctx, client.ObjectKey{Namespace: svc.Namespace, Name: name}, &secret)
	switch {
	case err == nil:
		if !metav1.IsControlledBy(&secret, svc) {
			return "", degraded(ReasonNotOwned, "a Secret %s exists and this FrameService did not create it: nothing written", name), true, nil
		}
		password := string(secret.Data[passwordKey])
		if password == "" {
			return "", degraded(ReasonAuthInvalid, "Secret %s has no %q key: delete it for a new password", name, passwordKey), true, nil
		}
		// A password edited by hand: the server reads auth.conf, the probe and
		// consumers read password, and the two must not part. A Secret from
		// before the salt gets one.
		conf := authConf(password)
		if salt := secret.Data[saltKey]; !bytes.Equal(secret.Data[authConfKey], conf) || len(salt) == 0 {
			secret.Data[authConfKey] = conf
			if len(salt) == 0 {
				fresh, err := randomBytes()
				if err != nil {
					return "", provider.Result{}, false, err
				}
				secret.Data[saltKey] = fresh
			}
			if err := p.client.Update(ctx, &secret); err != nil {
				if apierrors.IsForbidden(err) {
					return forbidden("write")
				}
				return "", provider.Result{}, false, fmt.Errorf("updating the auth Secret: %w", err)
			}
		}
		return authDigest(secret.Data[saltKey], password), provider.Result{}, false, nil
	case apierrors.IsForbidden(err):
		return forbidden("read")
	case !apierrors.IsNotFound(err):
		return "", provider.Result{}, false, fmt.Errorf("reading the auth Secret: %w", err)
	}

	raw, err := randomBytes()
	if err != nil {
		return "", provider.Result{}, false, err
	}
	salt, err := randomBytes()
	if err != nil {
		return "", provider.Result{}, false, err
	}
	password := hex.EncodeToString(raw)
	secret = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: svc.Namespace},
		Data: map[string][]byte{
			passwordKey: []byte(password),
			authConfKey: authConf(password),
			saltKey:     salt,
		},
	}
	if err := controllerutil.SetControllerReference(svc, &secret, p.client.Scheme()); err != nil {
		return "", provider.Result{}, false, err
	}
	if err := p.client.Create(ctx, &secret); err != nil {
		if apierrors.IsForbidden(err) {
			return forbidden("write")
		}
		return "", provider.Result{}, false, fmt.Errorf("creating the auth Secret: %w", err)
	}
	return authDigest(salt, password), provider.Result{}, false, nil
}

func randomBytes() ([]byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generating auth material: %w", err)
	}
	return raw, nil
}

// authDigest changes with the password, so a new one restarts the server, and
// says nothing about it to someone without the salt.
func authDigest(salt []byte, password string) string {
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(password))
	return hex.EncodeToString(mac.Sum(nil))
}

func authConf(password string) []byte { return fmt.Appendf(nil, "requirepass %q\n", password) }

func (p *Provider) createService(ctx context.Context, svc *servicesv1beta1.FrameService) error {
	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: svc.Name, Namespace: svc.Namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{OwnerLabel: svc.Name},
			Ports:    []corev1.ServicePort{{Name: containerName, Port: port, TargetPort: intstr.FromInt32(port)}},
		},
	}
	if err := controllerutil.SetControllerReference(svc, &service, p.client.Scheme()); err != nil {
		return err
	}
	if err := p.client.Create(ctx, &service); err != nil {
		return fmt.Errorf("creating the Service: %w", err)
	}
	return nil
}

func (p *Provider) createStatefulSet(
	ctx context.Context, svc *servicesv1beta1.FrameService, params map[string]string,
	template corev1.PodTemplateSpec, hash string,
) error {
	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svc.Name,
			Namespace: svc.Namespace,
			Annotations: map[string]string{
				profileAnnotation:      params["profile"],
				templateHashAnnotation: hash,
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    new(int32(1)),
			ServiceName: svc.Name,
			Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{OwnerLabel: svc.Name}},
			Template:    template,
			// OrderedReady leaves a rollout stuck behind a pod that never got
			// ready until someone deletes it by hand.
			PodManagementPolicy: appsv1.ParallelPodManagement,
		},
	}
	if params["profile"] == ProfileQueue {
		claim := corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: dataVolume},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(params["volumeSize"])},
				},
			},
		}
		if c := params["storageClass"]; c != "" {
			claim.Spec.StorageClassName = &c
		}
		sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{claim}
	}
	if err := controllerutil.SetControllerReference(svc, &sts, p.client.Scheme()); err != nil {
		return err
	}
	if err := p.client.Create(ctx, &sts); err != nil {
		return fmt.Errorf("creating the StatefulSet: %w", err)
	}
	return nil
}

// serverArgs is the whole configuration, checked against the real image.
// auth.conf comes first: valkey-server reads a config file only as its first
// argument.
func serverArgs(params map[string]string) ([]string, error) {
	maxMemory, err := resource.ParseQuantity(params["maxMemory"])
	if err != nil {
		return nil, fmt.Errorf("maxMemory: %w", err)
	}
	args := []string{
		"valkey-server", authPath + "/" + authConfKey,
		"--dir", dataPath,
		"--maxmemory", strconv.FormatInt(maxMemory.Value(), 10),
		"--save", "",
	}
	if params["profile"] == ProfileQueue {
		return append(args, "--maxmemory-policy", "noeviction", "--appendonly", "yes", "--appendfsync", "everysec"), nil
	}
	return append(args, "--maxmemory-policy", "allkeys-lru", "--appendonly", "no"), nil
}

func podTemplate(
	svc *servicesv1beta1.FrameService, params map[string]string, sizing provider.Sizing, digest string,
) (corev1.PodTemplateSpec, error) {
	args, err := serverArgs(params)
	if err != nil {
		return corev1.PodTemplateSpec{}, err
	}
	memory := resource.MustParse(sizing.Memory)

	data := corev1.Volume{Name: dataVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
	volumes := []corev1.Volume{{
		Name: authVolume,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: authSecretName(svc.Name),
			Items:      []corev1.KeyToPath{{Key: authConfKey, Path: authConfKey}},
		}},
	}}
	if params["profile"] != ProfileQueue {
		volumes = append(volumes, data)
	}

	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{OwnerLabel: svc.Name},
			Annotations: map[string]string{authDigestAnnotation: digest},
		},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: new(false),
			NodeSelector:                 map[string]string{serviceClassLabel: string(svc.Spec.ServiceClass)},
			PriorityClassName:            scheduling.PriorityClassForServiceClass(string(svc.Spec.ServiceClass)),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   new(true),
				RunAsUser:      new(runAsUser),
				RunAsGroup:     new(runAsGroup),
				FSGroup:        new(runAsGroup),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Volumes: volumes,
			Containers: []corev1.Container{{
				Name:  containerName,
				Image: image,
				Args:  args,
				Ports: []corev1.ContainerPort{{Name: containerName, ContainerPort: port}},
				// valkey-cli reads this for the readiness probe: the password
				// never lands in the probe's command.
				Env: []corev1.EnvVar{{
					Name: "REDISCLI_AUTH",
					ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: authSecretName(svc.Name)},
						Key:                  passwordKey,
					}},
				}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(sizing.CPU),
						corev1.ResourceMemory: memory,
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(cpuLimit),
						corev1.ResourceMemory: memory,
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: authVolume, MountPath: authPath, ReadOnly: true},
					{Name: dataVolume, MountPath: dataPath},
				},
				// Ready means answering, which a server replaying its AOF is not.
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
						Command: []string{"sh", "-c", "valkey-cli ping | grep -q PONG"},
					}},
					PeriodSeconds:  5,
					TimeoutSeconds: 3,
				},
				// A TCP check, never a command: a command stalls with an fsync
				// on a slow disk, and the kubelet then kills a healthy server.
				LivenessProbe: &corev1.Probe{
					ProbeHandler:     corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}},
					PeriodSeconds:    10,
					FailureThreshold: 6,
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: new(false),
					ReadOnlyRootFilesystem:   new(true),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}, nil
}

func hashOf(template corev1.PodTemplateSpec) (string, error) {
	raw, err := json.Marshal(template)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Bind hands consumers the address and the password.
func (p *Provider) Bind(ctx context.Context, svc *servicesv1beta1.FrameService) (provider.Binding, error) {
	var secret corev1.Secret
	key := client.ObjectKey{Namespace: svc.Namespace, Name: authSecretName(svc.Name)}
	if err := p.client.Get(ctx, key, &secret); err != nil {
		if apierrors.IsForbidden(err) {
			return provider.Binding{}, &provider.SecretAccessForbidden{Namespace: svc.Namespace, Action: "read"}
		}
		return provider.Binding{}, fmt.Errorf("reading the auth Secret: %w", err)
	}
	password := secret.Data[passwordKey]
	if len(password) == 0 {
		return provider.Binding{}, fmt.Errorf("secret %s has no %q key", key, passwordKey)
	}
	// Escaped: a password edited by hand can hold what a URL gives a meaning to.
	host := strings.Join([]string{svc.Name, svc.Namespace, "svc"}, ".")
	endpoint := fmt.Sprintf("%s:%d", host, port)
	return provider.Binding{
		Endpoint: endpoint,
		Data: map[string][]byte{
			"host":     []byte(host),
			"port":     []byte(strconv.Itoa(port)),
			"password": password,
			"url":      []byte((&url.URL{Scheme: "redis", User: url.UserPassword("", string(password)), Host: endpoint}).String()),
		},
	}, nil
}

var _ provider.Provisioner = (*Provider)(nil)
