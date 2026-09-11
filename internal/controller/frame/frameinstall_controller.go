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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/provision"
)

// frameInstallFinalizer blocks deletion of a FrameInstall while its machine
// may still carry attached media and a boot override. Without it, deleting
// the object mid-flight strands the hardware in that state with nothing
// left in the cluster that knows to eject it (design §10).
const frameInstallFinalizer = "frame.plume-labs.io/frameinstall"

// frameInstallSSHUser is the account the preseed creates on every installed
// machine (design §7), with the public half of SSHKeyRef and sudo, no
// password login at all.
const frameInstallSSHUser = "frame"

// frameInstallControllerUser is FrameTaskSpec.User for the two audit
// entries this controller writes itself, rather than through the UI proxy's
// TaskRecorder: creating a FrameInstall and reaching a terminal phase are
// controller-driven facts, not HTTP requests carrying a Frame identity.
// FrameTaskSpec.User has MinLength=1, so an audit entry needs a name for
// this actor even though no human or FrameUser is behind it; the
// system:-prefixed shape follows Kubernetes' own convention for a
// non-human identity.
const frameInstallControllerUser = "system:controller:frameinstall"

// defaultPhaseTimeouts and defaultPoll are the budgets provision.Install
// runs against when the reconciler's own fields are left unset. Every phase
// in provision.requiredPhaseTimeouts needs an entry, or Install refuses the
// spec before it ever reaches the machine (install.go's validateOptions).
var defaultPhaseTimeouts = map[provision.Phase]time.Duration{
	provision.PhasePending:       30 * time.Second,
	provision.PhasePreparing:     10 * time.Minute,
	provision.PhaseMediaAttached: 2 * time.Minute,
	provision.PhaseInstalling:    20 * time.Minute,
	provision.PhaseJoining:       5 * time.Minute,
	provision.PhaseReady:         5 * time.Minute,
}

const defaultPoll = 5 * time.Second

// FrameInstallReconciler turns a FrameInstall into a running installation.
//
// It is thin on purpose: every judgment about what an installation does --
// phase order, cleanup, retry-or-not -- lives in internal/provision, which
// Task 10's frame bootstrap command reaches the same way. This reconciler's
// own job is narrower: translate the CRD into a provision.Spec, refuse the
// three things a CEL rule cannot check because CEL sees only the object
// being validated and never another one (a missing FrameMachine, an
// unknown disk, a hostname collision), supply the four interfaces
// provision.Deps declares, and write what provision.Install reports back to
// status.
type FrameInstallReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// NewBMC builds the BMC a FrameMachine's Redfish credentials describe.
	// Tests replace it; production wiring is BuildRedfishBMC below, which
	// mirrors BuildRedfishClient's Secret/TLS resolution (redfish_client.go)
	// rather than inventing a second way to read a BMC credential.
	NewBMC func(ctx context.Context, kube client.Client, namespace string, bmc framev1beta1.BMCSpec) (provision.BMC, error)

	Images provision.ImageStore
	SSH    provision.SSHClient
	Nodes  provision.NodeChecker

	// PhaseTimeout and Poll configure every provision.Install call this
	// reconciler makes. Left nil/zero, defaultPhaseTimeouts/defaultPoll
	// apply; tests that need to prove a timeout set these to a fraction of a
	// second instead of waiting out the production budget.
	PhaseTimeout map[provision.Phase]time.Duration
	Poll         time.Duration

	// mu guards inFlight, the set of FrameInstall UIDs with a
	// provision.Install call currently running in a goroutine this process
	// started. It is memory, not a durable lock: a manager restart loses it,
	// which is exactly the case the finalizer's own media-eject (not "wait
	// for the goroutine that owned this UID") exists to still handle
	// correctly.
	mu       sync.Mutex
	inFlight map[types.UID]struct{}
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameinstalls,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameinstalls/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameinstalls/finalizers,verbs=update
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framemachines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

// Reconcile drives one FrameInstall.
func (r *FrameInstallReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var fi framev1beta1.FrameInstall
	if err := r.Get(ctx, req.NamespacedName, &fi); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !fi.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &fi)
	}

	if !controllerutil.ContainsFinalizer(&fi, frameInstallFinalizer) {
		controllerutil.AddFinalizer(&fi, frameInstallFinalizer)
		return ctrl.Result{}, r.Update(ctx, &fi)
	}

	// An installation runs once. Without this, every resync replays a
	// destructive operation -- the resync interval would become the
	// reinstall interval.
	if fi.Status.Phase == string(provision.PhaseReady) || fi.Status.Phase == string(provision.PhaseFailed) {
		return ctrl.Result{}, nil
	}
	if r.running(fi.UID) {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	fm, err := r.getMachine(ctx, fi.Namespace, fi.Spec.MachineRef)
	if err != nil {
		return r.failPending(ctx, &fi, fmt.Sprintf("machine %q: %v", fi.Spec.MachineRef, err))
	}

	if err := r.checkNotALiveClusterMember(ctx, fi.Spec.Hostname, fi.Spec.Network.Address); err != nil {
		return r.failPending(ctx, &fi, err.Error())
	}
	if err := r.checkHostnameNotTaken(ctx, &fi); err != nil {
		return r.failPending(ctx, &fi, err.Error())
	}
	if err := checkDisksKnown(&fi, fm); err != nil {
		return r.failPending(ctx, &fi, err.Error())
	}

	bmc, err := r.NewBMC(ctx, r.Client, fi.Namespace, fm.Spec.BMC)
	if err != nil {
		return r.failPending(ctx, &fi, fmt.Sprintf("building a BMC client for machine %s: %v", fm.Name, err))
	}

	sshPub, sshPriv, err := r.readSSHKey(ctx, fi.Namespace, fi.Spec.SSHKeyRef)
	if err != nil {
		return r.failPending(ctx, &fi, fmt.Sprintf("reading SSH key %s: %v", fi.Spec.SSHKeyRef, err))
	}

	var joinToken string
	if fi.Spec.Cluster.Mode == "join" {
		joinToken, err = r.readJoinToken(ctx, fi.Namespace, fi.Spec.Cluster.JoinTokenRef)
		if err != nil {
			return r.failPending(ctx, &fi, fmt.Sprintf("reading join token %s: %v", fi.Spec.Cluster.JoinTokenRef, err))
		}
	}

	if !r.startInstall(fi.UID) {
		// A concurrent reconcile already won the race to start this UID.
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	spec := toProvisionSpec(&fi, sshPub, joinToken)
	opts := r.options(&fi, sshPriv)
	key := client.ObjectKeyFromObject(&fi)
	deps := provision.Deps{
		BMC:    bmc,
		Images: r.Images,
		SSH:    r.SSH,
		Nodes:  r.Nodes,
		Report: func(p provision.Phase) { r.reportPhase(context.WithoutCancel(ctx), key, p) },
	}

	r.recordTask(ctx, &fi, framev1beta1.TaskVerbCreate,
		fmt.Sprintf("install %s on machine %s", fi.Spec.Hostname, fi.Spec.MachineRef))

	go r.runInstall(fi.UID, key, fi.Spec.MachineRef, deps, spec, opts)

	log.Info("started FrameInstall", "frameinstall", req.NamespacedName, "machine", fi.Spec.MachineRef)
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

// runInstall is the goroutine Reconcile starts, keyed by the object's UID so
// running() and startInstall() below can tell "already in flight" apart
// from "not yet started" without touching the apiserver. It always frees
// its slot on return, success or failure -- inFlight tracks "a call is
// running", not "the last call succeeded".
func (r *FrameInstallReconciler) runInstall(uid types.UID, key client.ObjectKey, machineRef string, deps provision.Deps, spec provision.Spec, opts provision.Options) {
	defer r.finishInstall(uid)
	ctx := context.Background()
	res, err := provision.Install(ctx, deps, spec, opts)
	r.finishStatus(ctx, key, machineRef, res, err)
}

// running reports whether a provision.Install call for uid is active in
// this process. Read-only and unlocked-fast-path-free on purpose: it is
// called on every reconcile of an object that has not reached a terminal
// phase yet, so it has to be cheap, but it must never race startInstall's
// check-and-set below -- both take the same mutex.
func (r *FrameInstallReconciler) running(uid types.UID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.inFlight[uid]
	return ok
}

// startInstall atomically claims uid, returning false if it was already
// claimed. This is the actual run-once guard against a race two concurrent
// Reconcile calls could otherwise hit: running() above is a fast, separate
// read that lets Reconcile skip the rest of its own work early, but only
// startInstall's check-and-set is what may not run twice for the same UID.
func (r *FrameInstallReconciler) startInstall(uid types.UID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight == nil {
		r.inFlight = map[types.UID]struct{}{}
	}
	if _, ok := r.inFlight[uid]; ok {
		return false
	}
	r.inFlight[uid] = struct{}{}
	return true
}

func (r *FrameInstallReconciler) finishInstall(uid types.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, uid)
}

// reportPhase is provision.Deps.Report: called on entering every phase,
// including PhaseFailed more than once in the same call to Install (once
// for a failing phase, once for a failing cleanup in its deferred func).
// It only ever writes the latest phase it was handed -- never counts calls,
// never fires a side effect per call -- which is what keeps it correct
// under that double-Failed shape: a consumer reading the last value stays
// right no matter how many times this runs, where one treating each call as
// a discrete event would not. The one-shot side effects that do fire on
// completion (the terminal FrameTask, HostKey/NodeName/KubeconfigSecret)
// live in finishStatus instead, gated on provision.Install's actual return
// value once, not on anything this closure sees.
func (r *FrameInstallReconciler) reportPhase(ctx context.Context, key client.ObjectKey, p provision.Phase) {
	var fi framev1beta1.FrameInstall
	if err := r.Get(ctx, key, &fi); err != nil {
		return
	}
	patch := client.MergeFrom(fi.DeepCopy())
	fi.Status.Phase = string(p)
	now := metav1.Now()
	fi.Status.PhaseSince = &now
	_ = r.Status().Patch(ctx, &fi, patch)
}

// finishStatus runs exactly once per provision.Install call, after it has
// returned -- the single point this reconciler treats as "the installation
// is over" and the only place it writes FailedPhase, Message, HostKey,
// NodeName, KubeconfigSecret or the terminal FrameTask. res.Phase is
// Install's own final word on how this ended; reportPhase above may have
// already written the same phase moments earlier as the last of possibly
// several PhaseFailed reports, and this write is idempotent with that, not
// a second, different event.
func (r *FrameInstallReconciler) finishStatus(ctx context.Context, key client.ObjectKey, machineRef string, res provision.Result, installErr error) {
	log := logf.FromContext(ctx)

	var fi framev1beta1.FrameInstall
	if err := r.Get(ctx, key, &fi); err != nil {
		log.Error(err, "FrameInstall vanished before its result could be recorded", "frameinstall", key)
		return
	}
	patch := client.MergeFrom(fi.DeepCopy())
	fi.Status.Phase = string(res.Phase)
	now := metav1.Now()
	fi.Status.PhaseSince = &now
	fi.Status.FailedPhase = string(res.FailedPhase)
	if installErr != nil {
		fi.Status.Message = truncateError(installErr, 512)
	} else {
		fi.Status.Message = ""
	}
	fi.Status.HostKey = res.HostKey
	fi.Status.NodeName = res.NodeName

	if res.Phase == provision.PhaseReady && len(res.Kubeconfig) > 0 {
		secretName, err := r.writeKubeconfigSecret(ctx, fi.Namespace, fi.Name, res.Kubeconfig)
		if err != nil {
			log.Error(err, "install succeeded but its kubeconfig Secret could not be written", "frameinstall", key)
			fi.Status.Message = truncateError(fmt.Errorf("install succeeded but saving the kubeconfig failed: %w", err), 512)
		} else {
			fi.Status.KubeconfigSecret = secretName
		}
	}

	if err := r.Status().Patch(ctx, &fi, patch); err != nil {
		log.Error(err, "could not record install result", "frameinstall", key)
		return
	}

	r.recordTask(ctx, &fi, framev1beta1.TaskVerbUpdate,
		fmt.Sprintf("install %s on machine %s reached %s", fi.Spec.Hostname, machineRef, fi.Status.Phase))
}

// failPending writes a refusal from one of the guards below -- every one of
// them fires before provision.Install is ever called, so every one of them
// fails in Pending, the phase the destructive guard lives in (design §8).
func (r *FrameInstallReconciler) failPending(ctx context.Context, fi *framev1beta1.FrameInstall, msg string) (ctrl.Result, error) {
	patch := client.MergeFrom(fi.DeepCopy())
	fi.Status.Phase = string(provision.PhaseFailed)
	fi.Status.FailedPhase = string(provision.PhasePending)
	fi.Status.Message = truncateString(msg, 512)
	now := metav1.Now()
	fi.Status.PhaseSince = &now
	if err := r.Status().Patch(ctx, fi, patch); err != nil {
		return ctrl.Result{}, err
	}
	r.recordTask(ctx, fi, framev1beta1.TaskVerbUpdate,
		fmt.Sprintf("install %s on machine %s refused: %s", fi.Spec.Hostname, fi.Spec.MachineRef, msg))
	return ctrl.Result{}, nil
}

// recordTask appends one FrameTask audit entry. This reconciler writes it
// directly with r.Client rather than through internal/uiproxy's
// TaskRecorder: that recorder brackets one HTTP request the UI made, and
// creating a FrameInstall here is a controller-driven fact with no request
// to bracket. boundAction (uiproxy) is unexported and not reachable from
// this package; truncateString (framemachine_controller.go) already counts
// runes rather than bytes, which is what FrameTaskSpec.Action's CRD
// maxLength counts too.
//
// A missing record must not cost the installation anything -- the same
// reasoning TaskRecorder.Start already applies -- so failures here only log.
func (r *FrameInstallReconciler) recordTask(ctx context.Context, fi *framev1beta1.FrameInstall, verb, action string) {
	log := logf.FromContext(ctx)
	task := &framev1beta1.FrameTask{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "frameinstall-task-", Namespace: fi.Namespace},
		Spec: framev1beta1.FrameTaskSpec{
			User: frameInstallControllerUser,
			Verb: verb,
			Target: framev1beta1.ObjectRef{
				Resource:  "frameinstalls",
				Namespace: fi.Namespace,
				Name:      fi.Name,
			},
			Action: truncateString(action, 200),
		},
	}
	if err := r.Create(ctx, task); err != nil {
		log.Error(err, "could not record FrameTask", "frameinstall", fi.Name)
		return
	}
	startedAt := metav1.Now()
	task.Status = framev1beta1.FrameTaskStatus{
		Phase:      framev1beta1.TaskPhaseSucceeded,
		StartedAt:  &startedAt,
		FinishedAt: &startedAt,
	}
	if err := r.Status().Update(ctx, task); err != nil {
		log.Error(err, "could not set FrameTask status", "task", task.Name)
	}
}

// getMachine reads the FrameMachine machineRef names. Its error is surfaced
// verbatim by the caller into the refusal message -- apierrors.IsNotFound's
// own error text already names the resource and the name, which is what
// "goes Failed with a message naming the missing machine" asks for.
func (r *FrameInstallReconciler) getMachine(ctx context.Context, namespace, name string) (*framev1beta1.FrameMachine, error) {
	var fm framev1beta1.FrameMachine
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &fm); err != nil {
		return nil, err
	}
	return &fm, nil
}

// checkNotALiveClusterMember is the destructive guard's third layer (design
// §8): a Node that is Ready right now is carrying workloads, and reinstalling
// the machine under it needs an explicit, separate act -- removing it from
// the cluster first -- not a FrameInstall alone. It checks both the hostname
// and the network address independently, because either one naming a Ready
// node is the same hazard: the object being created could share neither its
// name nor its address with the node it would actually overwrite.
func (r *FrameInstallReconciler) checkNotALiveClusterMember(ctx context.Context, hostname, cidrAddress string) error {
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}
	address := addressWithoutCIDR(cidrAddress)
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if !nodeIsReady(n) {
			continue
		}
		if n.Name == hostname {
			return fmt.Errorf("a Node named %q is Ready in this cluster; remove it from the cluster before reinstalling it", hostname)
		}
		for _, a := range n.Status.Addresses {
			if a.Address == address {
				return fmt.Errorf("a Ready Node (%q) already carries address %s; remove it from the cluster before reinstalling it", n.Name, address)
			}
		}
	}
	return nil
}

// checkHostnameNotTaken is the structural half of the disk/hostname guard
// CEL cannot express, minus the Ready-node case checkNotALiveClusterMember
// already refuses with its own, more specific message: a Node occupying
// this hostname that is not Ready, or another FrameInstall in the same
// namespace that names the same hostname and has not yet reached a terminal
// phase.
func (r *FrameInstallReconciler) checkHostnameNotTaken(ctx context.Context, fi *framev1beta1.FrameInstall) error {
	var node corev1.Node
	err := r.Get(ctx, types.NamespacedName{Name: fi.Spec.Hostname}, &node)
	if err == nil {
		return fmt.Errorf("a Node named %q already exists", fi.Spec.Hostname)
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("checking for a Node named %q: %w", fi.Spec.Hostname, err)
	}

	var installs framev1beta1.FrameInstallList
	if err := r.List(ctx, &installs, client.InNamespace(fi.Namespace)); err != nil {
		return fmt.Errorf("listing FrameInstalls: %w", err)
	}
	for i := range installs.Items {
		other := &installs.Items[i]
		if other.Name == fi.Name || other.Spec.Hostname != fi.Spec.Hostname {
			continue
		}
		if other.Status.Phase == string(provision.PhaseReady) || other.Status.Phase == string(provision.PhaseFailed) {
			continue
		}
		return fmt.Errorf("FrameInstall %q already targets hostname %q and has not reached a terminal phase", other.Name, fi.Spec.Hostname)
	}
	return nil
}

// checkDisksKnown is the other structural half CEL cannot express: every
// layout.disks[].byID must appear in the inventory of the FrameMachine
// machineRef names. confirmSerial proves which machine this is; it says
// nothing about which disks are on it, and a schema validation rule sees
// only the FrameInstall being validated, never the FrameMachine it points
// at, so nothing before this has ever checked the two against each other.
func checkDisksKnown(fi *framev1beta1.FrameInstall, fm *framev1beta1.FrameMachine) error {
	known := map[string]bool{}
	if fm.Status.Inventory != nil {
		for _, d := range fm.Status.Inventory.Drives {
			known[d.Name] = true
		}
	}
	for _, d := range fi.Spec.Layout.Disks {
		if !known[d.ByID] {
			return fmt.Errorf("disk %q is not in %s's inventory", d.ByID, fm.Name)
		}
	}
	return nil
}

// addressWithoutCIDR strips a CIDR suffix ("192.168.2.210/24" ->
// "192.168.2.210"), so an address from FrameInstallSpec.Network -- always a
// CIDR, per its CEL rule -- can be compared against a corev1.NodeAddress,
// which never carries one.
func addressWithoutCIDR(s string) string {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return s
}

// readSSHKey reads the Secret SSHKeyRef names. Its shape is fixed by
// FrameInstallSpec.SSHKeyRef's own doc comment: "id" (private) and "id.pub"
// (public). Only the public half ever reaches an installer image --
// provision.RenderPreseed enforces that on Spec.SSHPublicKey -- so nothing
// here needs to re-check that "id.pub" doesn't also carry the private half;
// that is preseed.go's checkPublicKeyOnly's job, downstream in
// provision.Install.
func (r *FrameInstallReconciler) readSSHKey(ctx context.Context, namespace, name string) (pub string, priv []byte, err error) {
	var sec corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &sec); err != nil {
		return "", nil, err
	}
	pubBytes, ok := sec.Data["id.pub"]
	if !ok || len(pubBytes) == 0 {
		return "", nil, fmt.Errorf("secret %s/%s has no id.pub", namespace, name)
	}
	privBytes, ok := sec.Data["id"]
	if !ok || len(privBytes) == 0 {
		return "", nil, fmt.Errorf("secret %s/%s has no id", namespace, name)
	}
	return strings.TrimSpace(string(pubBytes)), privBytes, nil
}

// readJoinToken reads the Secret JoinTokenRef names. Its key ("token") is
// not fixed anywhere else in this lot -- frameinstall_types.go's CEL rule
// requires joinTokenRef to be set for mode: join but is silent on the
// Secret's shape, the way BMCSpec.CredentialsRef ("username"/"password")
// and SSHKeyRef ("id"/"id.pub") both are documented to be. "token" is this
// reconciler's own choice, made here for lack of a precedent to follow.
func (r *FrameInstallReconciler) readJoinToken(ctx context.Context, namespace, name string) (string, error) {
	var sec corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &sec); err != nil {
		return "", err
	}
	tok, ok := sec.Data["token"]
	if !ok || len(tok) == 0 {
		return "", fmt.Errorf("secret %s/%s has no token", namespace, name)
	}
	return strings.TrimSpace(string(tok)), nil
}

// writeKubeconfigSecret stores a cluster-init install's kubeconfig. design
// §6: without this, a new cluster exists and nobody can talk to it.
func (r *FrameInstallReconciler) writeKubeconfigSecret(ctx context.Context, namespace, installName string, kubeconfig []byte) (string, error) {
	name := installName + "-kubeconfig"
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"kubeconfig": kubeconfig},
	}
	if err := r.Create(ctx, sec); err != nil {
		return "", err
	}
	return name, nil
}

// toProvisionSpec translates the CRD into the Kubernetes-free Spec
// provision.Install actually runs against.
func toProvisionSpec(fi *framev1beta1.FrameInstall, sshPublicKey, joinToken string) provision.Spec {
	var disks []provision.Disk
	for _, d := range fi.Spec.Layout.Disks {
		disks = append(disks, provision.Disk{ByID: d.ByID, SizeBytes: d.SizeBytes})
	}
	return provision.Spec{
		UID:      string(fi.UID),
		Hostname: fi.Spec.Hostname,
		Network: provision.Network{
			Address: fi.Spec.Network.Address,
			Gateway: fi.Spec.Network.Gateway,
			DNS:     fi.Spec.Network.DNS,
		},
		Layout: provision.Layout{
			Kind:  provision.LayoutKind(fi.Spec.Layout.Kind),
			Disks: disks,
			Raw:   fi.Spec.Layout.Raw,
		},
		SSHPublicKey: sshPublicKey,
		Cluster: provision.ClusterTarget{
			Mode:       provision.ClusterMode(fi.Spec.Cluster.Mode),
			ServerURL:  fi.Spec.Cluster.ServerURL,
			JoinToken:  joinToken,
			K3sVersion: fi.Spec.Cluster.K3sVersion,
		},
	}
}

// options builds the per-call Options provision.Install needs beyond Spec:
// the account and key it authenticates as, the budgets each phase runs
// under, and the destructive guard's hand-retyped confirmation.
func (r *FrameInstallReconciler) options(fi *framev1beta1.FrameInstall, sshKey []byte) provision.Options {
	phaseTimeout := r.PhaseTimeout
	if phaseTimeout == nil {
		phaseTimeout = defaultPhaseTimeouts
	}
	poll := r.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	return provision.Options{
		BootMode:      fi.Spec.BootMode,
		SSHUser:       frameInstallSSHUser,
		SSHKey:        sshKey,
		NodeAddress:   addressWithoutCIDR(fi.Spec.Network.Address),
		ConfirmSerial: fi.Spec.ConfirmSerial,
		PhaseTimeout:  phaseTimeout,
		Poll:          poll,
	}
}

// finalize runs on deletion. It ejects media and clears the boot override
// through the same BMC path a running install would have used, so a
// FrameInstall deleted mid-flight does not strand the machine with media
// attached and a one-time boot override set (design §10).
//
// A running goroutine for this UID is left to finish rather than raced:
// provision.Install already ejects media and clears the boot override on
// every one of its own exits (install.go's cleanup defer), so requeueing
// until it is done is simpler than two callers issuing BMC writes at once.
// The case this finalizer actually exists for is the one running() cannot
// see at all -- a manager restart during Preparing/MediaAttached/Installing
// loses inFlight, and the object's own status.phase (not Ready or Failed)
// combined with running() being false is exactly what tells this finalizer
// there is real cleanup to do here, not merely to wait for.
func (r *FrameInstallReconciler) finalize(ctx context.Context, fi *framev1beta1.FrameInstall) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(fi, frameInstallFinalizer) {
		return ctrl.Result{}, nil
	}
	if r.running(fi.UID) {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	fm, err := r.getMachine(ctx, fi.Namespace, fi.Spec.MachineRef)
	if err == nil {
		bmc, berr := r.NewBMC(ctx, r.Client, fi.Namespace, fm.Spec.BMC)
		if berr == nil {
			ejectErr := bmc.EjectMedia(ctx)
			clearErr := bmc.ClearBootOverride(ctx)
			if ejectErr != nil || clearErr != nil {
				return ctrl.Result{}, fmt.Errorf("finalizer: ejecting media / clearing boot override for %s: eject=%v clear=%v",
					fi.Name, ejectErr, clearErr)
			}
		}
		// A BMC Frame can no longer reach must not block deletion forever:
		// there is nothing left this finalizer could still eject media
		// from, and the object staying undeletable would be a worse outcome
		// than a machine an operator now has to check by hand.
	}
	// A machine that no longer exists is the same case, for the same reason.

	controllerutil.RemoveFinalizer(fi, frameInstallFinalizer)
	return ctrl.Result{}, r.Update(ctx, fi)
}

// BuildRedfishBMC mirrors BuildRedfishClient (redfish_client.go): the same
// Secret and TLS resolution, because a second way to read a BMC credential
// is exactly what that file's existing pattern exists to prevent. It
// returns a provision.BMC rather than a redfish.Client because
// provision.RedfishBMC needs the raw baseURL/username/password/TLS config
// directly -- redfish.Client's interface has no method that would let
// provision.RedfishBMC reach into an already-built *client for them. It is
// the reconciler's NewBMC in production, the same way BuildRedfishClient is
// FrameMachineReconciler's NewClient in cmd/main.go.
func BuildRedfishBMC(ctx context.Context, kube client.Client, namespace string, bmc framev1beta1.BMCSpec) (provision.BMC, error) {
	var sec corev1.Secret
	if err := kube.Get(ctx, types.NamespacedName{Name: bmc.CredentialsRef, Namespace: namespace}, &sec); err != nil {
		return nil, err
	}
	user := string(sec.Data["username"])
	pass := string(sec.Data["password"])
	if user == "" || pass == "" {
		return nil, fmt.Errorf("secret %s/%s must carry both username and password", namespace, bmc.CredentialsRef)
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case bmc.TLS.InsecureSkipVerify:
		tlsCfg.InsecureSkipVerify = true
	case bmc.TLS.CABundleRef != "":
		var cm corev1.ConfigMap
		if err := kube.Get(ctx, types.NamespacedName{Name: bmc.TLS.CABundleRef, Namespace: namespace}, &cm); err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cm.Data["ca.crt"])) {
			return nil, fmt.Errorf("configmap %s/%s has no usable ca.crt", namespace, bmc.TLS.CABundleRef)
		}
		tlsCfg.RootCAs = pool
	}

	return provision.NewRedfishBMC("https://"+bmc.Address, user, pass, tlsCfg), nil
}

// ClusterNodeChecker is the production provision.NodeChecker: Frame's own
// cluster when kubeconfig is nil, per provision.NodeChecker's own documented
// contract, and a fresh client-go Clientset built from kubeconfig otherwise.
// It is built fresh on every ClusterInit call rather than cached, because
// that kubeconfig only exists once provision.Join has returned it -- long
// after this reconciler, and any NodeChecker it holds, was constructed.
type ClusterNodeChecker struct {
	// Frame is Frame's own cluster client, used only when kubeconfig is nil.
	Frame client.Client
}

func (n *ClusterNodeChecker) NodeReady(ctx context.Context, kubeconfig []byte, name string) (bool, error) {
	if kubeconfig == nil {
		var node corev1.Node
		if err := n.Frame.Get(ctx, types.NamespacedName{Name: name}, &node); err != nil {
			return false, err
		}
		return nodeIsReady(&node), nil
	}

	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return false, fmt.Errorf("parsing the new cluster's kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return false, fmt.Errorf("building a client for the new cluster: %w", err)
	}
	node, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	return nodeIsReady(node), nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *FrameInstallReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.FrameInstall{}).
		Named("frameinstall").
		Complete(r)
}
