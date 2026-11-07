package inference

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

func catalogClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	s := runtime.NewScheme()
	if err := servicesv1beta1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func entry(name string, spec servicesv1beta1.InferenceModelSpec) *servicesv1beta1.InferenceModel {
	return &servicesv1beta1.InferenceModel{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
}

func TestSeedCatalogFillsOnlyAnEmptyCatalog(t *testing.T) {
	ctx := context.Background()
	c := catalogClient(t)
	if err := SeedCatalog(ctx, c, c); err != nil {
		t.Fatal(err)
	}
	var got servicesv1beta1.InferenceModelList
	if err := c.List(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != len(catalog) {
		t.Fatalf("seeded %d models, the defaults are %d", len(got.Items), len(catalog))
	}
	// The figures of the compiled catalog this replaces: an instance sized on them
	// must keep its pod template across the upgrade, so these are written out.
	frozen := map[string]servicesv1beta1.InferenceModelSpec{
		"llama-3.1-8b-instruct":  {WeightsMiB: 4696, Layers: 32, KVHeads: 8, HeadDim: 128},
		"llama-3.1-70b-instruct": {WeightsMiB: 40000, Layers: 80, KVHeads: 8, HeadDim: 128},
		"qwen2.5-7b-instruct":    {WeightsMiB: 4400, Layers: 28, KVHeads: 4, HeadDim: 128},
	}
	for _, m := range got.Items {
		if m.Spec != frozen[m.Name] {
			t.Errorf("%s seeded as %+v", m.Name, m.Spec)
		}
	}

	kept := catalogClient(t, entry("mine", servicesv1beta1.InferenceModelSpec{WeightsMiB: 1, Layers: 1, KVHeads: 1, HeadDim: 1}))
	if err := SeedCatalog(ctx, kept, kept); err != nil {
		t.Fatal(err)
	}
	if err := kept.List(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("a catalog with a model in it got %d", len(got.Items))
	}
}

// A seed cut short leaves a catalog that is not empty and not whole.
func TestSeedFinishesForAnInstanceAndLeavesDeletedDefaultsDeleted(t *testing.T) {
	ctx := context.Background()
	serving := &servicesv1beta1.FrameService{
		ObjectMeta: metav1.ObjectMeta{Name: "llm", Namespace: "shop"},
		Spec: servicesv1beta1.FrameServiceSpec{
			Type: "inference", Parameters: map[string]framev1beta1.ParameterValue{"model": "qwen2.5-7b-instruct"},
		},
	}
	spec := catalog["llama-3.1-8b-instruct"]
	c := catalogClient(t, serving, entry("llama-3.1-8b-instruct", servicesv1beta1.InferenceModelSpec(spec)))
	if err := SeedCatalog(ctx, c, c); err != nil {
		t.Fatal(err)
	}
	var got servicesv1beta1.InferenceModelList
	if err := c.List(ctx, &got); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(got.Items))
	for _, m := range got.Items {
		names = append(names, m.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"llama-3.1-8b-instruct", "qwen2.5-7b-instruct"}) {
		t.Fatalf("%v", names)
	}
}

// An empty catalog is seeded before this manager serves the conversion webhook
// that listing FrameServices can need.
func TestAnEmptyCatalogIsSeededWithoutListingFrameServices(t *testing.T) {
	inner := catalogClient(t)
	c := interceptor.NewClient(inner, interceptor.Funcs{
		List: func(ctx context.Context, w client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*servicesv1beta1.FrameServiceList); ok {
				return apierrors.NewServiceUnavailable("conversion webhook: connection refused")
			}
			return w.List(ctx, list, opts...)
		},
	})
	if err := SeedCatalog(context.Background(), c, c); err != nil {
		t.Fatal(err)
	}
}

// Two managers starting together: the slower one finds the object there.
func TestSeedToleratesAnotherManagerGettingThereFirst(t *testing.T) {
	inner := catalogClient(t)
	c := interceptor.NewClient(inner, interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.CreateOption) error {
			return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "inferencemodels"}, o.GetName())
		},
	})
	if err := SeedCatalog(context.Background(), c, c); err != nil {
		t.Fatal(err)
	}
}

// A read that failed is not a refusal: the controller must not say SizeRefused
// on an instance that is serving.
func TestAFailedCatalogReadIsNoRefusal(t *testing.T) {
	inner := catalogClient(t)
	c := interceptor.NewClient(inner, interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return apierrors.NewServiceUnavailable("etcd is away")
		},
	})
	_, err := New(7680, c, c).Size(map[string]string{"model": "m"})
	if !errors.Is(err, provider.ErrSizeUnread) {
		t.Fatalf("got %v", err)
	}
	listless := interceptor.NewClient(inner, interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return apierrors.NewServiceUnavailable("etcd is away")
		},
	})
	if _, err := New(7680, listless, listless).Size(map[string]string{"model": "gone"}); !errors.Is(err, provider.ErrSizeUnread) {
		t.Fatalf("got %v", err)
	}
	_, err = New(7680, inner, inner).Size(map[string]string{"model": "gone"})
	if err == nil || errors.Is(err, provider.ErrSizeUnread) {
		t.Fatalf("an unknown model is a refusal: %v", err)
	}
}

func TestSizeReadsTheCatalog(t *testing.T) {
	const gpuMiB = 7680
	spec := servicesv1beta1.InferenceModelSpec{WeightsMiB: 4000, Layers: 32, KVHeads: 8, HeadDim: 128, ContextLength: 8192}
	c := catalogClient(t, entry("m", spec))
	p := New(gpuMiB, c, c)

	// 128 KiB per token at f16, and the model's own context: 1024Mi.
	got, err := p.Size(map[string]string{"model": "m"})
	if err != nil {
		t.Fatal(err)
	}
	if got.GPUMemory != "5024Mi" || got.Memory != "8000Mi" {
		t.Fatalf("%+v", got)
	}

	_, err = p.Size(map[string]string{"model": "gone"})
	if err == nil || !strings.Contains(err.Error(), "known models are m") {
		t.Fatalf("got %v", err)
	}
}

func TestQuantisedKVCacheIsSizedSmaller(t *testing.T) {
	const gpuMiB = 7680
	base := servicesv1beta1.InferenceModelSpec{WeightsMiB: 4000, Layers: 32, KVHeads: 8, HeadDim: 128, ContextLength: 8192}
	for kv, want := range map[string]string{"": "5024Mi", "f16": "5024Mi", "q8_0": "4544Mi", "q4_0": "4288Mi"} {
		spec := base
		spec.KVCacheType = kv
		c := catalogClient(t, entry("m", spec))
		got, err := New(gpuMiB, c, c).Size(map[string]string{"model": "m"})
		if err != nil {
			t.Fatal(err)
		}
		if got.GPUMemory != want {
			t.Errorf("%q: %s, want %s", kv, got.GPUMemory, want)
		}
	}
}

func TestAnInstanceContextOverridesTheModels(t *testing.T) {
	spec := servicesv1beta1.InferenceModelSpec{WeightsMiB: 4000, Layers: 32, KVHeads: 8, HeadDim: 128, ContextLength: 8192}
	got, err := resolveContextLength(map[string]string{"contextLength": "1024"}, model(spec))
	if err != nil || got != 1024 {
		t.Fatalf("%d, %v", got, err)
	}
}

func TestHostMemoryAndCPUFollowTheModel(t *testing.T) {
	spec := servicesv1beta1.InferenceModelSpec{WeightsMiB: 4000, HostMemoryMiB: 20000, Threads: 6, Layers: 1, KVHeads: 1, HeadDim: 1}
	c := catalogClient(t, entry("m", spec))
	got, err := New(7680, c, c).Size(map[string]string{"model": "m"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Memory != "20000Mi" || got.CPU != "6" {
		t.Fatalf("%+v", got)
	}
}

func TestModelArgs(t *testing.T) {
	if got := modelArgs(catalog["llama-3.1-8b-instruct"]); len(got) != 0 {
		t.Fatalf("a default model must add no flag, or the upgrade restarts it: %v", got)
	}
	budget := int32(1024)
	got := modelArgs(model{
		Parallel: 2, KVCacheType: "q8_0", CPUMoELayers: 36,
		ReasoningBudget: &budget, ReasoningBudgetMessage: "wrap up now",
	})
	want := []string{
		"--parallel", "2", "--cache-type-k", "q8_0", "--cache-type-v", "q8_0",
		"-ngl", "9999", "--n-cpu-moe", "36", "--threads", "4",
		"--reasoning-budget", "1024", "--reasoning-budget-message", "wrap up now",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	if got := modelArgs(model{Threads: 6}); !slices.Equal(got, []string{"--threads", "6"}) {
		t.Fatalf("%q", got)
	}
	// $(LLAMA_API_KEY) would otherwise reach the model's output, expanded by the kubelet.
	got = modelArgs(model{ReasoningBudget: &budget, ReasoningBudgetMessage: "$(LLAMA_API_KEY)"})
	if got[len(got)-1] != "$$(LLAMA_API_KEY)" {
		t.Fatalf("%q", got)
	}
	zero := int32(0)
	if got := modelArgs(model{ReasoningBudget: &zero}); !slices.Equal(got, []string{"--reasoning-budget", "0"}) {
		t.Fatalf("a budget of 0 is a setting, not its absence: %q", got)
	}
}
