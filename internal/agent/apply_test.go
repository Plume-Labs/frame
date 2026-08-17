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

	v1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// Writing the drop-in is not enough — it only takes effect on the next unit
// start, so Apply must say so rather than let a caller assume it is live. A
// version that only writes files and always returns needsRestart=false fails
// this test.
func TestApplyKSMRequestsRestart(t *testing.T) {
	root := t.TempDir()
	yes := true
	needsRestart, err := Apply(root, v1beta1.NodeTuningSpec{
		KSM: &v1beta1.KSMSpec{Enabled: yes},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !needsRestart {
		t.Fatal("enabling KSM writes a systemd drop-in; that needs a restart")
	}
}

// The scanner knobs are live immediately, so changing only those must NOT
// demand a disruptive restart. A version that reports needsRestart whenever
// spec.KSM is non-nil (rather than only when the drop-in content actually
// changed) fails this test, even though it would pass the one above.
func TestApplyScannerKnobsAloneNeedNoRestart(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "etc/systemd/system/k3s-agent.service.d/10-ksm.conf"),
		"[Service]\nMemoryKSM=yes\n")
	four := int32(4000)
	needsRestart, err := Apply(root, v1beta1.NodeTuningSpec{
		KSM: &v1beta1.KSMSpec{Enabled: true, PagesToScan: &four},
	})
	if err != nil {
		t.Fatal(err)
	}
	if needsRestart {
		t.Fatal("scanner knobs apply live; demanding a restart would drain a node for nothing")
	}
}

// The unit allowlist is the whole security boundary of the agent: once a
// restart target is attacker- or typo-controlled, "restart a unit" is
// arbitrary root. It must be compile-time, never reachable from the CRD. A
// permissive IsRestartable that always returns true passes every case in the
// first loop but fails every case in the second.
func TestRestartableUnitsAreAllowlisted(t *testing.T) {
	for _, u := range []string{"k3s", "k3s-agent", "kubelet", "containerd"} {
		if !IsRestartable(u) {
			t.Errorf("%s should be restartable", u)
		}
	}
	for _, u := range []string{"sshd", "systemd-networkd", "nftables", "../k3s", ""} {
		if IsRestartable(u) {
			t.Errorf("%s must never be restartable", u)
		}
	}
}

// PagesToScan actually reaches the sysfs knob, not just the drop-in. A
// version that only handles the drop-in and silently ignores the scanner
// fields would pass both tests above (neither inspects the knob files) but
// fails this one.
func TestApplyWritesScannerKnobValues(t *testing.T) {
	root := t.TempDir()
	pages := int32(4000)
	sleep := int32(200)
	mergeAcrossNodes := false
	if _, err := Apply(root, v1beta1.NodeTuningSpec{
		KSM: &v1beta1.KSMSpec{
			Enabled:          true,
			PagesToScan:      &pages,
			SleepMillisecs:   &sleep,
			MergeAcrossNodes: &mergeAcrossNodes,
		},
	}); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, filepath.Join(root, "sys/kernel/mm/ksm/run"), "1")
	assertFileContent(t, filepath.Join(root, "sys/kernel/mm/ksm/pages_to_scan"), "4000")
	assertFileContent(t, filepath.Join(root, "sys/kernel/mm/ksm/sleep_millisecs"), "200")
	assertFileContent(t, filepath.Join(root, "sys/kernel/mm/ksm/merge_across_nodes"), "0")
}

// A field left nil (not requested) must not be restated as a zero value. A
// version that always writes every scanner knob field — treating "unset" the
// same as "set to 0" — fails this test by creating sleep_millisecs when
// nothing asked for it.
func TestApplyLeavesUnsetScannerKnobsUntouched(t *testing.T) {
	root := t.TempDir()
	if _, err := Apply(root, v1beta1.NodeTuningSpec{
		KSM: &v1beta1.KSMSpec{Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "sys/kernel/mm/ksm/sleep_millisecs")); !os.IsNotExist(err) {
		t.Fatalf("sleep_millisecs was not requested and must not be written, stat err: %v", err)
	}
}

// The kernel refuses merge_across_nodes with EBUSY once any page is merged.
// A writer that aborts the whole KSM apply on the first refused knob fails
// on every run after the first — this was observed for real on the live
// cluster. Simulate the refusal by making the target path a directory (any
// write to it fails), and confirm Apply both returns no error and still
// writes the knob that comes after it. A version that returns early on the
// first write error fails this test by leaving pages_to_scan unwritten; a
// version that propagates the error fails it by returning non-nil.
func TestApplyToleratesRefusedSysfsKnob(t *testing.T) {
	root := t.TempDir()
	mergePath := filepath.Join(root, "sys/kernel/mm/ksm/merge_across_nodes")
	if err := os.MkdirAll(mergePath, 0o755); err != nil {
		t.Fatal(err)
	}

	pages := int32(4000)
	mergeAcrossNodes := false
	needsRestart, err := Apply(root, v1beta1.NodeTuningSpec{
		KSM: &v1beta1.KSMSpec{
			Enabled:          true,
			PagesToScan:      &pages,
			MergeAcrossNodes: &mergeAcrossNodes,
		},
	})
	if err != nil {
		t.Fatalf("a refused sysfs knob must not fail Apply, got: %v", err)
	}
	if !needsRestart {
		t.Fatal("this is a fresh root, so the drop-in write alone still needs a restart")
	}
	assertFileContent(t, filepath.Join(root, "sys/kernel/mm/ksm/pages_to_scan"), "4000")
}

// A second Apply with the same spec must be a no-op with respect to the
// restart signal: nothing on disk changed, so nothing needs a restart. A
// version that always reports needsRestart=true whenever KSM is enabled
// (rather than comparing against what is already on disk) fails this test.
func TestApplyIsIdempotent(t *testing.T) {
	root := t.TempDir()
	spec := v1beta1.NodeTuningSpec{KSM: &v1beta1.KSMSpec{Enabled: true}}

	if _, err := Apply(root, spec); err != nil {
		t.Fatal(err)
	}
	needsRestart, err := Apply(root, spec)
	if err != nil {
		t.Fatal(err)
	}
	if needsRestart {
		t.Fatal("re-applying an already-applied spec must not ask for another restart")
	}
}

// A changed cpu-manager policy needs a kubelet restart and, per the design
// doc, removal of the stale cpu_manager_state so kubelet does not rebuild
// its CPU assignments from state written under the old policy. A version
// that tracks the policy but forgets the removal fails the second check
// here; a version that never compares against the previous policy (so it
// reports changed=true, or removes the state file, on every call including
// ones where the policy is unchanged) fails TestApplyCPUManagerPolicyUnchangedNeedsNoRestart below.
func TestApplyCPUManagerPolicyChangeRemovesStaleState(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "var/lib/kubelet/cpu_manager_state"), `{"policyName":"none"}`)

	needsRestart, err := Apply(root, v1beta1.NodeTuningSpec{CPUManagerPolicy: "static"})
	if err != nil {
		t.Fatal(err)
	}
	if !needsRestart {
		t.Fatal("changing the CPU manager policy needs a kubelet restart")
	}
	if _, err := os.Stat(filepath.Join(root, "var/lib/kubelet/cpu_manager_state")); !os.IsNotExist(err) {
		t.Fatalf("stale cpu_manager_state must be removed on a policy change, stat err: %v", err)
	}
}

// Discriminates a version that always reports needsRestart=true whenever
// CPUManagerPolicy is set (ignoring whether it actually changed) from one
// that compares against what was previously applied.
func TestApplyCPUManagerPolicyUnchangedNeedsNoRestart(t *testing.T) {
	root := t.TempDir()
	if _, err := Apply(root, v1beta1.NodeTuningSpec{CPUManagerPolicy: "static"}); err != nil {
		t.Fatal(err)
	}

	needsRestart, err := Apply(root, v1beta1.NodeTuningSpec{CPUManagerPolicy: "static"})
	if err != nil {
		t.Fatal(err)
	}
	if needsRestart {
		t.Fatal("re-applying the same CPU manager policy must not ask for another restart")
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s: want %q, got %q", path, want, string(got))
	}
}
