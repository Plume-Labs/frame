/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InferenceModelSpec is what the inference provider needs to size a set of
// weights and start llama.cpp on them. The weights themselves are
// <name>.gguf on the instance's model cache.
type InferenceModelSpec struct {
	// WeightsMiB is what the weights occupy on the GPU, with any CPU offload
	// below already taken off.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16777216
	WeightsMiB int64 `json:"weightsMiB"`

	// Layers, KVHeads and HeadDim give the KV cache per token. Layers counts
	// only those holding a growing cache: a hybrid model's linear-attention
	// layers do not.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	Layers int64 `json:"layers"`
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	KVHeads int64 `json:"kvHeads"`
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4096
	HeadDim int64 `json:"headDim"`

	// HostMemoryMiB is the pod's memory. Absent means twice WeightsMiB, enough
	// to load and map weights that end up on the GPU. A model offloading to
	// the CPU needs it set: the experts live here.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16777216
	HostMemoryMiB int64 `json:"hostMemoryMiB,omitempty"`

	// ContextLength is the context window of an instance that names none.
	// Absent means 4096. Slots share it: see Parallel.
	// +optional
	// +kubebuilder:validation:Minimum=1
	ContextLength int64 `json:"contextLength,omitempty"`

	// Parallel is llama.cpp's --parallel: server slots, which divide the
	// context between them. Absent leaves llama.cpp's own choice.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=64
	Parallel int32 `json:"parallel,omitempty"`

	// KVCacheType quantises the KV cache, K and V alike. Sizing follows it.
	// Absent is f16. A quantised V needs flash attention, which llama.cpp
	// decides by itself: a card that cannot do it fails at startup.
	// +optional
	// +kubebuilder:validation:Enum=f16;q8_0;q4_0
	KVCacheType string `json:"kvCacheType,omitempty"`

	// CPUMoELayers is llama.cpp's --n-cpu-moe: the experts of the first N
	// layers stay in host memory, and every layer is otherwise on the GPU.
	// Absent offloads nothing.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	CPUMoELayers int32 `json:"cpuMoELayers,omitempty"`

	// Threads is llama.cpp's --threads, and the pod's CPU. Absent keeps four
	// CPUs and, without CPU offload, llama.cpp's own choice. A pool sized on the
	// node's cores thrashes against the pod's quota, which is what offload pays.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=256
	Threads int32 `json:"threads,omitempty"`

	// ReasoningBudget is llama.cpp's --reasoning-budget: tokens of thinking,
	// -1 for no limit, 0 for none. Absent leaves llama.cpp's default.
	// +optional
	// +kubebuilder:validation:Minimum=-1
	ReasoningBudget *int32 `json:"reasoningBudget,omitempty"`

	// ReasoningBudgetMessage is injected when the budget runs out.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	ReasoningBudgetMessage string `json:"reasoningBudgetMessage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="!has(self.spec.reasoningBudgetMessage) || has(self.spec.reasoningBudget)",message="reasoningBudgetMessage needs reasoningBudget"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.cpuMoELayers) || has(self.spec.hostMemoryMiB)",message="cpuMoELayers needs hostMemoryMiB: the experts live there"
// +kubebuilder:printcolumn:name="Weights MiB",type=integer,JSONPath=`.spec.weightsMiB`
// +kubebuilder:printcolumn:name="Context",type=integer,JSONPath=`.spec.contextLength`
// +kubebuilder:printcolumn:name="KV cache",type=string,JSONPath=`.spec.kvCacheType`

// InferenceModel is one entry of the catalog FrameServices of type inference
// choose from. It has no status: nothing reconciles it, the provider reads it on
// each pass, so an edit that moves a flag or the pod's resources restarts the
// instances on their next one.
type InferenceModel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec InferenceModelSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// InferenceModelList contains a list of InferenceModel.
type InferenceModelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InferenceModel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InferenceModel{}, &InferenceModelList{})
}
