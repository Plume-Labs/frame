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

// FrameJobSpec defines the desired state of FrameJob.
//
// There is deliberately no rule coupling gpuCount to serviceClass. One was
// once enforced in the validating webhook for three pipeline names and
// nowhere else; it was removed in this freeze (F8) because it tied how much
// hardware a job wants to how preemptible it is, two orthogonal properties.
// Scheduling priority is spec.priority's, projected onto a frame-*
// PriorityClass by the controller.
//
// There is also deliberately no `namespace` field. v1alpha1 had one, and it
// named the namespace the backing ArgoWorkflow was created in — which need
// not have been the FrameJob's own. With the operator holding cluster-wide
// workflows.argoproj.io CRUD, that made a principal who could create a
// FrameJob in one namespace able to make the operator create a Workflow in
// any namespace, referencing any WorkflowTemplate there, executed under that
// namespace's ServiceAccount. A confused deputy (security review I4). The
// Workflow is now created beside its FrameJob. The rule this follows: no
// field in a Frame spec may name a namespace the CR does not live in, except
// through a mechanism that records what it wrote and refuses to touch
// anything it did not create — see internal/services/binding.go, whose
// spec.binding.projectTo does pass that test.
type FrameJobSpec struct {
	// Pipeline names the Argo WorkflowTemplate to run.
	//
	// Deliberately an open string with only form validation (F9). It names an
	// object that lives in the cluster and that Frame does not own; a closed
	// enum would make Frame's API the gatekeeper for a namespace of objects
	// someone else creates. The validating webhook warns — and only warns —
	// when the value is outside the list Frame knows about.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Pipeline string `json:"pipeline"`

	// ServiceClass is the resource tier this job's workloads run at.
	//
	// The default is in the schema, not in the mutating webhook where
	// v1alpha1 kept it. A CRD default applies before CEL and before
	// webhooks; a mutating-webhook default applies after CRD defaults, and
	// having one kind default at each stage is the ordering subtlety that
	// produced the has()-guard bug in the pre-freeze cleanup. It also means
	// kubectl and the UI now agree on what "unspecified" is: they did not
	// before — the webhook filled LOW, the SDK sent MEDIUM.
	// +optional
	// +kubebuilder:default=LOW
	ServiceClass ServiceClass `json:"serviceClass,omitempty"`

	// Priority is the scheduling urgency, separate from the resource tier: a
	// HIGH-tier nightly batch can legitimately be low-priority. It maps onto
	// a frame-* PriorityClass through internal/scheduling.
	// +optional
	// +kubebuilder:validation:Enum=critical;high;medium;low
	// +kubebuilder:default=medium
	Priority string `json:"priority,omitempty"`

	// GPUCount is how many GPUs the job asks for.
	//
	// The ceiling is three orders of magnitude above the one physical GPU
	// this cluster has, so it constrains nothing real — it turns an
	// accidental gpuCount: 100000 into a validation error rather than an
	// unschedulable pod (T5). A ceiling can only ever be raised after the
	// freeze, never introduced.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1024
	// +kubebuilder:default=0
	GPUCount int32 `json:"gpuCount,omitempty"`

	// Parameters are passed straight into the Argo Workflow's arguments.
	//
	// Bounded as an envelope (T3): 64 entries, 1024 characters per value. Key
	// form is not constrained — see the note on ParameterValue and the plan's
	// "Open disagreements" for why a key pattern is not expressible here
	// without an unbounded-cost CEL rule.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	Parameters map[string]ParameterValue `json:"parameters,omitempty"`

	// Suspended pauses the underlying Argo Workflow when true. Set to false
	// to resume.
	// +optional
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
}

// FrameJobStatus defines the observed state of FrameJob.
//
// There is no status.phase (F2). Conditions are the whole story: Ready
// carries the phase as its reason and is True only on Completed. A single
// enum forces the API to pick one dimension of health out of several and
// cannot express "provisioned but degraded", which is why SIG-Architecture
// has been steering away from phase since 2019. v1alpha1 still serves a
// phase field; it is computed out of these conditions on the way down and is
// never stored.
type FrameJobStatus struct {
	// ObservedGeneration is the metadata.generation this status was computed
	// from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the current state of the FrameJob resource.
	// Ready's reason is one of Submitted, Running, Suspended, Completed,
	// Failed.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ArgoWorkflowName is the name of the created Argo Workflow. It always
	// lives in the FrameJob's own namespace.
	// +optional
	ArgoWorkflowName string `json:"argoWorkflowName,omitempty"`

	// StartTime is when the job started running.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the job completed.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message provides additional information about the current status.
	// +optional
	Message string `json:"message,omitempty"`
}

// This is the conversion hub, but it is deliberately *not* the storage
// version yet — v1alpha1 still carries +kubebuilder:storageversion, and
// Task 19 moves the marker here when the conversion webhook starts serving.
// See the note on the v1alpha1 FrameJob for why.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fj
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
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
