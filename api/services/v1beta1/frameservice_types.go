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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// FrameServiceSpec is the envelope. Everything type-specific lives in
// Parameters, which the provider owns and the webhook validates.
type FrameServiceSpec struct {
	// Type selects the provider. The valid set is closed and enforced by the
	// webhook against the provider registry, so a typo is refused at
	// admission rather than leaving an instance Pending forever.
	//
	// The schema bounds only the *form* (T8). The closed set genuinely is
	// compiled-in here, unlike FrameJob.pipeline, so the registry is the
	// right authority — but a malformed value should fail on shape before it
	// reaches a registry lookup at all.
	//
	// MinLength=1 is carried over from v1alpha1 and is now redundant: the
	// pattern below already rejects the empty string, and stripping MinLength
	// alone fails no spec. It stays because dropping it would make the two
	// versions differ for no reason, and a bound can only be relaxed after
	// the freeze, never re-added.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Type string `json:"type"`

	// Parameters are provider-owned and validated at admission against the
	// JSON Schema that provider registers — not by this CRD's own OpenAPI.
	// They are deliberately outside the API compatibility guarantee: a
	// breaking parameter change ships as a new Type value rather than
	// redefining this one.
	//
	// The envelope is bounded anyway (T4): 64 entries, 1024 characters per
	// value. That carve-out is about parameter *meaning*; an unbounded map is
	// a resource concern regardless of who owns the keys.
	//
	// Key form is deliberately not constrained here. controller-gen emits a
	// bound only from the map's value type — see the note on
	// framev1beta1.ParameterValue — and the CEL alternative has no key
	// maxLength for the cost estimator to bound against, so it ships a CRD
	// the apiserver refuses to install. Each provider's ParameterSchema
	// already validates keys at admission, which is the only place they mean
	// anything.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	Parameters map[string]framev1beta1.ParameterValue `json:"parameters,omitempty"`

	// ServiceClass is the tier the instance's workloads run at, so the
	// existing FrameResourceQuota and SchedulingPolicy apply to it like any
	// other workload. It never names a node: Frame decides placement.
	//
	// It carries a second meaning, deliberately (F10): it is also the
	// instance's scheduling priority, mapped onto a frame-* PriorityClass by
	// internal/scheduling.PriorityClassForServiceClass. There is no
	// spec.priority and no spec.priorityClassName. A long-lived instance's
	// tier is its urgency — unlike a job's, where a HIGH-tier nightly batch
	// can legitimately be low-priority — so a second field would duplicate
	// this one with no case where they differ, and then they could disagree.
	// Naming a PriorityClass directly would break the invariant above by
	// letting a user reach a class Frame did not create, including a system
	// one. If a HIGH-tier instance ever needs to be evicted before a MEDIUM
	// one, that is a v1beta2 problem.
	//
	// MEDIUM, not FrameJob's LOW. The type is shared; the default is not.
	// An unspecified batch job should be preemptible; an unspecified
	// long-lived service instance should not be the first thing evicted.
	// +optional
	// +kubebuilder:default=MEDIUM
	ServiceClass framev1beta1.ServiceClass `json:"serviceClass,omitempty"`

	// +optional
	Binding BindingSpec `json:"binding,omitempty"`

	// DeletionPolicy decides what happens to the instance's data when this
	// object is deleted. Retain is the default because the failure modes are
	// not symmetric: a retained volume costs disk and is visible, a deleted
	// one costs the data at the moment someone meant to redeploy.
	// +optional
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
}

// BindingSpec is how the credentials Secret is named and where it is copied.
type BindingSpec struct {
	// SecretName defaults to the FrameService's own name.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	SecretName string `json:"secretName,omitempty"`

	// ProjectTo copies the credentials Secret into these namespaces. Opt-in
	// and explicit: a catalog that writes Secrets into namespaces nobody
	// listed is a cross-tenant leak dressed as convenience.
	//
	// This is the one place a Frame spec may name a namespace the CR does not
	// live in, and it earns it: the controller only ever writes at a
	// coordinate it has itself recorded in status.binding.projected, and
	// refuses to claim a coordinate where an object already exists. Authority
	// comes from a record only the controller can write, never from data the
	// requester supplied. See internal/controller/services/binding.go.
	//
	// It is opt-in by the *producer* and never by the receiving namespace.
	// That is safe while Frame is single-tenant and is recorded rather than
	// fixed here; widening it is not a schema change.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	ProjectTo []string `json:"projectTo,omitempty"`
}

// Sizing is what the provider derived from the parameters. It is reported
// rather than requested — nothing in the spec states it, and an operator has
// to be able to see what an instance costs.
type Sizing struct {
	// +optional
	GPU string `json:"gpu,omitempty"`
	// +optional
	GPUMemory string `json:"gpuMemory,omitempty"`
	// +optional
	CPU string `json:"cpu,omitempty"`
	// +optional
	Memory string `json:"memory,omitempty"`
}

// BindingStatus is the credential-free half of the binding: what a consumer
// needs to find the Secret, plus the record of every copy that exists.
type BindingStatus struct {
	// SecretRef names the credentials Secret in this FrameService's own
	// namespace. A LocalObjectReference, not an ObjectReference: a consumer
	// that could be pointed at an arbitrary namespace is a confused deputy.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// Endpoint is what a consumer connects to. Never contains credentials.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Projected records every Secret coordinate this controller has actually
	// written: the primary Secret beside the FrameService and every copy
	// spec.binding.projectTo asked for. It is the sole record the controller
	// consults to decide what it may write to and what it must delete when a
	// namespace leaves projectTo or secretName changes — never a label on the
	// Secret itself, which is data anyone with patch rights on Secrets can
	// set. See the comment on reconcileBinding in
	// internal/controller/services/binding.go for why that distinction is
	// load-bearing.
	// +optional
	Projected []ProjectedSecretRef `json:"projected,omitempty"`
}

// ProjectedSecretRef names one Secret coordinate this controller has written.
type ProjectedSecretRef struct {
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// ProvisionedRef names one object the provider created, so kubectl describe
// explains an instance without anyone knowing the provider's internals.
type ProvisionedRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// FrameServiceStatus defines the observed state of FrameService.
//
// There is no status.phase (F2). Ready carries it: True when the instance is
// serving, False otherwise with the reason naming why — UnknownType,
// NotProvisionable, SizeRefused, a provider degrade such as
// ModelCacheMissing, or a binding degrade. Those reasons are diagnostic and
// are deliberately *not* collapsed into a five-valued enum; v1alpha1's phase
// is projected back out of Ready.Status and the deletion timestamp, not out
// of the reason.
type FrameServiceStatus struct {
	// ObservedGeneration is the metadata.generation this status was computed
	// from, so a client can tell whether the controller has seen the current
	// spec without knowing this kind's condition vocabulary.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	Binding BindingStatus `json:"binding,omitempty"`
	// +optional
	Sizing Sizing `json:"sizing,omitempty"`
	// +optional
	Provisioned []ProvisionedRef `json:"provisioned,omitempty"`
}

// This is the conversion hub and the storage version. The marker arrived here
// in the same change that turned the conversion webhook on; see the note on
// the v1alpha1 FrameJob for why the two could not be separated.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.binding.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FrameService is the Schema for the frameservices API
type FrameService struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of FrameService
	// +required
	Spec FrameServiceSpec `json:"spec"`

	// status defines the observed state of FrameService
	// +optional
	Status FrameServiceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameServiceList contains a list of FrameService
type FrameServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameService{}, &FrameServiceList{})
}
