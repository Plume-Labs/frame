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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FrameServiceSpec is the envelope. Everything type-specific lives in
// Parameters, which the provider owns and the webhook validates.
type FrameServiceSpec struct {
	// Type selects the provider. The valid set is closed and enforced by the
	// webhook against the provider registry, so a typo is refused at admission
	// rather than leaving an instance Pending forever.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`

	// Parameters are provider-owned and validated at admission against the
	// JSON Schema that provider registers — not by this CRD's own OpenAPI.
	// They are deliberately outside the API compatibility guarantee: a breaking
	// parameter change ships as a new Type value rather than redefining this one.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`

	// ServiceClass is the tier the instance's workloads run at, so the existing
	// FrameResourceQuota and SchedulingPolicy apply to it like any other
	// workload. It never names a node: Frame decides placement.
	// +optional
	// +kubebuilder:validation:Enum=HIGH;MEDIUM;LOW
	// +kubebuilder:default=MEDIUM
	ServiceClass string `json:"serviceClass,omitempty"`

	// +optional
	Binding BindingSpec `json:"binding,omitempty"`

	// DeletionPolicy decides what happens to the instance's data when this
	// object is deleted. Retain is the default because the failure modes are
	// not symmetric: a retained volume costs disk and is visible, a deleted one
	// costs the data at the moment someone meant to redeploy.
	// +optional
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
}

type BindingSpec struct {
	// SecretName defaults to the FrameService's own name.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	SecretName string `json:"secretName,omitempty"`

	// ProjectTo copies the credentials Secret into these namespaces. Opt-in and
	// explicit: a catalog that writes Secrets into namespaces nobody listed is a
	// cross-tenant leak dressed as convenience.
	// +optional
	ProjectTo []string `json:"projectTo,omitempty"`
}

// Sizing is what the provider derived from the parameters. It is reported
// rather than requested — nothing in the spec states it, and an operator has to
// be able to see what an instance costs.
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

type BindingStatus struct {
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

type FrameServiceStatus struct {
	// +optional
	// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Degraded;Deleting
	Phase string `json:"phase,omitempty"`

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
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// v1alpha1 keeps +kubebuilder:storageversion until the conversion webhook
// serves; Task 19 moves the marker to v1beta1 atomically with it. See the
// note on the v1alpha1 FrameJob for the full reasoning.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:deprecatedversion:warning="services.plume-labs.io/v1alpha1 FrameService is deprecated; use services.plume-labs.io/v1beta1. status.phase is computed from status.conditions and is not stored."
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
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
