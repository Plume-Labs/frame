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

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
)

const frameNodeFinalizer = "frame.plume-labs.io/framenode"

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
	var fn framev1alpha1.FrameNode
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
	if fn.Status.Phase == "" || fn.Status.Phase == "Discovered" {
		return r.reconcileProvision(ctx, &fn)
	}

	// Phase 3: node should have joined Kubernetes → sync labels/status.
	return r.reconcileOnline(ctx, &fn)
}

// reconcileDiscovery contacts the node's Talos maintenance API (port 50000) and
// populates status.discoveredDisks / discoveredTalosVersion. Even if the call
// fails (node not yet reachable) the phase advances to Discovered so the UI can
// let the user fill in spec manually.
func (r *FrameNodeReconciler) reconcileDiscovery(ctx context.Context, fn *framev1alpha1.FrameNode) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if fn.Status.Phase == "Discovered" {
		// Waiting for user to patch spec with full disk/network/role info.
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(fn.DeepCopy())
	fn.Status.Phase = "Discovered"
	fn.Status.DiscoveredDisks = nil
	fn.Status.DiscoveredNICs = nil

	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	c, err := buildTalosInsecureClient(tctx, fn.Spec.IP)
	if err != nil {
		log.Info("Cannot build maintenance client", "ip", fn.Spec.IP, "err", err)
		r.Recorder.Event(fn, corev1.EventTypeWarning, "DiscoveryFailed", fmt.Sprintf("Cannot reach %s: %v", fn.Spec.IP, err))
	} else {
		defer c.Close()
		r.populateDiscovery(tctx, fn, c)
		r.Recorder.Event(fn, corev1.EventTypeNormal, "Discovered", "Maintenance API contacted")
	}

	setCondition(&fn.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "Discovered",
		Message:            "Discovery complete; waiting for spec",
		ObservedGeneration: fn.Generation,
	})
	return ctrl.Result{}, r.Status().Patch(ctx, fn, patch)
}

type talosDiscoveryClient interface {
	Version(context.Context, ...grpc.CallOption) (*machineapi.VersionResponse, error)
	Disks(context.Context, ...grpc.CallOption) (*storageapi.DisksResponse, error)
}

func (r *FrameNodeReconciler) populateDiscovery(ctx context.Context, fn *framev1alpha1.FrameNode, c talosDiscoveryClient) {
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
			fn.Status.DiscoveredDisks = append(fn.Status.DiscoveredDisks, framev1alpha1.DiskInfo{
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
func (r *FrameNodeReconciler) reconcileProvision(ctx context.Context, fn *framev1alpha1.FrameNode) (ctrl.Result, error) {
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
		defer c.Close()
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

// reconcileOnline syncs Kubernetes Node readiness back into the FrameNode status.
func (r *FrameNodeReconciler) reconcileOnline(ctx context.Context, fn *framev1alpha1.FrameNode) (ctrl.Result, error) {
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
	node.Labels["topology.kubernetes.io/rack"] = fn.Spec.Rack
	node.Labels["topology.kubernetes.io/zone"] = fn.Spec.Zone
	node.Labels["frame.plume-labs.io/service-class"] = fn.Spec.ServiceClass
	node.Labels["frame.plume-labs.io/role"] = fn.Spec.Role
	if fn.Spec.RDMAInterface != "" {
		node.Labels["frame.plume-labs.io/rdma"] = "true"
	}
	if err := r.Patch(ctx, &node, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching node labels: %w", err)
	}

	phase := nodePhase(&node)
	patch := client.MergeFrom(fn.DeepCopy())
	fn.Status.Phase = phase
	fn.Status.NodeName = node.Name
	fn.Status.Capacity = node.Status.Capacity
	fn.Status.Allocatable = node.Status.Allocatable
	fn.Status.KubeletVersion = node.Status.NodeInfo.KubeletVersion
	setCondition(&fn.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             conditionStatus(phase == "Online"),
		Reason:             phase,
		Message:            "Synced from k8s Node",
		ObservedGeneration: fn.Generation,
	})
	if err := r.Status().Patch(ctx, fn, patch); err != nil {
		return ctrl.Result{}, err
	}

	eventType := corev1.EventTypeNormal
	if phase != "Online" {
		eventType = corev1.EventTypeWarning
	}
	r.Recorder.Event(fn, eventType, phase, fmt.Sprintf("Node %s is %s", nodeName, phase))
	log.Info("Reconciled FrameNode", "phase", phase, "nodeName", nodeName)
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *FrameNodeReconciler) reconcileDelete(ctx context.Context, fn *framev1alpha1.FrameNode) (ctrl.Result, error) {
	nodeName := fn.Spec.Hostname
	if nodeName == "" {
		nodeName = fn.Name
	}

	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err == nil {
		base := node.DeepCopy()
		delete(node.Labels, "topology.kubernetes.io/rack")
		delete(node.Labels, "topology.kubernetes.io/zone")
		delete(node.Labels, "frame.plume-labs.io/service-class")
		delete(node.Labels, "frame.plume-labs.io/role")
		delete(node.Labels, "frame.plume-labs.io/rdma")
		if err := r.Patch(ctx, &node, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(fn, frameNodeFinalizer)
	return ctrl.Result{}, r.Update(ctx, fn)
}

func (r *FrameNodeReconciler) setPhase(ctx context.Context, fn *framev1alpha1.FrameNode, phase, msg string) error {
	patch := client.MergeFrom(fn.DeepCopy())
	fn.Status.Phase = phase
	setCondition(&fn.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             conditionStatus(false),
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
				return "Online"
			case corev1.ConditionFalse:
				return "Degraded"
			}
		}
	}
	return "Offline"
}

// generateMachineConfig produces a minimal Talos machineConfig YAML for bootstrapping.
func generateMachineConfig(fn *framev1alpha1.FrameNode) string {
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
		For(&framev1alpha1.FrameNode{}).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.nodeToFrameNode)).
		Named("framenode").
		Complete(r)
}

func (r *FrameNodeReconciler) nodeToFrameNode(ctx context.Context, obj client.Object) []reconcile.Request {
	var list framev1alpha1.FrameNodeList
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
