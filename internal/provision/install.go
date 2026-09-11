package provision

import (
	"context"
	"fmt"
	"net"
	"time"
)

// Install runs one installation from end to end: confirm the machine,
// build its image, attach it, wait for the installed system to prove it is
// ours, join it to a cluster, and wait for the cluster to agree.
//
// It never retries. A failure leaves a machine in a state a human should
// look at, and replaying a destructive operation on their behalf is a
// fault; a retry is a new installation, created deliberately.
func Install(ctx context.Context, d Deps, s Spec, o Options) (Result, error) {
	res := Result{Phase: PhasePending}
	report := func(p Phase) {
		res.Phase = p
		if d.Report != nil {
			d.Report(p)
		}
	}
	fail := func(p Phase, err error) (Result, error) {
		res.FailedPhase, res.Phase = p, PhaseFailed
		return res, err
	}

	// Pending: the guard, before anything is built or attached. The serial
	// is retyped by hand; this is what catches "I pointed at the wrong
	// machine", and it must refuse before a single byte is written anywhere.
	serial, err := d.BMC.Serial(ctx)
	if err != nil {
		return fail(PhasePending, fmt.Errorf("reading the machine's serial: %w", err))
	}
	if serial != o.ConfirmSerial {
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
	url, token, err := d.Images.Build(prepCtx, s)
	if err != nil {
		return fail(PhasePreparing, fmt.Errorf("building the installer image: %w", err))
	}

	// From here on the machine has been touched, so every exit cleans up.
	// A failed install that leaves the media attached and the boot override
	// set reboots into the installer forever, which is worse than the
	// failure that preceded it.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		_ = d.BMC.EjectMedia(cleanup)
		_ = d.BMC.ClearBootOverride(cleanup)
		_ = d.Images.Remove(cleanup, token)
	}()

	// MediaAttached: insert, then re-read. InsertVirtualMedia returning 200
	// is not the same as media being attached, and booting with none wastes
	// twenty minutes to reach a timeout that explains nothing.
	report(PhaseMediaAttached)
	mediaCtx, cancelMedia := context.WithTimeout(ctx, o.PhaseTimeout[PhaseMediaAttached])
	defer cancelMedia()
	if err := d.BMC.InsertMedia(mediaCtx, url); err != nil {
		return fail(PhaseMediaAttached, fmt.Errorf("inserting installer media: %w", err))
	}
	if ok, err := d.BMC.MediaInserted(mediaCtx); err != nil || !ok {
		return fail(PhaseMediaAttached, fmt.Errorf("the BMC accepted the insert but reports no media attached"))
	}
	if err := d.BMC.SetBootOnce(mediaCtx, "Cd", o.BootMode); err != nil {
		return fail(PhaseMediaAttached, fmt.Errorf("setting the one-time boot override: %w", err))
	}
	if err := d.BMC.Reset(mediaCtx, "ForceRestart"); err != nil {
		return fail(PhaseMediaAttached, fmt.Errorf("power-cycling the machine: %w", err))
	}

	// Installing: long and nearly blind. Between boot and the first SSH
	// answer, Redfish offers only PowerState and Oem.Hp.PostState, and lot 1
	// established this BMC replays cached sensor readings as live ones in
	// exactly this window, so nothing there is trusted. The phase ends on
	// the machine proving it is ours, not on anything the BMC reports.
	report(PhaseInstalling)
	instCtx, cancelInst := context.WithTimeout(ctx, o.PhaseTimeout[PhaseInstalling])
	hostKey, err := WaitForOurSystem(instCtx, d.SSH, net.JoinHostPort(o.NodeAddress, "22"), o.SSHUser, o.SSHKey, s.UID, o.Poll)
	cancelInst()
	if err != nil {
		return fail(PhaseInstalling, err)
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
	sess, err := d.SSH.Dial(joinCtx, net.JoinHostPort(o.NodeAddress, "22"), o.SSHUser, o.SSHKey, res.HostKey)
	if err != nil {
		return fail(PhaseJoining, err)
	}
	kubeconfig, err := Join(joinCtx, sess, s.Cluster, o.NodeAddress)
	_ = sess.Close()
	if err != nil {
		return fail(PhaseJoining, err)
	}
	res.Kubeconfig = kubeconfig

	// Ready, checked against the target cluster -- which for a new cluster
	// is not the one Frame is running in.
	readyCtx, cancelReady := context.WithTimeout(ctx, o.PhaseTimeout[PhaseReady])
	defer cancelReady()
	res.NodeName = s.Hostname
	for {
		ok, err := d.Nodes.NodeReady(readyCtx, kubeconfig, s.Hostname)
		if err == nil && ok {
			report(PhaseReady)
			return res, nil
		}
		select {
		case <-readyCtx.Done():
			return fail(PhaseReady, fmt.Errorf("node %s never became Ready", s.Hostname))
		case <-time.After(o.Poll):
		}
	}
}
