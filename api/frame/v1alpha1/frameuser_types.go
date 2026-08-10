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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"

	PasswordEnabled  = "enabled"
	PasswordDisabled = "disabled"
)

// WebAuthnCredential is one enrolled authenticator (a YubiKey, a phone
// passkey). Public data only: the private key never leaves the device.
type WebAuthnCredential struct {
	// ID is the base64url credential ID reported by the authenticator.
	ID string `json:"id"`
	// PublicKey is the base64-encoded COSE public key.
	PublicKey string `json:"publicKey"`
	// SignCount is the authenticator's counter as of the last successful
	// assertion. A value that fails to advance signals a cloned credential.
	SignCount uint32 `json:"signCount"`
	// AddedAt records enrolment time, so a user can tell two keys apart.
	AddedAt metav1.Time `json:"addedAt"`
	// Label is a human name for the key, e.g. "YubiKey 5C".
	// +optional
	Label string `json:"label,omitempty"`
}

type FrameUserSpec struct {
	// Email identifies the account and becomes the Kubernetes username.
	// +kubebuilder:validation:Pattern=`^[^@[:space:]]+@[^@[:space:]]+$`
	Email string `json:"email"`

	// Role decides which group the issued token carries.
	// +kubebuilder:validation:Enum=admin;operator;viewer
	Role string `json:"role"`

	// PasswordAuth controls whether this account may sign in with a password
	// at all. Defaults to disabled: an account is passkey-only unless someone
	// deliberately opens the other door.
	// +kubebuilder:validation:Enum=enabled;disabled
	// +kubebuilder:default=disabled
	PasswordAuth string `json:"passwordAuth,omitempty"`

	// PasswordHash is an argon2id PHC string, written only by authd. It is
	// meaningless while PasswordAuth is disabled.
	//
	// This field is readable at v1alpha1 and effectively read-only. v1beta1
	// moved it to status.passwordHash (F11) so that `patch frameusers` could
	// not set anyone's password; RBAC has no version dimension, so this field
	// was a way around that, and the validating webhook now closes it. A write
	// here that changes the stored value is rejected, and so is one that omits
	// the field on a full replace — that is a credential deletion wearing an
	// ordinary update's clothes. See guardPasswordHash in
	// internal/webhook/frame/v1beta1.
	// +optional
	PasswordHash string `json:"passwordHash,omitempty"`
}

type FrameUserStatus struct {
	// ObservedGeneration is the metadata.generation this status was computed
	// from. A client can compare it to metadata.generation to tell whether
	// the controller has seen the current spec yet, without knowing anything
	// about this kind's condition vocabulary.
	//
	// FrameUser has no controller — authd is its store — so nothing writes
	// this field today. It is present anyway because it is part of the
	// frozen shape: a future writer must not have to add a status field to
	// an already-frozen version.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Credentials are owned by authd, which is why they live in status: an
	// admin editing an account cannot corrupt a key by hand.
	// +optional
	Credentials []WebAuthnCredential `json:"credentials,omitempty"`
}

// This version is served and deprecated; v1beta1 is the storage version. The
// marker moved there in the same change that turned the conversion webhook on
// — see the note on the v1alpha1 FrameJob for the general reasoning.
//
// FrameUser is the kind where that atomicity is not merely tidy. Its
// difference from v1beta1 runs *both* ways: this version has spec.passwordHash
// and v1beta1 has status.passwordHash (F11 moved it), so under strategy None
// — which persists the intersection of the request-version and
// storage-version schemas — there is no lossless placement of the storage
// version at all. Whichever side it sat on, one hash was pruned on write and
// the apiserver still answered 200. Only the conversion webhook makes either
// placement lossless: turning it on without moving this marker, or moving the
// marker without it, silently breaks password login.
//
// +kubebuilder:object:root=true
// The warning is capped at 256 characters by the apiserver — envtest rejects
// the whole CRD past that — so it says what a client must do and leaves the
// reasoning to docs/upgrading.md.
//
// +kubebuilder:deprecatedversion:warning="frame.plume-labs.io/v1alpha1 FrameUser is deprecated; use frame.plume-labs.io/v1beta1. spec.passwordHash moved to status.passwordHash: a write that changes it is rejected, and so is a replace that omits it. Use the v1beta1 frameusers/status subresource."
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Email",type=string,JSONPath=`.spec.email`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.role`
// +kubebuilder:printcolumn:name="Password",type=string,JSONPath=`.spec.passwordAuth`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// Enrolled key count is deliberately absent: printer columns evaluate a
// JSONPath, and JSONPath has no way to count a list.

// FrameUser is a person who can sign in to the Cluster Control UI.
type FrameUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FrameUserSpec   `json:"spec,omitempty"`
	Status FrameUserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type FrameUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameUser{}, &FrameUserList{})
}
