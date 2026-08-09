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

// Package scheduling holds the one mapping from a Frame tier onto the
// frame-* PriorityClass objects SchedulingPolicy's controller creates.
//
// It lives outside internal/controller so that both the FrameJob controller
// and the inference provider can import it without the provider depending on
// a controller package — the provider is deliberately reachable only through
// the registry.
package scheduling

// The PriorityClass names SchedulingPolicy's reconcilePriorityClass creates.
// Nothing else may name a PriorityClass: Frame owns placement, and a spec
// field naming an arbitrary (possibly system) PriorityClass would break that.
const (
	PriorityClassCritical = "frame-critical"
	PriorityClassHigh     = "frame-high"
	PriorityClassMedium   = "frame-medium"
	PriorityClassLow      = "frame-low"
)

// PriorityClassForJobPriority maps FrameJob.spec.priority onto a
// Frame-managed PriorityClass. An unrecognised value — including the empty
// string — yields "", meaning "set no priorityClassName", which leaves the
// workload at the cluster's implicit default.
func PriorityClassForJobPriority(priority string) string {
	switch priority {
	case "critical":
		return PriorityClassCritical
	case "high":
		return PriorityClassHigh
	case "medium":
		return PriorityClassMedium
	case "low":
		return PriorityClassLow
	default:
		return ""
	}
}

// PriorityClassForServiceClass maps FrameService.spec.serviceClass onto a
// Frame-managed PriorityClass.
//
// A FrameService has no separate spec.priority, unlike a FrameJob, and that
// is deliberate (F10). A job's resource tier and its scheduling urgency are
// separable — a HIGH-tier nightly batch can legitimately be low-priority. A
// long-lived service instance has no such separation: its tier is its
// urgency, for its whole lifetime. Adding spec.priority would duplicate
// serviceClass with no case where the two would differ, and then they could
// disagree.
//
// There is no critical tier: serviceClass has three values and inventing a
// fourth here would put an instance above every job on the cluster.
func PriorityClassForServiceClass(serviceClass string) string {
	switch serviceClass {
	case "HIGH":
		return PriorityClassHigh
	case "MEDIUM":
		return PriorityClassMedium
	case "LOW":
		return PriorityClassLow
	default:
		return ""
	}
}
