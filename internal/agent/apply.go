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
	"strconv"
	"strings"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// Apply writes node-local state under root (normally "/", or a fixture tree
// in tests) so it matches spec, and reports whether anything it changed
// needs a unit restart to take effect. Apply never restarts anything itself
// — restarting k3s or the kubelet from the pod that is itself owned by that
// unit would kill the caller (see the design doc's "Restarting a node from a
// pod kills the caller"). It only writes state and answers the question; the
// controller decides when a restart is safe to schedule (Task 5/6).
const (
	// ksmDropInDir and ksmDropInFile locate the systemd drop-in that flips
	// MemoryKSM= on whichever unit owns containerd on this node. This is the
	// half of KSM that actually merges anything: PR_SET_MEMORY_MERGE has to
	// be set on the process before it forks, so it has to live on the unit,
	// not be poked at from outside. Writing it is not enough on its own —
	// systemd only picks it up on the unit's next start, which is exactly
	// why Apply reports needsRestart rather than letting a caller assume it
	// is already live.
	//
	// The unit itself is not hardcoded: workers run k3s-agent.service and
	// the control-plane server runs k3s.service, and on the live cluster the
	// server delivered most of KSM's benefit (109 of ~166 MB saved).
	// Hardcoding either one silently skips the other kind of node — see
	// DetectKSMUnit, which picks the real one.
	ksmDropInDir  = "etc/systemd/system/%s.service.d"
	ksmDropInFile = "10-ksm.conf"

	// ksmRunPath, ksmPagesToScanPath, ksmSleepMillisecsPath and
	// ksmMergeAcrossNodesPath are the KSM scanner knobs under sysfs. Unlike
	// the drop-in, the kernel applies every one of these live: no restart
	// needed, and demanding one would cordon and drain a node for nothing.
	ksmRunPath              = "sys/kernel/mm/ksm/run"
	ksmPagesToScanPath      = "sys/kernel/mm/ksm/pages_to_scan"
	ksmSleepMillisecsPath   = "sys/kernel/mm/ksm/sleep_millisecs"
	ksmMergeAcrossNodesPath = "sys/kernel/mm/ksm/merge_across_nodes"

	// cpuManagerPolicyCachePath records the last policy Apply wrote, so a
	// later call can tell "unchanged" from "changed" without a kubelet API
	// to ask. There is no equivalent to systemd's drop-in-plus-show pattern
	// for kubelet's CPU manager policy, so Apply owns this comparison itself
	// rather than re-deriving it from ObservedTuning.CPUManagerPolicy, which
	// Observe does not populate (see observe.go).
	cpuManagerPolicyCachePath = "run/frame-agent/cpu-manager-policy"

	// cpuManagerStatePath is kubelet's own persisted CPU assignments. A
	// state file written under one policy is invalid input to a different
	// one — kubelet must rebuild it from scratch after a policy change, so
	// changing the policy removes it. This is the standard kubelet path
	// regardless of distribution; k3s embeds a real kubelet at this location.
	cpuManagerStatePath = "var/lib/kubelet/cpu_manager_state"

	// migProfileCachePath records the MIG profile Apply was asked for.
	// Applying it as the node label the NVIDIA GPU operator watches needs a
	// Kubernetes client and this node's identity, neither of which Apply's
	// root-parameterized signature carries — deliberately, so it stays
	// testable without a real node or cluster (see the package doc). Apply's
	// job here is to record the desired value; the agent's Kubernetes-facing
	// loop reads it back with RecordedMIGProfile and writes the label (see
	// setNodeState in cmd/agent/main.go).
	migProfileCachePath = "run/frame-agent/mig-profile"
)

// Apply writes spec's node-local settings under root and reports whether any
// of them need a unit restart to take effect. A file that does not need to
// change is left untouched, and any node-only default (an unset optional
// field) is intentionally not restated — see NodeTuningSpec's field docs for
// why nil/empty means "untouched", not "off" or "reset to zero".
func Apply(root string, spec framev1beta1.NodeTuningSpec) (needsRestart bool, err error) {
	if spec.KSM != nil {
		changed, err := applyKSM(root, spec.KSM)
		if err != nil {
			return false, err
		}
		if changed {
			needsRestart = true
		}
	}

	if spec.CPUManagerPolicy != "" {
		changed, err := applyCPUManagerPolicy(root, spec.CPUManagerPolicy)
		if err != nil {
			return false, err
		}
		if changed {
			needsRestart = true
		}
	}

	if spec.TunedProfile != "" {
		if err := applyTunedProfile(root, spec.TunedProfile); err != nil {
			return false, err
		}
	}

	if spec.MIGProfile != "" {
		if err := recordMIGProfile(root, spec.MIGProfile); err != nil {
			return false, err
		}
	}

	return needsRestart, nil
}

// applyKSM writes the MemoryKSM= drop-in (restart-gated) and the sysfs
// scanner knobs (live) for ksm. It reports changed=true only for the
// drop-in: the scanner knobs never gate a restart, per the package doc.
func applyKSM(root string, ksm *framev1beta1.KSMSpec) (changed bool, err error) {
	unit, err := DetectKSMUnit(root)
	if err != nil {
		return false, fmt.Errorf("applying KSM: %w", err)
	}
	// Belt and braces: DetectKSMUnit can only ever return one of
	// ksmUnitCandidates, both of which are already on the allowlist, but the
	// drop-in path is about to be built from this value, and this is the
	// whole security boundary described in restartableUnits' doc — check it
	// again here rather than trust the caller above to have gotten it right.
	if !IsRestartable(unit) {
		return false, fmt.Errorf("applying KSM: detected unit %q is not on the restart allowlist", unit)
	}

	dropInPath := filepath.Join(root, fmt.Sprintf(ksmDropInDir, unit), ksmDropInFile)
	changed, err = writeFileIfChanged(dropInPath, ksmDropInContent(ksm.Enabled))
	if err != nil {
		return false, fmt.Errorf("writing KSM drop-in: %w", err)
	}

	// Scanner knobs apply live. Each is written independently and tolerates
	// a refusal: the kernel returns EBUSY on merge_across_nodes once any
	// page is already merged, and a writer that treated that as fatal would
	// fail on every apply cycle after the first — this was observed for
	// real on the live cluster by the ksm-tuner DaemonSet this design
	// replaces, which hit exactly this and tolerated it the same way (its
	// manifest was deploy/kubernetes/base/ksm-tuner/daemonset.yaml, removed
	// when this agent took over).
	writeSysfsKnobTolerant(filepath.Join(root, ksmRunPath), boolKnob(ksm.Enabled))
	if ksm.PagesToScan != nil {
		writeSysfsKnobTolerant(filepath.Join(root, ksmPagesToScanPath), strconv.Itoa(int(*ksm.PagesToScan)))
	}
	if ksm.SleepMillisecs != nil {
		writeSysfsKnobTolerant(filepath.Join(root, ksmSleepMillisecsPath), strconv.Itoa(int(*ksm.SleepMillisecs)))
	}
	if ksm.MergeAcrossNodes != nil {
		writeSysfsKnobTolerant(filepath.Join(root, ksmMergeAcrossNodesPath), boolKnob(*ksm.MergeAcrossNodes))
	}

	return changed, nil
}

// ksmDropInContent renders the systemd drop-in content for the given
// Enabled value. Byte-exact with what the ksm-tuner DaemonSet this design
// replaces wrote, and with what this package's tests write as a fixture —
// so an agent taking over from ksm-tuner on a live node rewrites nothing and
// demands no restart for a drop-in that is already correct.
func ksmDropInContent(enabled bool) string {
	value := "no"
	if enabled {
		value = "yes"
	}
	return fmt.Sprintf("[Service]\nMemoryKSM=%s\n", value)
}

// ksmUnitCandidates lists the systemd unit base names (without ".service")
// that might own containerd on a node, in the order DetectKSMUnit prefers
// them if — improbably — both exist on the same node. Workers run
// k3s-agent; the control-plane server runs k3s.
var ksmUnitCandidates = []string{"k3s-agent", "k3s"}

// ksmUnitSearchDirs are the root-relative directories a unit file can live
// under: the systemd package default and the local admin override tree —
// the same two locations the ksm-tuner DaemonSet this design replaces
// checked.
var ksmUnitSearchDirs = []string{"etc/systemd/system", "usr/lib/systemd/system"}

// DetectKSMUnit finds which of k3s-agent.service / k3s.service actually
// exists on the node under root, so the KSM drop-in lands on the unit that
// really owns containerd there. Hardcoding either one silently skips the
// other kind of node — confirmed on the live cluster, where the
// control-plane server runs k3s.service (not k3s-agent.service) and
// delivered most of KSM's measured benefit. k3s-agent is preferred if,
// improbably, both are present. A node with neither unit is not one this
// agent has any business writing a drop-in to, so that is a real error, not
// a silent no-op.
func DetectKSMUnit(root string) (string, error) {
	for _, name := range ksmUnitCandidates {
		for _, dir := range ksmUnitSearchDirs {
			if _, err := os.Stat(filepath.Join(root, dir, name+".service")); err == nil {
				return name, nil
			}
		}
	}
	return "", fmt.Errorf("no k3s or k3s-agent unit found under %s (checked %v in %v)", root, ksmUnitCandidates, ksmUnitSearchDirs)
}

// boolKnob renders a bool as the "1"/"0" vocabulary sysfs knobs expect,
// which is not the "yes"/"no" vocabulary systemd properties use (see
// readBool in observe.go for that one).
func boolKnob(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// applyCPUManagerPolicy writes the desired kubelet CPU manager policy and
// reports whether it changed. On change it also removes kubelet's persisted
// cpu_manager_state: a state file built under the old policy is invalid
// input to the new one, and kubelet must rebuild it from scratch after the
// restart this triggers (see the design doc's "needs a kubelet restart and
// removal of cpu_manager_state").
func applyCPUManagerPolicy(root, policy string) (changed bool, err error) {
	cachePath := filepath.Join(root, cpuManagerPolicyCachePath)
	changed, err = writeFileIfChanged(cachePath, policy)
	if err != nil {
		return false, fmt.Errorf("recording CPU manager policy: %w", err)
	}
	if !changed {
		return false, nil
	}

	statePath := filepath.Join(root, cpuManagerStatePath)
	if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("removing %s: %w", statePath, err)
	}
	return true, nil
}

// applyTunedProfile runs `tuned-adm profile <profile>` when the node's
// currently active profile disagrees with spec. Unlike the KSM drop-in and
// the CPU manager policy, this does not gate needsRestart: tuned profiles
// own sysctls, governor and scheduler, all of which apply live, and the
// design doc's own proof-of-effect for this setting is `tuned-adm active`
// agreeing, not a restart.
func applyTunedProfile(root, profile string) error {
	current, err := readString(filepath.Join(root, tunedActiveProfilePath))
	if err != nil {
		return fmt.Errorf("reading current tuned profile: %w", err)
	}
	if current == profile {
		return nil
	}

	cmd := exec.Command("tuned-adm", "profile", profile)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tuned-adm profile %s: %w: %s", profile, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RecordedMIGProfile returns the MIG profile the last Apply recorded under
// root, or "" when none was ever recorded. It is the read half of
// migProfileCachePath: the agent's Kubernetes-facing loop turns this into the
// node label the NVIDIA GPU operator watches (see cmd/agent/main.go), which
// is the step Apply deliberately cannot take.
//
// A missing file is "" and no error — a node no NodeTuning has ever asked for
// a MIG profile on is not a broken node.
func RecordedMIGProfile(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, migProfileCachePath))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading recorded MIG profile: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// recordMIGProfile persists the desired MIG profile under root. See
// migProfileCachePath's doc for why this does not itself reach the
// Kubernetes API.
func recordMIGProfile(root, profile string) error {
	path := filepath.Join(root, migProfileCachePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(profile), 0o644); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// writeFileIfChanged writes content to path only if the file does not
// already hold it byte-for-byte, and reports whether it wrote. A missing
// file counts as different from any non-empty content, so the first Apply
// on a fresh node always reports changed.
func writeFileIfChanged(path, content string) (changed bool, err error) {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if err == nil && string(existing) == content {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

// writeSysfsKnobTolerant writes value to a sysfs knob under root and
// swallows any error. The kernel refuses some KSM knobs outright once the
// setting they configure has already taken effect (merge_across_nodes
// returns EBUSY once any page is merged), so a writer that treated every
// refusal as fatal would fail on every apply cycle after the first, forever,
// on a real cluster — this was observed for real and is why every scanner
// knob is written individually rather than as a batch that aborts on the
// first error. It still creates the parent directory so tests that run
// without a real /sys tree behave the same as a live node, where the
// directory already exists and MkdirAll on an existing directory is a no-op.
func writeSysfsKnobTolerant(path, value string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(value), 0o644)
}

// restartableUnits is the compile-time allowlist of systemd units the agent
// may ever be told to restart. This is the agent's entire security boundary
// (see the package doc and the design doc's Security section): the agent
// runs privileged with hostPID, which is host-root-equivalent, so once a
// restart target is reachable from the CRD or from any argument a spec could
// influence, "restart a unit" is arbitrary root on every node. It is
// therefore a fixed map literal with no path from NodeTuningSpec, or from
// anything else in this package, to its contents.
var restartableUnits = map[string]bool{
	"k3s":        true,
	"k3s-agent":  true,
	"kubelet":    true,
	"containerd": true,
}

// IsRestartable reports whether unit is on the compile-time allowlist Task 6
// checks before scheduling any restart. It is a plain map lookup rather than
// a prefix/pattern match on purpose: "../k3s" or any other unit that merely
// contains an allowlisted name must not pass.
func IsRestartable(unit string) bool {
	return restartableUnits[unit]
}
