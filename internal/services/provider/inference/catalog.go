package inference

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	servicesv1beta1 "github.com/Plume-Labs/frame/api/services/v1beta1"
)

// The catalog is read uncached: Size runs at admission, which can come before
// the manager's caches have started, and a named Get costs no informer.
//
// +kubebuilder:rbac:groups=services.plume-labs.io,resources=inferencemodels,verbs=get;list;create

// lookupTimeout bounds a catalog read: Size has no context of its own.
const lookupTimeout = 10 * time.Second

// model is what sizing and llama.cpp need to know about a set of weights. The
// provider has to know the models it serves in order to size them; there is no
// way to derive a KV cache footprint from a name.
type model servicesv1beta1.InferenceModelSpec

// kvBytesPer32 is what 32 elements of the KV cache take: a quantised block of 32
// values carries a scale, so q8_0 is 34 bytes and not 32.
var kvBytesPer32 = map[string]int64{"": 64, "f16": 64, "q8_0": 34, "q4_0": 18}

// kvBytesPerToken is the cache one token occupies, in bytes:
//
//	2 (K and V) x Layers x KVHeads x HeadDim x bytes per element
func (m model) kvBytesPerToken() int64 {
	const kvAndV = 2
	return (kvAndV*m.Layers*m.KVHeads*m.HeadDim*kvBytesPer32[m.KVCacheType] + 31) / 32
}

// catalog is what a new cluster starts with, and what a caller without a reader
// sizes against. Q4_K_M throughout: it is what fits on Pascal-class hardware.
var catalog = map[string]model{
	// Llama 3.1 8B Instruct, Q4_K_M. 128Ki of KV cache per token.
	"llama-3.1-8b-instruct": {WeightsMiB: 4696, Layers: 32, KVHeads: 8, HeadDim: 128},
	// Llama 3.1 70B Instruct, Q4_K_M. Listed so the refusal names a real model
	// rather than an unknown one — it cannot fit a Pascal card, and saying so
	// is more useful than pretending it does not exist.
	"llama-3.1-70b-instruct": {WeightsMiB: 40000, Layers: 80, KVHeads: 8, HeadDim: 128},
	// Qwen2.5 7B Instruct, Q4_K_M.
	"qwen2.5-7b-instruct": {WeightsMiB: 4400, Layers: 28, KVHeads: 4, HeadDim: 128},
}

// SeedCatalog writes the defaults into a cluster with no InferenceModel at all,
// which is what an upgrade from the compiled catalog finds. A catalog with
// anything in it is the operator's: a default they deleted stays deleted.
func SeedCatalog(ctx context.Context, c client.Client, r client.Reader) error {
	var models servicesv1beta1.InferenceModelList
	if err := r.List(ctx, &models); err != nil {
		return fmt.Errorf("listing the inference models: %w", err)
	}
	if len(models.Items) > 0 {
		return nil
	}
	for name, m := range catalog {
		err := c.Create(ctx, &servicesv1beta1.InferenceModel{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       servicesv1beta1.InferenceModelSpec(m),
		})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating the inference model %s: %w", name, err)
		}
	}
	return nil
}

// lookup finds a model in the catalog. A provider built without a reader only
// validates, in a test: the defaults answer.
func (p *Provider) lookup(name string) (model, error) {
	if p.apiReader == nil {
		if m, ok := catalog[name]; ok {
			return m, nil
		}
		names := make([]string, 0, len(catalog))
		for n := range catalog {
			names = append(names, n)
		}
		return model{}, unknownModel(name, names)
	}

	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	var found servicesv1beta1.InferenceModel
	err := p.apiReader.Get(ctx, client.ObjectKey{Name: name}, &found)
	if err == nil {
		return model(found.Spec), nil
	}
	if !apierrors.IsNotFound(err) {
		return model{}, fmt.Errorf("reading the inference model %q: %w", name, err)
	}
	var all servicesv1beta1.InferenceModelList
	if err := p.apiReader.List(ctx, &all); err != nil {
		return model{}, fmt.Errorf("listing the inference models: %w", err)
	}
	names := make([]string, 0, len(all.Items))
	for i := range all.Items {
		names = append(names, all.Items[i].Name)
	}
	return model{}, unknownModel(name, names)
}

func unknownModel(name string, known []string) error {
	slices.Sort(known)
	return fmt.Errorf("parameters.model %q is not a model this provider serves: known models are %s",
		name, strings.Join(known, ", "))
}
