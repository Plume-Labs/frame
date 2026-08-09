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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// FrameJobSpec defines the desired state of FrameJob.
//
// There is deliberately no rule coupling gpuCount to serviceClass. One was
// once enforced in the validating webhook for three pipeline names and
// nowhere else; it was removed in the v1beta1 freeze (F8) because it tied
// how much hardware a job wants to how preemptible it is, two orthogonal
// properties. Scheduling priority is spec.priority's, projected onto a
// frame-* PriorityClass by the controller.
type FrameJobSpec struct {
	// Pipeline template to use (training, inference, batch)
	// +kubebuilder:validation:Required
	Pipeline string `json:"pipeline"`

	// Service class for resource allocation
	// +optional
	// +kubebuilder:validation:Enum=HIGH;MEDIUM;LOW
	ServiceClass string `json:"serviceClass,omitempty"`

	// Priority of the job
	// +optional
	// +kubebuilder:validation:Enum=critical;high;medium;low
	Priority string `json:"priority,omitempty"`

	// Kubernetes namespace for the job. Not required to match this FrameJob's
	// own namespace: the controller creates the backing ArgoWorkflow here
	// with cluster-wide RBAC, so a FrameJob in one namespace can direct
	// workflow creation into another. That cross-namespace reach is
	// deliberate-for-now; constraining it is Phase B's RBAC-tier
	// lock-down to settle, not this validation. This only rejects a
	// malformed value, not a cross-namespace one.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	// +kubebuilder:default="default"
	Namespace string `json:"namespace,omitempty"`

	// Number of GPUs required
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	GPUCount int32 `json:"gpuCount,omitempty"`

	// Template parameters for the pipeline
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`

	// Suspended pauses the underlying Argo Workflow when true. Set to false to resume.
	// +optional
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
}

// FrameJobStatus defines the observed state of FrameJob.
type FrameJobStatus struct {
	// ObservedGeneration is the metadata.generation this status was computed
	// from. A client can compare it to metadata.generation to tell whether
	// the controller has seen the current spec yet, without knowing anything
	// about this kind's condition vocabulary.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Current phase of the job
	// +kubebuilder:validation:Enum=Pending;Submitted;Running;Suspended;Completed;Failed
	Phase string `json:"phase,omitempty"`

	// Conditions represent the current state of the FrameJob resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ArgoWorkflowName is the name of the created Argo Workflow
	// +optional
	ArgoWorkflowName string `json:"argoWorkflowName,omitempty"`

	// StartTime is when the job started running
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the job completed
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message provides additional information about the current status
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fj
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Pipeline",type=string,JSONPath=".spec.pipeline"
// +kubebuilder:printcolumn:name="ServiceClass",type=string,JSONPath=".spec.serviceClass"
// +kubebuilder:printcolumn:name="GPUs",type=integer,JSONPath=".spec.gpuCount"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameJob is the Schema for the framejobs API
type FrameJob struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of FrameJob
	// +required
	Spec FrameJobSpec `json:"spec"`

	// status defines the observed state of FrameJob
	// +optional
	Status FrameJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameJobList contains a list of FrameJob
type FrameJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameJob{}, &FrameJobList{})
}
