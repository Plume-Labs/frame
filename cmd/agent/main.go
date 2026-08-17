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
// that reports the node's measured tuning state. This build is observe-only
// (see docs/superpowers/sdd/2026-08-17-node-tuning/task-2-brief.md) — it
// applies nothing and restarts nothing; that is Task 3.
//
// It resolves its own node from the downward API (NODE_NAME), calls
// agent.Observe every observeInterval, and patches status.nodes[] on every
// NodeTuning whose spec.nodeSelector matches this node's labels with what it
// measured. It never writes status.nodes[].phase — that is the controller's
// job (Task 4), computed by diffing spec against what this agent reports
// here.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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

	ctx := ctrl.SetupSignalHandler()
	slog.Info("frame-agent starting", "node", nodeName, "root", root, "interval", observeInterval)

	// Observe once immediately rather than waiting out the first tick, so a
	// freshly (re)started agent reports current state right away instead of
	// leaving status.nodes stale for up to observeInterval.
	observeAndReport(ctx, kc, nodeName, root)

	ticker := time.NewTicker(observeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			observeAndReport(ctx, kc, nodeName, root)
		}
	}
}

// observeAndReport measures the node's current tuning state and patches it
// into every NodeTuning that selects this node. Every failure is logged and
// swallowed rather than returned: this is a periodic loop, and one bad tick
// (a transient API server error, an unparsable selector on one object) must
// not stop the agent from reporting on the next tick or on the other
// objects.
func observeAndReport(ctx context.Context, kc client.Client, nodeName, root string) {
	// Refresh the systemd cache before every Observe, so MemoryKSM reflects
	// what systemd currently reports rather than a value nothing ever
	// updates. This has to run here, not in agent.Apply: Apply only writes
	// the drop-in, which systemd does not pick up until the unit's next
	// start, so querying systemd from inside Apply would just re-cache the
	// stale value and prove nothing changed. A node with no k3s unit at all
	// is logged and skipped rather than treated as fatal — the same
	// tolerant-tick discipline as everything else in this function.
	if unit, err := agent.DetectKSMUnit(root); err != nil {
		slog.Error("detecting k3s unit", "error", err)
	} else if err := agent.RefreshSystemdCache(root, unit, agent.ExecCommandRunner{}); err != nil {
		slog.Error("refreshing systemd cache", "unit", unit, "error", err)
	}

	observed, err := agent.Observe(root)
	if err != nil {
		slog.Error("observing node state", "error", err)
		return
	}

	var node corev1.Node
	if err := kc.Get(ctx, client.ObjectKey{Name: nodeName}, &node); err != nil {
		slog.Error("fetching own node", "node", nodeName, "error", err)
		return
	}

	var list framev1beta1.NodeTuningList
	if err := kc.List(ctx, &list); err != nil {
		slog.Error("listing NodeTunings", "error", err)
		return
	}

	for i := range list.Items {
		nt := &list.Items[i]
		matches, err := selectorMatches(nt.Spec.NodeSelector, node.Labels)
		if err != nil {
			slog.Error("invalid nodeSelector", "nodeTuning", nt.Name, "error", err)
			continue
		}
		if !matches {
			continue
		}
		if err := patchObserved(ctx, kc, nt, nodeName, observed); err != nil {
			slog.Error("patching NodeTuning status", "nodeTuning", nt.Name, "node", nodeName, "error", err)
		}
	}
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

// patchObserved upserts this node's Observed entry in nt.Status.Nodes. It
// only ever writes the Observed field — Phase, Realization and the rest of
// NodeTuningNodeStatus are the controller's to compute (Task 4+), from
// exactly this data. Upserting rather than requiring an existing entry keeps
// the agent independently useful before the controller exists at all, and
// resilient if a NodeTuning is created while an agent is already running.
func patchObserved(ctx context.Context, kc client.Client, nt *framev1beta1.NodeTuning, nodeName string, observed framev1beta1.ObservedTuning) error {
	base := nt.DeepCopy()

	found := false
	for i := range nt.Status.Nodes {
		if nt.Status.Nodes[i].Name == nodeName {
			nt.Status.Nodes[i].Observed = observed
			found = true
			break
		}
	}
	if !found {
		nt.Status.Nodes = append(nt.Status.Nodes, framev1beta1.NodeTuningNodeStatus{
			Name:     nodeName,
			Observed: observed,
		})
	}

	return kc.Status().Patch(ctx, nt, client.MergeFrom(base))
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
