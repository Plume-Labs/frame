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
