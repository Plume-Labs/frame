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
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type fakeCluster struct {
	calls     []string
	image     map[string]string
	failReady map[string]string // component -> image on which it never becomes ready
	failSet   map[string]bool
}

func (f *fakeCluster) SetImage(_ context.Context, comp, image string) error {
	f.calls = append(f.calls, "set "+comp+" "+image)
	if f.failSet[comp+image] {
		return errors.New("patch refused")
	}
	f.image[comp] = image
	return nil
}

func (f *fakeCluster) WaitReady(_ context.Context, comp string) error {
	f.calls = append(f.calls, "wait "+comp)
	if f.failReady[comp] == f.image[comp] {
		return fmt.Errorf("%s crash-looping", comp)
	}
	return nil
}

type fakeJournal struct {
	states []State
	failAt int // 1-based call that fails, 0 = never
}

func (j *fakeJournal) Record(_ context.Context, s State) error {
	j.states = append(j.states, s)
	if j.failAt != 0 && len(j.states) == j.failAt {
		return errors.New("api down")
	}
	return nil
}

func twoStepPlan() Plan {
	return Plan{Target: "1.2.0", Steps: []Step{
		{Component: "controller", From: "c1", To: "c2"},
		{Component: "ui", From: "u1", To: "u2"},
	}}
}

func newFake() *fakeCluster {
	return &fakeCluster{image: map[string]string{"controller": "c1", "ui": "u1"}, failReady: map[string]string{}, failSet: map[string]bool{}}
}

func TestExecuteSucceeds(t *testing.T) {
	c, j := newFake(), &fakeJournal{}
	st := Execute(context.Background(), twoStepPlan(), c, j, nil)
	if st.Phase != PhaseSucceeded || !reflect.DeepEqual(st.Applied, []string{"controller", "ui"}) {
		t.Fatalf("%+v", st)
	}
	want := []string{"set controller c2", "wait controller", "set ui u2", "wait ui"}
	if !reflect.DeepEqual(c.calls, want) {
		t.Fatalf("calls %v", c.calls)
	}
}

func TestFailedHealthGateRevertsEverythingInReverse(t *testing.T) {
	c, j := newFake(), &fakeJournal{}
	c.failReady["ui"] = "u2"
	st := Execute(context.Background(), twoStepPlan(), c, j, nil)
	if st.Phase != PhaseRolledBack {
		t.Fatalf("%+v", st)
	}
	if c.image["controller"] != "c1" || c.image["ui"] != "u1" {
		t.Fatalf("not reverted: %v", c.image)
	}
	// the failing step is reverted first, then the finished ones last to first
	want := []string{"set controller c2", "wait controller", "set ui u2", "wait ui", "set ui u1", "wait ui", "set controller c1", "wait controller"}
	if !reflect.DeepEqual(c.calls, want) {
		t.Fatalf("calls %v", c.calls)
	}
}

func TestFailedPatchAlsoReverts(t *testing.T) {
	c, j := newFake(), &fakeJournal{}
	c.failSet["controller"+"c2"] = true
	st := Execute(context.Background(), twoStepPlan(), c, j, nil)
	if st.Phase != PhaseRolledBack || c.image["controller"] != "c1" {
		t.Fatalf("%+v %v", st, c.image)
	}
}

func TestRevertThatFailsNeedsAHuman(t *testing.T) {
	c, j := newFake(), &fakeJournal{}
	c.failReady["ui"] = "u2"
	c.failReady["controller"] = "c1" // the old controller does not come back
	st := Execute(context.Background(), twoStepPlan(), c, j, nil)
	if st.Phase != PhaseFailed || st.Message == "" {
		t.Fatalf("%+v", st)
	}
}

func TestRollbackPlanNeverRevertsItself(t *testing.T) {
	c, j := newFake(), &fakeJournal{}
	p := twoStepPlan()
	p.Rollback = true
	c.failReady["ui"] = "u2"
	st := Execute(context.Background(), p, c, j, nil)
	if st.Phase != PhaseFailed {
		t.Fatalf("%+v", st)
	}
	if c.image["controller"] != "c2" {
		t.Fatalf("a failed rollback must not flip back to the version it left: %v", c.image)
	}
}

func TestResumeSkipsWhatIsDone(t *testing.T) {
	c, j := newFake(), &fakeJournal{}
	c.image["controller"] = "c2"
	st := Execute(context.Background(), twoStepPlan(), c, j, &State{Applied: []string{"controller"}})
	if st.Phase != PhaseSucceeded {
		t.Fatalf("%+v", st)
	}
	if want := []string{"set ui u2", "wait ui"}; !reflect.DeepEqual(c.calls, want) {
		t.Fatalf("calls %v", c.calls)
	}
}

func TestNoJournalNoStep(t *testing.T) {
	c, j := newFake(), &fakeJournal{failAt: 1}
	st := Execute(context.Background(), twoStepPlan(), c, j, nil)
	if st.Phase != PhaseFailed || len(c.calls) != 0 {
		t.Fatalf("a step started without a journal: %+v %v", st, c.calls)
	}
}

func TestRevertSurvivesACancelledContext(t *testing.T) {
	c, j := newFake(), &fakeJournal{}
	c.failReady["ui"] = "u2"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cc := &cancelOnUI{fakeCluster: c, cancel: cancel}
	st := Execute(ctx, twoStepPlan(), cc, j, nil)
	if st.Phase != PhaseRolledBack || c.image["ui"] != "u1" || c.image["controller"] != "c1" {
		t.Fatalf("%+v %v", st, c.image)
	}
}

// cancelOnUI cancels the caller's context as soon as the ui is patched, the way
// a closed browser tab or a shutting-down pod would.
type cancelOnUI struct {
	*fakeCluster
	cancel func()
}

func (c *cancelOnUI) SetImage(ctx context.Context, comp, image string) error {
	if comp == "ui" && image == "u2" {
		c.cancel()
	}
	return c.fakeCluster.SetImage(ctx, comp, image)
}
