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

// FrameUserSpec is what an administrator sets. Everything authd owns lives on
// status — see FrameUserStatus.
type FrameUserSpec struct {
	// Email identifies the account and becomes the Kubernetes username in the
	// token authd issues.
	//
	// The pattern is deliberately loose, which is right for email — the
	// grammar RFC 5322 actually specifies is not something a CRD should try
	// to enforce. The length is not: this value ends up as an RBAC subject
	// and in every audit-log entry, and an unbounded username there is a
	// poor idea. 254 is the RFC 5321 limit (T6).
	//
	// MaxLength and Pattern are each load-bearing and neither subsumes the
	// other: the pattern alone accepts an arbitrarily long address, and
	// MaxLength alone accepts a string with no "@" in it. The pattern is also
	// what rejects "" — no MinLength is needed, and adding one would only
	// duplicate it.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=254
	// +kubebuilder:validation:Pattern=`^[^@[:space:]]+@[^@[:space:]]+$`
	Email string `json:"email"`

	// Role decides which group the issued token carries.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=admin;operator;viewer
	Role string `json:"role"`

	// PasswordAuth controls whether this account may sign in with a password
	// at all. Defaults to disabled: an account is passkey-only unless someone
	// deliberately opens the other door.
	// +optional
	// +kubebuilder:validation:Enum=enabled;disabled
	// +kubebuilder:default=disabled
	PasswordAuth string `json:"passwordAuth,omitempty"`
}

// FrameUserStatus holds everything authd owns.
//
// PasswordHash lives here, not in spec (F11). It is credential material, and
// in spec it was readable by anything holding `get frameusers` and writable
// by anything holding `patch frameusers` — while the WebAuthn *public* key
// material sat safely in status, deliberately, so that an admin editing an
// account could not corrupt a key by hand. That asymmetry was the finding
// (security review M1): the public material was protected from hand-editing
// and the password hash was not. On status, only authd's frameusers/status
// grant can write it, and the viewer and editor tiers can be denied the
// subresource entirely.
//
// The destination is a Secret, not a status field — at-rest encryption and
// audit treatment a CR field does not have. That is a real design change
// (authd's store gains a second object to keep consistent, and the
// last-admin webhook guard would have to survive a partially-written pair),
// so it is recorded here rather than done in the freeze.
type FrameUserStatus struct {
	// ObservedGeneration is the metadata.generation this status was computed
	// from. A client can compare it to metadata.generation to tell whether
	// the writer has seen the current spec yet, without knowing anything
	// about this kind's condition vocabulary.
	//
	// FrameUser has no controller — authd is its store — so nothing writes
	// this field today, and under the current architecture nothing ever
	// will. It is present anyway because it is part of the frozen shape: a
	// future writer must not have to add a status field to an already-frozen
	// version. Part 0's Task 6 put it on v1alpha1 for the same reason, which
	// is also what keeps v1beta1 from having a field v1alpha1 lacks.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// PasswordHash is an argon2id PHC string, written only by authd. It is
	// meaningless while spec.passwordAuth is disabled.
	//
	// Deliberately unvalidated: a length or pattern bound here would be a
	// schema-level statement about a credential format, and getting it wrong
	// would lock out every account at once. internal/authd parses it and
	// treats an unparseable value as "no password", which is the safe
	// failure.
	// +optional
	PasswordHash string `json:"passwordHash,omitempty"`

	// Credentials are the enrolled authenticators. They live in status for
	// the same reason the hash now does: authd owns them, and an admin
	// editing an account must not be able to corrupt a key by hand.
	// +optional
	Credentials []WebAuthnCredential `json:"credentials,omitempty"`
}

// This is the conversion hub, but it is deliberately *not* the storage
// version yet — v1alpha1 still carries +kubebuilder:storageversion, and
// Task 19 moves the marker here when the conversion webhook starts serving.
// See the note on the v1alpha1 FrameJob for the full reasoning.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Email",type=string,JSONPath=`.spec.email`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.role`
// +kubebuilder:printcolumn:name="Password",type=string,JSONPath=`.spec.passwordAuth`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
//
// Enrolled key count is deliberately absent: printer columns evaluate a
// JSONPath, and JSONPath has no way to count a list. There is deliberately no
// Ready column either — FrameUser has no controller, so nothing would ever
// write one.

// FrameUser is a person who can sign in to the Cluster Control UI.
type FrameUser struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of FrameUser
	// +required
	Spec FrameUserSpec `json:"spec"`

	// status defines the observed state of FrameUser
	// +optional
	Status FrameUserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FrameUserList contains a list of FrameUser
type FrameUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FrameUser{}, &FrameUserList{})
}
