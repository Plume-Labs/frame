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

// FrameRelease is one published Hearthrack release as the in-cluster updater
// read it from the release channel (docs/specs/2026-11-24-mise-a-jour-en-un-clic.md,
// §3.1–3.2). It is a read-only mirror: only the release syncer writes it (never the
// updater that reads it), and only after it verified the signature of the
// manifest and of every image. The console lists these to
// show "version X available"; nothing here is a request to change anything.
//
// Every image is pinned by digest. A tag can be moved after the fact, a digest
// cannot, which is the whole point of the manifest.
type FrameReleaseSpec struct {
	// Version is the semver without the leading v ("1.0.0", "0.9.0-beta.1").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`
	// +kubebuilder:validation:MaxLength=64
	Version string `json:"version"`

	// Channel is the release channel the manifest was published on.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=stable;beta
	Channel string `json:"channel"`

	// Images maps a component (controller, ui, uiproxy, authd, agent,
	// provisiond) to its image reference, which must carry a sha256 digest.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=16
	Images map[string]string `json:"images"`

	// MinFrom is the oldest version an update to this release may start from.
	// Empty means no floor.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`
	// +kubebuilder:validation:MaxLength=64
	MinFrom string `json:"minFrom,omitempty"`

	// RollbackSafeTo is the oldest version one can return to from this release
	// without losing data. Empty means rollback is not offered.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`
	// +kubebuilder:validation:MaxLength=64
	RollbackSafeTo string `json:"rollbackSafeTo,omitempty"`

	// Notes is the short release note shown before the update.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Notes string `json:"notes,omitempty"`

	// Revoked marks a release withdrawn by the channel: it is never offered
	// and a pending update to it is refused.
	// +optional
	Revoked bool `json:"revoked,omitempty"`

	// PublishedAt is when the release was published.
	// +optional
	PublishedAt *metav1.Time `json:"publishedAt,omitempty"`
}

// FrameReleaseStatus records what the release syncer verified.
type FrameReleaseStatus struct {
	// ManifestDigest is the sha256 digest of the signed manifest artifact.
	// +optional
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	ManifestDigest string `json:"manifestDigest,omitempty"`

	// Verified is true once the manifest signature and every image signature
	// checked out against the pinned identity.
	// +optional
	Verified bool `json:"verified,omitempty"`

	// VerifiedAt is when that check last passed.
	// +optional
	VerifiedAt *metav1.Time `json:"verifiedAt,omitempty"`

	// Message says why verification failed, when it did.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=frel
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=".spec.version"
// +kubebuilder:printcolumn:name="Channel",type=string,JSONPath=".spec.channel"
// +kubebuilder:printcolumn:name="Verified",type=boolean,JSONPath=".status.verified"
// +kubebuilder:printcolumn:name="Revoked",type=boolean,JSONPath=".spec.revoked"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +kubebuilder:storageversion

// FrameRelease is a release the updater has seen on a channel.
type FrameRelease struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec   FrameReleaseSpec   `json:"spec"`
	Status FrameReleaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type FrameReleaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameRelease `json:"items"`
}

func init() { SchemeBuilder.Register(&FrameRelease{}, &FrameReleaseList{}) }
