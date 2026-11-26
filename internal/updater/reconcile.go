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

package updater

import (
	"context"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// WorkCluster is what the reconciler needs of the cluster: the executor's two
// calls plus a read of what runs now.
type WorkCluster interface {
	Cluster
	Running(ctx context.Context) ([]Running, error)
	Unready(ctx context.Context) (int, error)
}

// Reconciler turns one FrameUpdate into an outcome written on its status. It
// trusts FrameRelease.status.verified and never sets it: the signature check
// belongs to the code that creates releases, and the RBAC keeps everything else
// from writing that field.
type Reconciler struct {
	Client    client.Client
	Cluster   WorkCluster
	Namespace string
	// Activity reports what the cluster is doing besides the update itself
	// (jobs, installs, OSD replacements). Unready components are added here.
	Activity func(ctx context.Context) (Activity, error)
	Now      func() time.Time
}

// Reconcile handles one request. A request that is already decided is left
// alone; one that waits behind another update returns a delay to retry after.
func (r *Reconciler) Reconcile(ctx context.Context, name string) (time.Duration, error) {
	var upd framev1beta1.FrameUpdate
	key := types.NamespacedName{Namespace: r.Namespace, Name: name}
	if err := r.Client.Get(ctx, key, &upd); err != nil {
		return 0, client.IgnoreNotFound(err)
	}
	switch upd.Status.Phase {
	case framev1beta1.UpdatePhaseSucceeded, framev1beta1.UpdatePhaseRolledBack,
		framev1beta1.UpdatePhaseRefused, framev1beta1.UpdatePhaseFailed:
		return 0, nil
	case framev1beta1.UpdatePhaseRollingBack:
		// A revert cut short by a restart cannot be resumed with certainty which
		// image each component holds: say so instead of guessing.
		return 0, r.finish(ctx, &upd, framev1beta1.UpdatePhaseFailed,
			"the updater stopped during a revert: check each component's image and ready state by hand")
	}

	all, err := r.list(ctx)
	if err != nil {
		return 0, err
	}
	resuming := upd.Status.Phase == framev1beta1.UpdatePhaseApplying
	for i := range all {
		o := &all[i]
		if o.Name == upd.Name {
			continue
		}
		if isActive(o.Status.Phase) {
			return 30 * time.Second, nil // one update at a time
		}
		// The oldest pending request goes first.
		if !resuming && isWaiting(o.Status.Phase) && older(o, &upd) {
			return 30 * time.Second, nil
		}
	}

	releases, err := r.releases(ctx)
	if err != nil {
		return 0, err
	}
	running, err := r.Cluster.Running(ctx)
	if err != nil {
		return 0, err
	}

	in := Input{
		Request:  Request{TargetVersion: upd.Spec.TargetVersion, Rollback: upd.Spec.Rollback, Force: upd.Spec.Force},
		Running:  running,
		Releases: releases,
	}
	var resume *State
	if resuming {
		// Our own earlier steps made some components differ and some unready:
		// plan from what ran before the update, and let the journal say what is
		// already done.
		from, ok := releases[upd.Status.FromVersion]
		if !ok {
			return 0, r.finish(ctx, &upd, framev1beta1.UpdatePhaseFailed,
				fmt.Sprintf("cannot resume: the starting version %q is not a known release", upd.Status.FromVersion))
		}
		in.Current = upd.Status.FromVersion
		in.Running = runningFrom(from, running)
		resume = &State{Applied: appliedFrom(&upd)}
		in.Previous = r.previousFor(all, in.Current)
	} else {
		in.Current = DetectCurrent(running, releases)
		in.Previous = r.previousFor(all, in.Current)
		act, err := r.activity(ctx)
		if err != nil {
			return 0, err
		}
		in.Activity = act
	}

	plan := BuildPlan(in)
	if plan.Refusal != nil {
		return 0, r.finish(ctx, &upd, framev1beta1.UpdatePhaseRefused,
			plan.Refusal.Code+": "+plan.Refusal.Message)
	}

	if !resuming {
		now := metav1.NewTime(r.now())
		upd.Status.Phase = framev1beta1.UpdatePhaseApplying
		upd.Status.FromVersion = in.Current
		upd.Status.StartedAt = &now
		upd.Status.Components = componentStatuses(plan, nil, "")
		if err := r.Client.Status().Update(ctx, &upd); err != nil {
			return 0, err
		}
	}

	j := &statusJournal{r: r, key: key, plan: plan}
	final := Execute(ctx, plan, r.Cluster, j, resume)
	return 0, r.finish(ctx, &upd, mapPhase(final.Phase), final.Message)
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) activity(ctx context.Context) (Activity, error) {
	var a Activity
	if r.Activity != nil {
		var err error
		if a, err = r.Activity(ctx); err != nil {
			return Activity{}, err
		}
	}
	n, err := r.Cluster.Unready(ctx)
	if err != nil {
		return Activity{}, err
	}
	a.UnreadyComponents += n
	return a, nil
}

func (r *Reconciler) list(ctx context.Context) ([]framev1beta1.FrameUpdate, error) {
	var l framev1beta1.FrameUpdateList
	if err := r.Client.List(ctx, &l, client.InNamespace(r.Namespace)); err != nil {
		return nil, err
	}
	sort.Slice(l.Items, func(i, j int) bool { return older(&l.Items[i], &l.Items[j]) })
	return l.Items, nil
}

func older(a, b *framev1beta1.FrameUpdate) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Name < b.Name
}

func isActive(p string) bool {
	return p == framev1beta1.UpdatePhaseVerifying || p == framev1beta1.UpdatePhaseApplying || p == framev1beta1.UpdatePhaseRollingBack
}

func isWaiting(p string) bool { return p == "" || p == framev1beta1.UpdatePhasePending }

func (r *Reconciler) releases(ctx context.Context) (map[string]Release, error) {
	var l framev1beta1.FrameReleaseList
	if err := r.Client.List(ctx, &l, client.InNamespace(r.Namespace)); err != nil {
		return nil, err
	}
	out := map[string]Release{}
	for _, fr := range l.Items {
		out[fr.Spec.Version] = Release{
			Version:        fr.Spec.Version,
			Images:         fr.Spec.Images,
			MinFrom:        fr.Spec.MinFrom,
			RollbackSafeTo: fr.Spec.RollbackSafeTo,
			Revoked:        fr.Spec.Revoked,
			Verified:       fr.Status.Verified,
		}
	}
	return out, nil
}

// previousFor is the version an install ran before it reached `current`: the
// start of the latest successful, non-rollback update that ended on it. After a
// rollback there is no earlier version to offer.
func (r *Reconciler) previousFor(all []framev1beta1.FrameUpdate, current string) string {
	for i := len(all) - 1; i >= 0; i-- {
		u := all[i]
		if u.Status.Phase != framev1beta1.UpdatePhaseSucceeded {
			continue
		}
		if u.Spec.Rollback {
			return ""
		}
		if u.Spec.TargetVersion == current {
			return u.Status.FromVersion
		}
		return ""
	}
	return ""
}

// DetectCurrent names the release whose images the running workloads carry. A
// release matches when none of its images is contradicted by the cluster and at
// least one is confirmed; if several match equally, nothing is guessed.
func DetectCurrent(running []Running, releases map[string]Release) string {
	img := map[string]string{}
	for _, r := range running {
		img[r.Component] = r.Image
	}
	best, bestN := "", 0
	tie := false
	for v, rel := range releases {
		n, ok := 0, true
		for c, want := range rel.Images {
			have, present := img[c]
			if !present {
				continue
			}
			if have != want {
				ok = false
				break
			}
			n++
		}
		if !ok || n == 0 {
			continue
		}
		switch {
		case n > bestN:
			best, bestN, tie = v, n, false
		case n == bestN:
			tie = true
		}
	}
	if tie {
		return ""
	}
	return best
}

// runningFrom is what ran before the update: the starting release's images for
// the components that run in this install.
func runningFrom(from Release, now []Running) []Running {
	var out []Running
	for _, r := range now {
		if img, ok := from.Images[r.Component]; ok {
			out = append(out, Running{Component: r.Component, Image: img})
		} else {
			out = append(out, r)
		}
	}
	return out
}

func appliedFrom(u *framev1beta1.FrameUpdate) []string {
	var out []string
	for _, c := range u.Status.Components {
		if c.State == "Ready" {
			out = append(out, c.Name)
		}
	}
	return out
}

func mapPhase(p string) string {
	switch p {
	case PhaseSucceeded:
		return framev1beta1.UpdatePhaseSucceeded
	case PhaseRolledBack:
		return framev1beta1.UpdatePhaseRolledBack
	}
	return framev1beta1.UpdatePhaseFailed
}

func componentStatuses(p Plan, applied []string, inStep string) []framev1beta1.UpdateComponentStatus {
	done := map[string]bool{}
	for _, a := range applied {
		done[a] = true
	}
	out := make([]framev1beta1.UpdateComponentStatus, 0, len(p.Steps))
	for _, s := range p.Steps {
		st := "Pending"
		switch {
		case done[s.Component]:
			st = "Ready"
		case s.Component == inStep:
			st = "Applying"
		}
		out = append(out, framev1beta1.UpdateComponentStatus{Name: s.Component, From: s.From, To: s.To, State: st})
	}
	return out
}

// finish writes the end of a request.
func (r *Reconciler) finish(ctx context.Context, upd *framev1beta1.FrameUpdate, phase, msg string) error {
	key := client.ObjectKeyFromObject(upd)
	var cur framev1beta1.FrameUpdate
	if err := r.Client.Get(ctx, key, &cur); err != nil {
		return err
	}
	now := metav1.NewTime(r.now())
	cur.Status.Phase = phase
	cur.Status.Step = ""
	cur.Status.Message = truncate(msg, 1024)
	cur.Status.FinishedAt = &now
	switch phase {
	case framev1beta1.UpdatePhaseSucceeded:
		setAll(cur.Status.Components, "Ready")
	case framev1beta1.UpdatePhaseRolledBack:
		setAll(cur.Status.Components, "RolledBack")
	}
	return r.Client.Status().Update(ctx, &cur)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// statusJournal writes the executor's progress on the FrameUpdate's status.
type statusJournal struct {
	r    *Reconciler
	key  types.NamespacedName
	plan Plan
}

func (j *statusJournal) Record(ctx context.Context, s State) error {
	var cur framev1beta1.FrameUpdate
	if err := j.r.Client.Get(ctx, j.key, &cur); err != nil {
		return err
	}
	switch s.Phase {
	case PhaseApplying:
		cur.Status.Phase = framev1beta1.UpdatePhaseApplying
	case PhaseRollingBack:
		cur.Status.Phase = framev1beta1.UpdatePhaseRollingBack
	}
	if s.Step != "" {
		cur.Status.Step = truncate("Applying "+s.Step, 200)
	} else {
		cur.Status.Step = ""
	}
	if s.Phase == PhaseApplying || s.Phase == PhaseSucceeded {
		cur.Status.Components = componentStatuses(j.plan, s.Applied, s.Step)
	}
	if s.Message != "" {
		cur.Status.Message = truncate(s.Message, 1024)
	}
	return j.r.Client.Status().Update(ctx, &cur)
}

func setAll(cs []framev1beta1.UpdateComponentStatus, state string) {
	for i := range cs {
		cs[i].State = state
	}
}
