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
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// scrubExec records every argv and answers from a table keyed by pool.
type scrubExec struct {
	calls  [][]string
	stderr map[string]string
	fail   map[string]error
}

func (s *scrubExec) Exec(argv []string) (string, string, error) {
	s.calls = append(s.calls, slices.Clone(argv))
	pool := argv[len(argv)-1]
	return "", s.stderr[pool], s.fail[pool]
}

func present(pools ...framev1beta1.ZPool) *framev1beta1.ZFSStatus {
	return &framev1beta1.ZFSStatus{State: framev1beta1.StackPresent, Pools: pools}
}

func TestScrubPoolsStartsEachHealthyPoolUnderTimeout(t *testing.T) {
	x := &scrubExec{}
	got, err := ScrubPools(x, present(
		framev1beta1.ZPool{Name: "rpool", Health: "ONLINE"},
		framev1beta1.ZPool{Name: "tank", Health: "DEGRADED"},
		framev1beta1.ZPool{Name: "dead", Health: "SUSPENDED"},
		framev1beta1.ZPool{Name: "gone", Health: "FAULTED"},
	))
	if err != nil {
		t.Fatalf("ScrubPools: %v", err)
	}
	if !slices.Equal(got, []string{"rpool", "tank"}) {
		t.Fatalf("started %v, want [rpool tank]", got)
	}
	want := append(slices.Clone(timeoutPrefix), "zpool", "scrub", "rpool")
	if len(x.calls) != 2 || !slices.Equal(x.calls[0], want) {
		t.Fatalf("calls = %q, want first %q", x.calls, want)
	}
}

// A name that could read as an option, or carry anything but a pool name,
// never reaches the executor — whatever a hostile host printed in zpool list.
func TestScrubPoolsRefusesANameThatIsNotAPool(t *testing.T) {
	for _, name := range []string{"-s", "--help", "tank pool", "tank;reboot", "", "1tank", "tank/ds", strings.Repeat("a", 129)} {
		x := &scrubExec{}
		_, err := ScrubPools(x, present(framev1beta1.ZPool{Name: name, Health: "ONLINE"}))
		if err == nil {
			t.Errorf("pool %q: no error", name)
		}
		if len(x.calls) != 0 {
			t.Errorf("pool %q reached the executor: %q", name, x.calls)
		}
	}
}

func TestExecScrubRefusesAnythingButTheStart(t *testing.T) {
	for _, argv := range [][]string{
		{"zpool", "scrub", "-s", "tank"},
		{"zpool", "scrub", "-p", "tank"},
		{"zpool", "destroy", "tank"},
		{"zpool", "scrub"},
		{"zpool", "scrub", "-s"},
	} {
		x := &scrubExec{}
		if _, err := execScrub(x, argv); err == nil || len(x.calls) != 0 {
			t.Errorf("%q: err=%v calls=%q, want a refusal before the executor", argv, err, x.calls)
		}
	}
}

func TestScrubPoolsTakesAScrubAlreadyRunningAsStarted(t *testing.T) {
	x := &scrubExec{
		stderr: map[string]string{"tank": "cannot scrub tank: currently scrubbing; use 'zpool scrub -s' to cancel the current scrub"},
		fail:   map[string]error{"tank": errors.New("exit status 1")},
	}
	got, err := ScrubPools(x, present(framev1beta1.ZPool{Name: "tank", Health: "ONLINE"}))
	if err != nil || !slices.Equal(got, []string{"tank"}) {
		t.Fatalf("got %v, %v; want [tank], nil", got, err)
	}
}

func TestScrubPoolsFailsWhenNothingCanScrub(t *testing.T) {
	cases := map[string]*framev1beta1.ZFSStatus{
		"unread":      nil,
		"absent":      {State: framev1beta1.StackAbsent},
		"busy":        {State: framev1beta1.StackBusy},
		"no pool":     present(),
		"all faulted": present(framev1beta1.ZPool{Name: "tank", Health: "FAULTED"}),
	}
	for name, zfs := range cases {
		x := &scrubExec{}
		if _, err := ScrubPools(x, zfs); err == nil {
			t.Errorf("%s: no error", name)
		}
		if len(x.calls) != 0 {
			t.Errorf("%s: executor called %q", name, x.calls)
		}
	}
}

// One pool failing fails the whole request, but the others still start: a
// partial scrub is reported, not hidden.
func TestScrubPoolsReportsAFailedStart(t *testing.T) {
	x := &scrubExec{
		stderr: map[string]string{"tank": "cannot scrub tank: pool I/O is currently suspended"},
		fail:   map[string]error{"tank": errors.New("exit status 1")},
	}
	got, err := ScrubPools(x, present(
		framev1beta1.ZPool{Name: "rpool", Health: "ONLINE"},
		framev1beta1.ZPool{Name: "tank", Health: "ONLINE"},
	))
	if err == nil || !strings.Contains(err.Error(), "suspended") {
		t.Fatalf("err = %v, want the suspended pool's reason", err)
	}
	if !slices.Equal(got, []string{"rpool"}) {
		t.Fatalf("started %v, want [rpool]", got)
	}
}

func scrubNode(t *testing.T, ann map[string]string) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(newDiskStatusScheme(t)).
		WithObjects(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Annotations: ann}}).Build()
}

func nodeAnnotations(t *testing.T, c client.Client) map[string]string {
	t.Helper()
	var n corev1.Node
	if err := c.Get(context.Background(), client.ObjectKey{Name: "n1"}, &n); err != nil {
		t.Fatal(err)
	}
	return n.Annotations
}

var scrubClock = func() time.Time { return time.Date(2026, 11, 8, 2, 0, 0, 0, time.UTC) }

func TestServiceScrubRequestStartsAndAcknowledgesOnce(t *testing.T) {
	const req = "2026-11-08T01:59:00Z"
	c := scrubNode(t, map[string]string{framev1beta1.ZFSScrubRequestedAnnotation: req})
	x := &scrubExec{}
	zfs := present(framev1beta1.ZPool{Name: "rpool", Health: "ONLINE"})
	if err := ServiceScrubRequest(context.Background(), c, "n1", zfs, x, scrubClock); err != nil {
		t.Fatal(err)
	}
	ann := nodeAnnotations(t, c)
	if ann[framev1beta1.ZFSScrubAcknowledgedAnnotation] != req {
		t.Fatalf("acknowledged = %q, want %q", ann[framev1beta1.ZFSScrubAcknowledgedAnnotation], req)
	}
	if ann[framev1beta1.ZFSScrubStartedAtAnnotation] != "2026-11-08T02:00:00Z" {
		t.Fatalf("started-at = %q", ann[framev1beta1.ZFSScrubStartedAtAnnotation])
	}
	// The same request again: nothing runs.
	if err := ServiceScrubRequest(context.Background(), c, "n1", zfs, x, scrubClock); err != nil {
		t.Fatal(err)
	}
	if len(x.calls) != 1 {
		t.Fatalf("zpool ran %d times, want once", len(x.calls))
	}
}

func TestServiceScrubRequestDoesNothingWithoutARequestOrARead(t *testing.T) {
	zfs := present(framev1beta1.ZPool{Name: "rpool", Health: "ONLINE"})
	for name, tc := range map[string]struct {
		ann map[string]string
		zfs *framev1beta1.ZFSStatus
	}{
		"no request":  {nil, zfs},
		"no read yet": {map[string]string{framev1beta1.ZFSScrubRequestedAnnotation: "t1"}, nil},
	} {
		c := scrubNode(t, tc.ann)
		x := &scrubExec{}
		if err := ServiceScrubRequest(context.Background(), c, "n1", tc.zfs, x, scrubClock); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if len(x.calls) != 0 {
			t.Errorf("%s: zpool ran", name)
		}
		if _, ok := nodeAnnotations(t, c)[framev1beta1.ZFSScrubAcknowledgedAnnotation]; ok {
			t.Errorf("%s: acknowledged", name)
		}
	}
}

// A failed start is not acknowledged: the controller must not record a scrub
// that never ran.
func TestServiceScrubRequestLeavesAFailedStartUnacknowledged(t *testing.T) {
	c := scrubNode(t, map[string]string{framev1beta1.ZFSScrubRequestedAnnotation: "t1"})
	x := &scrubExec{fail: map[string]error{"rpool": errors.New("exit status 1")}}
	err := ServiceScrubRequest(context.Background(), c, "n1",
		present(framev1beta1.ZPool{Name: "rpool", Health: "ONLINE"}), x, scrubClock)
	if err == nil {
		t.Fatal("no error")
	}
	if _, ok := nodeAnnotations(t, c)[framev1beta1.ZFSScrubAcknowledgedAnnotation]; ok {
		t.Fatal("a failed start was acknowledged")
	}
}
