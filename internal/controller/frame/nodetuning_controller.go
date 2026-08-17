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

package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// NodeTuningReconciler reconciles a NodeTuning object.
//
// This is the diff-and-report half of the lifecycle (see the design doc's
// "Controller" section): it lists the nodes a NodeTuning selects, detects
// selector overlap with every other NodeTuning, and otherwise diffs
// spec against status.nodes[].observed to write InSync or Drifted. It never
// cordons, drains, or restarts anything — that is Task 5 (approval) and
// Task 6 (the restart itself), layered on top of the phases this reconciler
// writes.
type NodeTuningReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=nodetunings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=nodetunings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=nodetunings/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

func (r *NodeTuningReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var nt framev1beta1.NodeTuning
	if err := r.Get(ctx, req.NamespacedName, &nt); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// A nil selector matches no nodes rather than every node (see
	// NodeTuningSpec.NodeSelector's doc comment), so an object created
	// without one is inert instead of fleet-wide by accident.
	matched, err := r.matchingNodes(ctx, nt.Spec.NodeSelector)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing nodes for selector: %w", err)
	}

	var all framev1beta1.NodeTuningList
	if err := r.List(ctx, &all); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing NodeTunings: %w", err)
	}

	patch := client.MergeFrom(nt.DeepCopy())
	nt.Status.ObservedGeneration = nt.Generation

	newNodes := make([]framev1beta1.NodeTuningNodeStatus, 0, len(matched))
	for _, node := range matched {
		ns := existingNodeStatus(nt.Status.Nodes, node.Name)

		if others := overlappingNodeTunings(all.Items, nt.Name, node); len(others) > 0 {
			// Overlapping selectors are a configuration error, not a merge:
			// silently picking a winner (by name, by age, by anything) makes
			// the effective configuration of a node unpredictable, which is
			// worse than refusing. Nothing is applied and nothing about the
			// node's Observed/AppliedGeneration/RestartedAt is touched.
			ns.Phase = framev1beta1.PhaseFailed
			ns.Message = fmt.Sprintf("node also selected by NodeTuning %s", strings.Join(others, ", "))
		} else if reason := diffTuning(nt.Spec, ns.Observed); reason != "" {
			// Diff against what the agent measured (status.nodes[].observed),
			// never against what another NodeTuning intended: Observed is the
			// only value ever actually verified on the node.
			ns.Phase = framev1beta1.PhaseDrifted
			ns.AppliedGeneration = nt.Generation
			ns.Message = reason
		} else {
			ns.Phase = framev1beta1.PhaseInSync
			ns.AppliedGeneration = nt.Generation
			ns.Message = ""
		}
		newNodes = append(newNodes, ns)
	}
	sort.Slice(newNodes, func(i, j int) bool { return newNodes[i].Name < newNodes[j].Name })
	nt.Status.Nodes = newNodes

	if err := r.Status().Patch(ctx, &nt, patch); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Reconciled NodeTuning", "matchedNodes", len(matched))
	return ctrl.Result{}, nil
}

// matchingNodes returns the corev1.Nodes selected by sel, or none if sel is nil.
func (r *NodeTuningReconciler) matchingNodes(ctx context.Context, sel *metav1.LabelSelector) ([]corev1.Node, error) {
	if sel == nil {
		return nil, nil
	}
	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return nil, fmt.Errorf("invalid nodeSelector: %w", err)
	}
	var list corev1.NodeList
	if err := r.List(ctx, &list, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// existingNodeStatus returns the prior status entry for nodeName, or a fresh
// zero-value one. Observed and RestartedAt are the agent's and Task 6's
// fields respectively — this reconciler never writes them, so whatever was
// already there is carried forward unchanged.
func existingNodeStatus(nodes []framev1beta1.NodeTuningNodeStatus, nodeName string) framev1beta1.NodeTuningNodeStatus {
	for _, n := range nodes {
		if n.Name == nodeName {
			return n
		}
	}
	return framev1beta1.NodeTuningNodeStatus{Name: nodeName}
}

// overlappingNodeTunings returns the names (sorted) of every other NodeTuning
// whose selector also matches node, excluding selfName.
func overlappingNodeTunings(all []framev1beta1.NodeTuning, selfName string, node corev1.Node) []string {
	var others []string
	for _, other := range all {
		if other.Name == selfName || other.Spec.NodeSelector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(other.Spec.NodeSelector)
		if err != nil {
			continue
		}
		if sel.Matches(labels.Set(node.Labels)) {
			others = append(others, other.Name)
		}
	}
	sort.Strings(others)
	return others
}

// diffTuning compares the fields NodeTuningSpec declares against what the
// agent actually measured (observed), and returns a human-readable reason
// for the first mismatch found, or "" if everything the spec cares about
// matches. It never compares against another NodeTuning's spec and never
// treats a field the spec leaves unset as a mismatch: KSM's Enabled default
// aside, an unset field means "untouched", not "must equal the zero value".
//
// MIGProfile is deliberately not diffed here: ObservedTuning carries no MIG
// field (see the design doc — MIG's proof of effect is the GPU operator's
// own node status, not something the agent reads back), so there is nothing
// in Observed to diff it against yet.
func diffTuning(spec framev1beta1.NodeTuningSpec, observed framev1beta1.ObservedTuning) string {
	var mismatches []string

	if spec.TunedProfile != "" && spec.TunedProfile != observed.TunedProfile {
		mismatches = append(mismatches, fmt.Sprintf("tunedProfile: want %q, observed %q", spec.TunedProfile, observed.TunedProfile))
	}

	if spec.CPUManagerPolicy != "" && spec.CPUManagerPolicy != observed.CPUManagerPolicy {
		mismatches = append(mismatches, fmt.Sprintf("cpuManagerPolicy: want %q, observed %q", spec.CPUManagerPolicy, observed.CPUManagerPolicy))
	}

	if spec.KSM != nil {
		wantEnabled := spec.KSM.Enabled
		gotEnabled := observed.KSM != nil && observed.KSM.MemoryKSM
		if wantEnabled != gotEnabled {
			mismatches = append(mismatches, fmt.Sprintf("ksm.enabled: want %t, observed %t", wantEnabled, gotEnabled))
		}
	}

	return strings.Join(mismatches, "; ")
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeTuningReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.NodeTuning{}).
		// A Node's labels changing, joining, or leaving can change which
		// NodeTuning(s) select it, so every NodeTuning must be re-evaluated.
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.allNodeTunings)).
		// Overlap is only detectable by looking at every other NodeTuning:
		// creating "b" is what turns "a" Failed, so "a" needs to be
		// re-reconciled when any sibling object changes, not just itself.
		Watches(&framev1beta1.NodeTuning{}, handler.EnqueueRequestsFromMapFunc(r.allNodeTunings)).
		Named("nodetuning").
		Complete(r)
}

func (r *NodeTuningReconciler) allNodeTunings(ctx context.Context, _ client.Object) []reconcile.Request {
	var list framev1beta1.NodeTuningList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, nt := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&nt)})
	}
	return reqs
}
