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
	"errors"
	"fmt"
	"net"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	framev1beta1 "github.com/gothshoot/frame/api/frame/v1beta1"
	"github.com/gothshoot/frame/internal/redfish"
)

// conditionReachable is the FrameMachine analogue of this package's shared
// Ready condition type (helpers.go): whether the last Redfish probe of the
// machine's BMC succeeded. It has its own type, rather than reusing
// conditionTypeReady, because "reachable" and "ready" ask different
// questions for a physical chassis — a machine can answer its BMC (reachable)
// while powered off, which is not a state any other controller in this
// package would call Ready.
const conditionReachable = "Reachable"

// FrameMachineReconciler polls a FrameMachine's BMC over Redfish and records
// what it read, and when.
type FrameMachineReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// NewClient builds the Redfish client for a machine. Tests replace it.
	// It is called on every pass; the client it returns replaces the one
	// held for the machine only when it targets a different BMC, user,
	// password or TLS trust (framemachine_sessions.go), so a reused Redfish
	// session survives from one probe to the next.
	NewClient func(ctx context.Context, kube client.Client, namespace string, bmc framev1beta1.BMCSpec) (redfish.Client, error)
	// ProbeInterval is the delay between two successful probes. Zero means
	// DefaultProbeInterval; anything under MinProbeInterval is raised to it.
	ProbeInterval time.Duration

	machines machineClients
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framemachines,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framemachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framemachines/finalizers,verbs=update
// BMC credentials: get, per namespace, through the frame-manager-secret-reader
// ClusterRole bound where FrameMachines live (docs/deployment.md, "Manager
// access to Secrets"); nothing on Secrets cluster-wide.
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

// Reconcile polls the machine's BMC and records what it read.
//
// A failed probe deliberately does not clear the previously stored
// inventory, sensors or event log: the console shows the last good reading
// beside its age rather than showing nothing, so status.lastProbeAt going
// stale is the signal, not a wiped screen.
func (r *FrameMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var fm framev1beta1.FrameMachine
	if err := r.Get(ctx, req.NamespacedName, &fm); err != nil {
		if apierrors.IsNotFound(err) {
			// The machine is gone: end its session rather than leave it open
			// on the BMC until the idle timeout.
			r.forget(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	fresh, err := r.NewClient(ctx, r.Client, fm.Namespace, fm.Spec.BMC)
	if err != nil {
		r.forget(ctx, req.NamespacedName)
		patch := client.MergeFrom(fm.DeepCopy())
		r.setCondition(&fm, metav1.ConditionFalse, "CredentialsUnavailable", err.Error())
		if perr := r.Status().Patch(ctx, &fm, patch); perr != nil {
			return ctrl.Result{}, perr
		}
		log.Info("Could not build a Redfish client for FrameMachine", "machine", req.NamespacedName, "error", err.Error())
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}
	rc := r.clientFor(ctx, req.NamespacedName, fresh)

	// Captured before runPowerRequest, which mutates fm.Status.LastPowerAction
	// / LastPowerActionAt directly on fm: a patch captured after that
	// mutation would diff against an already-mutated fm and silently write
	// nothing for those fields. Reused across every branch below —
	// including the one where the probe that follows then fails — so the
	// power action is never lost even when the probe doesn't succeed.
	patch := client.MergeFrom(fm.DeepCopy())

	// runPowerRequest never fails the reconcile: an action that fails is
	// recorded in status.lastPowerActionError and a Warning Event on fm, and
	// the timestamp advances either way. Every branch below patches status
	// through the patch captured above, so that record is always persisted.
	r.runPowerRequest(ctx, &fm, rc)

	snap, err := rc.Probe(ctx)
	if err != nil {
		reason := probeFailureReason(err)
		r.setCondition(&fm, metav1.ConditionFalse, reason, err.Error())
		if perr := r.Status().Patch(ctx, &fm, patch); perr != nil {
			return ctrl.Result{}, perr
		}
		retry := r.probeFailed(req.NamespacedName)
		log.Info("Redfish probe failed", "machine", req.NamespacedName, "reason", reason, "error", err.Error(), "retryIn", retry.String())
		return ctrl.Result{RequeueAfter: retry}, nil
	}

	applySnapshot(&fm.Status, snap)
	fm.Status.ObservedGeneration = fm.Generation
	r.setCondition(&fm, metav1.ConditionTrue, "Probed", "")
	if err := r.Status().Patch(ctx, &fm, patch); err != nil {
		return ctrl.Result{}, err
	}
	// snap.LogPossiblyStale (internal/redfish/types.go) is the only place
	// this recurrence of C1 shows up: the probe itself succeeded, the
	// condition is Probed/True, and EventLog/EventLogCounts/EventLogTotal
	// are all populated — but from the machine's oldest page, not its
	// newest, because the jump to the true last page could not be
	// confirmed. That is silent everywhere except here and in
	// status.eventLogPossiblyStale, which is why this logs at the default
	// level rather than behind V(1) the way a routine successful probe does
	// below: this is the one outcome of a "successful" probe someone
	// watching logs needs to actually see, not have to opt into.
	if snap.LogPossiblyStale {
		log.Info("IML log pagination could not reach the last page; event log may show the oldest entries, not the newest",
			"machine", req.NamespacedName)
	}
	log.V(1).Info("Probed FrameMachine", "machine", req.NamespacedName)
	return ctrl.Result{RequeueAfter: r.probeSucceeded(req.NamespacedName)}, nil
}

// runPowerRequest executes spec.powerRequest at most once. The guard is the
// timestamp: acting on "newer than the last action" rather than on a desired
// state is what stops the controller re-asserting a power state against
// someone who pressed the physical button, and is what makes a restart
// expressible at all — the state before and after is identical. The same
// pattern restarts a Deployment in lot 2, by writing restartedAt.
//
// Returns true when it acted, so Reconcile knows the status carries a change
// even if the probe that follows fails.
func (r *FrameMachineReconciler) runPowerRequest(ctx context.Context, fm *framev1beta1.FrameMachine, rc redfish.Client) bool {
	req := fm.Spec.PowerRequest
	if req == nil {
		return false
	}
	if last := fm.Status.LastPowerActionAt; last != nil && !req.RequestedAt.After(last.Time) {
		return false
	}

	var err error
	switch req.Action {
	case framev1beta1.PowerActionOn:
		err = rc.Reset(ctx, "On")
	case framev1beta1.PowerActionGracefulShutdown:
		err = rc.Reset(ctx, "GracefulShutdown")
	case framev1beta1.PowerActionForceOff:
		err = rc.Reset(ctx, "ForceOff")
	case framev1beta1.PowerActionForceRestart:
		err = rc.Reset(ctx, "ForceRestart")
	case framev1beta1.PowerActionClearSEL:
		err = rc.ClearLog(ctx)
	case framev1beta1.PowerActionIndicatorLedOn:
		err = rc.SetIndicatorLED(ctx, true)
	case framev1beta1.PowerActionIndicatorLedOff:
		err = rc.SetIndicatorLED(ctx, false)
	default:
		err = fmt.Errorf("unknown power action %q", req.Action)
	}

	// The timestamp advances whether or not the action succeeded. A failed
	// action that left the timestamp behind would be retried on every
	// reconcile, once a minute, forever — and "force off, repeatedly, until
	// it works" is not a behaviour anyone asked for. The failure is carried
	// by status.lastPowerActionError and a Warning Event, deliberately not
	// by the Reachable condition: Reachable describes whether the BMC
	// answers, and a power action can fail while the BMC is perfectly
	// reachable — flipping Reachable would be lying about a different thing.
	now := metav1.Now()
	fm.Status.LastPowerAction = string(req.Action)
	fm.Status.LastPowerActionAt = &now

	if err != nil {
		// Truncated to LastPowerActionError's MaxLength=256: an over-long
		// value is rejected by the apiserver, which would turn a failed
		// power action into a failed status write, losing the record
		// entirely rather than just losing the tail of the message.
		fm.Status.LastPowerActionError = truncateError(err, 256)
		r.Recorder.Event(fm, corev1.EventTypeWarning, "PowerActionFailed",
			fmt.Sprintf("%s: %v", req.Action, err))
		return true
	}
	// A later success clears an earlier failure — without this, the field
	// would keep reporting an error the machine has since recovered from.
	fm.Status.LastPowerActionError = ""
	r.Recorder.Event(fm, corev1.EventTypeNormal, "PowerAction", string(req.Action))
	return true
}

// truncateError renders err's text, cut to at most n runes, so it always
// fits LastPowerActionError's MaxLength without the apiserver rejecting the
// status write.
func truncateError(err error, n int) string {
	return truncateString(err.Error(), n)
}

// truncateString cuts s to at most n runes. mapEventLog uses it on
// EventLogEntry.Message (MaxLength=512): the message is free text from the
// BMC's own log, this package doesn't control its length, and a single
// entry over the limit must not turn into a rejected status write — the
// whole point of giving the field a MaxLength was to make growth bounded
// and predictable, not to add a new way for one long line to fail the
// patch.
func truncateString(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// setCondition writes the Reachable condition via meta.SetStatusCondition
// rather than by hand: a hand-rolled replace-only-if-Status-differs check is
// exactly the class of bug setcondition_regression_test.go guards against
// elsewhere in this package — a reason-only change silently dropped.
func (r *FrameMachineReconciler) setCondition(fm *framev1beta1.FrameMachine, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&fm.Status.Conditions, metav1.Condition{
		Type:               conditionReachable,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: fm.Generation,
	})
}

// probeFailureReason maps a Probe error to a Reachable condition reason.
func probeFailureReason(err error) string {
	switch {
	case errors.Is(err, redfish.ErrTLS):
		return "TLSError"
	case errors.Is(err, redfish.ErrAuth):
		return "AuthFailed"
	case errors.Is(err, redfish.ErrUnsupported):
		return "Unsupported"
	case errors.Is(err, context.DeadlineExceeded):
		return "Timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "Timeout"
	}
	return "ProbeFailed"
}

// applySnapshot maps a Redfish Snapshot onto FrameMachineStatus, field by
// field against internal/redfish/types.go.
//
// PowerState, PostState, the inventory and the event log are always written
// — a probe that reached the machine learned all of those regardless of
// where POST is. Sensors is the one field that depends on
// snap.SensorsTrustworthy (Finding 1, internal/redfish/client.go): the
// captured iLO4 replays a cached Thermal/Power reading as if it were live
// whenever the machine is not powered on, or is still mid-POST — CPU1 at
// 40 C, Status.State "Enabled", twenty minutes after the machine was powered
// off in a 20 C room. Untrustworthy sensors are cleared rather than kept
// beside a caveat: a powered-off machine is a fact the console can state
// plainly, and a frozen 40 C rendered next to "powered off" invites the
// reader to believe the number. This is not the unreachable-BMC case — there
// (the probe-failure branch in Reconcile) the controller keeps the last
// reading beside its age, because nothing better is known; here something
// better is known, and it is that the reading is fiction. SensorsValidAt is
// left where it was in that case: it records when a trustworthy reading was
// last taken, and this probe did not take one.
func applySnapshot(status *framev1beta1.FrameMachineStatus, snap *redfish.Snapshot) {
	status.PowerState = snap.PowerState
	status.PostState = snap.PostState
	status.IndicatorLED = snap.IndicatorLED
	status.Inventory = mapInventory(&snap.Inventory)

	if snap.SensorsTrustworthy {
		status.Sensors = mapSensors(&snap.Sensors)
		validAt := metav1.Now()
		status.SensorsValidAt = &validAt
	} else {
		status.Sensors = nil
	}

	status.EventLog = mapEventLog(snap.Log)
	status.EventLogCounts = mapLogCounts(snap.LogCounts)
	status.EventLogTotal = int32(snap.LogTotal)
	status.EventLogPossiblyStale = snap.LogPossiblyStale
	now := metav1.Now()
	status.LastProbeAt = &now
}

// Inventory bounds, mirrored from the kubebuilder markers on
// api/frame/v1beta1/framemachine_types.go. The apiserver rejects a status
// update whole when one list is one item too long or one BMC string one
// character too long — and a BMC controls both. mapInventory therefore caps
// every list and clips every string to what the CRD accepts, so an odd
// firmware string costs its own tail, not the machine's entire status.
const (
	maxProcessors         = 8
	maxMemoryModules      = 48
	maxDrives             = 32
	maxStorageControllers = 8
	maxNetworkAdapters    = 16
	maxStatusReasons      = 16
)

// clip cuts s to at most n characters (Unicode code points, the unit of the
// CRD's MaxLength), never splitting a character.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// capped returns at most n leading items of xs.
func capped[T any](xs []T, n int) []T {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

func mapInventory(inv *redfish.Inventory) *framev1beta1.MachineInventory {
	out := &framev1beta1.MachineInventory{
		Manufacturer:   clip(inv.Manufacturer, 256),
		Model:          clip(inv.Model, 256),
		SerialNumber:   clip(inv.SerialNumber, 128),
		BIOSVersion:    clip(inv.BIOSVersion, 128),
		BMCFirmware:    clip(inv.BMCFirmware, 128),
		TotalMemoryGiB: inv.TotalMemoryGiB,
	}
	for _, p := range capped(inv.Processors, maxProcessors) {
		out.Processors = append(out.Processors, framev1beta1.ProcessorInfo{
			Socket:  clip(p.Socket, 64),
			Model:   clip(p.Model, 256),
			Cores:   p.Cores,
			Threads: p.Threads,
		})
	}
	for _, m := range capped(inv.MemoryModules, maxMemoryModules) {
		out.MemoryModules = append(out.MemoryModules, framev1beta1.MemoryModuleInfo{
			Slot:         clip(m.Slot, 64),
			SizeMiB:      m.SizeMiB,
			Type:         clip(m.Type, 64),
			Manufacturer: clip(m.Manufacturer, 256),
		})
	}
	// inv.Drives is legitimately empty on a machine whose BMC has no
	// SmartStorage tree (internal/redfish tolerates a not-found there rather
	// than failing the probe) — ranging over an empty slice is the normal
	// case here, not an error to special-case around.
	for _, d := range capped(inv.Drives, maxDrives) {
		out.Drives = append(out.Drives, framev1beta1.DriveInfo{
			Name:          clip(d.Name, 256),
			Model:         clip(d.Model, 256),
			SizeGB:        d.SizeGB,
			Protocol:      clip(d.Protocol, 64),
			Health:        clip(d.Health, 64),
			SerialNumber:  clip(d.SerialNumber, 128),
			Location:      clip(d.Location, 64),
			MediaType:     clip(d.MediaType, 32),
			StatusReasons: capped(d.StatusReasons, maxStatusReasons),

			Firmware:                clip(d.Firmware, 64),
			RotationSpeedRPM:        d.RotationSpeedRPM,
			LinkSpeedMbps:           d.LinkSpeedMbps,
			TemperatureCelsius:      d.TemperatureCelsius,
			PowerOnHours:            d.PowerOnHours,
			SSDEnduranceUsedPercent: d.SSDEnduranceUsedPercent,
		})
	}
	for _, sc := range capped(inv.StorageControllers, maxStorageControllers) {
		out.StorageControllers = append(out.StorageControllers, framev1beta1.StorageControllerInfo{
			Location:          clip(sc.Location, 128),
			Model:             clip(sc.Model, 256),
			Firmware:          clip(sc.Firmware, 64),
			Health:            clip(sc.Health, 64),
			BackupPowerStatus: clip(sc.BackupPowerStatus, 64),
			CacheStatus:       clip(sc.CacheStatus, 64),
		})
	}
	for _, n := range capped(inv.NetworkAdapters, maxNetworkAdapters) {
		out.NetworkAdapters = append(out.NetworkAdapters, framev1beta1.NetworkAdapterInfo{
			Name:   clip(n.Name, 256),
			MAC:    clip(n.MAC, 64),
			Status: clip(n.Status, 64),
		})
	}
	return out
}

func mapSensors(s *redfish.Sensors) *framev1beta1.MachineSensors {
	out := &framev1beta1.MachineSensors{
		PowerConsumedWatts: s.PowerConsumedWatts,
	}
	for _, t := range s.Temperatures {
		out.Temperatures = append(out.Temperatures, framev1beta1.TemperatureReading{
			Name:          t.Name,
			Celsius:       t.Celsius,
			UpperCritical: t.UpperCritical,
			Health:        t.Health,
		})
	}
	for _, f := range s.Fans {
		out.Fans = append(out.Fans, framev1beta1.FanReading{
			Name:    f.Name,
			Reading: f.Reading,
			Units:   f.Units,
			Health:  f.Health,
		})
	}
	for _, p := range s.PowerSupplies {
		out.PowerSupplies = append(out.PowerSupplies, framev1beta1.PowerSupplyReading{
			Name:                 p.Name,
			Health:               p.Health,
			State:                p.State,
			LastPowerOutputWatts: p.LastPowerOutputWatts,
		})
	}
	return out
}

// eventLogMessageMaxLength mirrors EventLogEntry.Message's
// +kubebuilder:validation:MaxLength (framemachine_types.go). A message this
// package didn't truncate itself would fail the whole status Patch on the
// one entry that happens to be long, rather than being cut the way
// LastPowerActionError already is.
const eventLogMessageMaxLength = 512

// mapEventLog retains only the most recent redfish.EventLogRetainCount
// entries, matching FrameMachineStatus.EventLog's
// +kubebuilder:validation:MaxItems=25 (kept as a literal there — a
// kubebuilder marker cannot reference a Go constant — so that number and
// redfish.EventLogRetainCount must be kept in sync by hand).
// internal/redfish sorts Snapshot.Log newest-first (decode.go's readLog
// sorts by Created.After before returning), so index 0 is the newest entry
// and keeping the head is what keeps the newest ones. The whole reason this
// retains 25 entries at all is to answer "why did this machine reboot" — a
// question about the newest entries — so keeping the tail would silently
// retain the oldest quarter-century of history instead.
func mapEventLog(entries []redfish.LogEntry) []framev1beta1.EventLogEntry {
	if len(entries) > redfish.EventLogRetainCount {
		entries = entries[:redfish.EventLogRetainCount]
	}
	out := make([]framev1beta1.EventLogEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, framev1beta1.EventLogEntry{
			ID:       e.ID,
			Severity: e.Severity,
			Message:  truncateString(e.Message, eventLogMessageMaxLength),
			Created:  metav1.NewTime(e.Created),
		})
	}
	return out
}

func mapLogCounts(counts map[string]int) map[string]int32 {
	if counts == nil {
		return nil
	}
	out := make(map[string]int32, len(counts))
	for k, v := range counts {
		out[k] = int32(v)
	}
	return out
}

// SetupWithManager sets up the controller with the Manager.
//
// Only generation changes wake the controller. Every successful probe writes
// status (lastProbeAt at the least), and without this filter that write came
// back as a watch event and started the next probe at once: on a real ML350
// Gen9 the machine was re-probed every ~13 s, the time one full Redfish walk
// takes, instead of every 60 s — the iLO answering one inventory walk after
// another, with no pause. The agent's status.storage writes (internal/agent) woke it
// too. A spec change (power request, nodeRef, BMC) bumps the generation and
// still reconciles immediately; the cadence otherwise comes from RequeueAfter
// alone: ProbeInterval after a success, a doubling backoff after a failure.
//
// The Redfish sessions the controller holds are ended when the manager
// stops (sessionLogoutOnStop).
func (r *FrameMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.Add(r.sessionLogoutOnStop()); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.FrameMachine{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("framemachine").
		Complete(r)
}
