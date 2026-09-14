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
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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

// beaconLostAfter is how long without a beacon before the installer is
// called unresponsive. The machine sends every 15 seconds, so this is four
// missed sends.
//
// Not one or two: the machine is mid-installation on a network Frame just
// reconfigured under it, and the cost of calling a live installer dead is an
// operator sent to a machine that needed nothing. It is a guess with a
// reason, and the first number to revisit once real installs have run.
const beaconLostAfter = 60 * time.Second

// installerRespondingCondition turns what provisiond reported into the one
// condition an operator reads. It is a pure function of its arguments --
// no client, no clock of its own -- because its whole job is a judgment that
// must be testable at every boundary without standing up a cluster.
//
// It returns a condition and nothing else. It cannot fail an install, end a
// phase, or shorten a timeout, and the test beside it asserts exactly that.
func installerRespondingCondition(st provision.BeaconState, known bool, err error, now time.Time) metav1.Condition {
	cond := metav1.Condition{Type: "InstallerResponding"}
	switch {
	case err != nil:
		// Frame could not ask. Never conflated with "the machine is silent":
		// one is a fact about the machine, the other about this process.
		cond.Status = metav1.ConditionUnknown
		cond.Reason = "Unavailable"
		cond.Message = fmt.Sprintf("could not read installer progress: %v", err)
	case !known:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "NeverSeen"
		cond.Message = "nothing has been heard from the installer: it may not have booted the media, or the network may never have come up"
	case now.Sub(st.LastSeen) > beaconLostAfter:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "HeartbeatLost"
		cond.Message = fmt.Sprintf("last reached %s, %s ago, and has not reported since",
			st.LastCheckpoint, now.Sub(st.LastSeen).Round(time.Second))
	default:
		cond.Status = metav1.ConditionTrue
		cond.Reason = "Heartbeat"
		cond.Message = fmt.Sprintf("at %s, last reported %s ago; an installer that stays here is waiting on a question",
			st.LastCheckpoint, now.Sub(st.LastSeen).Round(time.Second))
	}
	return cond
}

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

	// Progress reads what an installation has reported to provisiond. It is
	// diagnosis only: nothing read through it may end, advance or fail a
	// phase. Left nil, the InstallerResponding condition is simply never
	// written and every install behaves exactly as it did before this
	// existed.
	Progress provision.ProgressReader

	// ProvisiondMediaURL is the address a machine's BMC reaches
	// frame-provisiond's media listener at. It is the same value Images was
	// configured with; it is carried separately because Images is an
	// interface and cannot be asked.
	//
	// Unset or malformed, this reconciler refuses in Pending, before
	// anything is built or the machine is touched. It is deliberately NOT a
	// manager start-up gate: it was one, which made a flag only this
	// controller uses block a cluster that will never provision a machine
	// from upgrading at all.
	ProvisiondMediaURL string

	// OperatorNamespace is where a cluster-init install's kubeconfig Secret
	// is written -- the operator's own namespace, not the FrameInstall's,
	// because the Secret's OwnerReference only means anything to the
	// garbage collector when it resolves within the same namespace as the
	// child object; a FrameInstall this manager watches cluster-wide could
	// otherwise leave the Secret in a namespace the operator itself never
	// touches again. Left empty, writeKubeconfigSecret falls back to the
	// FrameInstall's own namespace -- the only sound choice when no operator
	// namespace was configured, and what every test in this package uses.
	OperatorNamespace string

	// PhaseTimeout and Poll configure every provision.Install call this
	// reconciler makes. Left nil/zero, defaultPhaseTimeouts/defaultPoll
	// apply; tests that need to prove a timeout set these to a fraction of a
	// second instead of waiting out the production budget.
	PhaseTimeout map[provision.Phase]time.Duration
	Poll         time.Duration

	// mu guards inFlight: which machineRef currently has a provision.Install
	// call running in a goroutine this process started, and which
	// FrameInstall UID owns it.
	//
	// Keyed by machineRef, not by the FrameInstall's own UID: a BMC is what
	// Reset/InsertMedia/EjectMedia actually act on, and two different
	// FrameInstall objects naming the same machineRef would each carry a
	// distinct UID, so a UID-keyed map lets both pass every guard and both
	// call Reset on the same BMC at once -- one's cleanup then ejecting
	// media the other's install still needs attached.
	//
	// It is memory, not a durable lock: a manager restart loses it, which is
	// exactly the case Reconcile's own restart-recovery branch and the
	// finalizer's media-eject both exist to still handle correctly -- see
	// the comment on the ObservedGeneration check in Reconcile.
	mu       sync.Mutex
	inFlight map[string]types.UID

	// tokens is the image token of each in-flight install, by object UID.
	// provision.Install produces it (Deps.ReportToken) and it is absent from
	// Result, so this is the only way to know it while the install is still
	// running -- which is exactly when it is needed.
	//
	// Deliberately NOT written to status: the token is the unguessable
	// handle protecting both the beacon route and the preseed (which carries
	// the install UID) on an unauthenticated LAN listener. Putting it on an
	// object widens who can read it to everyone with get on frameinstalls.
	// The cost is that a manager restart loses it -- and the condition then
	// reports Unavailable, which is the honest answer.
	tokens map[types.UID]string
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameinstalls,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameinstalls/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frameinstalls/finalizers,verbs=update
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framemachines,verbs=get;list;watch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frametasks,verbs=create
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=frametasks/status,verbs=get;update;patch
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

	// The machine this object targets is currently being driven by a
	// goroutine this same process started -- either for this exact object
	// (the ordinary "still running" case) or, because inFlight is keyed by
	// machineRef and not by UID, for a *different* FrameInstall naming the
	// same machine. Either way, nothing more may start against this
	// machineRef right now.
	if r.running(fi.Spec.MachineRef) {
		r.reportInstallerLiveness(ctx, &fi)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// committed reports whether *this* generation of *this* object was
	// already handed to provision.Install by some earlier Reconcile call --
	// stamped synchronously into status.observedGeneration, further down,
	// in the same call that starts the goroutine, strictly before that
	// goroutine is spawned. Checked only after running() above, and not
	// before: r.running is keyed by machineRef, so a label edit that bumps
	// Generation while a genuinely-running install is still in flight in
	// *this* process must not fall through past the running() guard just
	// because ObservedGeneration (stamped at the older generation) no
	// longer matches.
	//
	// If committed and running() is false, the goroutine that was driving
	// it is simply gone: either this manager restarted mid-install (inFlight
	// is memory, lost on restart) or, in the same shape, the process was
	// killed. A resync interval replaying that would make the restart
	// interval the reinstall interval, and the machine's real state is
	// unknown at that point -- media may still be attached, a boot override
	// may still be set, disks may be mid-write. finalize's own comment
	// already reads this identical combination -- non-terminal phase, plus
	// running() == false -- as "there is cleanup to do here"; Reconcile must
	// not read it as "start an install" instead.
	if fi.Status.ObservedGeneration == fi.Generation {
		return r.failRestarted(ctx, &fi)
	}

	fm, err := r.getMachine(ctx, fi.Namespace, fi.Spec.MachineRef)
	if err != nil {
		return r.failPending(ctx, &fi, fmt.Sprintf("machine %q: %v", fi.Spec.MachineRef, err))
	}

	// Before the BMC is touched and before anything is built: an image
	// whose boot arguments point nowhere a BMC can reach is an install that
	// wipes nothing and hangs until its phase deadline, with nothing in the
	// object saying the operator's cluster was never configured for this.
	if err := provision.ValidateMediaURL(r.ProvisiondMediaURL); err != nil {
		return r.failPending(ctx, &fi, fmt.Sprintf(
			"frame-provisiond's media URL (the manager's -provisiond-media-url flag, the chart's provisiond.media.url) %v -- this cluster is not configured to provision machines", err))
	}

	if err := r.checkNotALiveClusterMember(ctx, fi.Spec.Hostname, fi.Spec.Network.Address); err != nil {
		return r.failPending(ctx, &fi, err.Error())
	}
	if err := r.checkHostnameNotTaken(ctx, &fi); err != nil {
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

	if !r.startInstall(fi.Spec.MachineRef, fi.UID) {
		// A concurrent reconcile already won the race to start this
		// machineRef -- for this object or another one naming it.
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// Stamped synchronously, before the goroutine below is spawned, and
	// blocking: a process that dies between this Patch succeeding and the
	// goroutine's own first status write leaves status.observedGeneration
	// committed for this generation with no goroutine behind it in any
	// future process -- which is exactly the signal this Reconcile's own
	// restart-recovery check above reads as "resume is unsafe, fail
	// closed". Committing this *after* startInstall's in-memory claim,
	// rather than before, would leave a window where a crash between the
	// two could lose the in-memory claim (restart) while this write never
	// happened -- undetectable as a restart at all. Ordered the other way,
	// a crash in that same window is still caught: the durable write
	// happened, inFlight did not survive, and the check above reads that
	// correctly as "unsafe to resume".
	patch := client.MergeFrom(fi.DeepCopy())
	fi.Status.ObservedGeneration = fi.Generation
	if err := r.Status().Patch(ctx, &fi, patch); err != nil {
		r.finishInstall(fi.Spec.MachineRef, fi.UID)
		return ctrl.Result{}, err
	}

	// The install UID is generated here, per run. It used to be
	// string(fi.UID) -- the object's own metadata.uid, which every
	// viewer-tier account can read with `kubectl get frameinstall -o yaml`
	// BEFORE the install runs, which is when knowing it would matter.
	//
	// It is not written to any readable field on the happy path. It is not
	// secret afterwards either, and saying otherwise would be the same kind
	// of overclaim this replaced: on a marker mismatch,
	// provision.WaitForOurSystem's error names both the UID found and the
	// UID expected, and finishStatus writes that error into
	// status.message. So a FAILED install publishes its own UID. That costs
	// nothing -- a UID is scoped to one installation and is dead the moment
	// that installation ends, which is exactly when this happens.
	//
	// What the marker is for (design §7) is making trust-on-first-use
	// proportionate: an impostor answering at the target address would have
	// to present a UID that existed nowhere but inside this installation.
	// A value anyone with read access can look up does not carry that, and
	// three places in this codebase stated that it did.
	//
	// 16 bytes from crypto/rand, hex-encoded -- the same shape
	// cmd/bootstrap's newInstallUID and provision's newToken use, for the
	// same reason. It lives in this goroutine's Spec and nowhere else; a
	// manager restart loses it, which is correct, because a restart
	// mid-install is a failed install either way (failRestarted above).
	installUID, err := newInstallUID()
	if err != nil {
		r.finishInstall(fi.Spec.MachineRef, fi.UID)
		return r.failPending(ctx, &fi, fmt.Sprintf("generating an install UID: %v", err))
	}

	spec := toProvisionSpec(&fi, installUID, sshPub, joinToken)
	opts := r.options(&fi, sshPriv)
	key := client.ObjectKeyFromObject(&fi)
	// Captured as a scalar rather than read through &fi inside the closure
	// below: that closure runs on the goroutine spawned a few lines down,
	// after Reconcile has returned and &fi has been handed to recordTask in
	// between -- reading fi.UID from inside the closure would be reading an
	// object someone else has had a pointer to.
	uid := fi.UID
	deps := provision.Deps{
		BMC:         bmc,
		Images:      r.Images,
		SSH:         r.SSH,
		Nodes:       r.Nodes,
		Report:      func(p provision.Phase) { r.reportPhase(context.WithoutCancel(ctx), key, p) },
		ReportToken: func(tok string) { r.rememberToken(uid, tok) },
	}

	r.recordTask(ctx, &fi, framev1beta1.TaskVerbCreate,
		fmt.Sprintf("install %s on machine %s", fi.Spec.Hostname, fi.Spec.MachineRef))

	go r.runInstall(ctx, fi.Spec.MachineRef, uid, key, deps, spec, opts)

	log.Info("started FrameInstall", "frameinstall", req.NamespacedName, "machine", fi.Spec.MachineRef)
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

// failRestarted handles a non-terminal FrameInstall whose current
// generation was already committed to provision.Install (status.
// observedGeneration == metadata.generation) but has no goroutine driving
// it in this process. A restart mid-install is a failed install, not a
// resumed one: replaying the destructive sequence because a process died is
// the same fault a resync interval replaying it would be, and the
// machine's real state -- media attached or not, boot override set or not,
// disks mid-write or not -- is unknown at this point. It is never retried
// automatically, the same as any other Install failure; a retry is a new
// FrameInstall, created deliberately.
//
// The cleanup attempted here is the same one finalize makes on deletion:
// eject media, clear the boot override. A failure to do so is folded into
// the recorded message rather than returned as an error, because unlike
// finalize -- which must not let the object be deleted while cleanup is
// unresolved -- there is nothing to block here; retrying this exact
// function on the next resync would attempt the identical cleanup against
// the identical unknown machine state, not converge on anything new.
func (r *FrameInstallReconciler) failRestarted(ctx context.Context, fi *framev1beta1.FrameInstall) (ctrl.Result, error) {
	prevPhase := fi.Status.Phase
	if prevPhase == "" {
		// Committed (observedGeneration stamped) but the goroutine died
		// before its own first report(PhasePending) call ever landed --
		// still Pending is where every install starts.
		prevPhase = string(provision.PhasePending)
	}
	msg := fmt.Sprintf(
		"the controller restarted mid-install (was in phase %s); a mid-install restart cannot be resumed safely and is not retried automatically -- the machine may still have installer media attached and a one-time boot override set",
		prevPhase)

	fm, err := r.getMachine(ctx, fi.Namespace, fi.Spec.MachineRef)
	if err == nil {
		if bmc, berr := r.NewBMC(ctx, r.Client, fi.Namespace, fm.Spec.BMC); berr == nil {
			ejectErr := bmc.EjectMedia(ctx)
			clearErr := bmc.ClearBootOverride(ctx)
			if ejectErr != nil || clearErr != nil {
				msg = fmt.Sprintf("%s (cleanup also failed: eject=%v clear=%v)", msg, ejectErr, clearErr)
			}
		}
	}

	patch := client.MergeFrom(fi.DeepCopy())
	fi.Status.Phase = string(provision.PhaseFailed)
	fi.Status.FailedPhase = prevPhase
	fi.Status.Message = truncateString(msg, 512)
	now := metav1.Now()
	fi.Status.PhaseSince = &now
	if err := r.Status().Patch(ctx, fi, patch); err != nil {
		return ctrl.Result{}, err
	}
	r.Recorder.Event(fi, corev1.EventTypeWarning, "InstallRestarted", msg)
	r.recordTask(ctx, fi, framev1beta1.TaskVerbUpdate,
		fmt.Sprintf("install %s on machine %s reached Failed: controller restarted mid-install", fi.Spec.Hostname, fi.Spec.MachineRef))
	return ctrl.Result{}, nil
}

// runInstall is the goroutine Reconcile starts, keyed by machineRef so
// running() and startInstall() below can tell "already in flight" apart
// from "not yet started" without touching the apiserver. It always frees
// its slot on return, success or failure -- inFlight tracks "a call is
// running", not "the last call succeeded".
//
// ctx is the manager's own long-lived context (passed through from
// Reconcile, which controller-runtime hands the manager's Start context
// unwrapped -- there is no per-Reconcile-call cancellation to accidentally
// inherit here), not context.Background(): on shutdown the manager cancels
// it, which unwinds provision.Install's in-progress phase the same way any
// other deadline does, and its cleanup defer -- built on
// context.WithoutCancel internally -- still gets its own budget to eject
// media and clear the boot override before the process exits. The two
// status-writing calls below use a value derived with context.WithoutCancel
// for the same reason install.go's own cleanup does: the record of what
// happened must still be written even if ctx is already Done.
func (r *FrameInstallReconciler) runInstall(ctx context.Context, machineRef string, uid types.UID, key client.ObjectKey, deps provision.Deps, spec provision.Spec, opts provision.Options) {
	defer r.finishInstall(machineRef, uid)
	res, err := provision.Install(ctx, deps, spec, opts)
	r.finishStatus(context.WithoutCancel(ctx), key, machineRef, res, err)
}

// running reports whether a provision.Install call for machineRef is active
// in this process -- for this FrameInstall or, since a BMC is what is
// actually being driven and two objects can name the same machineRef, for a
// different one. Read-only and unlocked-fast-path-free on purpose: it is
// called on every reconcile of an object that has not reached a terminal
// phase yet, so it has to be cheap, but it must never race startInstall's
// check-and-set below -- both take the same mutex.
func (r *FrameInstallReconciler) running(machineRef string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.inFlight[machineRef]
	return ok
}

// startInstall atomically claims machineRef for uid, returning false if it
// was already claimed -- by this object or another one naming the same
// machine. This is the actual run-once guard against a race two concurrent
// Reconcile calls could otherwise hit: running() above is a fast, separate
// read that lets Reconcile skip the rest of its own work early, but only
// startInstall's check-and-set is what may not run twice for the same
// machineRef.
func (r *FrameInstallReconciler) startInstall(machineRef string, uid types.UID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight == nil {
		r.inFlight = map[string]types.UID{}
	}
	if _, ok := r.inFlight[machineRef]; ok {
		return false
	}
	r.inFlight[machineRef] = uid
	return true
}

func (r *FrameInstallReconciler) finishInstall(machineRef string, uid types.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, machineRef)
	delete(r.tokens, uid)
}

// rememberToken records the image token of an in-flight install, keyed by
// the FrameInstall's UID. It is provision.Deps.ReportToken's callback,
// invoked once an image exists -- see the comment on Deps.ReportToken for
// why this is the only place that token is ever kept.
func (r *FrameInstallReconciler) rememberToken(uid types.UID, token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tokens == nil {
		r.tokens = map[types.UID]string{}
	}
	r.tokens[uid] = token
}

// tokenFor returns the image token remembered for uid, if any. Absent means
// either no image has been built yet, or this manager did not start this
// installation (a restart loses tokens along with inFlight).
func (r *FrameInstallReconciler) tokenFor(uid types.UID) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tok, ok := r.tokens[uid]
	return tok, ok
}

// reportInstallerLiveness refreshes the InstallerResponding condition on the
// requeue this reconciler already performs every 15 seconds while an install
// is in flight. It returns nothing: there is no outcome here a caller could
// act on, which is the point.
//
// Only during Installing. In every other phase Frame has a better signal than
// a beacon -- it is talking to the BMC, or to the machine itself -- and a
// liveness condition there would be noise competing with fact.
func (r *FrameInstallReconciler) reportInstallerLiveness(ctx context.Context, fi *framev1beta1.FrameInstall) {
	if r.Progress == nil || fi.Status.Phase != string(provision.PhaseInstalling) {
		return
	}
	token, ok := r.tokenFor(fi.UID)
	if !ok {
		// Reachable, but not by a restart of *this* object: a restart loses
		// both inFlight and tokens together, and a restarted object's own
		// next Reconcile finds running() false and goes to failRestarted
		// before this method is ever called.
		//
		// What actually reaches this line: inFlight is keyed by machineRef,
		// tokens by UID. A second FrameInstall naming the same machineRef
		// can claim inFlight[machineRef] (nothing refuses that -- see
		// inFlight's own doc comment) while *this* object is still sitting
		// at Installing from a run this manager no longer has a token for.
		// r.running(machineRef) then reads true for this object too, this
		// method runs, and tokenFor(fi.UID) finds nothing, because the
		// token in memory now belongs to the other object's UID. Said
		// plainly rather than reported as a silent machine.
		r.setCondition(ctx, fi, installerRespondingCondition(provision.BeaconState{}, false,
			fmt.Errorf("this manager holds no image token for this installation -- either it restarted, or another FrameInstall now owns machine %q", fi.Spec.MachineRef), time.Now()))
		return
	}
	// Bounded independently of ctx, which is controller-runtime's reconcile
	// context and carries no deadline of its own. Progress is the one call
	// on this ImageStore that runs synchronously on the reconcile path
	// itself (Build/Remove run on the per-install goroutine instead), and
	// this reconciler processes one request at a time
	// (SetupWithManager sets no MaxConcurrentReconciles override). A
	// provisiond that accepts the connection and never answers would
	// otherwise wedge every FrameInstall reconcile indefinitely, including
	// the finalizer's media-eject -- the diagnostic this method exists to
	// report would itself become the outage. Do not remove this: a timeout
	// surfaces as InstallerResponding=Unavailable, which is already handled.
	pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, known, err := r.Progress.Progress(pollCtx, token)
	r.setCondition(ctx, fi, installerRespondingCondition(st, known, err, time.Now()))
}

// setCondition writes one condition, in the Get-then-Patch shape reportPhase
// uses. A failure to write is dropped on purpose: this is a diagnostic, and
// failing a reconcile because a diagnostic could not be recorded would let it
// affect the installation it only exists to describe.
//
// Re-checks Phase == Installing against the freshly-Get object, not the
// caller's possibly-stale fi: without this, a condition written just before
// Installing ends (the ordinary case -- WaitForOurSystem regularly outlasts
// beaconLostAfter after the installer's own reboot, so the last beacon-based
// write before Ready is usually HeartbeatLost) is never corrected, and every
// successfully installed machine keeps a permanent "installer stopped
// responding" condition. finishStatus removes the condition outright once
// Install returns, for the same reason: this reconciler must never be the
// last writer of a condition describing a phase that has already ended.
func (r *FrameInstallReconciler) setCondition(ctx context.Context, fi *framev1beta1.FrameInstall, cond metav1.Condition) {
	var latest framev1beta1.FrameInstall
	if err := r.Get(ctx, client.ObjectKeyFromObject(fi), &latest); err != nil {
		return
	}
	if latest.Status.Phase != string(provision.PhaseInstalling) {
		return
	}
	patch := client.MergeFrom(latest.DeepCopy())
	cond.ObservedGeneration = latest.Generation
	meta.SetStatusCondition(&latest.Status.Conditions, cond)
	_ = r.Status().Patch(ctx, &latest, patch)
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

	// This call is the one point that knows Installing has just ended, one
	// way or another -- res.Phase is never Installing itself. A condition
	// that stops describing anything is worse than none: WaitForOurSystem
	// routinely outlasts beaconLostAfter after the installer's own reboot,
	// so without this, the LAST InstallerResponding write before a normal,
	// successful Ready would almost always be HeartbeatLost, and it would
	// never be corrected -- every successfully installed machine keeping a
	// permanent "installer stopped responding" condition. A no-op if
	// reportInstallerLiveness never got here (installs that fail before
	// Installing never had this condition to begin with).
	meta.RemoveStatusCondition(&fi.Status.Conditions, "InstallerResponding")

	// NOT gated on res.Phase == Ready. provision.Join returns the
	// kubeconfig in the Joining phase; two later things can still fail the
	// install -- the Ready poll timing out, and cleanup failing, which a
	// Task 5 ruling deliberately made an install failure. In both cases
	// res.Kubeconfig is populated and, gated on Ready, was thrown away: the
	// cluster exists, has a node in it, and nobody can ever talk to it,
	// because the only copy of its admin credential lived in a goroutine
	// that has now returned. A failed install whose kubeconfig is saved can
	// be recovered by hand; one whose kubeconfig is gone cannot be
	// recovered at all.
	//
	// Mode == "init" is still the "did this install start a new cluster"
	// test, for the reason it always was. len() is the "is there one to
	// save" test, which under the new gating is a real question rather than
	// a restatement of Mode.
	if fi.Spec.Cluster.Mode == "init" && len(res.Kubeconfig) > 0 {
		ns := r.OperatorNamespace
		if ns == "" {
			ns = fi.Namespace
		}
		if err := r.writeKubeconfigSecret(ctx, ns, &fi, res.Kubeconfig); err != nil {
			// A cluster exists and nobody can talk to it. The kubeconfig
			// existed only in this goroutine's memory, gone the moment this
			// call returns, and this reconciler's RBAC grants secrets
			// create but not update, so there is nothing to retry with.
			// Whatever phase Install reported, that is a failure, and one
			// that cannot be recovered from -- so it must not be left
			// looking like Ready in the print column an operator scans.
			log.Error(err, "the cluster was created but its kubeconfig Secret could not be written", "frameinstall", key)
			fi.Status.Phase = string(provision.PhaseFailed)
			// An install that already failed keeps the phase it failed in;
			// only a run that got all the way through is newly failed here.
			if fi.Status.FailedPhase == "" {
				fi.Status.FailedPhase = string(provision.PhaseReady)
			}
			msg := fmt.Sprintf("the cluster was created but saving kubeconfig Secret %s/%s-kubeconfig failed, so nobody can talk to it: %v", ns, fi.Name, err)
			if fi.Status.Message != "" {
				msg = fi.Status.Message + "; additionally, " + msg
			}
			fi.Status.Message = truncateString(msg, 512)
		} else {
			fi.Status.KubeconfigSecret = fi.Name + "-kubeconfig"
		}
	}

	if err := r.Status().Patch(ctx, &fi, patch); err != nil {
		log.Error(err, "could not record install result", "frameinstall", key)
		return
	}

	if fi.Status.Phase == string(provision.PhaseReady) {
		r.Recorder.Event(&fi, corev1.EventTypeNormal, "InstallReady", "installation reached Ready")
	} else {
		r.Recorder.Event(&fi, corev1.EventTypeWarning, "InstallFailed", fi.Status.Message)
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
	r.Recorder.Event(fi, corev1.EventTypeWarning, "InstallRefused", msg)
	// msg first, the fixed "install X on machine Y" context after: msg is
	// the only variable-length part of this string and the one worth
	// keeping if truncateString's 200-rune cap (FrameTaskSpec.Action's CRD
	// maxLength) has to cut something. A long hostname or machineRef cutting
	// into the actual reason, rather than the reverse, is how a truncated
	// audit entry stops saying anything useful.
	r.recordTask(ctx, fi, framev1beta1.TaskVerbUpdate,
		fmt.Sprintf("refused: %s (install %s on machine %s)", msg, fi.Spec.Hostname, fi.Spec.MachineRef))
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

// checkHostnameNotTaken is the structural half of the hostname guard CEL
// cannot express, minus the Ready-node case checkNotALiveClusterMember
// already refuses with its own, more specific message: a Node occupying
// this hostname that is not Ready, or another FrameInstall -- in any
// namespace -- that names the same hostname and has not yet reached a
// terminal phase.
//
// Cluster-wide on purpose, not client.InNamespace(fi.Namespace): a hostname
// becomes a Node name, and Node names are cluster-global regardless of which
// namespace the FrameInstall that requested one lives in. This reconciler's
// RBAC is already a ClusterRole (frameinstalls has no namespace restriction
// in config/rbac/role.yaml), so scoping the List here bought no additional
// safety, only a blind spot: two FrameInstalls in different namespaces
// naming the same hostname used to pass this check and collide on the Node
// name itself once both tried to install.
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
	if err := r.List(ctx, &installs); err != nil {
		return fmt.Errorf("listing FrameInstalls: %w", err)
	}
	for i := range installs.Items {
		other := &installs.Items[i]
		// Compared by UID, not by Name: Name alone is only unique within a
		// namespace, and this list is no longer scoped to one -- two
		// different objects in two different namespaces can share a Name,
		// and a Name-only comparison would wrongly treat one as "this
		// object itself" and skip it.
		if other.UID == fi.UID || other.Spec.Hostname != fi.Spec.Hostname {
			continue
		}
		if other.Status.Phase == string(provision.PhaseReady) || other.Status.Phase == string(provision.PhaseFailed) {
			continue
		}
		return fmt.Errorf("FrameInstall %q already targets hostname %q and has not reached a terminal phase", other.Namespace+"/"+other.Name, fi.Spec.Hostname)
	}
	return nil
}

// There is no checkDisksKnown here. It was removed: internal/redfish
// deliberately leaves Inventory.Drives nil (redfish/client.go's Probe --
// physical drives live under HPE's OEM SmartStorage tree on an iLO4, which
// nothing in this codebase walks), and framemachine_controller.go's
// mapInventory carries that nil straight through, so the check could never
// see a disk regardless of the machine's real inventory -- every real
// FrameInstall naming disks, which CEL requires for both defined layouts,
// would refuse in Pending unconditionally. Separately, even with a
// populated inventory, DriveInfo.Name (a Redfish drive name) and
// InstallDisk.ByID (CEL-forced to /dev/disk/by-id/...) are different
// namespaces a BMC has no way to reconcile: nothing about a Redfish drive
// name predicts what the installed kernel will call it under
// /dev/disk/by-id. The guard this task's brief specified could only ever
// pass against a fixture built to agree with itself. The disk-level check
// that actually runs where the disks are is the preseed's on-machine size
// assertion (design §8, layer 4); a controller-side equivalent is on the
// lot's list of gaps to close, not a check kept here in a shape that never
// worked.

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

// readJoinToken reads the Secret JoinTokenRef names. Its key is not fixed
// anywhere else in this lot -- frameinstall_types.go's CEL rule requires
// joinTokenRef to be set for mode: join but is silent on the Secret's
// shape, the way BMCSpec.CredentialsRef ("username"/"password") and
// SSHKeyRef ("id"/"id.pub") both are documented to be. "token" is this
// reconciler's own choice, made here for lack of a precedent to follow.
//
// "node-token" is accepted too, and checked first: it is the literal
// filename the token lives at on a k3s server
// (/var/lib/rancher/k3s/server/node-token, the same path
// frameinstall_types.go's own CEL error message names), so
// `kubectl create secret generic --from-file=/var/lib/rancher/k3s/server/node-token`
// -- the command an administrator actually runs, per design §6 -- produces
// a Secret keyed "node-token", not "token". Refusing that at runtime with
// no hint of the expected key is a worse first encounter with this
// reconciler than accepting the key the documented command actually
// produces.
func (r *FrameInstallReconciler) readJoinToken(ctx context.Context, namespace, name string) (string, error) {
	var sec corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &sec); err != nil {
		return "", err
	}
	for _, key := range []string{"node-token", "token"} {
		if tok := sec.Data[key]; len(tok) > 0 {
			return strings.TrimSpace(string(tok)), nil
		}
	}
	return "", fmt.Errorf("secret %s/%s has neither a node-token nor a token key", namespace, name)
}

// writeKubeconfigSecret stores a cluster-init install's kubeconfig, in
// namespace -- the operator's own namespace when one is configured
// (Reconciler.OperatorNamespace), the FrameInstall's own otherwise. design
// §6: without this, a new cluster exists and nobody can talk to it.
//
// An OwnerReference is set only when namespace equals owner's own
// namespace: Kubernetes' garbage collector resolves an OwnerReference by
// looking the owner up *within the child's namespace*, so one pointing at
// an object in a different namespace is accepted by the API server but
// never acted on -- setting it anyway would claim a cleanup guarantee this
// call cannot actually make. When they match (every test in this package,
// and any single-tenant deployment where FrameInstall and the operator
// share a namespace), it is what stops deleting the FrameInstall from
// orphaning a cluster-admin credential forever: nothing else in this
// reconciler deletes this Secret.
func (r *FrameInstallReconciler) writeKubeconfigSecret(ctx context.Context, namespace string, owner *framev1beta1.FrameInstall, kubeconfig []byte) error {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: owner.Name + "-kubeconfig", Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"kubeconfig": kubeconfig},
	}
	if namespace == owner.Namespace {
		if err := controllerutil.SetControllerReference(owner, sec, r.Scheme); err != nil {
			return fmt.Errorf("setting owner reference: %w", err)
		}
	}
	return r.Create(ctx, sec)
}

// toProvisionSpec translates the CRD into the Kubernetes-free Spec
// provision.Install actually runs against.
// newInstallUID generates the value provision.Spec.UID carries: proof,
// checked over SSH by provision.WaitForOurSystem, that the machine
// answering at the target address is the one this exact run installed and
// not merely something already listening there. It must be unguessable by
// anything that can read the FrameInstall, which is why it is not derived
// from the object.
func newInstallUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating install UID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func toProvisionSpec(fi *framev1beta1.FrameInstall, installUID, sshPublicKey, joinToken string) provision.Spec {
	var disks []provision.Disk
	for _, d := range fi.Spec.Layout.Disks {
		disks = append(disks, provision.Disk{ByID: d.ByID, SizeBytes: d.SizeBytes})
	}
	return provision.Spec{
		UID:      installUID,
		Hostname: fi.Spec.Hostname,
		Network: provision.Network{
			Address: fi.Spec.Network.Address,
			Gateway: fi.Spec.Network.Gateway,
			DNS:     fi.Spec.Network.DNS,
		},
		Layout: provision.Layout{
			Kind:  provision.LayoutKind(fi.Spec.Layout.Kind),
			Disks: disks,
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
// A running goroutine for this machine is left to finish rather than raced:
// provision.Install already ejects media and clears the boot override on
// every one of its own exits (install.go's cleanup defer), so requeueing
// until it is done is simpler than two callers issuing BMC writes at once.
// The case this finalizer actually exists for is the one running() cannot
// see at all -- a manager restart during Preparing/MediaAttached/Installing
// loses inFlight, and the object's own status.phase (not Ready or Failed)
// combined with running() being false is exactly what tells this finalizer
// there is real cleanup to do here, not merely to wait for. Reconcile's own
// restart-recovery branch (the ObservedGeneration check) reads that
// identical combination the same way, for the same reason, on the
// non-deleting path.
func (r *FrameInstallReconciler) finalize(ctx context.Context, fi *framev1beta1.FrameInstall) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(fi, frameInstallFinalizer) {
		return ctrl.Result{}, nil
	}
	if r.running(fi.Spec.MachineRef) {
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
