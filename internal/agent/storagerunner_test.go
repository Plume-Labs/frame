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
// empty, the host is this machine). A child and a grandchild that both
// ignore SIGTERM, the grandchild holding stdout — what a stuck storage
// command looks like from here — must:
//   - be KILLed by the host-side `timeout -k` (exit 137), not waited for;
//   - return shortly after, not when the grandchild would end (WaitDelay);
//   - leave nothing behind: the grandchild, which `timeout --foreground`
//     never signals, is killed with the group when Exec returns.
func TestHostStorageExecutorLeavesNothingBehind(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("no coreutils timeout on this machine")
	}
	x := &HostStorageExecutor{HardDeadline: 6 * time.Second, WaitDelay: 500 * time.Millisecond}
	// sh ignores TERM; its child sleep inherits the ignore and holds stdout.
	argv := []string{"timeout", "--foreground", "-k", "1", "1", "sh", "-c", `trap "" TERM; sleep 8 & wait`}

	var pgid int
	x.onStart = func(p int) { pgid = p }
	start := time.Now()
	_, _, err := x.Exec(argv)
	elapsed := time.Since(start)
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

	if code := exitCode(err); code != 137 {
		t.Errorf("exit code %d (err %v), want 137: the host-side timeout must KILL after the grace period", code, err)
	}
	if elapsed > 4*time.Second {
		t.Errorf("returned after %s: Wait blocked on the grandchild's pipe", elapsed)
	}
	if !waitGroupGone(pgid, time.Second) {
		t.Fatalf("group %d still alive after Exec returned: the TERM-ignoring grandchild survived", pgid)
	}
}

// waitGroupGone polls until no process of the group exists (killed
// processes take a moment to be reaped).
func waitGroupGone(pgid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for groupAlive(pgid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	return !groupAlive(pgid)
}

// The busy lock, on a group the test controls (a process SIGKILL cannot
// stop, in D state, cannot be staged here — the lock is what matters): a
// command whose previous group is alive is refused, and runs again as soon
// as that group is gone.
func TestHostStorageExecutorBusyLockHoldsThenReleases(t *testing.T) {
	stuck := exec.Command("sleep", "30")
	stuck.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := stuck.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := stuck.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL); _ = stuck.Wait() })

	argv := []string{"true"}
	x := &HostStorageExecutor{HardDeadline: 5 * time.Second, WaitDelay: 200 * time.Millisecond}
	x.inflight = map[string]int{"true": pgid}

	if _, _, err := x.Exec(argv); !errors.Is(err, errBusy) {
		t.Fatalf("run while the previous group lives: err %v, want errBusy", err)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	_ = stuck.Wait()
	if !waitGroupGone(pgid, time.Second) {
		t.Fatal("staged group did not die")
	}
	if _, _, err := x.Exec(argv); err != nil {
		t.Fatalf("run after the previous group died: err %v, want it to run", err)
	}
	if _, ok := x.inflight["true"]; ok {
		t.Error("the lock was not released")
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
		if !waitGroupGone(pgid, 2*time.Second) {
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
