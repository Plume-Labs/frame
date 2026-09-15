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
	AlertStateFiring   = "Firing"
	AlertStateResolved = "Resolved"
)

// FrameAlertSpec is what Alertmanager said about one alert, written by the
// receiver alone. The object is named fa-<fingerprint>: Alertmanager's
// fingerprint is a stable hash of the labels, so one object carries the alert
// from firing to resolution without an index.
type FrameAlertSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{1,64}$`
	Fingerprint string `json:"fingerprint"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	AlertName string `json:"alertName,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Severity string `json:"severity,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace,omitempty"`
	// Bounded by the receiver (64 entries, 256-byte keys, 4096-byte values)
	// before it ever reaches the apiserver.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	Annotations map[string]string `json:"annotations,omitempty"`
	// +kubebuilder:validation:Required
	StartsAt metav1.Time `json:"startsAt"`
	// Empty while the alert fires.
	// +optional
	EndsAt *metav1.Time `json:"endsAt,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	GeneratorURL string `json:"generatorURL,omitempty"`
}

// AlertDelivery is the relay's record of one subscription for one alert.
type AlertDelivery struct {
	// +kubebuilder:validation:Required
	Subscription string `json:"subscription"`
	// The last state delivered successfully; empty until the first success.
	// +optional
	// +kubebuilder:validation:Enum=Firing;Resolved
	DeliveredState string `json:"deliveredState,omitempty"`
	// +optional
	Attempts int32 `json:"attempts,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	LastError string `json:"lastError,omitempty"`
	// +optional
	LastAttemptAt *metav1.Time `json:"lastAttemptAt,omitempty"`
	// +optional
	LastDeliveredAt *metav1.Time `json:"lastDeliveredAt,omitempty"`
	// A non-retryable answer (401, 400, a redirect). Lifted when the alert
	// changes state or the subscription's generation changes.
	// +optional
	PermanentFailure bool `json:"permanentFailure,omitempty"`
	// The alert state the permanent failure was recorded against.
	// +optional
	FailedState string `json:"failedState,omitempty"`
	// The subscription generation this entry was last evaluated against.
	// +optional
	SubscriptionGeneration int64 `json:"subscriptionGeneration,omitempty"`
	// True when this entry was recorded without ever sending anything,
	// because the subscription's filter did not match the alert at the
	// time it resolved. Spec §5.3: a filter that widens afterwards must not
	// replay the incident to a tenant who never had it open. Never set on
	// an entry that was genuinely delivered (deliveredState reached by an
	// actual send survives a filter that stops matching afterwards).
	// Cleared back to false the next time a delivery actually succeeds.
	// +optional
	Excluded bool `json:"excluded,omitempty"`
}

// FrameAlertStatus: state and lastReceivedAt belong to the receiver,
// deliveries to the relay. Each writes with a merge patch holding only its
// own fields.
type FrameAlertStatus struct {
	// +optional
	// +kubebuilder:validation:Enum=Firing;Resolved
	State string `json:"state,omitempty"`
	// Rewritten at most every 15 minutes: Alertmanager resends unchanged
	// groups every few minutes, and kine on the test cluster cannot absorb
	// a write per resend.
	// +optional
	LastReceivedAt *metav1.Time `json:"lastReceivedAt,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=subscription
	Deliveries []AlertDelivery `json:"deliveries,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Alert",type=string,JSONPath=`.spec.alertName`
// +kubebuilder:printcolumn:name="Severity",type=string,JSONPath=`.spec.severity`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FrameAlert is one cluster alert as Frame received it, with its relay record.
type FrameAlert struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec   FrameAlertSpec   `json:"spec"`
	Status FrameAlertStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type FrameAlertList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameAlert `json:"items"`
}

func init() { SchemeBuilder.Register(&FrameAlert{}, &FrameAlertList{}) }
