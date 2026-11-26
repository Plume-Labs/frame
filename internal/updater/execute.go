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
)

// Cluster is the only way the executor touches the world. The real
// implementation (P4b) patches the named workload's image and watches its
// rollout; tests use a fake. Both calls must be idempotent: after a restart of
// the updater the same step may be asked again.
type Cluster interface {
	// SetImage points the component at an image pinned by digest.
	SetImage(ctx context.Context, component, image string) error
	// WaitReady returns nil once the component is healthy on that image, or an
	// error once it clearly will not be (the health gate, spec §3.4).
	WaitReady(ctx context.Context, component string) error
}

// Journal records progress where a restarted updater can read it (the
// FrameUpdate status). Record is called before a step starts and after it
// ends, so the journal never claims more than was done.
type Journal interface {
	Record(ctx context.Context, s State) error
}

// Execution phases written to the journal.
const (
	PhaseApplying    = "Applying"
	PhaseSucceeded   = "Succeeded"
	PhaseRollingBack = "RollingBack"
	PhaseRolledBack  = "RolledBack"
	PhaseFailed      = "Failed"
)

// State is the journalled progress of one update.
type State struct {
	Phase string
	// Step is the component in progress, "" when none.
	Step string
	// Applied lists components whose step finished and passed its health gate,
	// in apply order. On resume these are skipped.
	Applied []string
	Message string
}

// Execute runs a plan step by step. It returns the final state; a failure is a
// state, not an error. When a step fails its health gate, every step applied so
// far, and the failing one, is put back to its previous image in reverse order.
// A plan that is itself a rollback never reverts: it stops as Failed, so that
// two versions cannot chase each other.
//
// resume, when non-nil, is the journal left by a previous run of the same plan:
// components already Applied are not touched again.
func Execute(ctx context.Context, p Plan, c Cluster, j Journal, resume *State) State {
	st := State{Phase: PhaseApplying}
	if resume != nil {
		st.Applied = append(st.Applied, resume.Applied...)
	}
	done := map[string]bool{}
	for _, a := range st.Applied {
		done[a] = true
	}

	for _, step := range p.Steps {
		if done[step.Component] {
			continue
		}
		st.Step = step.Component
		if err := j.Record(ctx, st); err != nil {
			// Without a journal a restart cannot resume: do not start a step
			// nobody could account for, and put back what was already done.
			if ctx.Err() != nil {
				return interrupted(st)
			}
			if len(st.Applied) > 0 && !p.Rollback {
				return failStep(ctx, p, c, j, st, step, fmt.Errorf("cannot record progress: %w", err))
			}
			st.Phase, st.Step = PhaseFailed, ""
			st.Message = fmt.Sprintf("cannot record progress before %s: %v", step.Component, err)
			return st
		}
		err := c.SetImage(ctx, step.Component, step.To)
		if err == nil {
			err = c.WaitReady(ctx, step.Component)
		}
		if err != nil {
			if ctx.Err() != nil {
				return interrupted(st)
			}
			return failStep(ctx, p, c, j, st, step, err)
		}
		st.Applied = append(st.Applied, step.Component)
		st.Step = ""
		_ = j.Record(ctx, st) // best effort: the next Record before a step, or the final one, is checked
	}

	st.Phase, st.Step = PhaseSucceeded, ""
	_ = j.Record(ctx, st)
	return st
}

// interrupted is the outcome when the caller's context ended mid-plan (the
// updater is being stopped): nothing is reverted, the journal still says
// Applying, and the next run of the updater resumes from it. A shutdown must
// not undo half an update only to start it again.
func interrupted(st State) State {
	st.Phase = PhaseApplying
	st.Message = "interrupted: the update resumes when the updater is back"
	return st
}

func failStep(ctx context.Context, p Plan, c Cluster, j Journal, st State, failed Step, cause error) State {
	if p.Rollback {
		st.Phase, st.Step = PhaseFailed, ""
		st.Message = fmt.Sprintf("%s did not become ready on %s: %v", failed.Component, failed.To, cause)
		_ = j.Record(context.WithoutCancel(ctx), st)
		return st
	}

	// The revert must run even if the caller's context was cancelled: leaving a
	// half-updated platform because someone closed a browser tab is worse than
	// the revert taking a few more seconds.
	rctx := context.WithoutCancel(ctx)
	st.Phase, st.Step = PhaseRollingBack, failed.Component
	st.Message = fmt.Sprintf("%s did not become ready on %s: %v", failed.Component, failed.To, cause)
	_ = j.Record(rctx, st)

	// Failing step first (it was set), then the finished ones, last to first.
	revert := []Step{failed}
	applied := map[string]bool{}
	for _, a := range st.Applied {
		applied[a] = true
	}
	for i := len(p.Steps) - 1; i >= 0; i-- {
		if s := p.Steps[i]; applied[s.Component] {
			revert = append(revert, s)
		}
	}
	for _, s := range revert {
		st.Step = s.Component
		err := c.SetImage(rctx, s.Component, s.From)
		if err == nil {
			err = c.WaitReady(rctx, s.Component)
		}
		if err != nil {
			st.Phase, st.Step = PhaseFailed, ""
			st.Message += fmt.Sprintf("; the revert of %s failed too: %v: manual intervention needed", s.Component, err)
			_ = j.Record(rctx, st)
			return st
		}
	}
	st.Phase, st.Step, st.Applied = PhaseRolledBack, "", nil
	_ = j.Record(rctx, st)
	return st
}
