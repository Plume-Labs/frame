package cache

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	servicesv1beta1 "github.com/Plume-Labs/frame/api/services/v1beta1"
	"github.com/Plume-Labs/frame/internal/services/provider"
)

const ns = "shop"

func instance(params map[string]framev1beta1.ParameterValue) *servicesv1beta1.FrameService {
	return &servicesv1beta1.FrameService{
		ObjectMeta: metav1.ObjectMeta{Name: "jobs", Namespace: ns, UID: "svc-uid"},
		Spec:       servicesv1beta1.FrameServiceSpec{Type: "cache", Parameters: params, ServiceClass: "MEDIUM"},
	}
}

func queue() *servicesv1beta1.FrameService {
	return instance(map[string]framev1beta1.ParameterValue{
		"profile": ProfileQueue, "maxMemory": "512Mi", "volumeSize": "2Gi", "storageClass": "fast",
	})
}

func lru() *servicesv1beta1.FrameService {
	return instance(map[string]framev1beta1.ParameterValue{"profile": ProfileCache, "maxMemory": "512Mi"})
}

// writes counts what a provider sent the apiserver, and whether any was a delete.
type writes struct {
	n        int
	deleted  bool
	patchErr error
}

func newProvider(t *testing.T, w *writes, objs ...client.Object) (*Provider, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, servicesv1beta1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(&appsv1.StatefulSet{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				w.n++
				return c.Create(ctx, o, opts...)
			},
			Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
				w.n++
				return c.Update(ctx, o, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
				w.n++
				if w.patchErr != nil {
					return w.patchErr
				}
				return c.Patch(ctx, o, p, opts...)
			},
			Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
				w.deleted = true
				return c.Delete(ctx, o, opts...)
			},
		}).Build()
	return New(c, c), c
}

func reconcileOK(t *testing.T, p *Provider, svc *servicesv1beta1.FrameService) provider.Result {
	t.Helper()
	res, err := p.Reconcile(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func statefulSet(t *testing.T, c client.Client) *appsv1.StatefulSet {
	t.Helper()
	var sts appsv1.StatefulSet
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "jobs"}, &sts); err != nil {
		t.Fatal(err)
	}
	return &sts
}

func markReady(t *testing.T, c client.Client) {
	t.Helper()
	sts := statefulSet(t, c)
	sts.Status.ReadyReplicas, sts.Status.UpdatedReplicas, sts.Status.Replicas = 1, 1, 1
	sts.Status.ObservedGeneration = sts.Generation
	if err := c.Status().Update(context.Background(), sts); err != nil {
		t.Fatal(err)
	}
}

func ownedVolume(size string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-jobs-0", Namespace: ns, Labels: map[string]string{OwnerLabel: "jobs"}},
		Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
		}},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
}

func TestSize(t *testing.T) {
	p := &Provider{}
	for _, tc := range []struct {
		name, memory, refused string
		params                map[string]string
	}{
		{name: "cache", params: map[string]string{"profile": "cache", "maxMemory": "1Gi"}, memory: "1344Mi"},
		{name: "queue doubles for the fork", params: map[string]string{"profile": "queue", "maxMemory": "1Gi", "volumeSize": "3Gi"}, memory: "2112Mi"},
		{name: "queue needs a volume", params: map[string]string{"profile": "queue", "maxMemory": "1Gi"}, refused: "needs volumeSize"},
		{name: "a volume under three times the dataset", params: map[string]string{"profile": "queue", "maxMemory": "1Gi", "volumeSize": "2Gi"}, refused: "three times"},
		{name: "cache with a volume", params: map[string]string{"profile": "cache", "maxMemory": "1Gi", "volumeSize": "2Gi"}, refused: "no volume"},
		{name: "cache with a restored volume", params: map[string]string{"profile": "cache", "maxMemory": "1Gi", "restoredVolumeUID": "x"}, refused: "no volume"},
		{name: "cache with a class", params: map[string]string{"profile": "cache", "maxMemory": "1Gi", "storageClass": "fast"}, refused: "no volume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := p.Size(tc.params)
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("want a refusal naming %q, got %v", tc.refused, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Memory != tc.memory {
				t.Fatalf("memory %s, want %s", got.Memory, tc.memory)
			}
		})
	}
}

// The two profiles differ in what they promise: eviction and persistence.
func TestProfilesConfigureTheServer(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w)
	reconcileOK(t, p, queue())
	spec := statefulSet(t, c).Spec
	args := spec.Template.Spec.Containers[0].Args
	for _, want := range [][]string{{"--maxmemory-policy", "noeviction"}, {"--appendonly", "yes"}, {"--appendfsync", "everysec"}, {"--maxmemory", "536870912"}} {
		if i := slices.Index(args, want[0]); i < 0 || args[i+1] != want[1] {
			t.Errorf("queue args %v lack %v", args, want)
		}
	}
	if len(spec.VolumeClaimTemplates) != 1 || *spec.VolumeClaimTemplates[0].Spec.StorageClassName != "fast" {
		t.Errorf("queue claim templates: %+v", spec.VolumeClaimTemplates)
	}
	if slices.ContainsFunc(spec.Template.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == dataVolume }) {
		t.Error("a queue's data must be its claim, not a pod volume that would shadow it")
	}

	var w2 writes
	p, c = newProvider(t, &w2)
	reconcileOK(t, p, lru())
	spec = statefulSet(t, c).Spec
	args = spec.Template.Spec.Containers[0].Args
	for _, want := range [][]string{{"--maxmemory-policy", "allkeys-lru"}, {"--appendonly", "no"}, {"--save", ""}} {
		if i := slices.Index(args, want[0]); i < 0 || args[i+1] != want[1] {
			t.Errorf("cache args %v lack %v", args, want)
		}
	}
	if len(spec.VolumeClaimTemplates) != 0 {
		t.Error("a cache gets no volume")
	}
}

// The password reaches the server through auth.conf alone.
func TestPasswordStaysOutOfThePodSpec(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w)
	reconcileOK(t, p, queue())
	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "jobs-valkey-auth"}, &secret); err != nil {
		t.Fatal(err)
	}
	password := string(secret.Data[passwordKey])
	if len(password) != 64 || !strings.Contains(string(secret.Data[authConfKey]), password) {
		t.Fatalf("auth Secret: %q / %q", password, secret.Data[authConfKey])
	}
	if !metav1.IsControlledBy(&secret, queue()) {
		t.Error("the auth Secret exposes the instance: it needs the owner reference")
	}
	raw, err := hashInput(statefulSet(t, c).Spec.Template)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, password) {
		t.Error("the pod template carries the password")
	}
}

func TestSecondPassWritesNothingAndReportsReady(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w)
	svc := queue()
	reconcileOK(t, p, svc)
	if err := c.Create(context.Background(), ownedVolume("2Gi")); err != nil {
		t.Fatal(err)
	}
	if res := reconcileOK(t, p, svc); res.Ready {
		t.Fatal("ready before the pod is")
	}
	markReady(t, c)
	before := w.n
	res := reconcileOK(t, p, svc)
	if !res.Ready || res.Reason != ReasonRunning {
		t.Fatalf("%+v", res)
	}
	if w.n != before {
		t.Fatalf("%d writes on an unchanged instance", w.n-before)
	}
}

func TestReadyNeedsTheWholeRollout(t *testing.T) {
	for name, spoil := range map[string]func(*appsv1.StatefulSetStatus, int64){
		"unobserved generation": func(st *appsv1.StatefulSetStatus, gen int64) { st.ObservedGeneration = gen - 1 },
		"no ready pod":          func(st *appsv1.StatefulSetStatus, _ int64) { st.ReadyReplicas = 0 },
		"no updated pod":        func(st *appsv1.StatefulSetStatus, _ int64) { st.UpdatedReplicas = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			var w writes
			p, c := newProvider(t, &w)
			svc := lru()
			reconcileOK(t, p, svc)
			sts := statefulSet(t, c)
			sts.Status.ReadyReplicas, sts.Status.UpdatedReplicas, sts.Status.ObservedGeneration = 1, 1, sts.Generation
			spoil(&sts.Status, sts.Generation)
			if err := c.Status().Update(context.Background(), sts); err != nil {
				t.Fatal(err)
			}
			if res := reconcileOK(t, p, svc); res.Ready {
				t.Fatalf("%+v", res)
			}
		})
	}
}

// What the probes, the Service and the hardening rest on, read off the object.
func TestThePodSpec(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w)
	svc := queue()
	reconcileOK(t, p, svc)
	sts := statefulSet(t, c)
	pod := sts.Spec.Template.Spec
	ctr := pod.Containers[0]

	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Error("the token must stay out of the pod")
	}
	sc := pod.SecurityContext
	if sc == nil || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot || sc.RunAsUser == nil || *sc.RunAsUser != runAsUser ||
		sc.FSGroup == nil || *sc.FSGroup != runAsGroup || sc.SeccompProfile == nil {
		t.Errorf("pod security: %+v", sc)
	}
	cs := ctr.SecurityContext
	if cs == nil || cs.ReadOnlyRootFilesystem == nil || !*cs.ReadOnlyRootFilesystem ||
		cs.AllowPrivilegeEscalation == nil || *cs.AllowPrivilegeEscalation || cs.Capabilities == nil || len(cs.Capabilities.Drop) != 1 {
		t.Errorf("container security: %+v", cs)
	}
	if len(ctr.Env) != 1 || ctr.Env[0].Name != "REDISCLI_AUTH" || ctr.Env[0].ValueFrom.SecretKeyRef.Key != passwordKey {
		t.Errorf("the readiness probe's valkey-cli reads REDISCLI_AUTH: %+v", ctr.Env)
	}
	if ctr.LivenessProbe.TCPSocket == nil {
		t.Error("liveness must not run a command")
	}
	if _, limited := ctr.Resources.Limits[corev1.ResourceCPU]; !limited {
		t.Error("a FrameResourceQuota with maxCPU refuses a pod with no CPU limit")
	}
	if pod.NodeSelector[serviceClassLabel] != "MEDIUM" {
		t.Errorf("selector: %v", pod.NodeSelector)
	}
	if sts.Spec.PodManagementPolicy != appsv1.ParallelPodManagement {
		t.Error("OrderedReady can leave a rollout stuck")
	}

	var service corev1.Service
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(svc), &service); err != nil {
		t.Fatal(err)
	}
	for k, v := range service.Spec.Selector {
		if sts.Spec.Template.Labels[k] != v {
			t.Errorf("the Service selects %s=%s, the pod has %v", k, v, sts.Spec.Template.Labels)
		}
	}
	if len(service.Spec.Selector) == 0 {
		t.Error("the Service selects nothing")
	}
}

func TestMovingMaxMemoryRollsThePod(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w)
	reconcileOK(t, p, lru())
	before := statefulSet(t, c)
	moved := instance(map[string]framev1beta1.ParameterValue{"profile": ProfileCache, "maxMemory": "1Gi"})
	if res := reconcileOK(t, p, moved); res.Reason != ReasonProvisioning {
		t.Fatalf("%+v", res)
	}
	after := statefulSet(t, c)
	if after.Annotations[templateHashAnnotation] == before.Annotations[templateHashAnnotation] {
		t.Error("hash did not move")
	}
	if !slices.Contains(after.Spec.Template.Spec.Containers[0].Args, "1073741824") {
		t.Errorf("args %v", after.Spec.Template.Spec.Containers[0].Args)
	}
	limit := after.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory]
	if limit.Cmp(resource.MustParse("1344Mi")) != 0 {
		t.Errorf("limit %s", limit.String())
	}
}

// A replaced Secret is a new password: the pod has to restart to read it.
func TestNewPasswordRollsThePod(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w)
	svc := lru()
	reconcileOK(t, p, svc)
	before := statefulSet(t, c).Annotations[templateHashAnnotation]
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "jobs-valkey-auth", Namespace: ns}}
	if err := c.Delete(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	reconcileOK(t, p, svc)
	if statefulSet(t, c).Annotations[templateHashAnnotation] == before {
		t.Error("a new password left the template alone")
	}
}

// A password edited by hand: the server has to follow, or the probe never passes.
func TestAnEditedPasswordReachesTheServer(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w)
	svc := lru()
	reconcileOK(t, p, svc)
	before := statefulSet(t, c).Annotations[templateHashAnnotation]
	ctx := context.Background()
	var secret corev1.Secret
	key := client.ObjectKey{Namespace: ns, Name: "jobs-valkey-auth"}
	if err := c.Get(ctx, key, &secret); err != nil {
		t.Fatal(err)
	}
	secret.Data[passwordKey] = []byte("rotated")
	if err := c.Update(ctx, &secret); err != nil {
		t.Fatal(err)
	}
	reconcileOK(t, p, svc)
	if err := c.Get(ctx, key, &secret); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secret.Data[authConfKey]), "rotated") {
		t.Fatalf("auth.conf: %s", secret.Data[authConfKey])
	}
	if statefulSet(t, c).Annotations[templateHashAnnotation] == before {
		t.Error("the pod must roll onto the new auth.conf")
	}
}

func TestASecretWithNoPasswordDegrades(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w)
	svc := lru()
	reconcileOK(t, p, svc)
	ctx := context.Background()
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "jobs-valkey-auth"}, &secret); err != nil {
		t.Fatal(err)
	}
	delete(secret.Data, passwordKey)
	if err := c.Update(ctx, &secret); err != nil {
		t.Fatal(err)
	}
	if res := reconcileOK(t, p, svc); res.Reason != ReasonAuthInvalid {
		t.Fatalf("%+v", res)
	}
}

func TestANameThePodsCannotCarryIsRefusedBeforeAnyWrite(t *testing.T) {
	for _, name := range []string{"a.b", "1jobs", strings.Repeat("a", 53)} {
		var w writes
		p, _ := newProvider(t, &w)
		svc := lru()
		svc.Name = name
		if res := reconcileOK(t, p, svc); res.Reason != ReasonInvalidName {
			t.Fatalf("%q: %+v", name, res)
		}
		if w.n != 0 {
			t.Fatalf("%q: %d writes", name, w.n)
		}
	}
	var w writes
	p, _ := newProvider(t, &w)
	svc := lru()
	svc.Name = strings.Repeat("a", 52)
	if res := reconcileOK(t, p, svc); res.Reason == ReasonInvalidName {
		t.Fatalf("the longest name that fits: %+v", res)
	}
}

func TestReplicasStayAtOne(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w)
	svc := lru()
	reconcileOK(t, p, svc)
	sts := statefulSet(t, c)
	sts.Spec.Replicas = new(int32(2))
	if err := c.Update(context.Background(), sts); err != nil {
		t.Fatal(err)
	}
	reconcileOK(t, p, svc)
	if got := *statefulSet(t, c).Spec.Replicas; got != 1 {
		t.Fatalf("two servers behind one Service, %d replicas", got)
	}
}

// A write that lost to a stale cache is the next pass's business, not an error.
func TestALostRaceIsNoFailure(t *testing.T) {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, servicesv1beta1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			if _, ok := o.(*appsv1.StatefulSet); ok {
				return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "statefulsets"}, o.GetName())
			}
			return c.Create(ctx, o, opts...)
		},
	}).Build()
	if res := reconcileOK(t, New(c, c), lru()); res.Reason != ReasonProvisioning {
		t.Fatalf("%+v", res)
	}
}

func TestAConflictIsNoFailure(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w)
	reconcileOK(t, p, lru())
	moved := instance(map[string]framev1beta1.ParameterValue{"profile": ProfileCache, "maxMemory": "1Gi"})
	raced := New(interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			return apierrors.NewConflict(schema.GroupResource{Resource: "statefulsets"}, "jobs", errors.New("stale"))
		},
	}), c)
	if res := reconcileOK(t, raced, moved); res.Reason != ReasonProvisioning {
		t.Fatalf("%+v", res)
	}
}

func TestAnEditedPasswordCannotBreakTheURL(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w)
	svc := lru()
	reconcileOK(t, p, svc)
	ctx := context.Background()
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "jobs-valkey-auth"}, &secret); err != nil {
		t.Fatal(err)
	}
	secret.Data[passwordKey] = []byte("p@ss/w#rd")
	if err := c.Update(ctx, &secret); err != nil {
		t.Fatal(err)
	}
	b, err := p.Bind(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(string(b.Data["url"]))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := u.User.Password(); got != "p@ss/w#rd" || u.Host != "jobs.shop.svc:6379" {
		t.Fatalf("%q: %q", u.Host, got)
	}
}

func TestForeignObjectsAreLeftAlone(t *testing.T) {
	foreign := metav1.ObjectMeta{Name: "jobs", Namespace: ns}
	for name, obj := range map[string]client.Object{
		"statefulset": &appsv1.StatefulSet{ObjectMeta: foreign},
		"service":     &corev1.Service{ObjectMeta: foreign},
		"secret":      &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "jobs-valkey-auth", Namespace: ns}},
		"volume":      &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-jobs-0", Namespace: ns}},
	} {
		t.Run(name, func(t *testing.T) {
			var w writes
			p, _ := newProvider(t, &w, obj)
			res := reconcileOK(t, p, queue())
			if res.Reason != ReasonNotOwned {
				t.Fatalf("%+v", res)
			}
			if w.n != 0 {
				t.Fatalf("%d writes beside a foreign %s", w.n, name)
			}
		})
	}
}

func TestProfileAndClassAreFixed(t *testing.T) {
	for name, next := range map[string]*servicesv1beta1.FrameService{
		"profile": lru(),
		"class": instance(map[string]framev1beta1.ParameterValue{
			"profile": ProfileQueue, "maxMemory": "512Mi", "volumeSize": "2Gi", "storageClass": "other",
		}),
	} {
		t.Run(name, func(t *testing.T) {
			var w writes
			p, _ := newProvider(t, &w)
			reconcileOK(t, p, queue())
			before := w.n
			if res := reconcileOK(t, p, next); res.Reason != ReasonSpecDrift {
				t.Fatalf("%+v", res)
			}
			if w.n != before {
				t.Fatal("a drift wrote")
			}
		})
	}
}

func TestAQueueThatServedIsNotRestartedEmpty(t *testing.T) {
	var w writes
	p, _ := newProvider(t, &w)
	svc := queue()
	svc.Status.Binding.Endpoint = "jobs.shop.svc:6379"
	if res := reconcileOK(t, p, svc); res.Reason != ReasonInstanceLost {
		t.Fatalf("%+v", res)
	}
	if w.n != 0 {
		t.Fatal("wrote beside a lost queue")
	}
}

// What the StatefulSet controller does with a lost claim: a new one, same name,
// same label, empty. Only the UID gives it away, and it must stay degraded.
func TestAReplacedVolumeIsNoticedAndStaysSo(t *testing.T) {
	ctx := context.Background()
	var w writes
	first := ownedVolume("2Gi")
	first.UID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	p, c := newProvider(t, &w, first)
	svc := queue()
	reconcileOK(t, p, svc) // creates the StatefulSet
	reconcileOK(t, p, svc) // records the volume
	if got := statefulSet(t, c).Annotations[volumeUIDAnnotation]; got != string(first.UID) {
		t.Fatalf("recorded %q", got)
	}
	markReady(t, c)
	if res := reconcileOK(t, p, svc); !res.Ready {
		t.Fatalf("%+v", res)
	}

	if err := c.Delete(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := ownedVolume("2Gi")
	second.ResourceVersion = ""
	second.UID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	if err := c.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		res := reconcileOK(t, p, svc)
		if res.Ready || res.Reason != ReasonInstanceLost || !strings.Contains(res.Message, string(second.UID)) {
			t.Fatalf("%+v", res)
		}
	}

	// A person says this volume is the one wanted, and only this one.
	accepted := queue()
	accepted.Spec.Parameters["restoredVolumeUID"] = framev1beta1.ParameterValue(second.UID)
	if res := reconcileOK(t, p, accepted); !res.Ready {
		t.Fatalf("%+v", res)
	}
	if got := statefulSet(t, c).Annotations[volumeUIDAnnotation]; got != string(second.UID) {
		t.Fatalf("recorded %q", got)
	}
	other := queue()
	other.Spec.Parameters["restoredVolumeUID"] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	if res := reconcileOK(t, p, other); !res.Ready {
		t.Fatalf("the recorded volume is still the live one: %+v", res)
	}
}

// A FrameService deleted under Retain leaves its volume, and the claim template
// of the same name is what mounts it again.
func TestARecreatedQueueKeepsItsVolume(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w, ownedVolume("2Gi"))
	if res := reconcileOK(t, p, queue()); res.Reason != ReasonProvisioning {
		t.Fatalf("%+v", res)
	}
	sts := statefulSet(t, c)
	if got := sts.Spec.VolumeClaimTemplates[0].Name + "-" + sts.Name + "-0"; got != "data-jobs-0" {
		t.Fatalf("the claim would be %s", got)
	}
}

func TestARetainedVolumeOfAnotherClassIsDrift(t *testing.T) {
	var w writes
	old := ownedVolume("2Gi")
	class := "slow"
	old.Spec.StorageClassName = &class
	p, _ := newProvider(t, &w, old)
	if res := reconcileOK(t, p, queue()); res.Reason != ReasonSpecDrift {
		t.Fatalf("%+v", res)
	}
	if w.n != 0 {
		t.Fatal("a drift wrote")
	}
}

func TestVolumeGrowsAndNeverShrinks(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w, ownedVolume("4Gi"))
	before := w.n
	if res := reconcileOK(t, p, queue()); res.Reason != ReasonVolumeShrink {
		t.Fatalf("%+v", res)
	}
	if w.n != before {
		t.Fatal("a refused shrink wrote")
	}

	grown := instance(map[string]framev1beta1.ParameterValue{
		"profile": ProfileQueue, "maxMemory": "512Mi", "volumeSize": "8Gi", "storageClass": "fast",
	})
	if res := reconcileOK(t, p, grown); res.Reason != ReasonProvisioning {
		t.Fatalf("%+v", res)
	}
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "data-jobs-0"}, &pvc); err != nil {
		t.Fatal(err)
	}
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(resource.MustParse("8Gi")) != 0 {
		t.Fatalf("volume is %s", got.String())
	}
}

// A class with no expansion: the status has to say so, and the other half of
// the same edit must not restart the server for nothing.
func TestARefusedGrowthDegradesBeforeTheTemplateMoves(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w, ownedVolume("2Gi"))
	svc := queue()
	reconcileOK(t, p, svc)
	reconcileOK(t, p, svc)
	before := statefulSet(t, c).Annotations[templateHashAnnotation]
	w.patchErr = apierrors.NewForbidden(schema.GroupResource{Resource: "persistentvolumeclaims"}, "data-jobs-0",
		errors.New("the storageclass does not support resize"))
	both := instance(map[string]framev1beta1.ParameterValue{
		"profile": ProfileQueue, "maxMemory": "1Gi", "volumeSize": "8Gi", "storageClass": "fast",
	})
	if res := reconcileOK(t, p, both); res.Reason != ReasonVolumeGrow {
		t.Fatalf("%+v", res)
	}
	if statefulSet(t, c).Annotations[templateHashAnnotation] != before {
		t.Fatal("the server restarted for an edit that was refused")
	}
}

// A claim waiting for its pod cannot grow, and the pod may be waiting on the
// template of this same edit.
func TestAnUnboundVolumeDoesNotHoldTheTemplateBack(t *testing.T) {
	var w writes
	unbound := ownedVolume("2Gi")
	unbound.Status.Phase = corev1.ClaimPending
	p, c := newProvider(t, &w, unbound)
	svc := queue()
	reconcileOK(t, p, svc)
	before := statefulSet(t, c).Annotations[templateHashAnnotation]
	w.patchErr = apierrors.NewInvalid(schema.GroupKind{Kind: "PersistentVolumeClaim"}, "data-jobs-0", nil)
	both := instance(map[string]framev1beta1.ParameterValue{
		"profile": ProfileQueue, "maxMemory": "256Mi", "volumeSize": "8Gi", "storageClass": "fast",
	})
	reconcileOK(t, p, both)
	if statefulSet(t, c).Annotations[templateHashAnnotation] == before {
		t.Fatal("the template stayed behind a claim that cannot grow yet")
	}
}

func TestARefusalTheStorageCallsInvalidDegradesToo(t *testing.T) {
	var w writes
	p, _ := newProvider(t, &w, ownedVolume("2Gi"))
	w.patchErr = apierrors.NewInvalid(schema.GroupKind{Kind: "PersistentVolumeClaim"}, "data-jobs-0", nil)
	grown := instance(map[string]framev1beta1.ParameterValue{
		"profile": ProfileQueue, "maxMemory": "512Mi", "volumeSize": "8Gi", "storageClass": "fast",
	})
	if res := reconcileOK(t, p, grown); res.Reason != ReasonVolumeGrow {
		t.Fatalf("%+v", res)
	}
}

func TestOwnership(t *testing.T) {
	var w writes
	p, c := newProvider(t, &w)
	svc := queue()
	reconcileOK(t, p, svc)
	sts := statefulSet(t, c)
	if !metav1.IsControlledBy(sts, svc) {
		t.Error("the StatefulSet needs the owner reference")
	}
	if len(sts.Spec.VolumeClaimTemplates[0].OwnerReferences) != 0 {
		t.Error("the volume is data: no owner reference")
	}
	if sts.Spec.PersistentVolumeClaimRetentionPolicy != nil {
		t.Error("the volume outlives the StatefulSet: the default retention is the contract")
	}
	markReady(t, c)
	res := reconcileOK(t, p, svc)
	if !res.Ready {
		t.Fatalf("%+v", res)
	}
	if len(res.Provisioned) != 0 {
		t.Errorf("status.provisioned is what deletionPolicy: Delete removes, and the manager cannot: %+v", res.Provisioned)
	}
	if w.deleted {
		t.Error("the manager holds no delete on what it wrote")
	}
}

func TestForbiddenSecretsDegrade(t *testing.T) {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, servicesv1beta1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			if _, ok := o.(*corev1.Secret); ok {
				return apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, k.Name, nil)
			}
			return c.Get(ctx, k, o, opts...)
		},
	}).Build()
	res := reconcileOK(t, New(c, c), lru())
	if res.Reason != provider.ReasonSecretAccessForbidden {
		t.Fatalf("%+v", res)
	}
}

func TestBind(t *testing.T) {
	var w writes
	p, _ := newProvider(t, &w)
	svc := lru()
	reconcileOK(t, p, svc)
	b, err := p.Bind(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	if b.Endpoint != "jobs.shop.svc:6379" || strings.Contains(b.Endpoint, string(b.Data["password"])) {
		t.Fatalf("endpoint %q", b.Endpoint)
	}
	if want := "redis://:" + string(b.Data["password"]) + "@jobs.shop.svc:6379"; string(b.Data["url"]) != want {
		t.Fatalf("url %q", b.Data["url"])
	}
}

func hashInput(template corev1.PodTemplateSpec) (string, error) {
	raw, err := template.Marshal()
	return string(raw), err
}
