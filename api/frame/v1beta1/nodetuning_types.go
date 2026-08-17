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

// NodeTuningPhase is the reconciliation phase for one node under a
// NodeTuning. It lives per-node in status.nodes, not as a single
// object-level phase (F2, followed here as in every other Frame type): a
// fleet applying a change to twenty nodes is not one phase, it is twenty.
type NodeTuningPhase string

const (
	PhaseInSync        NodeTuningPhase = "InSync"
	PhaseDrifted       NodeTuningPhase = "Drifted"
	PhaseRebootPending NodeTuningPhase = "RebootPending"
	PhaseApplying      NodeTuningPhase = "Applying"
	PhaseFailed        NodeTuningPhase = "Failed"
)

// Realization is how far a setting has actually taken effect on a node,
// separate from Phase: a node can be InSync while KSM is only Effective and
// not yet FullyRealized, because most of KSM's benefit comes from merging
// pages in containers that were already running when it was turned on.
type Realization string

const (
	// RealizationEffective means the setting is live in the running unit, so
	// containers created from now on get it.
	RealizationEffective Realization = "Effective"
	// RealizationFullyRealized additionally means every container on the node
	// was created after the change. For KSM this is where most of the benefit
	// actually is.
	RealizationFullyRealized Realization = "FullyRealized"
	RealizationPending       Realization = "Pending"
)

// ApprovalAnnotation carries the generation a human approved. A generation
// rather than a boolean, so approving one change never silently authorizes the
// next one.
const ApprovalAnnotation = "frame.plume-labs.io/tuning-approved"

// The annotations below are the whole controller/agent restart protocol, and
// they live on the corev1.Node rather than in NodeTuning's status because
// they are per-node facts that must survive the controller restarting and
// must be readable and clearable by a human with kubectl on the node alone.
//
// The controller writes TuningRolloutStarted/Baseline/RestartRequested; the
// node agent writes TuningUnit/TuningUnitActiveEnter. Neither trusts the
// other's values blindly: the controller passes TuningUnitAnnotation through
// the agent package's compile-time allowlist before acting on it, and the
// agent detects its own unit rather than restarting whatever it is told to.
const (
	// TuningRolloutStartedAnnotation is when the controller cordoned this
	// node for a tuning restart (RFC3339Nano). Its presence is the
	// cluster-wide "a rollout is in flight or unfinished here" lock: one node
	// at a time, and a node whose restart failed keeps it, which is what
	// halts the campaign until a human clears it.
	TuningRolloutStartedAnnotation = "frame.plume-labs.io/tuning-rollout-started"

	// TuningRestartBaselineAnnotation is the unit's ActiveEnterTimestamp as
	// it was before the restart was asked for (RFC3339Nano). A restart is
	// verified by this value moving, never by the node flapping NotReady.
	TuningRestartBaselineAnnotation = "frame.plume-labs.io/tuning-restart-baseline"

	// TuningRestartRequestedAnnotation is when the controller asked the agent
	// to schedule a detached restart (RFC3339Nano), which is also the anchor
	// the wait times out from. The agent acts on a value it has not acted on
	// before; the controller removes it once the restart is verified.
	TuningRestartRequestedAnnotation = "frame.plume-labs.io/tuning-restart-requested"

	// TuningUnitAnnotation is the systemd unit (base name, no ".service")
	// that owns containerd on this node, as detected by the agent. The
	// controller refuses any value the agent package's allowlist rejects.
	TuningUnitAnnotation = "frame.plume-labs.io/tuning-unit"

	// TuningUnitActiveEnterAnnotation is that unit's ActiveEnterTimestamp as
	// systemd last reported it (RFC3339Nano), republished by the agent every
	// tick. This is the single piece of evidence a restart actually happened.
	TuningUnitActiveEnterAnnotation = "frame.plume-labs.io/tuning-unit-active-enter"
)

// KSMSpec configures kernel same-page merging. Enabled defaults to false:
// KSM's cross-tenant page merging is a side channel (a process on one node
// can infer another's memory contents from merge timing), so turning it on
// is opt-in, never inherited from an unset field.
type KSMSpec struct {
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`
	// +kubebuilder:validation:Minimum=1
	PagesToScan *int32 `json:"pagesToScan,omitempty"`
	// +kubebuilder:validation:Minimum=1
	SleepMillisecs   *int32 `json:"sleepMillisecs,omitempty"`
	MergeAcrossNodes *bool  `json:"mergeAcrossNodes,omitempty"`
}

// NodeTuningSpec defines the desired state of NodeTuning.
type NodeTuningSpec struct {
	// NodeSelector picks which nodes this NodeTuning applies to. A nil
	// selector matches no nodes rather than every node, so a NodeTuning
	// created without one is inert instead of fleet-wide by accident.
	// +optional
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// TunedProfile owns sysctls, kernel modules, governor and scheduler. Frame
	// declares which profile runs and never restates its contents: tuned
	// re-applies at boot and on every change, so a second writer has no owner.
	// +optional
	TunedProfile string `json:"tunedProfile,omitempty"`

	// KSM configures kernel same-page merging. Nil means untouched, not off:
	// see KSMSpec for why Enabled itself still defaults to false.
	// +optional
	KSM *KSMSpec `json:"ksm,omitempty"`

	// CPUManagerPolicy sets the kubelet CPU manager policy.
	// +optional
	// +kubebuilder:validation:Enum=none;static
	CPUManagerPolicy string `json:"cpuManagerPolicy,omitempty"`

	// MIGProfile names the NVIDIA MIG profile to apply.
	// +optional
	MIGProfile string `json:"migProfile,omitempty"`
}

// ObservedKSM is what the node agent last read back from sysfs about KSM,
// as opposed to KSMSpec, which is what was asked for.
type ObservedKSM struct {
	MemoryKSM     bool  `json:"memoryKSM"`
	GeneralProfit int64 `json:"generalProfit"`
	PagesSharing  int64 `json:"pagesSharing"`
}

// ObservedTuning is what the node agent last read back from the node, used
// to detect drift against NodeTuningSpec.
type ObservedTuning struct {
	KSM              *ObservedKSM `json:"ksm,omitempty"`
	TunedProfile     string       `json:"tunedProfile,omitempty"`
	CPUManagerPolicy string       `json:"cpuManagerPolicy,omitempty"`
}

// NodeTuningNodeStatus is one node's reconciliation state under a
// NodeTuning.
type NodeTuningNodeStatus struct {
	Name string `json:"name"`
	// +optional
	Phase NodeTuningPhase `json:"phase,omitempty"`
	// +optional
	AppliedGeneration int64 `json:"appliedGeneration,omitempty"`
	// +optional
	Realization Realization `json:"realization,omitempty"`
	// +optional
	Observed ObservedTuning `json:"observed,omitempty"`

	// RestartedAt is when Task 6 verified the unit came back. Task 7 compares
	// pod start times against it to promote Effective to FullyRealized. It
	// lives in the API rather than being re-derived from the node's
	// ActiveEnterTimestamp, which would cost a second agent round-trip.
	// +optional
	RestartedAt *metav1.Time `json:"restartedAt,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// NodeTuningStatus defines the observed state of NodeTuning.
//
// No status.phase at the object level (F2, followed here as in every other
// Frame type): the per-node phase lives in status.nodes[].phase, since one
// NodeTuning can have some nodes InSync and others Drifted at once.
type NodeTuningStatus struct {
	// ObservedGeneration is the metadata.generation this status was computed
	// from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	Nodes []NodeTuningNodeStatus `json:"nodes,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="TunedProfile",type=string,JSONPath=".spec.tunedProfile"
// +kubebuilder:printcolumn:name="KSM",type=boolean,JSONPath=".spec.ksm.enabled"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// NodeTuning is the Schema for the nodetunings API. It is cluster-scoped:
// the node-level settings it manages (sysctls, KSM, CPU manager policy) have
// no namespace of their own.
type NodeTuning struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of NodeTuning
	// +optional
	Spec NodeTuningSpec `json:"spec,omitempty"`

	// status defines the observed state of NodeTuning
	// +optional
	Status NodeTuningStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeTuningList contains a list of NodeTuning
type NodeTuningList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeTuning `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeTuning{}, &NodeTuningList{})
}
