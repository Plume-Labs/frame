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

// FrameInstallSpec is one installation's intent. Every destructive input is
// named explicitly: there is no field here that means "figure it out".
type FrameInstallSpec struct {
	// MachineRef names the FrameMachine whose BMC drives this installation.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	MachineRef string `json:"machineRef"`

	// ConfirmSerial is the machine's serial, retyped by hand. The controller
	// refuses unless it equals what Redfish reports at
	// Systems/1.SerialNumber. A typo is a refusal, not an installation
	// somewhere else; this is what catches pointing at the wrong machine.
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:MinLength=1
	ConfirmSerial string `json:"confirmSerial"`

	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Hostname string `json:"hostname"`

	Network InstallNetwork `json:"network"`
	Layout  InstallLayout  `json:"layout"`
	Cluster InstallCluster `json:"cluster"`

	// BootMode is set on the machine rather than inherited. An image that
	// boots in the other mode stays on a black screen and reports nothing.
	// +kubebuilder:validation:Enum=UEFI;Legacy
	// +kubebuilder:default=UEFI
	BootMode string `json:"bootMode,omitempty"`

	// SSHKeyRef names a Secret holding "id" (private) and "id.pub" (public).
	// Only the public half ever reaches an installer image.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	SSHKeyRef string `json:"sshKeyRef"`
}

// InstallNetwork is the static network configuration the installer writes.
type InstallNetwork struct {
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:XValidation:rule="isCIDR(self)",message="address must be a CIDR, e.g. 192.168.2.210/24"
	Address string `json:"address"`
	// +kubebuilder:validation:MaxLength=45
	// +kubebuilder:validation:XValidation:rule="isIP(self)",message="gateway must be an IP address"
	Gateway string `json:"gateway"`
	// DNS resolvers, as IP addresses. There is no vlan or bond here on
	// purpose: both were declared, validated by the apiserver, and then
	// dropped on the floor by toProvisionSpec -- a FrameInstall asking for a
	// tagged VLAN installed untagged, silently. Task 1 removed them from
	// provision.Network for exactly that reason and they survived one layer
	// up, where the operator types. d-i bonding is not something to
	// implement blind; add them back when a machine needs one.
	// +kubebuilder:validation:MaxItems=3
	DNS []string `json:"dns,omitempty"`
}

// InstallLayout is the disk layout recipe.
//
// There is no raw escape hatch. It existed, and it could not do its job: a
// review round required it to be a single line (so the preseed's on-machine
// disk-size assertion could still be written), and a real partman recipe is
// multi-line. It was also the one kind whose CEL did not require disks,
// while PartmanRecipe refused it without them -- so the apiserver accepted a
// shape the code rejected one phase later, after the object existed and the
// operator believed it was valid. An unusual layout is added here as a named
// kind, reviewed, with tests.
//
// +kubebuilder:validation:XValidation:rule="self.kind != 'mirror' || size(self.disks) == 2",message="a mirror is exactly two disks"
// +kubebuilder:validation:XValidation:rule="self.kind != 'single-disk' || size(self.disks) == 1",message="single-disk is exactly one disk"
type InstallLayout struct {
	// +kubebuilder:validation:Enum=single-disk;mirror
	Kind string `json:"kind"`
	// +kubebuilder:validation:MaxItems=8
	Disks []InstallDisk `json:"disks,omitempty"`
}

// InstallDisk names one disk the layout consumes.
type InstallDisk struct {
	// ByID is a /dev/disk/by-id path. Kernel names reorder between boots and
	// between controllers; on the ML350 Gen9, IML entry 1832 warns that
	// residual volume metadata can hide disks from the host, which changes
	// that ordering. Naming /dev/sda means installing on whatever is first
	// that day.
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:XValidation:rule="self.startsWith('/dev/disk/by-id/')",message="disks must be named by /dev/disk/by-id, never by kernel name"
	ByID string `json:"byID"`
	// SizeBytes is asserted on the machine before partitioning.
	// +kubebuilder:validation:Minimum=1
	SizeBytes int64 `json:"sizeBytes"`
}

// InstallCluster says whether this installation starts a cluster or joins
// one that already exists.
//
// +kubebuilder:validation:XValidation:rule="self.mode != 'join' || (has(self.serverURL) && has(self.joinTokenRef))",message="joining an existing cluster needs both serverURL and joinTokenRef; the token lives at /var/lib/rancher/k3s/server/node-token on a server node and must be placed in a Secret first"
type InstallCluster struct {
	// +kubebuilder:validation:Enum=init;join
	Mode string `json:"mode"`
	// +kubebuilder:validation:MaxLength=255
	ServerURL string `json:"serverURL,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	JoinTokenRef string `json:"joinTokenRef,omitempty"`
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:MinLength=1
	K3sVersion string `json:"k3sVersion"`
}

// FrameInstallStatus defines the observed state of FrameInstall.
type FrameInstallStatus struct {
	// +kubebuilder:validation:Enum=Pending;Preparing;MediaAttached;Installing;Installed;Joining;Ready;Failed
	Phase string `json:"phase,omitempty"`
	// +optional
	PhaseSince *metav1.Time `json:"phaseSince,omitempty"`
	// FailedPhase names where it stopped, which is the first thing anyone
	// reading a failure needs.
	// +kubebuilder:validation:MaxLength=32
	FailedPhase string `json:"failedPhase,omitempty"`
	// +kubebuilder:validation:MaxLength=512
	Message string `json:"message,omitempty"`
	// HostKey is pinned on first contact. Frame cannot know it in advance:
	// baking it into the image would make the image a secret carrier.
	// +kubebuilder:validation:MaxLength=512
	HostKey string `json:"hostKey,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	NodeName string `json:"nodeName,omitempty"`
	// KubeconfigSecret names the Secret holding the new cluster's kubeconfig,
	// set only when this installation created a cluster.
	// +kubebuilder:validation:MaxLength=253
	KubeconfigSecret string `json:"kubeconfigSecret,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=fi
// +kubebuilder:printcolumn:name="Machine",type=string,JSONPath=`.spec.machineRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.status.nodeName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FrameInstall is the Schema for the frameinstalls API.
type FrameInstall struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec FrameInstallSpec `json:"spec,omitempty"`

	// +optional
	Status FrameInstallStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameInstallList contains a list of FrameInstall.
type FrameInstallList struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard list metadata
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []FrameInstall `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameInstall{}, &FrameInstallList{})
}
