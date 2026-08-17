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

// Command agent is the Frame node agent: a privileged, hostPID DaemonSet pod
// that applies and reports the node's tuning state, and services the
// controller's restart requests.
//
// It resolves its own node from the downward API (NODE_NAME) and, every
// observeInterval:
//
//  1. applies the node-local half of whichever NodeTuning selects this node
//     (agent.Apply — the systemd drop-in, the KSM scanner knobs, the tuned
//     profile, the recorded MIG profile);
//  2. projects the recorded MIG profile onto the node label the NVIDIA GPU
//     operator watches, which is the one setting Frame declares but does not
//     itself apply;
//  3. publishes the systemd unit it detected and that unit's
//     ActiveEnterTimestamp as Node annotations — the agent's half of the
//     restart protocol in internal/controller/frame/nodetuning_rollout.go;
//  4. services a restart request from the controller by scheduling a
//     *detached* restart, never an inline one;
//  5. measures what the node actually reports (agent.Observe) and patches it
//     into status.nodes[].observed on every NodeTuning that selects it.
//
// It never writes status.nodes[].phase — that is the controller's, computed
// by diffing spec against what this agent reports.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/agent"
)

// observeInterval is fixed at compile time rather than configurable: the
// agent is privileged and hostPID, so its behavior should be auditable from
// the binary alone, not from a value that can be changed via a Pod env var
// without a new image.
const observeInterval = 30 * time.Second

// migConfigLabel is the node label the NVIDIA GPU operator's mig-manager
// watches. Frame declares which MIG profile a node runs and then hands the
// work to the operator, which already owns draining GPU clients, toggling MIG
// mode and re-creating the instances. Reimplementing that here would be a
// second writer on the same hardware.
const migConfigLabel = "nvidia.com/mig.config"

// restartRequestCachePath records the value of the controller's restart
// request annotation that this agent last acted on.
//
// On disk, not in memory, and this matters: acting on the request restarts
// k3s, which restarts this very pod. An in-process latch would be empty again
// moments later while the request annotation is still on the node (the
// controller only clears it once it has verified the restart and uncordoned),
// so the agent would schedule a second restart of a node that is already
// restarting — and then a third. Under /run, so it is tmpfs on the node and a
// full node reboot forgets it, which is correct: after a reboot the unit's
// ActiveEnterTimestamp has already moved past the controller's baseline.
const restartRequestCachePath = "run/frame-agent/restart-request"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "frame-agent:", err)
		os.Exit(1)
	}
}

func run() error {
	nodeName, err := requiredEnv("NODE_NAME")
	if err != nil {
		return err
	}
	root := envOrDefault("FRAME_AGENT_ROOT", "/host")

	scheme := clientgoscheme.Scheme
	if err := framev1beta1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("registering scheme: %w", err)
	}
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	kc, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("building Kubernetes client: %w", err)
	}

	// Every systemd command runs in PID 1's namespaces. A `systemctl` run
	// inside this container would be interrogating and restarting a systemd
	// that does not exist — see HostCommandRunner.
	runner := agent.HostCommandRunner{}

	ctx := ctrl.SetupSignalHandler()
	slog.Info("frame-agent starting", "node", nodeName, "root", root, "interval", observeInterval)

	// Tick once immediately rather than waiting out the first interval, so a
	// freshly (re)started agent reports current state right away instead of
	// leaving status.nodes stale — and so a restart request that arrived
	// while it was down is serviced now rather than in 30 seconds.
	tick(ctx, kc, nodeName, root, runner)

	ticker := time.NewTicker(observeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			tick(ctx, kc, nodeName, root, runner)
		}
	}
}

// tick runs one full pass: apply, publish, service the restart request,
// observe, report. Every failure is logged and swallowed rather than
// returned: this is a periodic loop, and one bad tick (a transient API server
// error, an unparsable selector on one object, a node with no k3s unit) must
// not stop the agent from doing the rest of its work or from trying again on
// the next tick.
func tick(ctx context.Context, kc client.Client, nodeName, root string, runner agent.CommandRunner) {
	var node corev1.Node
	if err := kc.Get(ctx, client.ObjectKey{Name: nodeName}, &node); err != nil {
		// Nothing below is meaningful without the node: the selectors are
		// matched against its labels and the restart protocol lives on its
		// annotations.
		slog.Error("fetching own node", "node", nodeName, "error", err)
		return
	}

	var list framev1beta1.NodeTuningList
	if err := kc.List(ctx, &list); err != nil {
		slog.Error("listing NodeTunings", "error", err)
		return
	}
	matching := selecting(list.Items, node.Labels)

	applyMatched(root, matching, runner)

	// The systemd-facing half. A node with no k3s unit at all is logged and
	// skipped, not treated as fatal: it is a node this agent has no business
	// writing a drop-in to, and it still has an Observe worth reporting.
	if unit, err := agent.DetectKSMUnit(root); err != nil {
		slog.Error("detecting k3s unit", "error", err)
	} else {
		// Refresh the MemoryKSM cache before Observe, so it reflects what
		// systemd currently reports rather than a value nothing ever updates.
		// This cannot live in agent.Apply: Apply only writes the drop-in,
		// which systemd does not pick up until the unit's next start, so
		// querying systemd from inside Apply would just re-cache the stale
		// value and prove nothing changed.
		if err := agent.RefreshSystemdCache(root, unit, runner); err != nil {
			slog.Error("refreshing systemd cache", "unit", unit, "error", err)
		}
		publishNodeState(ctx, kc, &node, root, unit, runner)
		serviceRestartRequest(&node, root, unit, runner)
	}

	observed, err := agent.Observe(root)
	if err != nil {
		slog.Error("observing node state", "error", err)
		return
	}
	for _, nt := range matching {
		// nt comes from the List above, and everything between it and here —
		// Apply, the systemd round-trips, Observe — happens on the node, in
		// seconds. agent.PatchObserved therefore re-reads the object rather
		// than patching from this snapshot: the controller may have written
		// the node's restart record in the meantime, and a merge patch built
		// from a stale copy would revert it. See its doc comment.
		if err := agent.PatchObserved(ctx, kc, nt, nodeName, observed); err != nil {
			slog.Error("patching NodeTuning status", "nodeTuning", nt.Name, "node", nodeName, "error", err)
		}
	}
}

// selecting returns the NodeTunings whose spec.nodeSelector matches this
// node's labels. An object with an unparsable selector is logged and skipped
// rather than aborting the tick: one broken object must not stop the agent
// reporting on every other one.
func selecting(items []framev1beta1.NodeTuning, nodeLabels map[string]string) []*framev1beta1.NodeTuning {
	var matching []*framev1beta1.NodeTuning
	for i := range items {
		nt := &items[i]
		matches, err := selectorMatches(nt.Spec.NodeSelector, nodeLabels)
		if err != nil {
			slog.Error("invalid nodeSelector", "nodeTuning", nt.Name, "error", err)
			continue
		}
		if matches {
			matching = append(matching, nt)
		}
	}
	return matching
}

// applyMatched writes the node-local half of the one NodeTuning that selects
// this node.
//
// Exactly one, or none: two objects selecting the same node is a
// configuration error the controller refuses to act on (it reports Failed and
// applies nothing), and the agent has to refuse identically. Applying both
// would let two specs take turns writing the same drop-in every 30 seconds,
// with the node's effective configuration decided by whichever object the API
// server happened to list last.
func applyMatched(root string, matching []*framev1beta1.NodeTuning, runner agent.CommandRunner) {
	if len(matching) > 1 {
		names := make([]string, 0, len(matching))
		for _, nt := range matching {
			names = append(names, nt.Name)
		}
		slog.Error("refusing to apply: node selected by more than one NodeTuning",
			"nodeTunings", strings.Join(names, ","))
		return
	}
	if len(matching) == 0 {
		return
	}

	// needsRestart is deliberately discarded here. The agent never decides
	// when a restart happens — it writes the state and answers when asked;
	// the controller owns approval, cordon, drain and the request (see
	// internal/controller/frame/nodetuning_rollout.go).
	//
	// The runner is passed through because tuned runs on the node, not in
	// this container (see agent.Apply): a `tuned-adm` that lands inside the
	// pod configures nothing the node can see and leaves it Drifted forever.
	if _, err := agent.Apply(root, matching[0].Spec, runner); err != nil {
		slog.Error("applying node tuning", "nodeTuning", matching[0].Name, "error", err)
	}
}

// publishNodeState writes the agent's half of the restart protocol onto the
// Node: the systemd unit it detected, that unit's ActiveEnterTimestamp, and
// the MIG profile label.
//
// The controller cannot start a rollout without both annotations — it refuses
// to cordon a node whose restart could not afterwards be verified — so this
// is what unblocks the whole lifecycle, and it is published on every tick
// rather than once, because the timestamp is exactly the value that has to
// change for a restart to count as done.
func publishNodeState(ctx context.Context, kc client.Client, node *corev1.Node, root, unit string, runner agent.CommandRunner) {
	migProfile, err := agent.RecordedMIGProfile(root)
	if err != nil {
		slog.Error("reading recorded MIG profile", "error", err)
	}

	// A unit that has never been active has no timestamp to publish. Skipped
	// rather than published as a zero value: the controller compares a later
	// reading against this one to decide whether a restart happened, and a
	// fabricated baseline would make any reading at all look like a restart.
	activeEnter, err := agent.UnitActiveEnterTimestamp(unit, runner)
	if err != nil {
		slog.Error("reading unit ActiveEnterTimestamp", "unit", unit, "error", err)
	}

	base := node.DeepCopy()
	setNodeState(node, unit, activeEnter, migProfile)
	if equalNodeMetadata(base, node) {
		return
	}
	if err := kc.Patch(ctx, node, client.MergeFrom(base)); err != nil {
		slog.Error("publishing node tuning state", "node", node.Name, "error", err)
	}
}

// setNodeState applies the agent-owned annotations and labels to node. Split
// out from the patch so the protocol's exact keys and value formats are
// testable without an API server: RFC3339Nano is not decoration, it is what
// the controller parses (annotationTime in nodetuning_rollout.go), and a
// value in any other format reads to it as "no timestamp yet" forever.
//
// A zero activeEnter means the unit has never been active; the previously
// published annotation is then left exactly as it was rather than deleted —
// the last known ActiveEnterTimestamp is still the truth about the last time
// the unit started.
func setNodeState(node *corev1.Node, unit string, activeEnter time.Time, migProfile string) {
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	node.Annotations[framev1beta1.TuningUnitAnnotation] = unit
	if !activeEnter.IsZero() {
		node.Annotations[framev1beta1.TuningUnitActiveEnterAnnotation] = activeEnter.UTC().Format(time.RFC3339Nano)
	}

	// Only ever set, never cleared: removing the label would tell the GPU
	// operator to leave MIG at whatever it currently is, but Frame no longer
	// declaring a profile is not the same statement as "revert", and guessing
	// which one was meant would reconfigure GPUs nobody asked to touch.
	if migProfile != "" {
		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		node.Labels[migConfigLabel] = migProfile
	}
}

// equalNodeMetadata reports whether two versions of a node carry identical
// labels and annotations. Nothing else about the node is ever written here,
// so this is what decides "is there anything to patch" — and skipping the
// no-op patch matters: this runs on every node every 30 seconds, and a write
// per tick per node is a write the API server has to serve, a watch event
// every NodeTuning has to be re-reconciled for, and an audit entry, all for a
// value that did not change.
func equalNodeMetadata(a, b *corev1.Node) bool {
	return equalStringMaps(a.Annotations, b.Annotations) && equalStringMaps(a.Labels, b.Labels)
}

func equalStringMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// serviceRestartRequest schedules the detached restart the controller asked
// for, if it asked for one this agent has not already acted on.
//
// It compares the request annotation's VALUE, never its presence. The
// controller re-issues the request — with a new timestamp — when a halted
// rollout is released by hand, and it clears the old one when a rollout
// starts. An agent keyed on "the annotation exists" would either replay the
// original request forever or, having latched a boolean, never act on the
// re-issued one; either way a released rollout hangs until it times out.
//
// The unit comes from DetectKSMUnit, not from the annotation — the controller
// deliberately does not name one — but it is still passed through
// ScheduleDetachedRestart's allowlist check. This function is reached from
// data the controller writes onto a Node, and this agent runs privileged with
// hostPID: a restart target that anything outside internal/agent can steer is
// arbitrary root on every node in the cluster.
//
// It does not additionally demand the rollout-started annotation. Writing any
// of these annotations requires `patch` on nodes, which is already enough to
// cordon and taint the node outright, so a second annotation gate would add
// ceremony rather than authority. The boundary that matters is *which* unit
// may be restarted, and that one is compiled in.
func serviceRestartRequest(node *corev1.Node, root, unit string, runner agent.CommandRunner) {
	requested := node.Annotations[framev1beta1.TuningRestartRequestedAnnotation]
	if requested == "" {
		return
	}
	if requested == lastActedRequest(root) {
		return
	}

	if err := agent.ScheduleDetachedRestart(unit, 0, runner); err != nil {
		// Not recorded, so the next tick tries again. Recording first and
		// then failing would swallow the request permanently: the controller
		// would wait out its restart timeout and halt the whole campaign over
		// a transient systemd-run failure.
		slog.Error("scheduling detached restart", "unit", unit, "node", node.Name, "error", err)
		return
	}
	if err := recordActedRequest(root, requested); err != nil {
		// The restart is already armed. Failing to record it means the next
		// tick may arm it again, which is why this is loud.
		slog.Error("recording the serviced restart request", "node", node.Name, "error", err)
	}
	slog.Info("scheduled a detached restart", "unit", unit, "node", node.Name, "request", requested)
}

// lastActedRequest returns the request value this agent last acted on, or ""
// if it never did (or the record is unreadable, which fails towards acting:
// a missed restart halts a rollout, while a duplicate one is a restart of a
// node that is already cordoned and drained for exactly that).
func lastActedRequest(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, restartRequestCachePath))
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("reading the last serviced restart request", "error", err)
		}
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func recordActedRequest(root, value string) error {
	path := filepath.Join(root, restartRequestCachePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// selectorMatches reports whether labelSet satisfies sel. A nil selector
// matches no nodes, mirroring NodeTuningSpec.NodeSelector's documented
// behavior: a NodeTuning created without one is inert, not fleet-wide by
// accident.
func selectorMatches(sel *metav1.LabelSelector, labelSet map[string]string) (bool, error) {
	if sel == nil {
		return false, nil
	}
	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return false, err
	}
	return selector.Matches(labels.Set(labelSet)), nil
}

func requiredEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
