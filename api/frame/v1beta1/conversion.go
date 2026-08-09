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

// v1beta1 is the conversion hub: every other version converts to and from
// these types, and they are the storage version. A hub implements no
// conversion of its own — Hub() is a marker method, and its presence is what
// controller-runtime's conversion webhook dispatches on.
//
// Adding a v1beta2 later means moving Hub() there and writing
// v1beta1 <-> v1beta2 alongside the existing v1alpha1 pair. It does not mean
// touching v1alpha1's functions.
//
// "and they are the storage version" is the destination, not today's state:
// +kubebuilder:storageversion still sits on the v1alpha1 kinds and Task 19
// moves all eight atomically with the webhook. A hub that is not yet the
// storage version is harmless — Hub() only decides which side of a conversion
// is which — and moving the marker early is not, which is why it has not
// moved. See the note on the v1alpha1 FrameJob.

func (*FrameJob) Hub()           {}
func (*FrameNode) Hub()          {}
func (*FrameResourceQuota) Hub() {}
func (*SchedulingPolicy) Hub()   {}
func (*TalosMachineConfig) Hub() {}
func (*TalosUpgrade) Hub()       {}
func (*FrameUser) Hub()          {}
