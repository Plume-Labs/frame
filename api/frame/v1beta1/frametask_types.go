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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	TaskPhaseRunning   = "Running"
	TaskPhaseSucceeded = "Succeeded"
	TaskPhaseFailed    = "Failed"

	TaskVerbCreate = "create"
	TaskVerbUpdate = "update"
	TaskVerbPatch  = "patch"
	TaskVerbDelete = "delete"
)

// ObjectRef points at a Kubernetes object without importing its type.
type ObjectRef struct {
	// +optional
	Group string `json:"group,omitempty"`
	// Resource is the plural resource name as it appears in the request
	// path ("nodes", "framejobs"), not a Kind. The proxy reads paths, and a
	// path carries the resource; deriving a Kind from it would need a
	// RESTMapper and buy nothing the screen cannot render.
	// +kubebuilder:validation:Required
	Resource string `json:"resource"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// FrameTaskSpec is the record of one mutating request the UI made.
type FrameTaskSpec struct {
	// User is the Kubernetes username the request was impersonated as —
	// the FrameUser's email.
	//
	// MinLength is what actually rejects a missing user through a typed
	// client: Required alone only checks that the key is present, and a Go
	// client always sends "user" (a non-pointer string with no omitempty),
	// so an absent user arrives here as "" rather than as a missing key.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=254
	User string `json:"user"`

	// Verb is the Kubernetes verb the HTTP method mapped to. Reads are not
	// recorded, so there is no get/list/watch here.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=create;update;patch;delete
	Verb string `json:"verb"`

	// Target is the object the request acted on.
	// +kubebuilder:validation:Required
	Target ObjectRef `json:"target"`

	// Action is a human-readable label supplied by the UI through the
	// X-Frame-Action header ("cordon node w2"). Absent when the request
	// came from something other than the UI.
	// +optional
	// +kubebuilder:validation:MaxLength=200
	Action string `json:"action,omitempty"`

	// Ref points at an object that carries the action's progress — a
	// FrameJob, a TalosUpgrade, a Velero Backup. The Tasks screen reads
	// that object's own status rather than copying it here, which is what
	// lets this kind exist without a controller.
	// +optional
	Ref *ObjectRef `json:"ref,omitempty"`
}

// FrameTaskStatus is the outcome, written by the proxy once the apiserver
// has answered.
//
// This kind reports a phase rather than a Ready condition, against the
// convention the other eight kinds follow, and the reason is that it is not
// reconciled: there is no desired state, no controller, and nothing to
// converge. A record of a finished HTTP call has exactly one dimension of
// health, which is what phase expresses well and conditions do not.
type FrameTaskStatus struct {
	// +kubebuilder:validation:Enum=Running;Succeeded;Failed
	// +optional
	Phase string `json:"phase,omitempty"`
	// HTTPCode is the apiserver's status code, 0 while running.
	// +optional
	HTTPCode int32 `json:"httpCode,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.user`
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.spec.action`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Code",type=integer,JSONPath=`.status.httpCode`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FrameTask is the trace of one write made through the Frame UI.
type FrameTask struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec   FrameTaskSpec   `json:"spec"`
	Status FrameTaskStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type FrameTaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameTask `json:"items"`
}

func init() { SchemeBuilder.Register(&FrameTask{}, &FrameTaskList{}) }
