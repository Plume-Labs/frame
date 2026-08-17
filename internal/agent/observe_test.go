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
	"os"
	"path/filepath"
	"testing"
)

// The bug this whole design exists to catch: the drop-in is on disk while
// systemd still reports the old value. Observing the file is not observing the
// effect, so Observe must read what systemd reports, not what we wrote.
func TestObserveReportsSystemdNotTheFile(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "etc/systemd/system/k3s-agent.service.d/10-ksm.conf"),
		"[Service]\nMemoryKSM=yes\n")
	mustWrite(t, filepath.Join(root, "run/frame-agent/memory-ksm"), "no\n")

	got, err := Observe(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.KSM == nil || got.KSM.MemoryKSM {
		t.Fatalf("drop-in present but systemd says no: want MemoryKSM=false, got %+v", got.KSM)
	}
}

// The inverse of the founding-bug test: once systemd's own cache agrees the
// unit is running with KSM, Observe must report that too, not just "not the
// file". A version that hardcoded MemoryKSM=false (e.g. to cheat the test
// above) fails this one.
func TestObserveReportsSystemdTrueWhenCacheSaysYes(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "run/frame-agent/memory-ksm"), "yes\n")

	got, err := Observe(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.KSM == nil || !got.KSM.MemoryKSM {
		t.Fatalf("systemd cache says yes: want MemoryKSM=true, got %+v", got.KSM)
	}
}

// An unconfigured node (nothing written yet, no drop-in, no cache file) is
// the normal case for ksm.enabled=false, not an error. A version that treats
// a missing file as a failure fails this test; a version that panics on a nil
// KSM pointer also fails it.
func TestObserveMissingFilesYieldZeroValueNoError(t *testing.T) {
	root := t.TempDir()

	got, err := Observe(root)
	if err != nil {
		t.Fatalf("missing files must not be an error, got: %v", err)
	}
	if got.KSM == nil {
		t.Fatal("KSM must be non-nil even when unconfigured, so callers see explicit zero values")
	}
	if got.KSM.MemoryKSM {
		t.Fatalf("want MemoryKSM=false on an unconfigured node, got true")
	}
	if got.KSM.GeneralProfit != 0 || got.KSM.PagesSharing != 0 {
		t.Fatalf("want zero KSM counters on an unconfigured node, got %+v", got.KSM)
	}
	if got.TunedProfile != "" {
		t.Fatalf("want empty TunedProfile on an unconfigured node, got %q", got.TunedProfile)
	}
}

// Discriminates a version that forgets to parse the sysfs counters, or reads
// the wrong file, from one that reports them correctly.
func TestObserveReadsKSMSysfsCounters(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "run/frame-agent/memory-ksm"), "yes\n")
	mustWrite(t, filepath.Join(root, "sys/kernel/mm/ksm/general_profit"), "18679296\n")
	mustWrite(t, filepath.Join(root, "sys/kernel/mm/ksm/pages_sharing"), "5528\n")

	got, err := Observe(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.KSM == nil || got.KSM.GeneralProfit != 18679296 || got.KSM.PagesSharing != 5528 {
		t.Fatalf("want GeneralProfit=18679296 PagesSharing=5528, got %+v", got.KSM)
	}
}

// Discriminates a version that ignores etc/tuned/active_profile, or reports
// it verbatim including trailing whitespace, from one that reads and trims it
// correctly.
func TestObserveReadsTunedActiveProfile(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "etc/tuned/active_profile"), "throughput-performance\n")

	got, err := Observe(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.TunedProfile != "throughput-performance" {
		t.Fatalf("want TunedProfile=%q, got %q", "throughput-performance", got.TunedProfile)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
