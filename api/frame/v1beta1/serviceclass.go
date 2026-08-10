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

// ServiceClass is the tier a Frame object belongs to. One named type, used by
// FrameJob, FrameNode, FrameResourceQuota and (through an import)
// FrameService — before v1beta1 the same three-valued concept was declared
// four times, no two alike: different enums, different optionality,
// different defaults, and a fourth answer again in the SDK's fallbacks (F4).
//
// The empty string is deliberately not a member. Absence means
// "unclassified"; an explicitly-empty tier and an omitted one were
// indistinguishable and only FrameNode ever allowed it, on no stored object.
//
// The *default* is deliberately not unified. FrameJob defaults to LOW and
// FrameService to MEDIUM, and both are right for their kind: an unspecified
// batch job should be preemptible, an unspecified long-lived service
// instance should not be the first thing evicted. FrameNode has no default
// at all — a node is discovered before it is classified, and defaulting it
// would classify hardware nobody has looked at. FrameResourceQuota requires
// it: a quota that does not say what it caps is meaningless.
//
// +kubebuilder:validation:Enum=HIGH;MEDIUM;LOW
type ServiceClass string

const (
	ServiceClassHigh   ServiceClass = "HIGH"
	ServiceClassMedium ServiceClass = "MEDIUM"
	ServiceClassLow    ServiceClass = "LOW"
)

// ParameterValue bounds a value in one of the free-form parameter maps.
// Declared as a named type rather than as a marker on the map, because
// controller-gen emits a value bound only from the map's value *type* —
// verified with controller-gen v0.20.1, which emits
// additionalProperties.maxLength from a named value type and silently drops
// markers on a named key type.
//
// +kubebuilder:validation:MaxLength=1024
type ParameterValue string
