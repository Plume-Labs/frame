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
	// +optional
	PasswordHash string `json:"passwordHash,omitempty"`
}

type FrameUserStatus struct {
	// Credentials are owned by authd, which is why they live in status: an
	// admin editing an account cannot corrupt a key by hand.
	// +optional
	Credentials []WebAuthnCredential `json:"credentials,omitempty"`
}

// +kubebuilder:object:root=true
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
