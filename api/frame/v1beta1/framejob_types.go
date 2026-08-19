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
//
// pipeline and container are mutually exclusive substrates, enforced below.
// Before this rule, pipeline was the only substrate a FrameJob could name,
// so every existing object satisfies "exactly one of the two is set" simply
// by having pipeline and never having heard of container — the rule widens
// what the API accepts, it narrows nothing a stored object already relies
// on. See docs/superpowers/specs/2026-08-19-frame-typed-job-submission-design.md
// (Neura repo) section 3.2 for why container exists at all: Frame had no
// typed way to accept a container workload, so callers reached past it to
// Volcano and the Kubernetes Job API directly.
// +kubebuilder:validation:XValidation:rule="has(self.pipeline) != has(self.container)",message="exactly one of pipeline or container must be set"
type FrameJobSpec struct {
	// Pipeline names the Argo WorkflowTemplate to run.
	//
	// Deliberately an open string with only form validation (F9). It names an
	// object that lives in the cluster and that Frame does not own; a closed
	// enum would make Frame's API the gatekeeper for a namespace of objects
	// someone else creates. The validating webhook warns — and only warns —
	// when the value is outside the list Frame knows about.
	//
	// Required -> optional (loosened, not tightened, so every stored object
	// stays valid): a FrameJob now names exactly one of pipeline or
	// container, and this is the one Frame has always supported. Argo
	// remains the substrate for every pipeline job regardless of the new
	// `type` field — `type` only steers container jobs, see the note there.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Pipeline string `json:"pipeline,omitempty"`

	// Container runs a plain container workload instead of an Argo pipeline
	// — the other half of the mutual-exclusion rule above. This is new
	// surface, not a relaxation of anything that shipped before the freeze,
	// so it is free to be as strict as it needs to be (F required, bounded
	// command/args/env) without touching the freeze's no-tightening rule,
	// which only binds fields that already existed at v1beta1.
	// +optional
	Container *ContainerSpec `json:"container,omitempty"`

	// Type is the substrate a container job runs on: realtime, batch, or
	// background. See WorkloadType for the default and why. It is ignored
	// for a pipeline job — Argo is that substrate regardless of type. For a
	// container job, the controller dispatches on it (stage 2 of the design
	// in the doc referenced above): realtime and background both produce a
	// batch/v1 Job on the default scheduler, batch produces a Volcano Job.
	// See framejob_container.go's buildContainerObject for the dispatch and
	// its reasoning.
	// +optional
	// +kubebuilder:default=background
	Type WorkloadType `json:"type,omitempty"`

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

// ContainerSpec is the container substrate for a FrameJob, the alternative
// to naming an Argo pipeline. It is deliberately narrow — image, command,
// args, env, resources — rather than an embedded corev1.PodSpec: a FrameJob
// is not a pod template, and the fields it does not expose (volumes,
// affinity, tolerations, service account) are ones stage 2's controller
// decides for every container job alike, not ones a caller should be able to
// override on a case-by-case basis. Widening this later is possible under
// the freeze (new optional fields); narrowing it is not, which is the reason
// to start narrow.
//
// Command, args and env follow the same bounded-envelope shape Parameters
// uses (T3: a ceiling exists to turn a mistake into a validation error, not
// to describe a real workload) — 64 entries is generous for any of the
// three, and MaxLength on each string is the same reasoning ParameterValue
// documents: unconstrained string length inside a bounded list is still an
// unbounded object. Env is a corev1.EnvVar slice rather than a local type
// because Frame does not own the semantics of an environment variable the
// way it owns a parameter map; the ceiling is on the list, not on
// corev1.EnvVar's own fields, which Frame has no marker access to.
type ContainerSpec struct {
	// Image is the container image to run. Required: unlike Pipeline, there
	// is no sensible default for "which image" the way there is for
	// serviceClass or priority.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=512
	Image string `json:"image"`

	// Command overrides the image's entrypoint.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=1024
	Command []string `json:"command,omitempty"`

	// Args overrides the image's default command arguments.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=1024
	Args []string `json:"args,omitempty"`

	// Env sets environment variables in the container.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Resources are the compute resource requirements for the container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
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
	// Failed — contractual across every substrate stage 2 added (clients,
	// including Neura, branch on it), not just the pipeline path it started
	// on. Two more condition types appear here only for a container-substrate
	// job: SchedulingPolicyResolved (batch type only — records whether the
	// frame-<type>-named SchedulingPolicy that supplies the Volcano queue was
	// found, so a fallback to the default queue is visible rather than
	// silent) and SuspendApplied (batch type only — records that Volcano has
	// no live pause primitive once a Job has started, so a suspend request
	// against a running one is visible as unhonored rather than silently
	// dropped). Neither repurposes Ready; both are additive condition types
	// in the same list.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ArgoWorkflowName is the name of the created Argo Workflow. It always
	// lives in the FrameJob's own namespace.
	//
	// Empty for a container-substrate job — see ContainerJobName below. This
	// field is not repurposed to also name a container job's primitive: it
	// has always meant "the name of the created Argo Workflow" specifically,
	// and the freeze forbids giving a stored field a new meaning even to
	// widen what it can name (docs/upgrading.md). A container job gets its
	// own, new, optional field instead.
	// +optional
	ArgoWorkflowName string `json:"argoWorkflowName,omitempty"`

	// ContainerJobName is the name of the batch/v1 Job or Volcano Job
	// (batch.volcano.sh/v1alpha1) a container-substrate FrameJob created —
	// the container-path counterpart to ArgoWorkflowName above. Always equal
	// to the FrameJob's own name and always in its own namespace, mirroring
	// ArgoWorkflowName's own contract. Empty for a pipeline job.
	// +optional
	ContainerJobName string `json:"containerJobName,omitempty"`

	// ContainerJobKind identifies which primitive ContainerJobName names, as
	// "<Kind>.<Group>": "Job.batch" for realtime/background, or
	// "Job.batch.volcano.sh" for batch. Kind alone is ambiguous — Kubernetes'
	// batch/v1 Job and Volcano's batch.volcano.sh/v1alpha1 Job both use the
	// Kind "Job" — so a client that only read Kind could not tell them apart
	// without also inspecting spec.type. Empty for a pipeline job.
	// +optional
	ContainerJobKind string `json:"containerJobKind,omitempty"`

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

// This is the conversion hub and the storage version. The marker arrived here
// in the same change that turned the conversion webhook on; see the note on
// the v1alpha1 FrameJob for why the two could not be separated.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
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
