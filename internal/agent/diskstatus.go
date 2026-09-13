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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/storage"
)

// PatchObservedDisks writes disks into the status of whichever FrameMachine
// names nodeName in spec.nodeRef, and recomputes the divergences against
// that machine's BMC drive list.
//
// It writes status.storage and nothing else. FrameMachine.status has two
// writers — this one and the machine controller — and a CRD status patch is
// a JSON merge patch, which replaces an array wholesale. The same two
// mechanisms as PatchObserved are therefore load-bearing here, and neither
// is sufficient alone:
//
//  1. The object is re-read inside the retry loop rather than patched from
//     a caller's copy. The caller's copy comes from the top of a tick that
//     spends seconds in nsenter running lsblk; a patch built from it
//     reverts every field the controller wrote in between — power state,
//     sensors, event log, the whole inventory.
//
//  2. The patch carries a resourceVersion precondition, so a controller
//     write landing between the read and the patch is a 409 that
//     RetryOnConflict redoes, not a silent revert.
//
// A node no FrameMachine claims is an error, never a silent no-op: a
// machine registered without a nodeRef is a misconfiguration the operator
// has to see, and an agent that swallowed it would leave the disks screen
// permanently empty with nothing to explain why.
func PatchObservedDisks(
	ctx context.Context,
	c client.Client,
	nodeName string,
	disks []framev1beta1.ObservedDisk,
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var machines framev1beta1.FrameMachineList
		if err := c.List(ctx, &machines); err != nil {
			return fmt.Errorf("listing FrameMachines: %w", err)
		}

		var target *framev1beta1.FrameMachine
		for i := range machines.Items {
			if machines.Items[i].Spec.NodeRef == nodeName {
				target = &machines.Items[i]
				break
			}
		}
		if target == nil {
			return fmt.Errorf("no FrameMachine has spec.nodeRef %q", nodeName)
		}

		base := target.DeepCopy()
		var bmcDrives []framev1beta1.DriveInfo
		if target.Status.Inventory != nil {
			bmcDrives = target.Status.Inventory.Drives
		}
		now := metav1.Now()
		target.Status.Storage = &framev1beta1.MachineStorage{
			Observed:    disks,
			Divergences: storage.Join(bmcDrives, disks),
			ObservedAt:  &now,
		}

		return c.Status().Patch(ctx, target, client.MergeFromWithOptions(
			base, client.MergeFromWithOptimisticLock{},
		))
	})
}
