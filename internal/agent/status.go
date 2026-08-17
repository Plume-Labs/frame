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

package agent

import (
	"context"

	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// PatchObserved upserts nodeName's entry in nt's status.nodes and writes back
// nothing but its Observed field. Phase, Realization, AppliedGeneration,
// RestartedAt and RestartedGeneration are the controller's to compute — from
// exactly this data — and the agent must never author them.
//
// It lives here rather than in cmd/agent so the controller's own envtest suite
// can drive the real agent write against the real reconciler write and prove
// they survive each other. That is not a testing convenience: status.nodes has
// two writers, and neither had a way to notice the other.
//
// Two mechanisms, both load-bearing, and neither sufficient alone:
//
//  1. The object is re-read here rather than patched from the caller's copy.
//     The caller's copy comes from a List at the top of a tick, and a tick
//     spends seconds in nsenter before reaching this point; a merge patch
//     built from that snapshot reverts everything the controller wrote in the
//     meantime. Because a CRD status patch is a JSON merge patch, "everything"
//     is literal: JSON merge patch replaces an array wholesale, so one stale
//     entry takes the whole of status.nodes with it, every node included.
//
//  2. The patch carries a resourceVersion precondition, so a write landing in
//     the window between the read above and the patch below is a 409 rather
//     than a silent revert, and RetryOnConflict then redoes the read. Without
//     it the window is small but the consequence is not: the fields most
//     likely to be written concurrently are RestartedAt and
//     RestartedGeneration, which are the loop guard that stops an approved
//     node being cordoned, drained and restarted more than once.
func PatchObserved(
	ctx context.Context,
	c client.Client,
	nt *framev1beta1.NodeTuning,
	nodeName string,
	observed framev1beta1.ObservedTuning,
) error {
	key := client.ObjectKeyFromObject(nt)
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &framev1beta1.NodeTuning{}
		if err := c.Get(ctx, key, fresh); err != nil {
			return err
		}
		base := fresh.DeepCopy()

		found := false
		for i := range fresh.Status.Nodes {
			if fresh.Status.Nodes[i].Name == nodeName {
				fresh.Status.Nodes[i].Observed = observed
				found = true
				break
			}
		}
		// Upserting rather than requiring an existing entry keeps the agent
		// independently useful before the controller has ever reconciled this
		// object, and resilient if a NodeTuning is created while an agent is
		// already running.
		if !found {
			fresh.Status.Nodes = append(fresh.Status.Nodes, framev1beta1.NodeTuningNodeStatus{
				Name:     nodeName,
				Observed: observed,
			})
		}

		return c.Status().Patch(ctx, fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	})
}
