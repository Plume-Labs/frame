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
	}, &fakeCommandRunner{})
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
	}, &fakeCommandRunner{})
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
	}, &fakeCommandRunner{}); err != nil {
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
	}, &fakeCommandRunner{}); err != nil {
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
	}, &fakeCommandRunner{}); err != nil {
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
	}, &fakeCommandRunner{}); err != nil {
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
	}, &fakeCommandRunner{}); err == nil {
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
	}, &fakeCommandRunner{}); err != nil {
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
	}, &fakeCommandRunner{}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "sys/kernel/mm/ksm/sleep_millisecs")); !os.IsNotExist(err) {
		t.Fatalf("sleep_millisecs was not requested and must not be written, stat err: %v", err)
	}
}

// The kernel refuses some KSM knobs outright once the setting they configure
// has taken effect (merge_across_nodes returns EBUSY once any page is
// merged), so a writer that aborts the whole KSM apply on the first refusal
// fails on every run after the first — observed for real on the live cluster.
//
// The refused knob is `run`, which is the FIRST one applyKSM writes, and the
// assertions are on knobs written after it. That ordering is the whole test:
// an earlier version of this spec refused merge_across_nodes — the last knob
// written — and then asserted pages_to_scan, which had already been written
// before the refusal could happen, so it could not tell "continues past a
// refusal" from "aborts on the first one". As written now, a version that
// returns early on the first write error fails on both assertions, and a
// version that propagates the error fails by returning non-nil.
func TestApplyToleratesRefusedSysfsKnob(t *testing.T) {
	root := t.TempDir()
	mustWriteUnitFile(t, root, "k3s-agent")
	// A directory where a file is expected: every write to it fails, which is
	// the closest a test can get to the kernel's own refusal.
	if err := os.MkdirAll(filepath.Join(root, "sys/kernel/mm/ksm/run"), 0o755); err != nil {
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
	}, &fakeCommandRunner{})
	if err != nil {
		t.Fatalf("a refused sysfs knob must not fail Apply, got: %v", err)
	}
	if !needsRestart {
		t.Fatal("this is a fresh root, so the drop-in write alone still needs a restart")
	}
	assertFileContent(t, filepath.Join(root, "sys/kernel/mm/ksm/pages_to_scan"), "4000")
	assertFileContent(t, filepath.Join(root, "sys/kernel/mm/ksm/merge_across_nodes"), "0")
}

// A second Apply with the same spec must be a no-op with respect to the
// restart signal: nothing on disk changed, so nothing needs a restart. A
// version that always reports needsRestart=true whenever KSM is enabled
// (rather than comparing against what is already on disk) fails this test.
func TestApplyIsIdempotent(t *testing.T) {
	root := t.TempDir()
	mustWriteUnitFile(t, root, "k3s-agent")
	spec := v1beta1.NodeTuningSpec{KSM: &v1beta1.KSMSpec{Enabled: true}}

	if _, err := Apply(root, spec, &fakeCommandRunner{}); err != nil {
		t.Fatal(err)
	}
	needsRestart, err := Apply(root, spec, &fakeCommandRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if needsRestart {
		t.Fatal("re-applying an already-applied spec must not ask for another restart")
	}
}

// Nothing here tests a cpu-manager policy: the field is gone from the CRD.
// It used to write an agent-side cache file and delete kubelet's
// cpu_manager_state while nothing wrote the kubelet configuration that
// actually selects the policy, so it reported needsRestart for a change that
// was never made — a node cordoned, drained and restarted for nothing. See
// the design doc's Scope section for the deferral.

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

// tuned lives on the node, not in this container. A version that exec'd
// `tuned-adm` directly would apply the profile to the container — which owns
// none of the sysctls, governor or scheduler the profile exists to set — and
// would write the container's /etc/tuned/active_profile while Observe reads
// the node's, so the node would sit in Drifted forever with a command that
// reported success every time.
//
// Asserting the argv, not just a nil error, is what makes that visible: a
// direct exec returns nil here too (there is no tuned-adm on a test machine,
// so it would in fact return an error — but on a machine that has one it
// would return nil and this test would still be the only thing that catches
// the wrong machine). The discriminating half is that the injected runner is
// the one invoked, exactly once, with exactly this command.
func TestApplyTunedProfileGoesThroughTheInjectedRunner(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "etc/tuned/active_profile"), "balanced\n")
	fake := &fakeCommandRunner{}

	if _, err := Apply(root, v1beta1.NodeTuningSpec{TunedProfile: "throughput-performance"}, fake); err != nil {
		t.Fatal(err)
	}

	want := []string{"tuned-adm", "profile", "throughput-performance"}
	if len(fake.calls) != 1 {
		t.Fatalf("want exactly one command run through the runner, got %v", fake.calls)
	}
	for i := range want {
		if fake.calls[0][i] != want[i] {
			t.Fatalf("want %v, got %v", want, fake.calls[0])
		}
	}
}

// A profile the node already runs is not re-applied: `tuned-adm profile`
// re-runs every plugin, and doing that every 30 seconds forever would make
// the agent the noisiest writer on the node.
func TestApplyTunedProfileDoesNothingWhenAlreadyActive(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "etc/tuned/active_profile"), "throughput-performance\n")
	fake := &fakeCommandRunner{}

	if _, err := Apply(root, v1beta1.NodeTuningSpec{TunedProfile: "throughput-performance"}, fake); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("nothing to change, so nothing to run; got %v", fake.calls)
	}
}

// Falling back to exec'ing in the container when no runner is supplied is the
// exact bug the parameter exists to prevent, so a missing runner is a
// refusal — and it must still be a refusal only when tuned actually has work
// to do, which the test above pins down.
func TestApplyTunedProfileRefusesWithoutARunner(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "etc/tuned/active_profile"), "balanced\n")

	if _, err := Apply(root, v1beta1.NodeTuningSpec{TunedProfile: "throughput-performance"}, nil); err == nil {
		t.Fatal("want a refusal when there is no way to reach the node's tuned")
	}
}

// Every setting after a failing one is silently not applied, because the
// first error ends the call. MIG is a single file write that cannot fail for
// anything another setting caused, so it goes first: a GPU node must not be
// left on the wrong MIG profile — with nothing in the error to say so —
// because tuned could not be reached. A version that records MIG after tuned
// passes every other test here and fails this one.
func TestApplyRecordsMIGEvenWhenTunedFails(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "etc/tuned/active_profile"), "balanced\n")
	fake := &fakeCommandRunner{err: errors.New("tuned-adm: not found")}

	_, err := Apply(root, v1beta1.NodeTuningSpec{
		TunedProfile: "throughput-performance",
		MIGProfile:   "a100-4x2g",
	}, fake)
	if err == nil {
		t.Fatal("want the tuned failure reported, not swallowed")
	}

	got, readErr := RecordedMIGProfile(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got != "a100-4x2g" {
		t.Fatalf("the MIG profile must survive another setting's failure, got %q", got)
	}
}

// The read half of the MIG seam: a node no NodeTuning ever asked a profile of
// is not a broken node, and must not make the whole tick fail.
func TestRecordedMIGProfileIsEmptyWhenNeverRecorded(t *testing.T) {
	got, err := RecordedMIGProfile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("want no profile, got %q", got)
	}
}
