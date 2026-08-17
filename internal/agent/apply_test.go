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
//
// The k3s-agent.service stub represents a worker node (see
// mustWriteUnitFile): Apply now detects which unit actually owns containerd
// rather than assuming one, so a fixture with no unit file at all — which
// this test had before that fix — no longer models any real node and would
// (correctly) fail with "no k3s or k3s-agent unit found".
func TestApplyKSMRequestsRestart(t *testing.T) {
	root := t.TempDir()
	mustWriteUnitFile(t, root, "k3s-agent")
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
	mustWriteUnitFile(t, root, "k3s-agent")
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

// FINDING 1 fix — the server node runs k3s.service, not k3s-agent.service.
// Hardcoding k3s-agent silently never configured the control plane, which
// delivered most of KSM's benefit on the live cluster (109 of ~166 MB
// saved). A detectUnit that always returns "k3s-agent" — the bug being
// fixed — passes every other KSM test in this file (all of them model a
// worker) but fails this one loudly: the drop-in would land at the
// k3s-agent path, which this test asserts must NOT exist, and the k3s path,
// which it asserts must, would be missing.
func TestApplyDetectsControlPlaneUnitK3s(t *testing.T) {
	root := t.TempDir()
	mustWriteUnitFile(t, root, "k3s")

	if _, err := Apply(root, v1beta1.NodeTuningSpec{
		KSM: &v1beta1.KSMSpec{Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, filepath.Join(root, "etc/systemd/system/k3s.service.d/10-ksm.conf"),
		"[Service]\nMemoryKSM=yes\n")
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system/k3s-agent.service.d/10-ksm.conf")); !os.IsNotExist(err) {
		t.Fatalf("must not write a k3s-agent drop-in on a server node that has no k3s-agent.service, stat err: %v", err)
	}
}

// The worker-node counterpart of the control-plane test above — the two
// together are what "test both layouts" means. A version that only handles
// one of the two unit names (whichever direction) fails one or the other.
func TestApplyDetectsWorkerUnitK3sAgent(t *testing.T) {
	root := t.TempDir()
	mustWriteUnitFile(t, root, "k3s-agent")

	if _, err := Apply(root, v1beta1.NodeTuningSpec{
		KSM: &v1beta1.KSMSpec{Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, filepath.Join(root, "etc/systemd/system/k3s-agent.service.d/10-ksm.conf"),
		"[Service]\nMemoryKSM=yes\n")
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system/k3s.service.d/10-ksm.conf")); !os.IsNotExist(err) {
		t.Fatalf("must not write a k3s drop-in on a worker node that has no k3s.service, stat err: %v", err)
	}
}

// If both unit files somehow exist on the same node, k3s-agent must win
// (that is the one actually running containerd for workloads on a worker;
// a node cannot really run both, but detection must still pick
// deterministically rather than depend on directory iteration order). A
// version that iterates a map instead of the ordered candidate slice, or
// that prefers k3s, fails this test.
func TestApplyPrefersK3sAgentWhenBothUnitsPresent(t *testing.T) {
	root := t.TempDir()
	mustWriteUnitFile(t, root, "k3s")
	mustWriteUnitFile(t, root, "k3s-agent")

	if _, err := Apply(root, v1beta1.NodeTuningSpec{
		KSM: &v1beta1.KSMSpec{Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, filepath.Join(root, "etc/systemd/system/k3s-agent.service.d/10-ksm.conf"),
		"[Service]\nMemoryKSM=yes\n")
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system/k3s.service.d/10-ksm.conf")); !os.IsNotExist(err) {
		t.Fatalf("k3s-agent must win when both units exist, but a k3s drop-in was also written, stat err: %v", err)
	}
}

// detectKSMUnit also checks usr/lib/systemd/system, the systemd package
// default location, not just the /etc override tree — this is where a
// distro-packaged unit file normally lives if it was never locally
// overridden. A version that only checks etc/systemd/system fails this test
// even though it would pass every other one in this file.
func TestApplyDetectsUnitUnderPackageSystemdDir(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "usr/lib/systemd/system/k3s.service"), "[Unit]\nDescription=k3s\n")

	if _, err := Apply(root, v1beta1.NodeTuningSpec{
		KSM: &v1beta1.KSMSpec{Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}

	// The override drop-in still goes under /etc regardless of where the
	// unit file itself lives — that is where systemd looks for drop-ins.
	assertFileContent(t, filepath.Join(root, "etc/systemd/system/k3s.service.d/10-ksm.conf"),
		"[Service]\nMemoryKSM=yes\n")
}

// A node with neither unit is not one this agent has any business writing a
// drop-in to — per FINDING 1, that must be a real, reported error, not a
// silent no-op that leaves the node unconfigured with no trace. A version
// that falls back to a hardcoded default unit name when detection finds
// nothing fails this test by returning nil error and writing a drop-in
// anyway.
func TestApplyErrorsWhenNoK3sUnitExists(t *testing.T) {
	root := t.TempDir()

	if _, err := Apply(root, v1beta1.NodeTuningSpec{
		KSM: &v1beta1.KSMSpec{Enabled: true},
	}); err == nil {
		t.Fatal("want an error when no k3s or k3s-agent unit exists under root")
	}

	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("no unit found must not write anything, but root now has: %v", entries)
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
	mustWriteUnitFile(t, root, "k3s-agent")
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
	mustWriteUnitFile(t, root, "k3s-agent")
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
	mustWriteUnitFile(t, root, "k3s-agent")
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
	mustWriteUnitFile(t, root, "k3s-agent")
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

// mustWriteUnitFile writes a stub systemd unit file under root's default
// (etc/systemd/system) search location, so DetectKSMUnit finds it. Content
// is irrelevant — only existence is checked — but it is non-empty to look
// like a real unit rather than an accidental empty file.
func mustWriteUnitFile(t *testing.T, root, unit string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, "etc/systemd/system", unit+".service"), "[Unit]\nDescription="+unit+"\n")
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
