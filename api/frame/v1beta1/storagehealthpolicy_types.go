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

// StorageHealthPolicyName is the only name the kind accepts: the thresholds
// are one cluster-wide setting, so there is nothing to merge or to order.
const StorageHealthPolicyName = "default"

// The thresholds a cluster with no StorageHealthPolicy runs on. The CRD's
// defaults repeat them, so a console that writes one field leaves the other
// where this says.
const (
	DefaultZFSCapacityPercent int32 = 80
	DefaultSSDWearPercent     int32 = 80
)

// StorageHealthPolicySpec is what the machine controller compares a machine's
// storage against. Only numbers: what counts as degraded (a failed md member,
// a pool that is not ONLINE, a vdev with errors) is not a setting.
type StorageHealthPolicySpec struct {
	// ZFSCapacityPercent raises StorageCapacityHigh when a pool's capacity
	// reaches it. ZFS slows down well before a pool is full, and a pool that
	// fills cannot always free space. It clears two points below, so a pool
	// sitting on the threshold does not alert every minute.
	// +kubebuilder:default=80
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	ZFSCapacityPercent int32 `json:"zfsCapacityPercent,omitempty"`

	// SSDWearPercent raises DiskWearHigh when a drive's endurance use, from its
	// BMC or else from the disk's SMART data, reaches it. A drive with neither
	// never raises it: the condition then says Unknown, not False.
	// +kubebuilder:default=80
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	SSDWearPercent int32 `json:"ssdWearPercent,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default'",message="the policy is a singleton named default"
// +kubebuilder:printcolumn:name="ZFS capacity %",type=integer,JSONPath=`.spec.zfsCapacityPercent`
// +kubebuilder:printcolumn:name="SSD wear %",type=integer,JSONPath=`.spec.ssdWearPercent`

// StorageHealthPolicy holds the thresholds behind FrameMachine's storage
// conditions and alerts. Its absence means the defaults above. It has no
// status: nothing reconciles it, the machine controller reads it on each pass.
type StorageHealthPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec StorageHealthPolicySpec `json:"spec"`
}

// +kubebuilder:object:root=true

// StorageHealthPolicyList contains a list of StorageHealthPolicy.
type StorageHealthPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StorageHealthPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StorageHealthPolicy{}, &StorageHealthPolicyList{})
}
