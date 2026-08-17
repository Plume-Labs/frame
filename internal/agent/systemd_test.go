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
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeCommandRunner is CommandRunner with a scripted answer, so these tests
// never exec a real binary. It also records every call, so a test can assert
// exactly what was asked — not just what came back.
type fakeCommandRunner struct {
	output string
	err    error
	calls  [][]string
}

func (f *fakeCommandRunner) Run(name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.output, f.err
}

// RefreshSystemdCache must write what systemd actually reports, trimmed,
// and must ask systemd about MemoryKSM on the exact unit it was given. A
// version that queries the wrong property (e.g. "MemoryHigh") or hardcodes
// a unit name regardless of the argument would pass a test that only checked
// the cache file's content; asserting the exact command run rules that out.
func TestRefreshSystemdCacheWritesTrimmedOutputAndQueriesTheGivenUnit(t *testing.T) {
	root := t.TempDir()
	fake := &fakeCommandRunner{output: "yes\n"}

	if err := RefreshSystemdCache(root, "k3s", fake); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(root, "run/frame-agent/memory-ksm"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "yes\n" {
		t.Fatalf("want cache content %q, got %q", "yes\n", string(got))
	}

	if len(fake.calls) != 1 {
		t.Fatalf("want exactly one command run, got %d: %v", len(fake.calls), fake.calls)
	}
	want := []string{"systemctl", "show", "k3s.service", "-p", "MemoryKSM", "--value"}
	if len(fake.calls[0]) != len(want) {
		t.Fatalf("want command %v, got %v", want, fake.calls[0])
	}
	for i := range want {
		if fake.calls[0][i] != want[i] {
			t.Fatalf("want command %v, got %v", want, fake.calls[0])
		}
	}
}

// A failed query must not overwrite a previously-good cache with an empty or
// garbage value: the whole point of the cache is that Observe can trust it,
// and a version that writes the (empty) output on error anyway silently
// erases the last known-good measurement rather than just leaving it stale.
func TestRefreshSystemdCacheDoesNotClobberCacheOnError(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "run/frame-agent/memory-ksm"), "yes\n")
	fake := &fakeCommandRunner{err: errors.New("boom")}

	if err := RefreshSystemdCache(root, "k3s-agent", fake); err == nil {
		t.Fatal("want an error when the command runner fails")
	}

	got, err := os.ReadFile(filepath.Join(root, "run/frame-agent/memory-ksm"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "yes\n" {
		t.Fatalf("a failed refresh must not touch the cache, got %q", string(got))
	}
}

// unit is not attacker- or spec-controlled here — it comes from
// DetectKSMUnit — but RefreshSystemdCache still must not run systemctl
// against an arbitrary unit name, the same discipline as the restart
// allowlist itself. A version that skips this check would still pass the
// two tests above (both use allowlisted units) but fails this one, and — the
// discriminating part — never even calls the fake runner while doing so.
func TestRefreshSystemdCacheRejectsUnitNotOnAllowlist(t *testing.T) {
	root := t.TempDir()
	fake := &fakeCommandRunner{output: "yes\n"}

	if err := RefreshSystemdCache(root, "sshd", fake); err == nil {
		t.Fatal("want an error for a unit that is not on the restart allowlist")
	}
	if len(fake.calls) != 0 {
		t.Fatalf("must not shell out for a disallowed unit, got calls: %v", fake.calls)
	}
}

// The whole point of HostCommandRunner is that the command lands on the
// node's systemd, not on the container's non-existent one. A version that
// dropped the nsenter wrapper would still "work" in the sense of running
// something and returning output — it would just be interrogating and
// restarting the wrong system, silently — so the assertion is on the argv,
// which is the only place that distinction is visible.
func TestHostCommandRunnerEntersPID1Namespaces(t *testing.T) {
	inner := &fakeCommandRunner{output: "ok\n"}
	host := HostCommandRunner{Runner: inner}

	out, err := host.Run("systemctl", "show", "k3s.service")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok\n" {
		t.Fatalf("want the inner runner's output passed through, got %q", out)
	}

	want := []string{"nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "--",
		"systemctl", "show", "k3s.service"}
	if len(inner.calls) != 1 || !equalArgs(inner.calls[0], want) {
		t.Fatalf("want %v, got %v", want, inner.calls)
	}
}

// The timestamp must be read in UTC and parsed as UTC. A version that asked
// systemd for the node-local format would get an abbreviation Go cannot
// resolve ("CEST"), invent +0000 for it, and publish a wall-clock time hours
// off — which the controller then compares a restart against. Asserting both
// the argv and the parsed instant catches that; asserting only "it parsed"
// would not.
func TestUnitActiveEnterTimestampParsesUTCSystemdOutput(t *testing.T) {
	fake := &fakeCommandRunner{output: "Sun 2026-08-17 03:19:00.123456 UTC\n"}

	got, err := UnitActiveEnterTimestamp("k3s", fake)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 17, 3, 19, 0, 123456000, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("want %s, got %s", want, got)
	}

	wantCall := []string{"systemctl", "show", "k3s.service", "-p", "ActiveEnterTimestamp", "--value", "--timestamp=us+utc"}
	if len(fake.calls) != 1 || !equalArgs(fake.calls[0], wantCall) {
		t.Fatalf("want %v, got %v", wantCall, fake.calls)
	}
}

// systemd omits the fractional part when there is none, so the whole-second
// form has to parse too — otherwise a node whose unit happened to enter
// active on a round microsecond publishes nothing, and the controller waits
// forever for a baseline it will never get.
func TestUnitActiveEnterTimestampParsesWholeSeconds(t *testing.T) {
	fake := &fakeCommandRunner{output: "Sun 2026-08-17 03:19:00 UTC\n"}

	got, err := UnitActiveEnterTimestamp("k3s-agent", fake)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 8, 17, 3, 19, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("want %s, got %s", want, got)
	}
}

// "n/a" is systemd saying the unit has never been active. That is an absence
// of evidence, not a timestamp: a version that let it through would parse it
// as the zero time and publish 0001-01-01 as the unit's ActiveEnterTimestamp,
// which the controller would happily read as a baseline that any later
// reading "beats" — verifying a restart that never happened.
func TestUnitActiveEnterTimestampRejectsNeverActive(t *testing.T) {
	for _, raw := range []string{"n/a\n", "\n"} {
		fake := &fakeCommandRunner{output: raw}
		if _, err := UnitActiveEnterTimestamp("k3s", fake); err == nil {
			t.Fatalf("want an error for %q, got none", raw)
		}
	}
}

// The restart must be handed to the node's systemd as a transient timer unit
// and never run inline: restarting k3s stops the kubelet that owns the pod
// issuing the command, so an inline `systemctl restart` kills its own caller
// partway through. A version that ran the restart directly would still pass
// any test that only checked "no error"; this one fails it, because the argv
// is where inline and detached differ.
func TestScheduleDetachedRestartArmsATransientTimer(t *testing.T) {
	fake := &fakeCommandRunner{}

	if err := ScheduleDetachedRestart("k3s-agent", 5*time.Second, fake); err != nil {
		t.Fatal(err)
	}

	want := []string{"systemd-run", "--collect", "--on-active=5", "--unit=frame-agent-restart-k3s-agent",
		"systemctl", "restart", "k3s-agent.service"}
	if len(fake.calls) != 1 || !equalArgs(fake.calls[0], want) {
		t.Fatalf("want %v, got %v", want, fake.calls)
	}
}

// The unit to restart arrives over a Node annotation the controller writes,
// which is caller-influenced input, and this agent is privileged with
// hostPID: a restart target that anything outside this package can steer is
// arbitrary root on every node. A version without the allowlist check would
// pass every other test here (they all use allowlisted units) and fail only
// this one — and the discriminating half is that nothing is executed at all.
func TestScheduleDetachedRestartRefusesUnitNotOnAllowlist(t *testing.T) {
	for _, unit := range []string{"sshd", "../k3s", "k3s.service", ""} {
		fake := &fakeCommandRunner{}
		if err := ScheduleDetachedRestart(unit, time.Second, fake); err == nil {
			t.Fatalf("want a refusal for unit %q", unit)
		}
		if len(fake.calls) != 0 {
			t.Fatalf("must not run anything for unit %q, got %v", unit, fake.calls)
		}
	}
}

// equalArgs compares two argv slices element by element.
func equalArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
