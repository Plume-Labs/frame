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
	"errors"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	framev1beta1 "github.com/gothshoot/frame/api/frame/v1beta1"
)

// The audit's proof, replayed with real processes (no nsenter: Prefix is
// empty, the host is this machine). A command that ignores SIGTERM and
// leaves a grandchild holding stdout — what a zpool blocked on a suspended
// pool looks like from here — must:
//   - be KILLed by the host-side `timeout -k` (exit 137), not by waiting;
//   - return shortly after, not when the grandchild finally exits (Wait
//     must not block on the pipe it holds: WaitDelay);
//   - not be started again while any process of its group is alive.
func TestHostStorageExecutorBoundsACommandThatWillNotDie(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("no coreutils timeout on this machine")
	}
	x := &HostStorageExecutor{HardDeadline: 6 * time.Second, WaitDelay: 500 * time.Millisecond}
	// sh ignores TERM; its child sleep 8 inherits the ignore and holds stdout.
	argv := []string{"timeout", "--foreground", "-k", "1", "1", "sh", "-c", `trap "" TERM; sleep 8; echo late`}
	var pgid atomic.Int64
	t.Cleanup(func() {
		if p := pgid.Load(); p > 0 {
			_ = syscall.Kill(-int(p), syscall.SIGKILL)
		}
	})

	start := time.Now()
	_, _, err := x.Exec(argv)
	elapsed := time.Since(start)
	x.mu.Lock()
	for _, p := range x.inflight {
		pgid.Store(int64(p))
	}
	x.mu.Unlock()

	if code := exitCode(err); code != 137 {
		t.Errorf("exit code %d (err %v), want 137: the host-side timeout must KILL after the grace period", code, err)
	}
	if elapsed > 4*time.Second {
		t.Errorf("returned after %s: Wait blocked on the grandchild's pipe", elapsed)
	}
	if pgid.Load() == 0 {
		t.Fatal("the surviving grandchild was not recorded as in flight")
	}

	start = time.Now()
	if _, _, err := x.Exec(argv); !errors.Is(err, errBusy) {
		t.Errorf("second run while the first is alive: err %v, want errBusy", err)
	}
	if time.Since(start) > time.Second {
		t.Error("the busy check waited instead of refusing")
	}
}

// A hung host side past the agent's own deadline: the whole group is killed.
func TestHostStorageExecutorKillsTheGroupAtTheHardDeadline(t *testing.T) {
	x := &HostStorageExecutor{HardDeadline: 500 * time.Millisecond, WaitDelay: 200 * time.Millisecond}
	start := time.Now()
	// A grandchild (sleep, backgrounded) so that killing only the direct
	// child would leave the group alive.
	_, _, err := x.Exec([]string{"sh", "-c", `trap "" TERM; sleep 5 & wait`})
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("err %v after %s, want a kill at the hard deadline", err, time.Since(start))
	}
	// Killed processes may take a moment to be reaped; a group killed as a
	// whole is empty well within a second, one whose grandchild survived is
	// not (sleep 5).
	for _, pgid := range x.inflight {
		deadline := time.Now().Add(2 * time.Second)
		for groupAlive(pgid) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if groupAlive(pgid) {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			t.Errorf("group %d still alive 2 s after the hard-deadline kill", pgid)
		}
	}
}

// blockingExec never answers until released: a zpool hung for good.
type blockingExec struct{ release chan struct{} }

func (b blockingExec) Exec([]string) (string, string, error) {
	<-b.release
	return "", "", nil
}

// The tick must not wait on a hung collection beyond its bound, and must
// not start a second one on top of it.
func TestLocalCollectorNeverHoldsTheTick(t *testing.T) {
	b := blockingExec{release: make(chan struct{})}
	defer close(b.release)
	var starts atomic.Int32
	src := LocalSources{
		Exec:      b,
		ReadFile:  func(string) ([]byte, error) { starts.Add(1); return nil, errors.New("x") },
		PathExist: func(string) bool { return true },
		Now:       time.Now,
	}
	c := NewLocalCollector(src)
	start := time.Now()
	if got := c.Latest(nil, 200*time.Millisecond); got != nil {
		t.Errorf("no collection has completed, got %+v", got)
	}
	c.Latest(nil, 200*time.Millisecond)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("Latest waited %s", time.Since(start))
	}
	// ZFS hangs first; md (ReadFile) is only reached once zpool returns. A
	// second collection would have been started on top otherwise — both
	// would block in Exec, but neither reaches ReadFile, so count starts on
	// the running channel instead.
	c.mu.Lock()
	running := c.running != nil
	c.mu.Unlock()
	if !running {
		t.Fatal("the hung collection is not tracked as running")
	}
}

// LVM is read every lvmInterval, not every cycle; a busy ZFS keeps the
// last complete lists.
func TestLocalStorageSpacesLVMAndKeepsBusyLists(t *testing.T) {
	r := zfsNode(t)
	src := sources(r, nil, "/sys/module/zfs")
	first := ObserveLocalStorage(src, nil, nil)

	r.calls = nil
	r.fail = map[string]error{"zpool": errBusy}
	src.Now = func() time.Time { return testNow.Add(time.Minute) }
	second := ObserveLocalStorage(src, first, nil)
	for _, c := range toolCalls(r) {
		if c[0] == "pvs" || c[0] == "vgs" || c[0] == "lvs" {
			t.Errorf("LVM re-read one minute later: %q", c)
		}
	}
	if second.ZFS.State != framev1beta1.StackBusy || len(second.ZFS.Pools) != 2 || !second.ZFS.ReadAt.Equal(first.ZFS.ReadAt) {
		t.Errorf("busy ZFS: state %q, %d pools, readAt %v (first %v)", second.ZFS.State, len(second.ZFS.Pools), second.ZFS.ReadAt, first.ZFS.ReadAt)
	}

	r.calls = nil
	src.Now = func() time.Time { return testNow.Add(6 * time.Minute) }
	ObserveLocalStorage(src, second, nil)
	read := false
	for _, c := range toolCalls(r) {
		read = read || c[0] == "pvs"
	}
	if !read {
		t.Error("LVM not re-read after the interval")
	}
}
