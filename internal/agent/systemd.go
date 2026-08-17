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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CommandRunner runs an external command and returns its combined output.
// Injected everywhere this package needs to shell out, so unit tests
// substitute a fake instead of exec'ing a real binary: no real k3s-agent
// unit exists in a test environment (or in many CI containers, no systemd
// at all), and even where systemctl does happen to be present and working,
// querying it from a test proves nothing about behavior — only a fake lets
// a test assert what RefreshSystemdCache does with a given answer.
type CommandRunner interface {
	Run(name string, args ...string) (output string, err error)
}

// ExecCommandRunner is the production CommandRunner: it actually execs the
// command. cmd/agent/main.go uses this; tests use a fake.
type ExecCommandRunner struct{}

// Run implements CommandRunner by shelling out for real.
func (ExecCommandRunner) Run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// HostCommandRunner runs a command in PID 1's namespaces — that is, on the
// host — instead of inside the agent's own container.
//
// Every systemd interaction in this package is about the *node's* systemd,
// and a container has none: `systemctl` run plainly inside the pod talks to
// whatever init the image has (nothing), so it would answer about a system
// that does not exist. Worse, it would answer *successfully* for some
// properties, which is the same class of lie as reading the drop-in file and
// calling it applied. Entering PID 1's namespaces uses the node's own
// binaries and the node's own systemd, so there is exactly one system being
// asked about.
//
// This needs `hostPID: true` and a privileged container — which the agent's
// DaemonSet already declares, and which is why the restart allowlist exists.
type HostCommandRunner struct {
	// Runner actually execs the nsenter invocation. nil means
	// ExecCommandRunner; tests substitute a fake to assert the argv.
	Runner CommandRunner
}

// nsenterArgs enters PID 1's mount, UTS, IPC, network and PID namespaces.
// The mount namespace is the load-bearing one (it is what makes the host's
// /usr/bin/systemctl and its unit tree visible); the rest keep the command
// from observing the container's view of anything else.
var nsenterArgs = []string{"--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "--"}

// Run implements CommandRunner by re-executing name through nsenter.
func (h HostCommandRunner) Run(name string, args ...string) (string, error) {
	runner := h.Runner
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	argv := make([]string, 0, len(nsenterArgs)+1+len(args))
	argv = append(argv, nsenterArgs...)
	argv = append(argv, name)
	argv = append(argv, args...)
	return runner.Run("nsenter", argv...)
}

// RefreshSystemdCache asks systemd for unit's actual, current MemoryKSM
// property and writes it to the cache Observe reads (memoryKSMCachePath),
// so Observe reports what systemd has measured rather than a value nothing
// ever refreshed.
//
// Apply deliberately never calls this: Apply only writes the drop-in, which
// systemd does not pick up until the unit's next start, so querying systemd
// from inside Apply would just re-cache the old value and prove nothing
// changed. The caller is expected to call this once per tick, before
// Observe (cmd/agent/main.go does), so the cache tracks whatever is
// currently true — including right after a restart Task 6 schedules, which
// is the moment this value actually becomes meaningful.
//
// unit must be on the compile-time restart allowlist (IsRestartable): this
// function only ever needs to ask about the units this package already
// knows how to name, and refusing anything else keeps the set of units this
// package will run systemctl against just as fixed as the set it will
// restart.
func RefreshSystemdCache(root, unit string, run CommandRunner) error {
	if !IsRestartable(unit) {
		return fmt.Errorf("refreshing systemd cache: unit %q is not on the restart allowlist", unit)
	}

	out, err := run.Run("systemctl", "show", unit+".service", "-p", "MemoryKSM", "--value")
	if err != nil {
		return fmt.Errorf("systemctl show %s.service -p MemoryKSM: %w", unit, err)
	}

	// Written only on success: a failed query must not clobber a
	// previously-good cache with a garbage or empty value.
	path := filepath.Join(root, memoryKSMCachePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(out)+"\n"), 0o644); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// activeEnterLayouts are the shapes `systemctl show -p ActiveEnterTimestamp
// --value --timestamp=us+utc` can return. systemd prints the microseconds
// only when there are any, so both a fractional and a whole-second form have
// to parse. "MST" matches the trailing "UTC" that --timestamp=us+utc
// guarantees, and Go resolves that abbreviation to real UTC rather than to a
// fabricated zero-offset zone.
//
// The zone is pinned to UTC on purpose rather than parsed from the node's
// local abbreviation: Go cannot resolve an arbitrary abbreviation ("CEST")
// to an offset and silently invents +0000 for it, which would publish a
// wall-clock time hours away from the truth — and this value is the one the
// controller compares a restart against.
var activeEnterLayouts = []string{
	"Mon 2006-01-02 15:04:05.999999 MST",
	"Mon 2006-01-02 15:04:05 MST",
}

// UnitActiveEnterTimestamp asks systemd when unit last entered the active
// state. This is the only evidence the controller accepts that a restart
// actually happened (see internal/controller/frame/nodetuning_rollout.go): a
// node going NotReady is not evidence, because a healthy k3s restart usually
// never flaps the node at all.
//
// A unit that has never been active reports "n/a", which is not a failure of
// this function but an absence of evidence, and is returned as an error so
// the caller publishes nothing rather than publishing something meaningless.
//
// unit is checked against the compile-time allowlist for the same reason
// RefreshSystemdCache checks it: the set of units this package will run
// systemctl against stays exactly as fixed as the set it will restart.
func UnitActiveEnterTimestamp(unit string, run CommandRunner) (time.Time, error) {
	if !IsRestartable(unit) {
		return time.Time{}, fmt.Errorf("reading ActiveEnterTimestamp: unit %q is not on the restart allowlist", unit)
	}

	out, err := run.Run("systemctl", "show", unit+".service", "-p", "ActiveEnterTimestamp", "--value", "--timestamp=us+utc")
	if err != nil {
		return time.Time{}, fmt.Errorf("systemctl show %s.service -p ActiveEnterTimestamp: %w: %s", unit, err, strings.TrimSpace(out))
	}

	raw := strings.TrimSpace(out)
	if raw == "" || raw == "n/a" {
		return time.Time{}, fmt.Errorf("%s.service reports no ActiveEnterTimestamp (%q)", unit, raw)
	}
	for _, layout := range activeEnterLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%s.service ActiveEnterTimestamp %q is not a UTC systemd timestamp", unit, raw)
}

const (
	// restartTransientUnit names the transient unit systemd-run creates. Fixed
	// per target unit so a second request reuses the same name instead of
	// littering the node with one transient unit per restart it ever did.
	restartTransientUnit = "frame-agent-restart-%s"

	// defaultRestartDelay is how long systemd waits before running the
	// restart. It only has to outlive this process's own tick — the point of
	// the timer is that the command is owned by the node's systemd rather than
	// by the pod that asked for it, so it survives the pod dying, which is
	// exactly what restarting k3s does to it.
	defaultRestartDelay = 5 * time.Second
)

// ScheduleDetachedRestart asks the node's systemd to restart unit shortly,
// from a transient timer unit, and returns as soon as the timer is armed.
//
// Detached is not a style choice. Restarting k3s (or the kubelet, or
// containerd) stops the kubelet that owns the pod issuing the command, so an
// inline `systemctl restart` kills its own caller partway through: systemd
// may then see the job's controlling process vanish, and the unit may never
// come back — observed for real on the live cluster. Handing the work to a
// transient unit means the thing running the restart is not a child of
// anything the restart takes down.
//
// unit is passed through the compile-time allowlist before anything is run.
// It arrives here from a request the controller makes over a Node annotation,
// which is caller-influenced input, and this agent is privileged with
// hostPID: a restart target an attacker or a typo can steer is arbitrary root
// on every node in the cluster. This check is the agent's entire security
// boundary.
func ScheduleDetachedRestart(unit string, delay time.Duration, run CommandRunner) error {
	if !IsRestartable(unit) {
		return fmt.Errorf("scheduling restart: unit %q is not on the restart allowlist", unit)
	}
	if delay <= 0 {
		delay = defaultRestartDelay
	}

	out, err := run.Run("systemd-run",
		// Without --collect the transient units stay around after they exit
		// (and a failed one stays around indefinitely), and the *next*
		// request under the same name fails with "unit already exists" — so
		// the second restart of a node's life would never happen.
		"--collect",
		fmt.Sprintf("--on-active=%d", int(delay.Round(time.Second)/time.Second)),
		fmt.Sprintf("--unit=%s", fmt.Sprintf(restartTransientUnit, unit)),
		"systemctl", "restart", unit+".service")
	if err != nil {
		return fmt.Errorf("systemd-run restart %s.service: %w: %s", unit, err, strings.TrimSpace(out))
	}
	return nil
}
