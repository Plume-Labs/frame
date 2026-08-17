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
