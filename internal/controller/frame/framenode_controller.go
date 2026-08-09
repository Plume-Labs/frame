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
	"strings"
	"time"

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	storageapi "github.com/siderolabs/talos/pkg/machinery/api/storage"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

const frameNodeFinalizer = "frame.plume-labs.io/framenode"

// FrameNode lifecycle phases.
const (
	// nodePhaseDiscovered means the maintenance API has been contacted (or an
	// attempt was made) and status.discoveredDisks/discoveredTalosVersion are
	// populated; the node is waiting for a user to fill in the full spec.
	nodePhaseDiscovered = "Discovered"
	// nodePhaseOnline means the corresponding Kubernetes Node is Ready.
	nodePhaseOnline = "Online"
)

// nodePhaseFromStatus is the FrameNode reconciler's state-machine input.
//
// It used to read fn.Status.Phase. That field is gone (F2), and the Ready
// condition's reason has always carried the same value — the controller set
// Reason: phase alongside it — so this is a read of the same information
// from the place it now lives. It is deliberately not the conversion
// package's FrameNodePhaseFromConditions: controllers work on the hub and
// must not depend on a spoke's projection, which answers "" for any reason
// outside v1alpha1's phase enum precisely because it exists to feed a
// v1alpha1 reader, not to drive this state machine.
func nodePhaseFromStatus(fn *framev1beta1.FrameNode) string {
	return readyReason(fn.Status.Conditions)
}

// FrameNodeReconciler reconciles a FrameNode object
type FrameNodeReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framenodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framenodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framenodes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;patch;update

func (r *FrameNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fn framev1beta1.FrameNode
	if err := r.Get(ctx, req.NamespacedName, &fn); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !fn.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &fn)
	}

	if !controllerutil.ContainsFinalizer(&fn, frameNodeFinalizer) {
		controllerutil.AddFinalizer(&fn, frameNodeFinalizer)
		return ctrl.Result{}, r.Update(ctx, &fn)
	}

	// Phase 1: disk not yet set → contact maintenance API to discover disks/version.
	if fn.Spec.Disk == "" {
		return r.reconcileDiscovery(ctx, &fn)
	}

	// Phase 2: full spec present, not yet provisioned → apply machineConfig.
	if p := nodePhaseFromStatus(&fn); p == "" || p == nodePhaseDiscovered {
		return r.reconcileProvision(ctx, &fn)
	}

	// Phase 3: node should have joined Kubernetes → sync labels/status.
	return r.reconcileOnline(ctx, &fn)
}

// reconcileDiscovery contacts the node's Talos maintenance API (port 50000) and
// populates status.discoveredDisks / discoveredTalosVersion. Even if the call
// fails (node not yet reachable) the phase advances to Discovered so the UI can
// let the user fill in spec manually.
func (r *FrameNodeReconciler) reconcileDiscovery(ctx context.Context, fn *framev1beta1.FrameNode) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if nodePhaseFromStatus(fn) == nodePhaseDiscovered {
		// Waiting for user to patch spec with full disk/network/role info.
		// The controller has still looked at this generation even though
		// there's nothing else to do with it, so observedGeneration must
		// still catch up — otherwise a spec-only edit that never sets Disk
		// leaves it stuck behind metadata.generation indefinitely.
		if fn.Status.ObservedGeneration != fn.Generation {
			patch := client.MergeFrom(fn.DeepCopy())
			fn.Status.ObservedGeneration = fn.Generation
			return ctrl.Result{}, r.Status().Patch(ctx, fn, patch)
		}
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(fn.DeepCopy())
	fn.Status.ObservedGeneration = fn.Generation
	fn.Status.DiscoveredDisks = nil
	fn.Status.DiscoveredNICs = nil

	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	c, err := buildTalosInsecureClient(tctx, fn.Spec.IP)
	if err != nil {
		log.Info("Cannot build maintenance client", "ip", fn.Spec.IP, "err", err)
		r.Recorder.Event(fn, corev1.EventTypeWarning, "DiscoveryFailed", fmt.Sprintf("Cannot reach %s: %v", fn.Spec.IP, err))
	} else {
		defer c.Close() //nolint:errcheck // discovery already ran; a failed close of the maintenance-mode client isn't actionable
		r.populateDiscovery(tctx, fn, c)
		r.Recorder.Event(fn, corev1.EventTypeNormal, nodePhaseDiscovered, "Maintenance API contacted")
	}

	meta.SetStatusCondition(&fn.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             nodePhaseDiscovered,
		Message:            "Discovery complete; waiting for spec",
		ObservedGeneration: fn.Generation,
	})
	return ctrl.Result{}, r.Status().Patch(ctx, fn, patch)
}

type talosDiscoveryClient interface {
	Version(context.Context, ...grpc.CallOption) (*machineapi.VersionResponse, error)
	Disks(context.Context, ...grpc.CallOption) (*storageapi.DisksResponse, error)
}

func (r *FrameNodeReconciler) populateDiscovery(ctx context.Context, fn *framev1beta1.FrameNode, c talosDiscoveryClient) {
	log := logf.FromContext(ctx)

	if vr, err := c.Version(ctx); err == nil && len(vr.Messages) > 0 && vr.Messages[0].Version != nil {
		fn.Status.DiscoveredTalosVersion = vr.Messages[0].Version.Tag
	} else if err != nil {
		log.Info("Version call failed", "err", err)
	}

	dr, err := c.Disks(ctx)
	if err != nil {
		log.Info("Disk discovery unavailable in maintenance mode", "err", err)
		return
	}
	for _, msg := range dr.Messages {
		for _, d := range msg.Disks {
			fn.Status.DiscoveredDisks = append(fn.Status.DiscoveredDisks, framev1beta1.DiskInfo{
				Name: "/dev/" + d.DeviceName,
				Size: humanizeBytes(d.Size),
				Type: diskTypeStr(d.Type),
			})
		}
	}
}

// reconcileProvision generates a Talos machineConfig and applies it via the maintenance API
// (port 50000, unauthenticated). Whether the call succeeds or not, phase becomes Provisioning
// so that reconcileOnline can start watching for the k8s Node to appear.
func (r *FrameNodeReconciler) reconcileProvision(ctx context.Context, fn *framev1beta1.FrameNode) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 3s is enough for a LAN node and keeps envtest tests fast on unreachable hosts.
	tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	msg := "Config applied; waiting for node to join cluster"

	c, err := buildTalosInsecureClient(tctx, fn.Spec.IP)
	if err != nil {
		r.Recorder.Event(fn, corev1.EventTypeWarning, "ClientBuildFailed", err.Error())
		log.Info("Cannot reach node for provisioning", "ip", fn.Spec.IP, "err", err)
		msg = "Waiting to apply config: " + err.Error()
	} else {
		defer c.Close() //nolint:errcheck // config apply already ran; a failed close of the maintenance-mode client isn't actionable
		if _, aerr := c.ApplyConfiguration(tctx, &machineapi.ApplyConfigurationRequest{
			Data: []byte(generateMachineConfig(fn)),
			Mode: machineapi.ApplyConfigurationRequest_AUTO,
		}); aerr != nil {
			r.Recorder.Event(fn, corev1.EventTypeWarning, "ApplyFailed", aerr.Error())
			log.Info("ApplyConfiguration failed", "err", aerr)
			msg = "Waiting to apply config: " + aerr.Error()
		} else {
			r.Recorder.Event(fn, corev1.EventTypeNormal, "ConfigApplied", "MachineConfig applied; node is rebooting")
		}
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setPhase(ctx, fn, "Provisioning", msg)
}

// The labels Frame projects from a FrameNode onto its corev1.Node. These are
// API, not an implementation detail: the inference provider's NodeSelector
// and the FrameJob controller's Workflow labels both read
// nodeLabelServiceClass, so renaming one of these silently unschedules
// everything that selects on it (F12).
//
// rack is under frame.plume-labs.io, not topology.kubernetes.io: the
// well-known keys in the kubernetes.io namespace are zone and region, and
// rack is not one of them — that prefix is reserved for upstream.
//
// A caution for anyone reading nodeLabelServiceClass and generalising: the
// same key means something else on a Namespace, where
// FrameResourceQuota uses it to select which namespaces a quota projects
// into. Two meanings, one key, distinguished only by what it is attached to.
const (
	nodeLabelRack         = "frame.plume-labs.io/rack"
	nodeLabelZone         = "topology.kubernetes.io/zone"
	nodeLabelServiceClass = "frame.plume-labs.io/service-class"
	nodeLabelRole         = "frame.plume-labs.io/role"
	nodeLabelRDMA         = "frame.plume-labs.io/rdma"
)

// frameNodeLabels is every key reconcileDelete strips. Keeping the list in
// one place is what stops a fifth label being added to the write path and
// forgotten in the delete path.
var frameNodeLabels = []string{
	nodeLabelRack, nodeLabelZone, nodeLabelServiceClass, nodeLabelRole, nodeLabelRDMA,
}

// applyNodeLabel sets key when value is non-empty and removes it otherwise.
// Writing an empty value is legal but makes "unclassified" and "explicitly
// empty" indistinguishable to a selector, which is not a distinction Frame
// wants to have to explain.
func applyNodeLabel(labels map[string]string, key, value string) {
	if value == "" {
		delete(labels, key)
		return
	}
	labels[key] = value
}

// reconcileOnline syncs Kubernetes Node readiness back into the FrameNode status.
func (r *FrameNodeReconciler) reconcileOnline(ctx context.Context, fn *framev1beta1.FrameNode) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	nodeName := fn.Spec.Hostname
	if nodeName == "" {
		nodeName = fn.Name
	}

	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Node not yet joined", "nodeName", nodeName)
			r.Recorder.Event(fn, corev1.EventTypeWarning, "NodeNotFound", "Waiting for node "+nodeName+" to join the cluster")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setPhase(ctx, fn, "Provisioning", "Waiting for node to join the cluster")
		}
		return ctrl.Result{}, err
	}

	base := node.DeepCopy()
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	applyNodeLabel(node.Labels, nodeLabelRack, fn.Spec.Rack)
	applyNodeLabel(node.Labels, nodeLabelZone, fn.Spec.Zone)
	applyNodeLabel(node.Labels, nodeLabelServiceClass, string(fn.Spec.ServiceClass))
	applyNodeLabel(node.Labels, nodeLabelRole, fn.Spec.Role)
	rdma := ""
	if fn.Spec.RDMAInterface != "" {
		rdma = "true"
	}
	applyNodeLabel(node.Labels, nodeLabelRDMA, rdma)
	// The old topology.kubernetes.io/rack key is removed here as well as in
	// reconcileDelete, so an existing node relabels itself on its next
	// reconcile rather than carrying both keys until someone deletes the
	// FrameNode.
	delete(node.Labels, "topology.kubernetes.io/rack")
	if err := r.Patch(ctx, &node, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching node labels: %w", err)
	}

	phase := nodePhase(&node)
	patch := client.MergeFrom(fn.DeepCopy())
	fn.Status.ObservedGeneration = fn.Generation
	fn.Status.NodeName = node.Name
	fn.Status.Capacity = node.Status.Capacity
	fn.Status.Allocatable = node.Status.Allocatable
	fn.Status.KubeletVersion = node.Status.NodeInfo.KubeletVersion
	meta.SetStatusCondition(&fn.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             conditionStatus(phase == nodePhaseOnline),
		Reason:             phase,
		Message:            "Synced from k8s Node",
		ObservedGeneration: fn.Generation,
	})
	if err := r.Status().Patch(ctx, fn, patch); err != nil {
		return ctrl.Result{}, err
	}

	eventType := corev1.EventTypeNormal
	if phase != nodePhaseOnline {
		eventType = corev1.EventTypeWarning
	}
	r.Recorder.Event(fn, eventType, phase, fmt.Sprintf("Node %s is %s", nodeName, phase))
	log.Info("Reconciled FrameNode", "phase", phase, "nodeName", nodeName)
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *FrameNodeReconciler) reconcileDelete(ctx context.Context, fn *framev1beta1.FrameNode) (ctrl.Result, error) {
	nodeName := fn.Spec.Hostname
	if nodeName == "" {
		nodeName = fn.Name
	}

	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err == nil {
		base := node.DeepCopy()
		for _, key := range frameNodeLabels {
			delete(node.Labels, key)
		}
		// The pre-freeze key, for nodes labelled before F12.
		delete(node.Labels, "topology.kubernetes.io/rack")
		if err := r.Patch(ctx, &node, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(fn, frameNodeFinalizer)
	return ctrl.Result{}, r.Update(ctx, fn)
}

func (r *FrameNodeReconciler) setPhase(ctx context.Context, fn *framev1beta1.FrameNode, phase, msg string) error {
	patch := client.MergeFrom(fn.DeepCopy())
	fn.Status.ObservedGeneration = fn.Generation
	// Status is derived from the phase rather than hardcoded to False, so
	// setPhase(ctx, fn, nodePhaseOnline, ...) stays honest if it is ever
	// called that way. Today every caller passes a non-Online phase.
	meta.SetStatusCondition(&fn.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             conditionStatus(phase == nodePhaseOnline),
		Reason:             phase,
		Message:            msg,
		ObservedGeneration: fn.Generation,
	})
	return r.Status().Patch(ctx, fn, patch)
}

func nodePhase(node *corev1.Node) string {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			switch c.Status {
			case corev1.ConditionTrue:
				return nodePhaseOnline
			case corev1.ConditionFalse:
				return "Degraded"
			}
		}
	}
	return "Offline"
}

// generateMachineConfig produces a minimal Talos machineConfig YAML for bootstrapping.
func generateMachineConfig(fn *framev1beta1.FrameNode) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w("version: v1alpha1")
	w("machine:")
	w("  type: %s", fn.Spec.Role)
	if fn.Spec.Hostname != "" {
		w("  hostname: %s", fn.Spec.Hostname)
	}
	w("  install:")
	w("    disk: %s", fn.Spec.Disk)
	w("  network:")
	w("    interfaces:")
	w("      - interface: eth0")
	w("        addresses:")
	w("          - %s", fn.Spec.Network.Address)
	w("        routes:")
	w("          - network: 0.0.0.0/0")
	w("            gateway: %s", fn.Spec.Network.Gateway)
	w("        mtu: 1500")

	if fn.Spec.Network.VLAN != nil {
		w("        vlans:")
		w("          - vlanId: %d", *fn.Spec.Network.VLAN)
		w("            addresses:")
		w("              - %s", fn.Spec.Network.Address)
		w("            routes:")
		w("              - network: 0.0.0.0/0")
		w("                gateway: %s", fn.Spec.Network.Gateway)
	}
	if fn.Spec.Network.Bond != nil {
		w("        bond:")
		w("          interfaces:")
		w("            - %s", *fn.Spec.Network.Bond)
	}

	if len(fn.Spec.Network.DNS) > 0 {
		w("    nameservers:")
		for _, dns := range fn.Spec.Network.DNS {
			w("      - %s", dns)
		}
	}
	if fn.Spec.RDMAInterface != "" {
		w("# RDMA interface: %s", fn.Spec.RDMAInterface)
	}
	w("cluster:")
	w("  allowSchedulingOnControlPlanes: true")
	w("  discovery:")
	w("    enabled: true")
	w("  network:")
	w("    dnsDomain: cluster.local")
	if fn.Spec.Network.Address != "" {
		w("  etcd:")
		w("    advertisedSubnets:")
		w("      - %s", fn.Spec.Network.Address)
	}

	return b.String()
}

func humanizeBytes(n uint64) string {
	const (
		tb = uint64(1) << 40
		gb = uint64(1) << 30
		mb = uint64(1) << 20
	)
	switch {
	case n >= tb:
		return fmt.Sprintf("%.1fTB", float64(n)/float64(tb))
	case n >= gb:
		return fmt.Sprintf("%.1fGB", float64(n)/float64(gb))
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/float64(mb))
	}
}

func diskTypeStr(t storageapi.Disk_DiskType) string {
	switch t {
	case storageapi.Disk_NVME:
		return "nvme"
	case storageapi.Disk_SSD:
		return "ssd"
	case storageapi.Disk_HDD:
		return "hdd"
	default:
		return "unknown"
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *FrameNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.FrameNode{}).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.nodeToFrameNode)).
		Named("framenode").
		Complete(r)
}

func (r *FrameNodeReconciler) nodeToFrameNode(ctx context.Context, obj client.Object) []reconcile.Request {
	var list framev1beta1.FrameNodeList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, fn := range list.Items {
		name := fn.Spec.Hostname
		if name == "" {
			name = fn.Name
		}
		if name == obj.GetName() {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: fn.Name, Namespace: fn.Namespace},
			})
		}
	}
	return reqs
}
