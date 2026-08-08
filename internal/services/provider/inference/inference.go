// Package inference provisions llama.cpp model servers.
//
// llama.cpp is the only backend this package supports, and that is a hardware
// fact rather than a preference: the cluster's Tesla P4 is Pascal, compute
// capability 6.1, and vLLM and KubeAI both need sm_7.0 or newer. The choice is
// internal to this provider, so a newer card means a new implementation behind
// the same spec.type, not an API change.
package inference

import (
	"fmt"
	"strconv"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/rmocq/frame/internal/services/provider"
)

const (
	// defaultContextLength is what llama.cpp itself defaults to.
	defaultContextLength = 4096
	// bytesPerMiB converts the KV cache arithmetic into the unit the catalog
	// and the resource requests both speak.
	bytesPerMiB = 1024 * 1024
)

// Provider serves models with llama.cpp.
type Provider struct {
	// gpuMemoryMiB is the memory of the card an instance will land on. Injected
	// rather than probed so the arithmetic is testable and so a heterogeneous
	// cluster can size against the smallest card rather than the largest.
	gpuMemoryMiB int64
}

// New builds the provider for a card of the given size.
func New(gpuMemoryMiB int64) *Provider {
	return &Provider{gpuMemoryMiB: gpuMemoryMiB}
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

	ctx := int64(defaultContextLength)
	if raw, set := params["contextLength"]; set && raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			return provider.Sizing{}, fmt.Errorf(
				"parameters.contextLength %q is not a positive whole number of tokens", raw)
		}
		ctx = parsed
	}

	// The weights are the floor: no context window makes an oversized model fit,
	// so say that rather than blaming the context the operator can change.
	if m.WeightsMiB >= p.gpuMemoryMiB {
		return provider.Sizing{}, fmt.Errorf(
			"model %q needs %dMi for weights alone, and the GPU has %dMi",
			name, m.WeightsMiB, p.gpuMemoryMiB)
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
