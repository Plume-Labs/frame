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

// FrameStorageSpec declares one storage entry, in the sense Proxmox VE gives
// the word: a typed, named place where volumes of declared content types can
// live, available on some set of nodes.
type FrameStorageSpec struct {
	// Type is what backs this entry. NFS and iSCSI are deliberately out of
	// this lot: neither exists in the park, and a backend nobody runs is a
	// backend nobody tests.
	// +kubebuilder:validation:Enum=ceph-rbd;ceph-bucket;local-path
	Type string `json:"type"`

	// Content is what may be stored here — a list, not a single value: a
	// Ceph pool legitimately holds both workload volumes and model
	// artifacts, and forcing a choice would just produce a second entry on
	// the same pool.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=5
	// +kubebuilder:validation:items:Enum=workload;model;backup;artifact;scratch
	Content []string `json:"content"`

	// StorageClassName is the StorageClass this entry owns or adopts.
	// +kubebuilder:validation:MaxLength=253
	StorageClassName string `json:"storageClassName"`

	// AdoptExisting must be set explicitly for an entry naming a
	// StorageClass Frame did not create. Without it, admission refuses:
	// the cluster's existing classes carry production volumes, and an
	// entry that silently took ownership of one would delete it on its own
	// deletion. An adopted entry owns nothing and deletes nothing.
	// +optional
	AdoptExisting bool `json:"adoptExisting,omitempty"`

	// Nodes empty means every node. A non-empty list names the nodes where
	// this entry is available — the local/shared distinction made concrete.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Nodes []string `json:"nodes,omitempty"`
}

// StorageCapacity reports usable space first. Raw is optional and never
// stands alone: the park's capacity incident came from reading raw numbers
// on a pool whose replication divides them by three.
type StorageCapacity struct {
	// +optional
	// +kubebuilder:validation:MaxLength=32
	Usable string `json:"usable,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=32
	Used string `json:"used,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=32
	Raw string `json:"raw,omitempty"`
}

// ClaimCounts is the gap between the policy and the cluster: how many PVCs
// use this entry's class, and how many of them carry the usage label the
// content-type webhook selects on. A large Total with a zero Labelled is
// the normal state on the day the webhook lands, and is meant to be read,
// not fixed automatically.
type ClaimCounts struct {
	Total    int32 `json:"total,omitempty"`
	Labelled int32 `json:"labelled,omitempty"`
}

type FrameStorageStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Shared is derived from Type, never declared. ceph-* entries are
	// shared; local-path is not. It lives in status because a user who
	// could write it could declare a local disk shared and have Frame
	// believe it.
	// +optional
	Shared bool `json:"shared,omitempty"`

	// +optional
	// +kubebuilder:validation:Enum=Ready;Degraded;Unknown
	Phase string `json:"phase,omitempty"`

	// +optional
	Capacity *StorageCapacity `json:"capacity,omitempty"`

	// +optional
	Claims *ClaimCounts `json:"claims,omitempty"`

	// Adopted records that this entry did not create its StorageClass, and
	// therefore must never delete it.
	// +optional
	Adopted bool `json:"adopted,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=fstor
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=".spec.storageClassName"
// +kubebuilder:printcolumn:name="Shared",type=boolean,JSONPath=".status.shared"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Usable",type=string,JSONPath=".status.capacity.usable"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// FrameStorage is the Schema for the framestorages API.
type FrameStorage struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec FrameStorageSpec `json:"spec,omitempty"`

	// +optional
	Status FrameStorageStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameStorageList contains a list of FrameStorage.
type FrameStorageList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameStorage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameStorage{}, &FrameStorageList{})
}
