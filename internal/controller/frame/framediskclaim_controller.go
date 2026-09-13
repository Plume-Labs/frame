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

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// observationMaxAge is how old the agent's disk report may be and still
// authorise a destructive act. The agent reports every 30 seconds; five
// minutes is ten missed reports, which is a node that has stopped talking.
//
// This is deliberately not configurable. A knob here is a knob that gets
// widened the one time someone is in a hurry.
const observationMaxAge = 5 * time.Minute

// Wiper performs the destructive work on the node and records that it did.
// The marker it writes lives on the machine, not in this process: in the
// previous lot a manager restart replayed an entire destructive sequence
// because the only record that it had already run was process-local. That
// was reproduced, not supposed.
type Wiper interface {
	// ReadMarker returns the claim UID already recorded for this disk on
	// this machine, or "" if none.
	ReadMarker(ctx context.Context, machine, byIDPath string) (string, error)
	// Claim writes the marker and then does the destructive work.
	Claim(ctx context.Context, machine, byIDPath, claimUID, destination string) error
}

type FrameDiskClaimReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Wiper  Wiper
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framediskclaims,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framediskclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framediskclaims/finalizers,verbs=update

func (r *FrameDiskClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var c framev1beta1.FrameDiskClaim
	if err := r.Get(ctx, req.NamespacedName, &c); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// A terminal phase is terminal. Re-running means a new object.
	if c.Status.Phase == "Ready" || c.Status.Phase == "Failed" {
		return ctrl.Result{}, nil
	}

	disk, err := r.authorise(ctx, &c)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, &c, err.Error())
	}

	// GUARD 4: the marker is read from the machine, never from memory.
	claimUID := string(c.UID)
	existing, err := r.Wiper.ReadMarker(ctx, c.Spec.MachineRef.Name, c.Spec.ByIDPath)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reading the claim marker on %s: %w", c.Spec.MachineRef.Name, err)
	}
	if existing == claimUID {
		return ctrl.Result{}, r.succeed(ctx, &c, claimUID, "already claimed by this object")
	}
	if existing != "" {
		return ctrl.Result{}, r.fail(ctx, &c,
			fmt.Sprintf("disk %s already carries claim marker %q; a second claim on a claimed disk is refused",
				c.Spec.ByIDPath, existing))
	}

	if err := r.setPhase(ctx, &c, "Claiming", claimUID, fmt.Sprintf("claiming %s", disk.Path)); err != nil {
		return ctrl.Result{}, err
	}

	// GUARD 5: the error from the destructive call is returned, never
	// swallowed. A phase does not become Ready because the failure happened
	// after the useful work.
	if err := r.Wiper.Claim(ctx, c.Spec.MachineRef.Name, c.Spec.ByIDPath, claimUID, c.Spec.Destination); err != nil {
		if serr := r.setPhase(ctx, &c, "Claiming", claimUID,
			fmt.Sprintf("claim of %s did not complete cleanly: %v", disk.Path, err)); serr != nil {
			return ctrl.Result{}, serr
		}
		return ctrl.Result{}, fmt.Errorf("claiming %s on %s: %w", c.Spec.ByIDPath, c.Spec.MachineRef.Name, err)
	}

	return ctrl.Result{}, r.succeed(ctx, &c, claimUID, fmt.Sprintf("%s claimed for %s", disk.Path, c.Spec.Destination))
}

// authorise runs guards 1 to 3 and returns the observed disk the claim
// names. Every branch that cannot positively identify a free disk is a
// refusal: not knowing is not an authorisation.
func (r *FrameDiskClaimReconciler) authorise(ctx context.Context, c *framev1beta1.FrameDiskClaim) (*framev1beta1.ObservedDisk, error) {
	// GUARD 1, first half: an empty serial matches nothing, because two
	// empty strings are equal and that is how a disk gets wiped with no
	// confirmation.
	if strings.TrimSpace(c.Spec.Serial) == "" {
		return nil, fmt.Errorf("spec.serial is empty: the retyped serial is the confirmation, and an empty one confirms nothing")
	}

	// GUARD 2: an sdX path names a different disk on every boot.
	if !strings.HasPrefix(c.Spec.ByIDPath, "/dev/disk/by-id/") {
		return nil, fmt.Errorf("spec.byIDPath %q is not under /dev/disk/by-id: sdX ordering changes across boots", c.Spec.ByIDPath)
	}

	var m framev1beta1.FrameMachine
	if err := r.Get(ctx, types.NamespacedName{Name: c.Spec.MachineRef.Name, Namespace: c.Namespace}, &m); err != nil {
		return nil, fmt.Errorf("reading FrameMachine %q: %v", c.Spec.MachineRef.Name, err)
	}

	// GUARD 3, the half that gets forgotten: a machine whose agent has
	// never reported, or reported too long ago, authorises nothing.
	if m.Status.Storage == nil || m.Status.Storage.ObservedAt == nil {
		return nil, fmt.Errorf("machine %q has never reported its disks: not knowing is not an authorisation", m.Name)
	}
	if age := time.Since(m.Status.Storage.ObservedAt.Time); age > observationMaxAge {
		return nil, fmt.Errorf("machine %q's disk report is stale (%s old, limit %s)", m.Name, age.Truncate(time.Second), observationMaxAge)
	}

	// GUARD 1, second half: the retyped serial must match exactly one
	// observed disk, and it must be the same disk the path names. Two
	// disks answering to the same non-empty serial is not an identity —
	// the claim cannot say which one it destroys, so an ambiguous match
	// refuses just as a missing one does.
	var matches []*framev1beta1.ObservedDisk
	for i := range m.Status.Storage.Observed {
		d := &m.Status.Storage.Observed[i]
		if d.SerialNumber != "" && d.SerialNumber == c.Spec.Serial {
			matches = append(matches, d)
		}
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("serial %q matches %d disks on machine %q; refusing an ambiguous claim",
			c.Spec.Serial, len(matches), m.Name)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no disk with serial %q is reported on machine %q", c.Spec.Serial, m.Name)
	}
	d := matches[0]
	if d.Path != c.Spec.ByIDPath {
		return nil, fmt.Errorf("serial %q is at %s on %s, not at the requested %s",
			c.Spec.Serial, d.Path, m.Name, c.Spec.ByIDPath)
	}
	// GUARD 3, the occupancy half: fail closed on anything but free.
	if d.Occupancy != "free" {
		return nil, fmt.Errorf("disk %s is %s; a claim destroys data and only proceeds on a free disk", d.Path, d.Occupancy)
	}
	return d, nil
}

func (r *FrameDiskClaimReconciler) setPhase(ctx context.Context, c *framev1beta1.FrameDiskClaim, phase, claimUID, msg string) error {
	base := c.DeepCopy()
	now := metav1.Now()
	c.Status.Phase = phase
	c.Status.PhaseSince = &now
	c.Status.ClaimUID = claimUID
	c.Status.Message = msg
	return r.Status().Patch(ctx, c, client.MergeFrom(base))
}

func (r *FrameDiskClaimReconciler) fail(ctx context.Context, c *framev1beta1.FrameDiskClaim, msg string) error {
	return r.setPhase(ctx, c, "Failed", c.Status.ClaimUID, msg)
}

func (r *FrameDiskClaimReconciler) succeed(ctx context.Context, c *framev1beta1.FrameDiskClaim, claimUID, msg string) error {
	return r.setPhase(ctx, c, "Ready", claimUID, msg)
}

func (r *FrameDiskClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.FrameDiskClaim{}).
		Named("framediskclaim").
		Complete(r)
}
