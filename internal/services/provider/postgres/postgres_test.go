package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	servicesv1beta1 "github.com/Plume-Labs/frame/api/services/v1beta1"
	"github.com/Plume-Labs/frame/internal/services/provider"
)

const (
	ns  = "shop"
	uid = "11111111-2222-4333-8444-555555555555"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, gvk := range []schema.GroupVersionKind{clusterGVK, bucketGVK} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		list := gvk
		list.Kind += "List"
		s.AddKnownTypeWithName(list, &unstructured.UnstructuredList{})
	}
	return s
}

// liveCluster is a cluster a chart rendered: what an adoption finds, down to
// the fields this provider must never touch.
func liveCluster() *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"teamId":               "shop",
			"numberOfInstances":    int64(1),
			"postgresql":           map[string]any{"version": "18"},
			"volume":               map[string]any{"size": "5Gi", "storageClass": "fast"},
			"users":                map[string]any{"shop": []any{"superuser", "createdb"}},
			"databases":            map[string]any{"shop": "shop"},
			"podPriorityClassName": "site-interactive",
			"patroni":              map[string]any{"failsafe_mode": true, "ttl": int64(90)},
			"nodeAffinity":         map[string]any{"from": "the chart"},
			"env": []any{
				map[string]any{"name": "WAL_S3_BUCKET", "valueFrom": map[string]any{
					"configMapKeyRef": map[string]any{"name": "shop-wal", "key": "BUCKET_NAME"},
				}},
				map[string]any{"name": "AWS_ENDPOINT", "value": "http://rgw:80"},
				map[string]any{"name": "BACKUP_NUM_TO_RETAIN", "value": "14"},
			},
		},
		"status": map[string]any{"PostgresClusterStatus": "Running"},
	}}
	u.SetGroupVersionKind(clusterGVK)
	u.SetName("shop-db")
	u.SetNamespace(ns)
	u.SetUID(types.UID(uid))
	u.SetAnnotations(map[string]string{"meta.helm.sh/release-name": "shop"})
	return u
}

func liveBucket() *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"generateBucketName": "shop-wal"}}}
	u.SetGroupVersionKind(bucketGVK)
	u.SetName("shop-wal")
	u.SetNamespace(ns)
	return u
}

func adopting() map[string]string {
	return map[string]string{
		"instances": "1", "volumeSize": "5Gi", "storageClass": "fast",
		"walBucketClaim": "shop-wal", "walEndpoint": "http://rgw:80", "adoptUID": uid,
	}
}

func service(params map[string]string) *servicesv1beta1.FrameService {
	spec := servicesv1beta1.FrameServiceSpec{Type: "postgres", ServiceClass: framev1beta1.ServiceClass("HIGH")}
	spec.Parameters = map[string]framev1beta1.ParameterValue{}
	for k, v := range params {
		spec.Parameters[k] = framev1beta1.ParameterValue(v)
	}
	return &servicesv1beta1.FrameService{ObjectMeta: metav1.ObjectMeta{Name: "shop-db", Namespace: ns}, Spec: spec}
}

// writes counts what reaches the apiserver, so "nothing written" is measured
// rather than inferred from the object looking the same.
type writes struct{ creates, patches []string }

func newProvider(t *testing.T, objs ...client.Object) (*Provider, client.Client, *writes) {
	t.Helper()
	w := &writes{}
	c := interceptor.NewClient(fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build(), interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			w.creates = append(w.creates, o.GetObjectKind().GroupVersionKind().Kind+"/"+o.GetName())
			return c.Create(ctx, o, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
			data, _ := p.Data(o)
			w.patches = append(w.patches, o.GetObjectKind().GroupVersionKind().Kind+":"+string(data))
			return c.Patch(ctx, o, p, opts...)
		},
	})
	return New(c), c, w
}

func reconcile(t *testing.T, p *Provider, svc *servicesv1beta1.FrameService) provider.Result {
	t.Helper()
	res, err := p.Reconcile(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func cluster(t *testing.T, c client.Client) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(clusterGVK)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "shop-db"}, u); err != nil {
		t.Fatal(err)
	}
	return u
}

func specOf(t *testing.T, u *unstructured.Unstructured) map[string]any {
	t.Helper()
	s, _, err := unstructured.NestedMap(u.Object, "spec")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSchemaIsOneTheWebhookEnforces(t *testing.T) {
	provider.NewRegistry(New(nil)) // panics otherwise
}

func TestSizeRefusesWhatCannotWork(t *testing.T) {
	p := New(nil)
	for name, params := range map[string]map[string]string{
		"adopt and create":          {"instances": "1", "volumeSize": "1Gi", "adoptUID": uid, "create": "true"},
		"archive with no endpoint":  {"instances": "1", "volumeSize": "1Gi", "walBucketClaim": "b"},
		"more instances than nodes": {"instances": "3", "volumeSize": "1Gi", "nodes": "a,b"},
	} {
		if _, err := p.Size(params); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := p.Size(map[string]string{"instances": "2", "volumeSize": "1Gi", "nodes": "a,b"}); err != nil {
		t.Errorf("two instances on two nodes: %v", err)
	}
}

func TestCreatesAClusterOnceWhenThereIsNone(t *testing.T) {
	p, c, w := newProvider(t)
	svc := service(map[string]string{
		"instances": "2", "volumeSize": "10Gi", "storageClass": "fast", "nodes": "a,b",
		"walBucketClaim": "shop-wal", "walEndpoint": "http://rgw:80", "create": "true",
	})
	if res := reconcile(t, p, svc); res.Ready || res.Reason != ReasonProvisioning {
		t.Fatalf("%+v", res)
	}
	if len(w.creates) != 2 {
		t.Fatalf("creates: %v", w.creates)
	}
	got := cluster(t, c)
	if len(got.GetOwnerReferences()) != 0 {
		t.Error("the cluster is data, it must carry no owner reference")
	}
	if got.GetAnnotations()[keepAnnotation] != keepValue || got.GetLabels()[OwnerLabel] != "shop-db" {
		t.Errorf("metadata: %v %v", got.GetAnnotations(), got.GetLabels())
	}
	spec := specOf(t, got)
	if spec["teamId"] != "shop" || spec["numberOfInstances"] != int64(2) {
		t.Errorf("%v", spec)
	}
	claim, endpoint, retention := liveWAL(got)
	if claim != "shop-wal" || endpoint != "http://rgw:80" || retention != defaultRetentionDays {
		t.Errorf("WAL: %s %s %s", claim, endpoint, retention)
	}
	// What was created must satisfy this provider's own reading of a cluster,
	// or the next pass would degrade on its own work.
	if drift := driftOf(got, mustSettings(t, svc)); len(drift) != 0 {
		t.Errorf("drift on its own creation: %v", drift)
	}
	// The operator's status arrives; nothing further is written.
	if err := unstructured.SetNestedField(got.Object, "Running", "status", "PostgresClusterStatus"); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	w.patches = nil
	if res := reconcile(t, p, svc); !res.Ready {
		t.Fatalf("%+v", res)
	}
	if len(w.patches) != 0 {
		t.Errorf("patched a converged cluster: %v", w.patches)
	}
	if len(res(t, p, svc).Provisioned) != 0 {
		t.Error("data objects listed in provisioned: deletionPolicy Delete would reach them")
	}
}

func res(t *testing.T, p *Provider, svc *servicesv1beta1.FrameService) provider.Result {
	t.Helper()
	return reconcile(t, p, svc)
}

func mustSettings(t *testing.T, svc *servicesv1beta1.FrameService) settings {
	t.Helper()
	s, ok := settingsOf(svc)
	if !ok {
		t.Fatal("settings")
	}
	return s
}

func TestNeverRecreates(t *testing.T) {
	served := service(map[string]string{"instances": "1", "volumeSize": "1Gi", "create": "true"})
	served.Status.Binding.Endpoint = "shop-db.shop.svc:5432"
	removed := adopting()
	delete(removed, "adoptUID")
	leftover := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "pgdata-shop-db-0", Namespace: ns}}
	tests := []struct {
		name string
		svc  *servicesv1beta1.FrameService
		objs []client.Object
		want string
	}{
		{"adoption target gone", service(adopting()), nil, ReasonAdoptionMissing},
		{"cluster gone, adoptUID removed, never served", service(removed), nil, ReasonClusterMissing},
		{"a served cluster gone", served, nil, ReasonInstanceLost},
		{"never served, data volume still there", service(map[string]string{"instances": "1", "volumeSize": "1Gi", "create": "true"}),
			[]client.Object{leftover}, ReasonInstanceLost},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, w := newProvider(t, tt.objs...)
			if res := reconcile(t, p, tt.svc); res.Reason != tt.want {
				t.Fatalf("%+v", res)
			}
			if len(w.creates) != 0 {
				t.Fatalf("created %v", w.creates)
			}
		})
	}
}

func TestAnExistingClusterIsNotClaimedByAccident(t *testing.T) {
	tests := map[string]map[string]string{
		"no adoptUID":        {"instances": "1", "volumeSize": "5Gi"},
		"create is no claim": {"instances": "1", "volumeSize": "5Gi", "create": "true"},
		"wrong UID": func() map[string]string {
			p := adopting()
			p["adoptUID"] = "00000000-0000-0000-0000-000000000000"
			return p
		}(),
	}
	for name, params := range tests {
		t.Run(name, func(t *testing.T) {
			p, _, w := newProvider(t, liveCluster(), liveBucket())
			res := reconcile(t, p, service(params))
			if res.Reason != ReasonNotOwned || !strings.Contains(res.Message, uid) {
				t.Fatalf("%+v", res)
			}
			if len(w.patches)+len(w.creates) != 0 {
				t.Fatalf("wrote: %v %v", w.patches, w.creates)
			}
		})
	}
}

// A CR restored from a backup taken after the adoption: label there, UID new.
func TestTheLabelDoesNotReplaceTheUID(t *testing.T) {
	restored := liveCluster()
	restored.SetUID("99999999-2222-4333-8444-555555555555")
	restored.SetLabels(map[string]string{OwnerLabel: "shop-db"})
	p, _, w := newProvider(t, restored, liveBucket())
	if res := reconcile(t, p, service(adopting())); res.Reason != ReasonNotOwned {
		t.Fatalf("%+v", res)
	}
	removed := adopting()
	delete(removed, "adoptUID")
	removed["instances"] = "2"
	if res := reconcile(t, p, service(removed)); res.Reason != ReasonNotOwned {
		t.Fatalf("label alone: %+v", res)
	}
	if len(w.patches) != 0 {
		t.Fatalf("wrote: %v", w.patches)
	}
}

// The same, for a cluster Frame created: a restore under a new UID is taken
// back by no one but a person.
func TestARestoredClusterIsNotClaimedByCreate(t *testing.T) {
	restored := liveCluster()
	restored.SetUID("99999999-2222-4333-8444-555555555555")
	restored.SetLabels(map[string]string{OwnerLabel: "shop-db"})
	restored.SetAnnotations(map[string]string{ClaimedUIDAnnotation: uid, keepAnnotation: keepValue})
	p, _, w := newProvider(t, restored, liveBucket())
	params := map[string]string{"instances": "2", "volumeSize": "5Gi", "create": "true"}
	if res := reconcile(t, p, service(params)); res.Reason != ReasonNotOwned {
		t.Fatalf("%+v", res)
	}
	if len(w.patches) != 0 {
		t.Fatalf("wrote: %v", w.patches)
	}
}

// Re-adopting a restore: the UID is given again, and the settings that moved
// since must be stated, not applied to the restored database by surprise.
func TestReAdoptingARestoreComparesTheMovableSettings(t *testing.T) {
	restored := liveCluster()
	restored.SetLabels(map[string]string{OwnerLabel: "shop-db"})
	restored.SetAnnotations(map[string]string{ClaimedUIDAnnotation: "99999999-2222-4333-8444-555555555555"})
	params := adopting()
	params["instances"], params["nodes"] = "2", "a,b"
	p, _, w := newProvider(t, restored, liveBucket())
	if res := reconcile(t, p, service(params)); res.Reason != ReasonSpecDrift {
		t.Fatalf("%+v", res)
	}
	if len(w.patches) != 0 {
		t.Fatalf("wrote: %v", w.patches)
	}
}

func TestAVolumeCheckThatFailsCreatesNothing(t *testing.T) {
	var created []string
	c := interceptor.NewClient(fake.NewClientBuilder().WithScheme(testScheme(t)).Build(), interceptor.Funcs{
		Get: func(ctx context.Context, w client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			if strings.HasPrefix(k.Name, "pgdata-") {
				return errors.New("apiserver unavailable")
			}
			return w.Get(ctx, k, o, opts...)
		},
		Create: func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.CreateOption) error {
			created = append(created, o.GetName())
			return nil
		},
	})
	svc := service(map[string]string{"instances": "1", "volumeSize": "1Gi", "create": "true"})
	if _, err := New(c).Reconcile(context.Background(), svc); err == nil {
		t.Fatal("no error")
	}
	if len(created) != 0 {
		t.Fatalf("created %v", created)
	}
}

func TestTheAdoptionPassWritesNoSpec(t *testing.T) {
	tests := map[string]func(map[string]string){
		"more instances": func(p map[string]string) { p["instances"] = "2" },
		"larger volume":  func(p map[string]string) { p["volumeSize"] = "6Gi" },
		"smaller volume": func(p map[string]string) { p["volumeSize"] = "4Gi" },
		"placement":      func(p map[string]string) { p["nodes"] = "a,b" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			params := adopting()
			live := liveCluster()
			if mutate == nil {
				if err := unstructured.SetNestedField(live.Object, "other", "spec", "teamId"); err != nil {
					t.Fatal(err)
				}
			} else {
				mutate(params)
			}
			p, _, w := newProvider(t, live, liveBucket())
			if res := reconcile(t, p, service(params)); res.Reason != ReasonSpecDrift {
				t.Fatalf("%+v", res)
			}
			if len(w.patches) != 0 {
				t.Fatalf("wrote: %v", w.patches)
			}
		})
	}
}

func TestTheSameSizeSpelledDifferentlyIsNoDrift(t *testing.T) {
	params := adopting()
	params["volumeSize"] = "5120Mi"
	p, _, _ := newProvider(t, liveCluster(), liveBucket())
	if res := reconcile(t, p, service(params)); !res.Ready {
		t.Fatalf("%+v", res)
	}
}

func TestAdoptionClaimsTheBucketWithoutOwningIt(t *testing.T) {
	p, c, _ := newProvider(t, liveCluster(), liveBucket())
	reconcile(t, p, service(adopting()))
	got := liveBucket()
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(got), got); err != nil {
		t.Fatal(err)
	}
	if got.GetLabels()[OwnerLabel] != "shop-db" || got.GetAnnotations()[keepAnnotation] != keepValue {
		t.Fatalf("%v %v", got.GetLabels(), got.GetAnnotations())
	}
	if len(got.GetOwnerReferences()) != 0 || len(cluster(t, c).GetOwnerReferences()) != 0 {
		t.Fatal("an owner reference would hand the data to garbage collection")
	}
}

func TestABucketOfAnotherServiceIsNotTaken(t *testing.T) {
	b := liveBucket()
	b.SetLabels(map[string]string{OwnerLabel: "other-db"})
	p, _, w := newProvider(t, liveCluster(), b)
	res := reconcile(t, p, service(adopting()))
	if res.Reason != ReasonNotOwned {
		t.Fatalf("%+v", res)
	}
	for _, patch := range w.patches {
		if strings.HasPrefix(patch, "ObjectBucketClaim") {
			t.Fatalf("wrote: %s", patch)
		}
	}
}

func TestAnExistingBucketIsNotRecreated(t *testing.T) {
	p, _, w := newProvider(t, liveBucket())
	svc := service(map[string]string{"instances": "1", "volumeSize": "1Gi", "create": "true", "walBucketClaim": "shop-wal", "walEndpoint": "http://rgw:80"})
	reconcile(t, p, svc)
	for _, c := range w.creates {
		if strings.HasPrefix(c, "ObjectBucketClaim") {
			t.Fatalf("created: %v", w.creates)
		}
	}
}

func TestKeepIsPutBackIfRemoved(t *testing.T) {
	p, c, w := newProvider(t, liveCluster(), liveBucket())
	svc := service(adopting())
	reconcile(t, p, svc)
	got := cluster(t, c)
	got.SetAnnotations(map[string]string{})
	if err := c.Update(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	w.patches = nil
	reconcile(t, p, svc)
	if cluster(t, c).GetAnnotations()[keepAnnotation] != keepValue {
		t.Fatal("keep not restored")
	}
}

func TestBindingNoneTouchesNoSecret(t *testing.T) {
	params := adopting()
	params["binding"] = "none"
	c := interceptor.NewClient(fake.NewClientBuilder().WithScheme(testScheme(t)).Build(), interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			t.Fatal("read a Secret")
			return nil
		},
	})
	b, err := New(c).Bind(context.Background(), service(params))
	if err != nil || !b.None || b.Endpoint == "" || b.Data != nil {
		t.Fatalf("%+v %v", b, err)
	}
}

func TestAdoptionWritesOnlyTheClaim(t *testing.T) {
	p, c, w := newProvider(t, liveCluster(), liveBucket())
	before := specOf(t, cluster(t, c))
	svc := service(adopting())

	if res := reconcile(t, p, svc); !res.Ready {
		t.Fatalf("%+v", res)
	}
	after := cluster(t, c)
	if got := specOf(t, after); !equalJSON(before, got) {
		t.Fatalf("spec moved:\n%v\n%v", before, got)
	}
	if string(after.GetUID()) != uid {
		t.Fatal("not the same object")
	}
	if after.GetLabels()[OwnerLabel] != "shop-db" || after.GetAnnotations()[keepAnnotation] != keepValue ||
		after.GetAnnotations()[ClaimedUIDAnnotation] != uid {
		t.Fatalf("not claimed: %v %v", after.GetLabels(), after.GetAnnotations())
	}
	if after.GetAnnotations()["meta.helm.sh/release-name"] != "shop" {
		t.Error("the chart's annotations are the chart's to retire, not the provider's")
	}
	for _, patch := range w.patches {
		if strings.Contains(patch, `"spec"`) {
			t.Fatalf("spec patched: %s", patch)
		}
	}

	// Claimed, it stays quiet.
	w.patches = nil
	reconcile(t, p, svc)
	if len(w.patches) != 0 {
		t.Fatalf("second pass wrote: %v", w.patches)
	}
}

func TestDriftWritesNothing(t *testing.T) {
	tests := map[string]func(map[string]string){
		"storage class": func(p map[string]string) { p["storageClass"] = "slow" },
		"version":       func(p map[string]string) { p["postgresVersion"] = "17" },
		"owner":         func(p map[string]string) { p["owner"] = "other" },
		"database":      func(p map[string]string) { p["database"] = "other" },
		"WAL bucket":    func(p map[string]string) { p["walBucketClaim"] = "other" },
		"WAL off":       func(p map[string]string) { delete(p, "walBucketClaim"); delete(p, "walEndpoint") },
		"WAL endpoint":  func(p map[string]string) { p["walEndpoint"] = "http://other" },
		"WAL retention": func(p map[string]string) { p["walRetentionDays"] = "7" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			params := adopting()
			mutate(params)
			p, _, w := newProvider(t, liveCluster(), liveBucket())
			if res := reconcile(t, p, service(params)); res.Reason != ReasonSpecDrift {
				t.Fatalf("%+v", res)
			}
			if len(w.patches) != 0 {
				t.Fatalf("wrote: %v", w.patches)
			}
		})
	}
}

func TestOwnerMustBeAUserOfTheCluster(t *testing.T) {
	live := liveCluster()
	if err := unstructured.SetNestedField(live.Object, map[string]any{"other": []any{}}, "spec", "users"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := newProvider(t, live, liveBucket())
	if res := reconcile(t, p, service(adopting())); res.Reason != ReasonSpecDrift {
		t.Fatalf("%+v", res)
	}
}

func TestMovesOnlyWhatMayMove(t *testing.T) {
	p, c, _ := newProvider(t, liveCluster(), liveBucket())
	svc := service(adopting())
	reconcile(t, p, svc)
	before := specOf(t, cluster(t, c))

	params := adopting()
	params["instances"], params["volumeSize"], params["nodes"] = "2", "8Gi", "a,b"
	res := reconcile(t, p, service(params))
	if res.Ready || res.Reason != ReasonProvisioning {
		t.Fatalf("%+v", res)
	}
	after := specOf(t, cluster(t, c))
	if after["numberOfInstances"] != int64(2) {
		t.Error("instances not moved")
	}
	volume := after["volume"].(map[string]any)
	if volume["size"] != "8Gi" || volume["storageClass"] != "fast" {
		t.Errorf("volume: %v", volume)
	}
	spread, _, _ := unstructured.NestedSlice(after, "topologySpreadConstraints")
	if len(spread) != 1 {
		t.Fatalf("spread: %v", spread)
	}
	if sel, _, _ := unstructured.NestedString(spread[0].(map[string]any), "labelSelector", "matchLabels", clusterNameLabel); sel != "shop-db" {
		t.Errorf("a spread with no selector counts no pod: %v", spread)
	}
	if !strings.Contains(toJSON(after["nodeAffinity"]), `"values":["a","b"]`) {
		t.Errorf("nodeAffinity: %v", after["nodeAffinity"])
	}
	for _, field := range []string{"patroni", "env", "users", "databases", "podPriorityClassName", "teamId", "postgresql"} {
		if !equalJSON(before[field], after[field]) {
			t.Errorf("%s moved", field)
		}
	}
}

func TestPlacementIsLeftAloneWhenNotAskedFor(t *testing.T) {
	p, c, _ := newProvider(t, liveCluster(), liveBucket())
	params := adopting()
	params["instances"] = "2"
	reconcile(t, p, service(params))
	if got := specOf(t, cluster(t, c))["nodeAffinity"]; !equalJSON(got, map[string]any{"from": "the chart"}) {
		t.Fatalf("nodeAffinity replaced: %v", got)
	}
}

func TestVolumeNeverShrinks(t *testing.T) {
	p, _, w := newProvider(t, liveCluster(), liveBucket())
	reconcile(t, p, service(adopting()))
	w.patches = nil
	params := adopting()
	params["volumeSize"], params["instances"] = "4Gi", "2"
	if res := reconcile(t, p, service(params)); res.Reason != ReasonVolumeShrink {
		t.Fatalf("%+v", res)
	}
	if len(w.patches) != 0 {
		t.Fatalf("wrote: %v", w.patches)
	}
}

func TestReportsANotRunningCluster(t *testing.T) {
	live := liveCluster()
	if err := unstructured.SetNestedField(live.Object, "SyncFailed", "status", "PostgresClusterStatus"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := newProvider(t, live, liveBucket())
	if res := reconcile(t, p, service(adopting())); res.Ready || res.Reason != ReasonNotRunning || !strings.Contains(res.Message, "SyncFailed") {
		t.Fatalf("%+v", res)
	}
}

func TestNameWithoutTeamIsDegraded(t *testing.T) {
	p, _, w := newProvider(t)
	svc := service(map[string]string{"instances": "1", "volumeSize": "1Gi", "create": "true"})
	svc.Name = "shopdb"
	if res := reconcile(t, p, svc); res.Reason != ReasonInvalidName {
		t.Fatalf("%+v", res)
	}
	if len(w.creates) != 0 {
		t.Fatal(w.creates)
	}
}

func TestMissingOperatorIsDegraded(t *testing.T) {
	c := interceptor.NewClient(fake.NewClientBuilder().WithScheme(testScheme(t)).Build(), interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return &meta.NoKindMatchError{GroupKind: clusterGVK.GroupKind()}
		},
	})
	if res := reconcile(t, New(c), service(adopting())); res.Reason != ReasonOperatorMissing {
		t.Fatalf("%+v", res)
	}
}

func TestBindCarriesTheOperatorsCredentials(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "shop.shop-db.credentials.postgresql.acid.zalan.do"},
		Data:       map[string][]byte{"username": []byte("shop"), "password": []byte("s3cret")},
	}
	p, _, _ := newProvider(t, secret)
	b, err := p.Bind(context.Background(), service(adopting()))
	if err != nil {
		t.Fatal(err)
	}
	if string(b.Data["password"]) != "s3cret" || string(b.Data["database"]) != "shop" {
		t.Fatalf("%v", b.Data)
	}
	if strings.Contains(b.Endpoint, "s3cret") || b.Endpoint != "shop-db.shop.svc:5432" {
		t.Fatal(b.Endpoint)
	}
}

func TestBindSaysWhichGrantIsMissing(t *testing.T) {
	c := interceptor.NewClient(fake.NewClientBuilder().WithScheme(testScheme(t)).Build(), interceptor.Funcs{
		Get: func(_ context.Context, _ client.WithWatch, k client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			return apierrors.NewForbidden(corev1.Resource("secrets"), k.Name, errors.New("no"))
		},
	})
	_, err := New(c).Bind(context.Background(), service(adopting()))
	var forbidden *provider.SecretAccessForbidden
	if !errors.As(err, &forbidden) {
		t.Fatalf("%v", err)
	}
}

func toJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func equalJSON(a, b any) bool { return toJSON(a) == toJSON(b) }
