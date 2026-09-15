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

// AlertTokenRef names a Secret key in the subscription's own namespace.
type AlertTokenRef struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
}

// AlertFilter decides which alerts a subscription receives. Empty lists
// mean "no restriction", except excludeAlertNames which defaults to the two
// meta alerts that never resolve.
type AlertFilter struct {
	// +optional
	// +kubebuilder:default={"Watchdog","InfoInhibitor"}
	ExcludeAlertNames []string `json:"excludeAlertNames,omitempty"`
	// +optional
	Severities []string `json:"severities,omitempty"`
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`
}

type FrameAlertSubscriptionSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://.+`
	URL string `json:"url"`
	// +kubebuilder:validation:Required
	TokenSecretRef AlertTokenRef `json:"tokenSecretRef"`
	// +optional
	// +kubebuilder:default={}
	Filter AlertFilter `json:"filter,omitempty"`
	// +optional
	Paused bool `json:"paused,omitempty"`
}

type FrameAlertSubscriptionStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	LastSuccessAt *metav1.Time `json:"lastSuccessAt,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	LastError string `json:"lastError,omitempty"`
	// +optional
	PendingDeliveries int32 `json:"pendingDeliveries,omitempty"`
	// When the status was last computed; bounds recomputation to once a minute.
	// +optional
	ComputedAt *metav1.Time `json:"computedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.url`
// +kubebuilder:printcolumn:name="Pending",type=integer,JSONPath=`.status.pendingDeliveries`
// +kubebuilder:printcolumn:name="LastSuccess",type=date,JSONPath=`.status.lastSuccessAt`

// FrameAlertSubscription declares a tenant endpoint that receives cluster alerts.
type FrameAlertSubscription struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec   FrameAlertSubscriptionSpec   `json:"spec"`
	Status FrameAlertSubscriptionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type FrameAlertSubscriptionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameAlertSubscription `json:"items"`
}

func init() { SchemeBuilder.Register(&FrameAlertSubscription{}, &FrameAlertSubscriptionList{}) }
