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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// 03:00 in Paris on a Sunday: inside a 02:00 + 4 h window.
var scrubNow = time.Date(2026, 11, 8, 2, 0, 0, 0, time.UTC)

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func schedule() *framev1beta1.ZFSScrubSchedule {
	return &framev1beta1.ZFSScrubSchedule{
		NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"zfs": "yes"}},
		IntervalDays: 30, WindowStart: "02:00", WindowHours: 4, TimeZone: "Europe/Paris",
	}
}

func pool(scan string) *framev1beta1.ZFSStatus {
	readAt := metav1.NewTime(scrubNow.Add(-time.Minute))
	return &framev1beta1.ZFSStatus{
		State: framev1beta1.StackPresent, ReadAt: &readAt,
		Pools: []framev1beta1.ZPool{{Name: "rpool", Health: "ONLINE", Scan: scan}},
	}
}

func zfsNode(name string, ann map[string]string, zfs *framev1beta1.ZFSStatus) scrubNode {
	return scrubNode{Name: name, Labels: map[string]string{"zfs": "yes"}, Annotations: ann, ZFS: zfs}
}

func onlyStepOf(t *testing.T) func([]scrubStep, error) scrubStep {
	return func(steps []scrubStep, err error) scrubStep {
		t.Helper()
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if len(steps) != 1 {
			t.Fatalf("steps = %+v, want one", steps)
		}
		return steps[0]
	}
}

func TestPlanScrubsRequestsTheMostOverdueNodeOnly(t *testing.T) {
	steps, err := planScrubs(schedule(), []scrubNode{
		zfsNode("a", map[string]string{framev1beta1.ZFSScrubCompletedAnnotation: stamp(scrubNow.Add(-40 * 24 * time.Hour))}, pool("none requested")),
		zfsNode("b", nil, pool("none requested")), // never scrubbed by Frame
		zfsNode("c", map[string]string{framev1beta1.ZFSScrubCompletedAnnotation: stamp(scrubNow.Add(-5 * 24 * time.Hour))}, pool("none requested")),
	}, scrubNow)
	s := onlyStepOf(t)(steps, err)
	if s.Node != "b" || s.Set[framev1beta1.ZFSScrubRequestedAnnotation] != stamp(scrubNow) {
		t.Fatalf("step = %+v, want a request on b", s)
	}
}

func TestPlanScrubsStartsNothingBesideAScrubInProgress(t *testing.T) {
	for name, nodes := range map[string][]scrubNode{
		"another node scrubbing (its cron)": {
			zfsNode("a", nil, pool("none requested")),
			{Name: "x", ZFS: pool("scrub in progress since Sun Nov  8 01:00:00 2026")},
		},
		"a resilver": {
			zfsNode("a", nil, pool("resilver in progress since Sun Nov  8 01:00:00 2026")),
		},
		"a node whose ZFS read failed": {
			zfsNode("a", nil, pool("none requested")),
			{Name: "x", ZFS: &framev1beta1.ZFSStatus{State: framev1beta1.StackError}},
		},
		"a request not yet taken": {
			zfsNode("a", nil, pool("none requested")),
			zfsNode("b", map[string]string{framev1beta1.ZFSScrubRequestedAnnotation: stamp(scrubNow.Add(-time.Minute))}, pool("none requested")),
		},
	} {
		steps, err := planScrubs(schedule(), nodes, scrubNow)
		if err != nil || len(steps) != 0 {
			t.Errorf("%s: steps %+v, err %v; want none", name, steps, err)
		}
	}
}

func TestPlanScrubsHonoursWindowSelectorAndEligibility(t *testing.T) {
	cases := map[string]struct {
		s     *framev1beta1.ZFSScrubSchedule
		nodes []scrubNode
		now   time.Time
	}{
		"outside the window": {schedule(), []scrubNode{zfsNode("a", nil, pool(""))}, scrubNow.Add(6 * time.Hour)},
		"no schedule":        {nil, []scrubNode{zfsNode("a", nil, pool(""))}, scrubNow},
		"nil selector":       {&framev1beta1.ZFSScrubSchedule{WindowHours: 24}, []scrubNode{zfsNode("a", nil, pool(""))}, scrubNow},
		"selector misses":    {schedule(), []scrubNode{{Name: "a", ZFS: pool("")}}, scrubNow},
		"no ZFS read":        {schedule(), []scrubNode{zfsNode("a", nil, nil)}, scrubNow},
		"ZFS absent":         {schedule(), []scrubNode{zfsNode("a", nil, &framev1beta1.ZFSStatus{State: framev1beta1.StackAbsent})}, scrubNow},
		"failed an hour ago": {schedule(), []scrubNode{zfsNode("a", map[string]string{framev1beta1.ZFSScrubFailedAnnotation: stamp(scrubNow.Add(-time.Hour))}, pool(""))}, scrubNow},
	}
	for name, tc := range cases {
		steps, err := planScrubs(tc.s, tc.nodes, tc.now)
		if err != nil || len(steps) != 0 {
			t.Errorf("%s: steps %+v, err %v; want none", name, steps, err)
		}
	}
}

func TestInScrubWindowCrossesMidnightInTheZone(t *testing.T) {
	s := &framev1beta1.ZFSScrubSchedule{WindowStart: "23:00", WindowHours: 3, TimeZone: "Europe/Paris"}
	for at, want := range map[string]bool{
		"2026-11-07T21:59:00Z": false, // 22:59 Paris
		"2026-11-07T22:00:00Z": true,  // 23:00
		"2026-11-08T00:59:00Z": true,  // 01:59
		"2026-11-08T01:00:00Z": false, // 02:00
	} {
		now, _ := time.Parse(time.RFC3339, at)
		if got, err := inScrubWindow(s, now); err != nil || got != want {
			t.Errorf("%s: %v, %v; want %v", at, got, err, want)
		}
	}
	if _, err := inScrubWindow(&framev1beta1.ZFSScrubSchedule{TimeZone: "Mars/Olympus"}, scrubNow); err == nil {
		t.Error("an unknown zone was accepted")
	}
}

func TestPlanScrubsConcludesARequest(t *testing.T) {
	req := stamp(scrubNow.Add(-2 * time.Hour))
	started := scrubNow.Add(-2*time.Hour + 30*time.Second)
	acked := map[string]string{
		framev1beta1.ZFSScrubRequestedAnnotation:    req,
		framev1beta1.ZFSScrubAcknowledgedAnnotation: req,
		framev1beta1.ZFSScrubStartedAtAnnotation:    stamp(started),
	}

	t.Run("finished", func(t *testing.T) {
		s := onlyStepOf(t)(planScrubs(schedule(), []scrubNode{zfsNode("a", acked, pool("scrub repaired 0B in 01:10:00 with 0 errors on Sun Nov  8 02:40:00 2026"))}, scrubNow))
		if s.Reason != "ScrubCompleted" || s.Set[framev1beta1.ZFSScrubCompletedAnnotation] == "" || len(s.Remove) != 3 {
			t.Fatalf("step = %+v", s)
		}
	})
	t.Run("stopped or paused is a failure, not a completion", func(t *testing.T) {
		for _, scan := range []string{"scrub canceled on Sun Nov  8 02:30:00 2026", "scrub paused since Sun Nov  8 02:30:00 2026", "none requested"} {
			s := onlyStepOf(t)(planScrubs(schedule(), []scrubNode{zfsNode("a", acked, pool(scan))}, scrubNow))
			if s.Reason != reasonScrubFailed || s.Set[framev1beta1.ZFSScrubCompletedAnnotation] != "" {
				t.Errorf("%q: step = %+v, want a failure", scan, s)
			}
		}
	})
	t.Run("still scrubbing holds the lock", func(t *testing.T) {
		steps, err := planScrubs(schedule(), []scrubNode{
			zfsNode("a", acked, pool("scrub in progress since …")),
			zfsNode("b", nil, pool("none requested")),
		}, scrubNow)
		if err != nil || len(steps) != 0 {
			t.Fatalf("steps %+v, err %v", steps, err)
		}
	})
	t.Run("a read older than the start proves nothing", func(t *testing.T) {
		z := pool("none requested")
		old := metav1.NewTime(started.Add(-time.Second))
		z.ReadAt = &old
		steps, err := planScrubs(schedule(), []scrubNode{zfsNode("a", acked, z)}, started.Add(time.Minute))
		if err != nil || len(steps) != 0 {
			t.Fatalf("steps %+v, err %v", steps, err)
		}
	})
	t.Run("a failed read proves nothing", func(t *testing.T) {
		z := pool("")
		z.State, z.Pools = framev1beta1.StackError, nil
		steps, err := planScrubs(schedule(), []scrubNode{zfsNode("a", acked, z)}, started.Add(time.Minute))
		if err != nil || len(steps) != 0 {
			t.Fatalf("steps %+v, err %v", steps, err)
		}
	})
	t.Run("no read for too long fails it", func(t *testing.T) {
		z := pool("none requested")
		old := metav1.NewTime(started.Add(-time.Second))
		z.ReadAt = &old
		s := onlyStepOf(t)(planScrubs(schedule(), []scrubNode{zfsNode("a", acked, z)}, scrubNow.Add(-2*time.Hour+zfsScrubAckTimeout+zfsScrubReadTimeout+time.Minute)))
		if s.Reason != "ScrubFailed" {
			t.Fatalf("step = %+v", s)
		}
	})
	t.Run("never acknowledged fails it", func(t *testing.T) {
		ann := map[string]string{framev1beta1.ZFSScrubRequestedAnnotation: stamp(scrubNow.Add(-zfsScrubAckTimeout - time.Minute))}
		s := onlyStepOf(t)(planScrubs(schedule(), []scrubNode{zfsNode("a", ann, pool(""))}, scrubNow))
		if s.Reason != "ScrubFailed" || s.Set[framev1beta1.ZFSScrubFailedAnnotation] == "" {
			t.Fatalf("step = %+v", s)
		}
	})
}

func scrubScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := framev1beta1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// The reconciler reads the machine's storage through spec.nodeRef and writes
// the request on the Node.
func TestZFSScrubReconcilerWritesTheRequestOnTheNode(t *testing.T) {
	m := &framev1beta1.FrameMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "m1", Namespace: "default"},
		Spec:       framev1beta1.FrameMachineSpec{NodeRef: "n1"},
		Status: framev1beta1.FrameMachineStatus{Storage: &framev1beta1.MachineStorage{
			Local: &framev1beta1.LocalStorage{ZFS: *pool("none requested")},
		}},
	}
	c := fake.NewClientBuilder().WithScheme(scrubScheme(t)).WithObjects(
		&framev1beta1.StorageHealthPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: framev1beta1.StorageHealthPolicyName},
			Spec:       framev1beta1.StorageHealthPolicySpec{Scrub: schedule()},
		},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{"zfs": "yes"}}},
		m,
	).Build()
	r := &ZFSScrubReconciler{Client: c, Recorder: record.NewFakeRecorder(8), Now: func() time.Time { return scrubNow }}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: framev1beta1.StorageHealthPolicyName}})
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != zfsScrubPoll {
		t.Errorf("requeue %v, want %v", res.RequeueAfter, zfsScrubPoll)
	}
	var n corev1.Node
	if err := c.Get(context.Background(), client.ObjectKey{Name: "n1"}, &n); err != nil {
		t.Fatal(err)
	}
	if n.Annotations[framev1beta1.ZFSScrubRequestedAnnotation] != stamp(scrubNow) {
		t.Fatalf("annotations = %v", n.Annotations)
	}
}

// A failed request releases the request, not the pools: the scrub may be
// running all the same. No other node is asked until the failed one shows,
// in a read dated after the failure's, that nothing is scanning.
func TestPlanScrubsKeepsTheLockAfterAFailureUntilAFreshRead(t *testing.T) {
	ann := map[string]string{framev1beta1.ZFSScrubRequestedAnnotation: stamp(scrubNow.Add(-zfsScrubAckTimeout - time.Minute))}
	stale := pool("none requested") // read before the agent started anything
	s := onlyStepOf(t)(planScrubs(schedule(), []scrubNode{zfsNode("a", ann, stale), zfsNode("b", nil, pool("none requested"))}, scrubNow))
	if s.Node != "a" || s.Reason != reasonScrubFailed || s.Set[framev1beta1.ZFSScrubUnsettledAnnotation] != stamp(stale.ReadAt.Time) {
		t.Fatalf("step = %+v, want a failure on a recording the read it saw", s)
	}

	failed := map[string]string{
		framev1beta1.ZFSScrubFailedAnnotation:    stamp(scrubNow),
		framev1beta1.ZFSScrubUnsettledAnnotation: s.Set[framev1beta1.ZFSScrubUnsettledAnnotation],
	}
	b := zfsNode("b", nil, pool("none requested"))
	later := scrubNow.Add(5 * time.Minute)

	// The same read, or a read showing the scrub after all: still locked.
	for name, z := range map[string]*framev1beta1.ZFSStatus{"same read": stale, "no read": nil, "scrubbing": pool("scrub in progress since …")} {
		if z != nil && name == "scrubbing" {
			at := metav1.NewTime(later)
			z.ReadAt = &at
		}
		steps, err := planScrubs(schedule(), []scrubNode{zfsNode("a", failed, z), b}, later)
		if err != nil || len(steps) != 0 {
			t.Errorf("%s: steps %+v, err %v; want the lock held", name, steps, err)
		}
	}

	// A fresh read with nothing scanning settles it; the next pass asks b.
	fresh := pool("scrub repaired 0B in 00:01:00 with 0 errors on …")
	at := metav1.NewTime(later)
	fresh.ReadAt = &at
	s = onlyStepOf(t)(planScrubs(schedule(), []scrubNode{zfsNode("a", failed, fresh), b}, later))
	if s.Node != "a" || s.Reason != reasonScrubSettled || s.Remove[0] != framev1beta1.ZFSScrubUnsettledAnnotation {
		t.Fatalf("step = %+v, want a settled", s)
	}
	delete(failed, framev1beta1.ZFSScrubUnsettledAnnotation)
	s = onlyStepOf(t)(planScrubs(schedule(), []scrubNode{zfsNode("a", failed, fresh), b}, later))
	if s.Node != "b" || s.Reason != reasonScrubRequested {
		t.Fatalf("step = %+v, want b requested", s)
	}
}

// A node that stopped reporting before any request (powered off, agent
// down, its FrameMachine still holding the last read) is not asked: the
// request would fail with nobody to read the outcome, and the lock that
// failure takes would stop the whole plan until the node came back.
func TestPlanScrubsSkipsANodeThatStoppedReporting(t *testing.T) {
	silent := pool("none requested")
	old := metav1.NewTime(scrubNow.Add(-3 * 24 * time.Hour))
	silent.ReadAt = &old
	capped := pool("none requested")
	capped.PoolsOmitted = 2
	unread := pool("none requested")
	unread.ReadAt = nil
	done := map[string]string{framev1beta1.ZFSScrubCompletedAnnotation: stamp(scrubNow.Add(-40 * 24 * time.Hour))}
	s := onlyStepOf(t)(planScrubs(schedule(), []scrubNode{
		zfsNode("a", nil, silent), // never scrubbed: would come first
		zfsNode("c", nil, capped), // pools left out of the read
		zfsNode("d", nil, unread),
		zfsNode("b", done, pool("none requested")),
	}, scrubNow))
	if s.Node != "b" || s.Reason != reasonScrubRequested {
		t.Fatalf("step = %+v, want b requested", s)
	}
}

// The failure's lock: a failure with no read at all records "none", which
// holds until a read arrives; a read dated before the one recorded (the
// agent's clock set back) is still a new read and releases it.
func TestPlanScrubsSettlesOnAnyNewRead(t *testing.T) {
	b := zfsNode("b", nil, pool("none requested"))
	ann := map[string]string{framev1beta1.ZFSScrubRequestedAnnotation: stamp(scrubNow.Add(-zfsScrubAckTimeout - time.Minute))}
	s := onlyStepOf(t)(planScrubs(schedule(), []scrubNode{zfsNode("a", ann, nil), b}, scrubNow))
	if s.Node != "a" || s.Reason != reasonScrubFailed || s.Set[framev1beta1.ZFSScrubUnsettledAnnotation] != "none" {
		t.Fatalf("step = %+v, want a failure recording no read", s)
	}
	none := map[string]string{
		framev1beta1.ZFSScrubFailedAnnotation:    stamp(scrubNow),
		framev1beta1.ZFSScrubUnsettledAnnotation: "none",
	}
	if steps, err := planScrubs(schedule(), []scrubNode{zfsNode("a", none, nil), b}, scrubNow); err != nil || len(steps) != 0 {
		t.Fatalf("no read yet: steps %+v, err %v; want the lock held", steps, err)
	}
	s = onlyStepOf(t)(planScrubs(schedule(), []scrubNode{zfsNode("a", none, pool("none requested")), b}, scrubNow))
	if s.Node != "a" || s.Reason != reasonScrubSettled {
		t.Fatalf("first read: step = %+v, want a settled", s)
	}

	back := map[string]string{
		framev1beta1.ZFSScrubFailedAnnotation:    stamp(scrubNow),
		framev1beta1.ZFSScrubUnsettledAnnotation: stamp(scrubNow.Add(time.Hour)), // recorded ahead
	}
	s = onlyStepOf(t)(planScrubs(schedule(), []scrubNode{zfsNode("a", back, pool("none requested")), b}, scrubNow))
	if s.Node != "a" || s.Reason != reasonScrubSettled {
		t.Fatalf("clock set back: step = %+v, want a settled", s)
	}
}
