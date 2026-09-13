package v1

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func validatorWith(entries ...*framev1beta1.FrameStorage) *PVCCustomValidator {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = framev1beta1.AddToScheme(s)
	b := fake.NewClientBuilder().WithScheme(s)
	for _, e := range entries {
		b = b.WithObjects(e)
	}
	return &PVCCustomValidator{Client: b.Build()}
}

func storageEntry(name, class string, content ...string) *framev1beta1.FrameStorage {
	return &framev1beta1.FrameStorage{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: framev1beta1.FrameStorageSpec{
			Type: "ceph-rbd", Content: content, StorageClassName: class,
		},
	}
}

func pvc(class string, labels map[string]string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "v", Namespace: "default", Labels: labels},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: &class},
	}
}

func TestRefusesAContentTypeTheEntryDoesNotDeclare(t *testing.T) {
	v := validatorWith(storageEntry("models", "ceph-rbd", "model", "artifact"))
	_, err := v.ValidateCreate(context.Background(), pvc("ceph-rbd", map[string]string{framev1beta1.UsageLabel: "backup"}))
	if err == nil {
		t.Fatal("want a refusal: the entry does not declare backup")
	}
	// The message must name the entry and the allowed list, or the person
	// reading it has to go find both by hand.
	if !strings.Contains(err.Error(), "models") {
		t.Errorf("refusal does not name the entry: %v", err)
	}
	if !strings.Contains(err.Error(), "model") || !strings.Contains(err.Error(), "artifact") {
		t.Errorf("refusal does not list what is allowed: %v", err)
	}
}

func TestAcceptsADeclaredContentType(t *testing.T) {
	v := validatorWith(storageEntry("models", "ceph-rbd", "model", "artifact"))
	if _, err := v.ValidateCreate(context.Background(), pvc("ceph-rbd", map[string]string{framev1beta1.UsageLabel: "model"})); err != nil {
		t.Fatalf("want acceptance: %v", err)
	}
}

// TestAcceptsAnUnlabelledClaim is the migration property: the nineteen PVCs
// already in the cluster carry no usage label. The objectSelector means
// they never reach this code at all, but the code must agree — a webhook
// that would refuse them if it ever saw them is one selector edit away
// from a cluster that cannot create volumes.
func TestAcceptsAnUnlabelledClaim(t *testing.T) {
	v := validatorWith(storageEntry("models", "ceph-rbd", "model"))
	if _, err := v.ValidateCreate(context.Background(), pvc("ceph-rbd", nil)); err != nil {
		t.Fatalf("an unlabelled PVC must pass: %v", err)
	}
}

// TestAcceptsAClassNoEntryDescribes: a class Frame knows nothing about is
// not Frame's to police. local-path is the cluster's default class and
// three PVCs use it today.
func TestAcceptsAClassNoEntryDescribes(t *testing.T) {
	v := validatorWith(storageEntry("models", "ceph-rbd", "model"))
	if _, err := v.ValidateCreate(context.Background(), pvc("local-path", map[string]string{framev1beta1.UsageLabel: "scratch"})); err != nil {
		t.Fatalf("a class with no FrameStorage entry must pass: %v", err)
	}
}

func TestAcceptsAClaimWithNoStorageClass(t *testing.T) {
	v := validatorWith(storageEntry("models", "ceph-rbd", "model"))
	p := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "v", Namespace: "default",
			Labels: map[string]string{framev1beta1.UsageLabel: "model"}},
	}
	if _, err := v.ValidateCreate(context.Background(), p); err != nil {
		t.Fatalf("a PVC with no class named yet must pass: %v", err)
	}
}

// TestAcceptsWhenTheEntryListCannotBeRead is the final review's item 10.
// failurePolicy: Ignore covers a webhook the API server cannot REACH; it
// does not cover a webhook that is reached and answers "deny". A manager
// that is up but cannot list FrameStorage entries — RBAC withdrawn,
// informer not synced, apiserver refusing the read — used to return that
// error, which the API server renders as a refusal and which would stop
// volume creation cluster-wide with every failure-tolerance setting still
// reading as correct. The design's rule is that a policy which cannot be
// evaluated must not stop the cluster.
func TestAcceptsWhenTheEntryListCannotBeRead(t *testing.T) {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = framev1beta1.AddToScheme(s)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(storageEntry("models", "ceph-rbd", "model")).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				return errors.New("the cache has not synced")
			},
		}).Build()
	v := &PVCCustomValidator{Client: c}

	// Deliberately a claim this webhook WOULD refuse if it could read the
	// entries: "backup" is not among models' content types. That is what
	// makes this discriminating — a version that still denied on a list
	// error, and a version that denied on the content type, are told apart
	// only because the answer here must be "accept" for a claim that is
	// otherwise refusable.
	if _, err := v.ValidateCreate(context.Background(),
		pvc("ceph-rbd", map[string]string{framev1beta1.UsageLabel: "backup"})); err != nil {
		t.Fatalf("a policy that cannot be evaluated must not stop the cluster: %v", err)
	}
}
