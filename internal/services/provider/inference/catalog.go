package inference

import "sort"

// model is what sizing needs to know about a set of weights. The provider has
// to know the models it serves in order to size them; there is no way to derive
// a KV cache footprint from a name.
type model struct {
	// WeightsMiB is the on-GPU size of the quantised weights this provider
	// serves. Q4_K_M throughout: it is what fits on Pascal-class hardware.
	WeightsMiB int64
	// Layers, KVHeads and HeadDim give the KV cache per token:
	//   2 (K and V) x Layers x KVHeads x HeadDim x 2 bytes (f16)
	Layers  int64
	KVHeads int64
	HeadDim int64
}

// kvBytesPerToken is the cache one token occupies, in bytes.
func (m model) kvBytesPerToken() int64 {
	const kvAndV = 2
	const bytesPerElementF16 = 2
	return kvAndV * m.Layers * m.KVHeads * m.HeadDim * bytesPerElementF16
}

// catalog is the set of models this provider can serve. Adding one is a code
// change on purpose: the numbers below decide whether an instance is admitted,
// and a wrong one turns into a crash loop on a shared GPU.
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

// knownModels lists the catalog, sorted, for error messages.
func knownModels() []string {
	out := make([]string, 0, len(catalog))
	for name := range catalog {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
