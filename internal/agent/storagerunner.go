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
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// HostStorageExecutor runs the local-storage commands on the host, bounded
// so that a command that will not die costs one stack, not the agent.
//
//   - Each command runs in its own process group (Setpgid). nsenter's child
//     and the host's `timeout --foreground` stay in it, and so does the
//     storage command itself.
//   - At HardDeadline the whole group gets SIGKILL (cmd.Cancel), and
//     WaitDelay stops Wait from blocking on a pipe that a surviving
//     grandchild still holds open — without it, Wait returns only when that
//     grandchild exits (observed: 6 s for a 2 s backstop).
//   - After Wait, if any process of the group is still alive (a zpool in
//     uninterruptible sleep survives SIGKILL), the command is marked busy:
//     it is not started again until that group is empty, so a stuck pool
//     never accumulates orphans on the host.
type HostStorageExecutor struct {
	// Prefix is prepended to every argv: nsenter into PID 1's namespaces in
	// production, nothing in tests.
	Prefix       []string
	HardDeadline time.Duration
	WaitDelay    time.Duration

	mu       sync.Mutex
	inflight map[string]int // argv key → process group still alive
}

// NewHostStorageExecutor runs through nsenter, with a hard deadline past the
// host-side timeout and its kill grace.
func NewHostStorageExecutor() *HostStorageExecutor {
	return &HostStorageExecutor{
		Prefix:       append([]string{"nsenter"}, nsenterArgs...),
		HardDeadline: time.Duration(storageTimeoutSeconds+storageKillAfterSeconds+3) * time.Second,
		WaitDelay:    2 * time.Second,
	}
}

// groupAlive reports whether any process of group pgid still exists.
func groupAlive(pgid int) bool {
	return syscall.Kill(-pgid, 0) == nil
}

// Exec implements StorageExecutor.
func (h *HostStorageExecutor) Exec(argv []string) (string, string, error) {
	key := strings.Join(argv, "\x00")
	h.mu.Lock()
	if h.inflight == nil {
		h.inflight = map[string]int{}
	}
	if pgid, ok := h.inflight[key]; ok {
		if groupAlive(pgid) {
			h.mu.Unlock()
			return "", "", errBusy
		}
		delete(h.inflight, key)
	}
	h.mu.Unlock()

	full := append(append([]string{}, h.Prefix...), argv...)
	ctx, cancel := context.WithTimeout(context.Background(), h.HardDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// The whole group, not just the direct child.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = h.WaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return "", "", err
	}
	pgid := cmd.Process.Pid
	err := cmd.Wait()
	if groupAlive(pgid) {
		h.mu.Lock()
		h.inflight[key] = pgid
		h.mu.Unlock()
	}
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.String(), stderr.String(), errors.Join(err, errors.New("killed at the agent's hard deadline"))
	}
	return stdout.String(), stderr.String(), err
}
