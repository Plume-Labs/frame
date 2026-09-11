package provision

import (
	"context"
	"fmt"
	"net"
	"time"
)

// requiredPhaseTimeouts is every phase Install waits on. Each must carry its
// own configured budget: a missing map entry is a zero Duration, which is a
// context already expired before it is even used, and an operator seeing
// "context deadline exceeded" for that has no way to tell "this genuinely
// ran out of time" from "nobody configured this phase's budget at all".
var requiredPhaseTimeouts = []Phase{
	PhasePending, PhasePreparing, PhaseMediaAttached, PhaseInstalling, PhaseJoining, PhaseReady,
}

// validateOptions refuses malformed input before the machine is ever
// touched: a boot mode that isn't exactly what SetBootOnce accepts, or a
// phase with no configured timeout (see requiredPhaseTimeouts).
func validateOptions(o Options) error {
	if o.BootMode != "UEFI" && o.BootMode != "Legacy" {
		return fmt.Errorf("boot mode %q: must be exactly \"UEFI\" or \"Legacy\", never inherited from the machine", o.BootMode)
	}
	for _, p := range requiredPhaseTimeouts {
		if o.PhaseTimeout[p] <= 0 {
			return fmt.Errorf("phase %s has no configured timeout", p)
		}
	}
	return nil
}

// validatePendingSpec runs the shape checks Join would otherwise run for the
// first time deep into the Joining phase -- after the disk has already been
// wiped. None of them need the machine, so all of them run here instead,
// before it is ever touched.
func validatePendingSpec(s Spec, nodeAddress string) error {
	if err := validateK3sVersion(s.Cluster.K3sVersion); err != nil {
		return fmt.Errorf("cluster target: %w", err)
	}
	if err := validateNodeAddress(nodeAddress); err != nil {
		return fmt.Errorf("cluster target: %w", err)
	}
	if s.Cluster.Mode == ClusterJoin {
		if err := validateJoinToken(s.Cluster.JoinToken); err != nil {
			return fmt.Errorf("cluster target: %w", err)
		}
		if err := validateServerURL(s.Cluster.ServerURL); err != nil {
			return fmt.Errorf("cluster target: %w", err)
		}
	}
	return nil
}

// phaseErr distinguishes three things that otherwise print identically: this
// phase's own timeout expiring, the caller canceling the whole Install call,
// and a collaborator that simply failed on its own terms. An operator
// reading a failure needs to know which one happened, and phaseCtx.Err() is
// only non-nil in the first two cases -- so the underlying error is returned
// unchanged whenever the phase's own context is not the reason this failed.
func phaseErr(phaseCtx context.Context, underlying error) error {
	switch phaseCtx.Err() {
	case context.DeadlineExceeded:
		return fmt.Errorf("this phase's own timeout expired: %w", underlying)
	case context.Canceled:
		return fmt.Errorf("the request was canceled: %w", underlying)
	default:
		return underlying
	}
}

// Install runs one installation from end to end: confirm the machine,
// build its image, attach it, wait for the installed system to prove it is
// ours, join it to a cluster, and wait for the cluster to agree.
//
// It never retries. A failure leaves a machine in a state a human should
// look at, and replaying a destructive operation on their behalf is a
// fault; a retry is a new installation, created deliberately.
func Install(ctx context.Context, d Deps, s Spec, o Options) (res Result, err error) {
	res = Result{Phase: PhasePending}
	report := func(p Phase) {
		res.Phase = p
		if d.Report != nil {
			d.Report(p)
		}
	}
	fail := func(p Phase, e error) (Result, error) {
		res.FailedPhase = p
		report(PhaseFailed)
		return res, e
	}
	report(PhasePending)

	// Pure input validation first, before the machine -- or even the BMC --
	// is touched at all. A malformed K3sVersion or ServerURL used to be
	// refused only deep into Joining, after the disk was already wiped; a
	// missing phase timeout used to surface as a bare "context deadline
	// exceeded" with nothing saying the budget was never configured.
	if err := validateOptions(o); err != nil {
		return fail(PhasePending, err)
	}
	if err := validatePendingSpec(s, o.NodeAddress); err != nil {
		return fail(PhasePending, err)
	}

	// Pending: the guard, before anything is built or attached. The serial
	// is retyped by hand; this is what catches "I pointed at the wrong
	// machine", and it must refuse before a single byte is written anywhere.
	pendingCtx, cancelPending := context.WithTimeout(ctx, o.PhaseTimeout[PhasePending])
	defer cancelPending()
	serial, serialErr := d.BMC.Serial(pendingCtx)
	if serialErr != nil {
		return fail(PhasePending, phaseErr(pendingCtx, fmt.Errorf("reading the machine's serial: %w", serialErr)))
	}
	switch {
	// An empty confirmation and an empty reported serial compare equal to
	// each other, so the comparison below must never be reached with either
	// side empty: doing so would run the whole destructive sequence with no
	// confirmation having actually happened. Checked, and named, separately
	// -- ssh.go's WaitForOurSystem guards the identical shape for an empty
	// install UID, on the less dangerous of the two checks here (a wrong
	// marker match versus a wrong machine wiped).
	case o.ConfirmSerial == "":
		return fail(PhasePending, fmt.Errorf("no serial was confirmed for this install: refusing, because an empty confirmation proves nothing about which machine this is"))
	case serial == "":
		return fail(PhasePending, fmt.Errorf("the machine reports an empty serial: refusing, because it cannot be compared against the confirmed serial %q", o.ConfirmSerial))
	case serial != o.ConfirmSerial:
		return fail(PhasePending, fmt.Errorf(
			"this machine reports serial %q and the request confirmed %q: refusing, because the cost of being wrong here is a wiped disk on the wrong machine",
			serial, o.ConfirmSerial))
	}

	// Preparing. Every phase gets its own deadline: a build that hangs must
	// not consume the install's whole budget before the machine is ever
	// touched.
	prepCtx, cancelPrep := context.WithTimeout(ctx, o.PhaseTimeout[PhasePreparing])
	defer cancelPrep()
	report(PhasePreparing)
	url, token, buildErr := d.Images.Build(prepCtx, s)
	if buildErr != nil {
		return fail(PhasePreparing, phaseErr(prepCtx, fmt.Errorf("building the installer image: %w", buildErr)))
	}

	// From here on the machine has been touched, so every exit cleans up.
	// Named returns are what let this defer amend the outcome the rest of
	// the function already decided: a machine left with media attached and
	// a boot override set is not a completed install, whatever the earlier
	// phases reported, and cleanup failing must make Install fail too.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()

		ejectErr := d.BMC.EjectMedia(cleanup)
		clearErr := d.BMC.ClearBootOverride(cleanup)

		// The image is only removed once the eject it depends on has
		// actually succeeded: removing it unconditionally deletes the ISO
		// out from under a virtual-media URL that, if eject failed, may
		// still be attached.
		var removeErr error
		if ejectErr == nil {
			removeErr = d.Images.Remove(cleanup, token)
		}

		if ejectErr != nil || clearErr != nil || removeErr != nil {
			priorPhase := res.Phase
			report(PhaseFailed)
			if res.FailedPhase == "" {
				res.FailedPhase = priorPhase
			}
			cleanupErr := fmt.Errorf(
				"cleanup failed and the machine needs attention -- it may still have installer media attached and a one-time boot override set (eject: %v, clear boot override: %v, remove image: %v)",
				ejectErr, clearErr, removeErr)
			if err != nil {
				err = fmt.Errorf("%w; additionally, %w", err, cleanupErr)
			} else {
				err = cleanupErr
			}
		}
	}()

	// MediaAttached: insert, then re-read. InsertVirtualMedia returning 200
	// is not the same as media being attached, and booting with none wastes
	// twenty minutes to reach a timeout that explains nothing.
	report(PhaseMediaAttached)
	mediaCtx, cancelMedia := context.WithTimeout(ctx, o.PhaseTimeout[PhaseMediaAttached])
	defer cancelMedia()
	if err := d.BMC.InsertMedia(mediaCtx, url); err != nil {
		return fail(PhaseMediaAttached, phaseErr(mediaCtx, fmt.Errorf("inserting installer media: %w", err)))
	}
	ok, mediaErr := d.BMC.MediaInserted(mediaCtx)
	if mediaErr != nil {
		return fail(PhaseMediaAttached, phaseErr(mediaCtx, fmt.Errorf("reading media-inserted status: %w", mediaErr)))
	}
	if !ok {
		return fail(PhaseMediaAttached, fmt.Errorf("the BMC accepted the insert but reports no media attached"))
	}
	if err := d.BMC.SetBootOnce(mediaCtx, "Cd", o.BootMode); err != nil {
		return fail(PhaseMediaAttached, phaseErr(mediaCtx, fmt.Errorf("setting the one-time boot override: %w", err)))
	}
	if err := d.BMC.Reset(mediaCtx, "ForceRestart"); err != nil {
		return fail(PhaseMediaAttached, phaseErr(mediaCtx, fmt.Errorf("power-cycling the machine: %w", err)))
	}

	// Installing: long and nearly blind. Between boot and the first SSH
	// answer, Redfish offers only PowerState and Oem.Hp.PostState, and lot 1
	// established this BMC replays cached sensor readings as live ones in
	// exactly this window, so nothing there is trusted. The phase ends on
	// the machine proving it is ours, not on anything the BMC reports.
	//
	// No phaseErr wrapping here: WaitForOurSystem already puts its own
	// context's deadline-or-cancellation state into the error it returns,
	// so wrapping it again would only nest the same fact twice.
	report(PhaseInstalling)
	instCtx, cancelInst := context.WithTimeout(ctx, o.PhaseTimeout[PhaseInstalling])
	hostKey, err2 := WaitForOurSystem(instCtx, d.SSH, net.JoinHostPort(o.NodeAddress, "22"), o.SSHUser, o.SSHKey, s.UID, o.Poll)
	cancelInst()
	if err2 != nil {
		return fail(PhaseInstalling, err2)
	}
	res.HostKey = hostKey
	report(PhaseInstalled)

	// Joining. Its own deadline too, same as every other phase -- a k3s
	// install that hangs over SSH must not be able to run past its budget
	// just because it happens to run last.
	report(PhaseJoining)
	joinCtx, cancelJoin := context.WithTimeout(ctx, o.PhaseTimeout[PhaseJoining])
	defer cancelJoin()
	// res.HostKey was captured a moment ago by WaitForOurSystem. Passing it
	// here is what makes the pin load-bearing rather than decorative:
	// without it, this connection trusts whatever key is presented, and the
	// value captured above is never read by anything.
	sess, dialErr := d.SSH.Dial(joinCtx, net.JoinHostPort(o.NodeAddress, "22"), o.SSHUser, o.SSHKey, res.HostKey)
	if dialErr != nil {
		return fail(PhaseJoining, phaseErr(joinCtx, dialErr))
	}
	kubeconfig, joinErr := Join(joinCtx, sess, s.Cluster, o.NodeAddress)
	_ = sess.Close()
	if joinErr != nil {
		return fail(PhaseJoining, phaseErr(joinCtx, joinErr))
	}
	res.Kubeconfig = kubeconfig

	// Ready, checked against the target cluster -- which for a new cluster
	// is not the one Frame is running in. For ClusterJoin, kubeconfig is
	// nil (Join produces none for an agent joining an existing cluster);
	// NodeChecker's contract is that nil means "check Frame's own cluster".
	readyCtx, cancelReady := context.WithTimeout(ctx, o.PhaseTimeout[PhaseReady])
	defer cancelReady()
	res.NodeName = s.Hostname
	var lastReadyErr error
	for {
		ok, nerr := d.Nodes.NodeReady(readyCtx, kubeconfig, s.Hostname)
		if nerr == nil && ok {
			report(PhaseReady)
			return res, nil
		}
		if nerr != nil {
			lastReadyErr = nerr
		}
		select {
		case <-readyCtx.Done():
			if lastReadyErr != nil {
				return fail(PhaseReady, phaseErr(readyCtx, fmt.Errorf("node %s never became Ready: %w", s.Hostname, lastReadyErr)))
			}
			return fail(PhaseReady, phaseErr(readyCtx, fmt.Errorf("node %s never became Ready", s.Hostname)))
		case <-time.After(o.Poll):
		}
	}
}
