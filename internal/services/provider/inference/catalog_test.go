package inference

import (
	"context"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	servicesv1beta1 "github.com/Plume-Labs/frame/api/services/v1beta1"
)

func catalogClient(t *testing.T, objs ...client.Object) client.Client {
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
	// The sizing an upgrade relies on: what the code held is what the cluster holds.
	for _, m := range got.Items {
		if model(m.Spec) != catalog[m.Name] {
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

func TestHostMemoryFollowsTheModel(t *testing.T) {
	spec := servicesv1beta1.InferenceModelSpec{WeightsMiB: 4000, HostMemoryMiB: 20000, Layers: 1, KVHeads: 1, HeadDim: 1}
	c := catalogClient(t, entry("m", spec))
	got, err := New(7680, c, c).Size(map[string]string{"model": "m"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Memory != "20000Mi" {
		t.Fatal(got.Memory)
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
		"-ngl", "99", "--n-cpu-moe", "36",
		"--reasoning-budget", "1024", "--reasoning-budget-message", "wrap up now",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	zero := int32(0)
	if got := modelArgs(model{ReasoningBudget: &zero}); !slices.Equal(got, []string{"--reasoning-budget", "0"}) {
		t.Fatalf("a budget of 0 is a setting, not its absence: %q", got)
	}
}
