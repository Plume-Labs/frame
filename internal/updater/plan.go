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

import "fmt"

// Refusal codes. The console shows the code's message; tests pin the codes.
const (
	RefusedUnknownRelease  = "UnknownRelease"
	RefusedUnverified      = "Unverified"
	RefusedRevoked         = "Revoked"
	RefusedBelowMinFrom    = "BelowMinFrom"
	RefusedAlreadyCurrent  = "AlreadyCurrent"
	RefusedDowngrade       = "DowngradeNotAllowed"
	RefusedNoPrevious      = "NoPreviousVersion"
	RefusedRollbackUnsafe  = "RollbackNotSafe"
	RefusedOSDReplacement  = "OSDReplacementInProgress"
	RefusedInFlight        = "WorkInFlight"
	RefusedNothingToChange = "NothingToChange"
	RefusedUnknownCurrent  = "UnknownCurrentVersion"
)

// Refusal is why an update cannot start. A refusal is a normal outcome, not an
// error: the request was understood and declined.
type Refusal struct {
	Code    string
	Message string
	// Overridable is true when Force would lift it. A signature, revocation or
	// OSD-replacement refusal is never overridable.
	Overridable bool
}

// Activity is what is going on in the cluster at the moment of the decision.
// It is the same set the Updates screen already shows (src/lib/updates.ts).
type Activity struct {
	RunningJobs           int
	InstallsInProgress    int
	OSDReplacementsActive int
	UnreadyComponents     int
}

// Running is one workload the update would touch, as the cluster reports it.
type Running struct {
	Component string // controller, ui, ...
	Image     string // reference as running
}

// Request is the decision input from a FrameUpdate's spec.
type Request struct {
	TargetVersion string
	Rollback      bool
	Force         bool
}

// Input is everything BuildPlan reads.
type Input struct {
	Request  Request
	Current  string // version running now
	Previous string // version before the last successful update, "" if none
	Running  []Running
	Releases map[string]Release // by version
	Activity Activity
}

// Step is one component's change.
type Step struct {
	Component string
	From      string
	To        string
}

// Plan is the outcome: either the ordered steps, or why not.
type Plan struct {
	Target string
	// Rollback is true when the plan returns to the previous version: a failure
	// then stops instead of reverting, so two versions never chase each other.
	Rollback bool
	Steps    []Step
	Refusal  *Refusal
}

func refuse(target, code string, overridable bool, format string, a ...any) Plan {
	return Plan{Target: target, Refusal: &Refusal{Code: code, Message: fmt.Sprintf(format, a...), Overridable: overridable}}
}

// BuildPlan decides whether the request may start and what it would change.
// Order matters: everything that no flag can lift (the release itself, its
// signature, the version rules, an OSD replacement) is checked before the
// things Force may lift (work in flight), so that Force can never reach past
// a hard refusal.
func BuildPlan(in Input) Plan {
	target := in.Request.TargetVersion
	if in.Request.Rollback {
		if in.Previous == "" {
			return refuse("", RefusedNoPrevious, false, "there is no earlier version to return to")
		}
		target = in.Previous
	}

	rel, ok := in.Releases[target]
	if !ok {
		return refuse(target, RefusedUnknownRelease, false, "version %s is not a release this install knows", target)
	}
	if !rel.Verified {
		return refuse(target, RefusedUnverified, false, "version %s has no verified signature", target)
	}
	if rel.Revoked {
		return refuse(target, RefusedRevoked, false, "version %s was withdrawn by the release channel", target)
	}

	if in.Current == "" {
		return refuse(target, RefusedUnknownCurrent, false, "the running version cannot be determined")
	}
	cmp, err := CompareVersions(target, in.Current)
	if err != nil {
		return refuse(target, RefusedUnknownCurrent, false, "cannot compare %s with %s: %v", target, in.Current, err)
	}
	switch {
	case cmp == 0:
		return refuse(target, RefusedAlreadyCurrent, false, "already running version %s", target)
	case cmp < 0 && !in.Request.Rollback:
		return refuse(target, RefusedDowngrade, false, "version %s is older than the running %s: use the rollback", target, in.Current)
	}

	if in.Request.Rollback {
		cur, ok := in.Releases[in.Current]
		if !ok {
			return refuse(target, RefusedUnknownCurrent, false, "the running version %s is not a known release, so a rollback cannot be judged safe", in.Current)
		}
		if cur.RollbackSafeTo == "" {
			return refuse(target, RefusedRollbackUnsafe, false, "version %s does not offer a rollback", in.Current)
		}
		if c, err := CompareVersions(target, cur.RollbackSafeTo); err != nil || c < 0 {
			return refuse(target, RefusedRollbackUnsafe, false, "returning to %s would lose data: %s is the oldest safe version", target, cur.RollbackSafeTo)
		}
	} else if rel.MinFrom != "" {
		if c, err := CompareVersions(in.Current, rel.MinFrom); err != nil || c < 0 {
			return refuse(target, RefusedBelowMinFrom, false, "version %s can only be installed from %s or later (running %s)", target, rel.MinFrom, in.Current)
		}
	}

	if in.Activity.OSDReplacementsActive > 0 {
		return refuse(target, RefusedOSDReplacement, false, "an OSD replacement is in progress: wait for it to finish")
	}
	if !in.Request.Force {
		if n := in.Activity.RunningJobs + in.Activity.InstallsInProgress + in.Activity.UnreadyComponents; n > 0 {
			return refuse(target, RefusedInFlight, true,
				"work is in flight (%d jobs, %d installs, %d components not ready)",
				in.Activity.RunningJobs, in.Activity.InstallsInProgress, in.Activity.UnreadyComponents)
		}
	}

	running := map[string]string{}
	for _, r := range in.Running {
		running[r.Component] = r.Image
	}
	var steps []Step
	for _, name := range sortedComponents(rel.Images) {
		from, present := running[name]
		if !present {
			// A component the release names but this install does not run (an
			// optional one): nothing to update, and the updater never creates.
			continue
		}
		if from == rel.Images[name] {
			continue
		}
		steps = append(steps, Step{Component: name, From: from, To: rel.Images[name]})
	}
	if len(steps) == 0 {
		return refuse(target, RefusedNothingToChange, false, "every component already runs the images of %s", target)
	}
	return Plan{Target: target, Rollback: in.Request.Rollback, Steps: steps}
}
