package provision

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeBMC struct {
	serial      string
	calls       []string
	inserted    bool
	insertedLie bool // report not-inserted even after a successful insert
	failInsert  error
	failSerial  error
	failEject   error
}

func (b *fakeBMC) Serial(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if b.failSerial != nil {
		return "", b.failSerial
	}
	return b.serial, nil
}
func (b *fakeBMC) InsertMedia(ctx context.Context, url string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.calls = append(b.calls, "insert:"+url)
	if b.failInsert != nil {
		return b.failInsert
	}
	b.inserted = true
	return nil
}
func (b *fakeBMC) MediaInserted(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if b.insertedLie {
		return false, nil
	}
	return b.inserted, nil
}
func (b *fakeBMC) EjectMedia(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.calls = append(b.calls, "eject")
	b.inserted = false
	if b.failEject != nil {
		return b.failEject
	}
	return nil
}
func (b *fakeBMC) SetBootOnce(ctx context.Context, target, mode string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.calls = append(b.calls, "boot:"+target+":"+mode)
	return nil
}
func (b *fakeBMC) ClearBootOverride(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.calls = append(b.calls, "clearboot")
	return nil
}
func (b *fakeBMC) Reset(ctx context.Context, t string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.calls = append(b.calls, "reset:"+t)
	return nil
}

type fakeImages struct {
	built, removed int
	failBuild      error
}

// Build counts every call, not just successful ones: a caller that retries a
// failed build past the point of no return must be visible even though
// nothing here ever returns success.
func (i *fakeImages) Build(ctx context.Context, _ Spec) (string, string, error) {
	i.built++
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if i.failBuild != nil {
		return "", "", i.failBuild
	}
	return "http://provisiond/iso/tok.iso", "tok", nil
}
func (i *fakeImages) Remove(ctx context.Context, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	i.removed++
	return nil
}

type fakeNodes struct {
	ready         bool
	failReady     error
	called        bool
	gotKubeconfig []byte
}

func (n *fakeNodes) NodeReady(ctx context.Context, kubeconfig []byte, _ string) (bool, error) {
	n.called = true
	n.gotKubeconfig = kubeconfig
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if n.failReady != nil {
		return false, n.failReady
	}
	return n.ready, nil
}

func happyDeps() (Deps, *fakeBMC, *fakeImages) {
	b := &fakeBMC{serial: "CZ3xxxxxxx"}
	i := &fakeImages{}
	sess := &recordingSession{files: map[string]string{
		markerPath:                  "b3f1c2d4-0000-4000-8000-000000000001",
		"/etc/rancher/k3s/k3s.yaml": k3sKubeconfig,
	}}
	sess.hostKey = "ssh-ed25519 AAAAhost"
	return Deps{
		BMC:    b,
		Images: i,
		SSH:    &fakeSSH{session: sess},
		Nodes:  &fakeNodes{ready: true},
	}, b, i
}

func opts() Options {
	return Options{
		BootMode: "UEFI", SSHUser: "frame", NodeAddress: "192.168.2.210",
		ConfirmSerial: "CZ3xxxxxxx", Poll: time.Millisecond,
		PhaseTimeout: map[Phase]time.Duration{
			PhasePending: time.Second, PhasePreparing: time.Second, PhaseMediaAttached: time.Second,
			PhaseInstalling: time.Second, PhaseJoining: time.Second, PhaseReady: time.Second,
		},
	}
}

// goodSpec() is ClusterInit; this is its ClusterJoin counterpart, for the
// path goodSpec() cannot exercise.
func joinSpec() Spec {
	s := goodSpec()
	s.Cluster = ClusterTarget{
		Mode:       ClusterJoin,
		ServerURL:  "https://192.168.2.201:6443",
		JoinToken:  "K10abcdef123::server:mypassword",
		K3sVersion: "v1.33.4+k3s1",
	}
	return s
}

// The serial is retyped by hand. This is what catches "I pointed at the wrong
// machine", and it must refuse before anything is built or attached.
func TestInstallRefusesWhenTheConfirmedSerialDoesNotMatch(t *testing.T) {
	d, b, i := happyDeps()
	o := opts()
	o.ConfirmSerial = "CZ3typo"
	res, err := Install(context.Background(), d, goodSpec(), o)
	if err == nil {
		t.Fatal("want a refusal, got nil")
	}
	if res.FailedPhase != PhasePending {
		t.Errorf("failed in %s, want Pending", res.FailedPhase)
	}
	if i.built != 0 {
		t.Error("an image was built for a machine whose serial did not match")
	}
	if len(b.calls) != 0 {
		t.Errorf("the BMC was touched: %v", b.calls)
	}
}

// InsertVirtualMedia returning 200 is not the same as media being attached.
// Booting a machine whose media is not there wastes twenty minutes and ends in
// a timeout that says nothing useful.
func TestInstallRereadsThatTheMediaIsActuallyAttached(t *testing.T) {
	d, b, _ := happyDeps()
	b.insertedLie = true
	res, err := Install(context.Background(), d, goodSpec(), opts())
	if err == nil {
		t.Fatal("want an error when the media did not attach, got nil")
	}
	if res.FailedPhase != PhaseMediaAttached {
		t.Errorf("failed in %s, want MediaAttached", res.FailedPhase)
	}
	for _, c := range b.calls {
		if strings.HasPrefix(c, "reset:") {
			t.Error("the machine was booted with no media attached: " + c)
		}
	}
	// A mid-sequence failure -- not the terminal one -- must still clean up.
	// The only failure test until now failed at the very last phase, which
	// could not tell "cleanup runs on every exit" apart from "cleanup runs
	// when the loop just happens to reach the end".
	if !contains(b.calls, "eject") {
		t.Errorf("media was not ejected after a mid-sequence failure: %v", b.calls)
	}
	if !contains(b.calls, "clearboot") {
		t.Errorf("boot override was not cleared after a mid-sequence failure: %v", b.calls)
	}
}

// A failed install that leaves the boot override and the media in place
// reboots into the installer forever.
func TestInstallEjectsAndClearsTheOverrideOnFailure(t *testing.T) {
	d, b, _ := happyDeps()
	d.Nodes = &fakeNodes{ready: false} // never becomes Ready
	if _, err := Install(context.Background(), d, goodSpec(), opts()); err == nil {
		t.Fatal("want a timeout error, got nil")
	}
	if !contains(b.calls, "eject") {
		t.Errorf("media was not ejected after failure: %v", b.calls)
	}
	if !contains(b.calls, "clearboot") {
		t.Errorf("boot override was not cleared after failure: %v", b.calls)
	}
}

func TestInstallEjectsAndClearsTheOverrideOnSuccess(t *testing.T) {
	d, b, i := happyDeps()
	res, err := Install(context.Background(), d, goodSpec(), opts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Phase != PhaseReady {
		t.Errorf("phase = %s, want Ready", res.Phase)
	}
	if !contains(b.calls, "eject") || !contains(b.calls, "clearboot") {
		t.Errorf("cleanup did not run on success: %v", b.calls)
	}
	if i.removed != 1 {
		t.Errorf("the built image was not removed (removed=%d)", i.removed)
	}
	if res.HostKey == "" {
		t.Error("the host key was not pinned")
	}
	if len(res.Kubeconfig) == 0 {
		t.Error("cluster-init produced no kubeconfig, so nobody can talk to the new cluster")
	}
}

// The boot mode is set explicitly. An image that boots in the other mode stays
// on a black screen and says nothing. Both modes are exercised: a version
// that hardcoded "UEFI" into the call and ignored o.BootMode entirely would
// still pass a UEFI-only test.
func TestInstallSetsTheBootModeExplicitly(t *testing.T) {
	for _, mode := range []string{"UEFI", "Legacy"} {
		t.Run(mode, func(t *testing.T) {
			d, b, _ := happyDeps()
			o := opts()
			o.BootMode = mode
			if _, err := Install(context.Background(), d, goodSpec(), o); err != nil {
				t.Fatal(err)
			}
			if !contains(b.calls, "boot:Cd:"+mode) {
				t.Errorf("boot mode was not set explicitly: %v", b.calls)
			}
		})
	}
}

// Replaying a destructive operation without a human asking is a fault.
//
// i.built counts every call to Build, not just successful ones (see
// fakeImages.Build) -- it must, since a failed build never reaches the
// success branch a naive counter would rely on. Without that, a version of
// Install that retried the build call two or three times before giving up
// would still show 0 failed calls, and this test would stay green while
// missing the one property it exists to defend.
func TestInstallNeverRetries(t *testing.T) {
	d, _, i := happyDeps()
	i.failBuild = errors.New("builder is down")
	if _, err := Install(context.Background(), d, goodSpec(), opts()); err == nil {
		t.Fatal("want an error, got nil")
	}
	if i.built != 1 {
		t.Errorf("Build was called %d times, want exactly 1: a failed build must not be retried", i.built)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func containsPhase(ps []Phase, want Phase) bool {
	for _, p := range ps {
		if p == want {
			return true
		}
	}
	return false
}

// --- fix round 1 ---

// C1: an empty confirmed serial and an empty reported serial compare equal
// to each other, so the guard must refuse before that comparison is ever
// reached -- otherwise the whole destructive sequence runs with no
// confirmation having actually happened. The identical shape is already
// guarded in ssh.go's WaitForOurSystem for an empty install UID, on the less
// dangerous of the two checks (a wrong marker match versus a wrong machine
// wiped).
func TestInstallRefusesAnEmptyConfirmedSerialEvenAgainstAnEmptyReportedSerial(t *testing.T) {
	d, b, i := happyDeps()
	b.serial = ""
	o := opts()
	o.ConfirmSerial = ""
	res, err := Install(context.Background(), d, goodSpec(), o)
	if err == nil {
		t.Fatal("want a refusal when both the reported and confirmed serial are empty, got nil")
	}
	if res.FailedPhase != PhasePending {
		t.Errorf("failed in %s, want Pending", res.FailedPhase)
	}
	if i.built != 0 {
		t.Error("an image was built for a machine with no confirmed serial")
	}
	if len(b.calls) != 0 {
		t.Errorf("the BMC was touched: %v", b.calls)
	}
}

// C2: cleanup failing must itself fail Install, whatever the phases before
// it reported. This is checked on the otherwise-succeeding path so nothing
// else could produce the error: every phase reaches Ready correctly, and
// only EjectMedia -- which nothing else here calls -- returns something for
// the failure to come from. Also proves the image is only removed once
// eject has actually succeeded: removing it regardless would delete the ISO
// out from under a virtual-media URL that, if eject failed, may still be
// attached.
func TestInstallFailsIfCleanupFailsEvenAfterReachingReady(t *testing.T) {
	d, _, i := happyDeps()
	b := d.BMC.(*fakeBMC)
	b.failEject = errors.New("BMC refused to eject")

	res, err := Install(context.Background(), d, goodSpec(), opts())
	if err == nil {
		t.Fatal("want an error when cleanup fails after an otherwise-successful install, got nil")
	}
	if !strings.Contains(err.Error(), "needs attention") {
		t.Errorf("error = %q; it must say the machine needs attention", err)
	}
	if res.Phase != PhaseFailed {
		t.Errorf("phase = %s, want Failed", res.Phase)
	}
	if i.removed != 0 {
		t.Error("the image was removed even though eject failed")
	}
}

// I3 also covers the ClusterJoin-mode wiring: M2's fakes give NodeReady its
// own failure mode, used below to prove Install does not trust ready=true
// alone.
//
// M2: ready=true is not enough on its own if NodeReady also returned an
// error -- dropping the "err == nil" half of the loop's success condition
// would report Ready on a call that actually failed. The existing
// ready=false test already covers !ok; this is the discriminating case
// (ok=true, err!=nil) that only a guard checking both can still refuse.
func TestInstallDoesNotTrustReadyWhenNodeReadyAlsoErrored(t *testing.T) {
	d, _, _ := happyDeps()
	d.Nodes = &fakeNodes{ready: true, failReady: errors.New("apiserver unreachable")}
	o := opts()
	o.PhaseTimeout[PhaseReady] = 50 * time.Millisecond
	if _, err := Install(context.Background(), d, goodSpec(), o); err == nil {
		t.Fatal("want an error when NodeReady reports ready=true but also returns an error")
	}
}

// M2: give fakeBMC.Serial a failure mode and use it -- previously
// unreachable, since Serial() could never fail in any fake.
func TestInstallFailsIfReadingTheSerialFails(t *testing.T) {
	d, b, i := happyDeps()
	b.failSerial = errors.New("BMC unreachable")
	res, err := Install(context.Background(), d, goodSpec(), opts())
	if err == nil {
		t.Fatal("want an error when reading the serial fails, got nil")
	}
	if res.FailedPhase != PhasePending {
		t.Errorf("failed in %s, want Pending", res.FailedPhase)
	}
	if i.built != 0 {
		t.Error("an image was built when the serial could not even be read")
	}
}

// M2: give fakeBMC.InsertMedia's failInsert a test -- it was declared and
// never set by anything.
func TestInstallFailsIfInsertMediaFails(t *testing.T) {
	d, b, _ := happyDeps()
	b.failInsert = errors.New("BMC rejected the insert")
	res, err := Install(context.Background(), d, goodSpec(), opts())
	if err == nil {
		t.Fatal("want an error when InsertMedia fails, got nil")
	}
	if res.FailedPhase != PhaseMediaAttached {
		t.Errorf("failed in %s, want MediaAttached", res.FailedPhase)
	}
	for _, c := range b.calls {
		if c == "reset:ForceRestart" {
			t.Error("the machine was booted after InsertMedia failed")
		}
	}
}

// I6. Ruling: nil kubeconfig means "check the cluster Frame itself is in",
// documented on NodeChecker. goodSpec() is ClusterInit throughout this file,
// so nothing else exercises the ClusterJoin path Install itself must
// support: Join returns (nil, nil) for it, and this proves that nil reaches
// NodeReady rather than being refused or fabricated into something else.
func TestInstallClusterJoinChecksReadinessAgainstFramesOwnCluster(t *testing.T) {
	d, _, _ := happyDeps()
	nodes := &fakeNodes{ready: true}
	d.Nodes = nodes

	res, err := Install(context.Background(), d, joinSpec(), opts())
	if err != nil {
		t.Fatal(err)
	}
	if res.Phase != PhaseReady {
		t.Errorf("phase = %s, want Ready", res.Phase)
	}
	if !nodes.called {
		t.Fatal("NodeReady was never called")
	}
	if nodes.gotKubeconfig != nil {
		t.Errorf("NodeReady was called with a %d-byte kubeconfig for a ClusterJoin install; want nil, meaning check Frame's own cluster", len(nodes.gotKubeconfig))
	}
	if len(res.Kubeconfig) != 0 {
		t.Errorf("Result.Kubeconfig = %d bytes for a ClusterJoin install; want none, since Join produced none", len(res.Kubeconfig))
	}
}

// I7. Ruling: Task 4's shape validators run in Pending, beside the serial
// check, not for the first time three phases later inside Join. Checked by
// phase, not merely by "an error occurred": Join's own validateK3sVersion
// would still refuse this eventually, at Joining, so an error-only assertion
// would pass for the wrong reason -- what this proves is that it never gets
// that far.
func TestInstallRefusesAMalformedK3sVersionBeforeTouchingTheMachine(t *testing.T) {
	d, b, i := happyDeps()
	s := goodSpec()
	s.Cluster.K3sVersion = "not-a-version"
	res, err := Install(context.Background(), d, s, opts())
	if err == nil {
		t.Fatal("want an error for a malformed k3s version, got nil")
	}
	if res.FailedPhase != PhasePending {
		t.Errorf("failed in %s, want Pending -- refused before anything was touched", res.FailedPhase)
	}
	if i.built != 0 {
		t.Error("an image was built for a spec with a malformed k3s version")
	}
	if len(b.calls) != 0 {
		t.Errorf("the BMC was touched for a spec with a malformed k3s version: %v", b.calls)
	}
}

// I7: Options.BootMode reaches the BMC verbatim; refuse anything that isn't
// exactly "UEFI" or "Legacy" before the machine is touched.
func TestInstallRefusesAMalformedBootMode(t *testing.T) {
	d, b, i := happyDeps()
	o := opts()
	o.BootMode = "uefi"
	res, err := Install(context.Background(), d, goodSpec(), o)
	if err == nil {
		t.Fatal("want an error for a malformed boot mode, got nil")
	}
	if res.FailedPhase != PhasePending {
		t.Errorf("failed in %s, want Pending", res.FailedPhase)
	}
	if i.built != 0 || len(b.calls) != 0 {
		t.Error("the machine was touched despite a malformed boot mode")
	}
}

// M8: a missing PhaseTimeout entry is a zero Duration -- a context already
// expired -- and an operator reading "context deadline exceeded" for that
// has no way to tell "genuinely timed out" from "nobody configured this
// phase's budget". Refused up front instead.
func TestInstallRefusesIfAPhaseHasNoConfiguredTimeout(t *testing.T) {
	d, b, i := happyDeps()
	o := opts()
	delete(o.PhaseTimeout, PhaseJoining)
	res, err := Install(context.Background(), d, goodSpec(), o)
	if err == nil {
		t.Fatal("want an error when a phase has no configured timeout, got nil")
	}
	if res.FailedPhase != PhasePending {
		t.Errorf("failed in %s, want Pending -- refused before anything was touched", res.FailedPhase)
	}
	if i.built != 0 || len(b.calls) != 0 {
		t.Error("the machine was touched despite a missing phase timeout")
	}
}

// M6: Report's doc says "called on entering each phase", but PhasePending
// and PhaseFailed were never among them -- a status-projecting consumer
// watching only Report would never see the terminal failure, or that the
// machine was ever confirmed Pending at all.
func TestInstallReportsPendingAndFailed(t *testing.T) {
	d, b, _ := happyDeps()
	b.insertedLie = true // fails during MediaAttached
	var reported []Phase
	d.Report = func(p Phase) { reported = append(reported, p) }

	if _, err := Install(context.Background(), d, goodSpec(), opts()); err == nil {
		t.Fatal("want an error, got nil")
	}

	if !containsPhase(reported, PhasePending) {
		t.Errorf("Report was never called with PhasePending: %v", reported)
	}
	if !containsPhase(reported, PhaseFailed) {
		t.Errorf("Report was never called with PhaseFailed: %v", reported)
	}
}

// --- coverage beyond the brief: the host key pin is actually wired ---
//
// Install dials twice: once inside WaitForOurSystem (trust-on-first-use,
// expectedHostKey ""), and once for the join connection, which must pass the
// key WaitForOurSystem just captured. Nothing else in this file's fakes can
// catch a caller that stops doing that -- fakeSSH.Dial otherwise ignores
// expectedHostKey and always succeeds regardless of what it's given, unlike
// the real sshClient tested against a live handshake in ssh_test.go. Without
// this test, "Install passes res.HostKey on the join dial" is asserted only
// in a doc comment.
func TestInstallPinsTheHostKeyItCapturedOnTheJoinDial(t *testing.T) {
	d, _, _ := happyDeps()
	ssh := d.SSH.(*fakeSSH)

	res, err := Install(context.Background(), d, goodSpec(), opts())
	if err != nil {
		t.Fatal(err)
	}

	if len(ssh.dialHostKeys) != 2 {
		t.Fatalf("Dial was called %d times, want 2 (WaitForOurSystem, then join): %v", len(ssh.dialHostKeys), ssh.dialHostKeys)
	}
	if ssh.dialHostKeys[0] != "" {
		t.Errorf("first dial (WaitForOurSystem) expectedHostKey = %q, want \"\" (trust on first use)", ssh.dialHostKeys[0])
	}
	if ssh.dialHostKeys[1] != res.HostKey {
		t.Errorf("join dial expectedHostKey = %q, want the captured host key %q", ssh.dialHostKeys[1], res.HostKey)
	}
	if ssh.dialHostKeys[1] == "" {
		t.Error("join dial pinned nothing: expectedHostKey was empty, so any machine's key would be accepted")
	}
}

// --- coverage beyond the brief: the Joining phase's own budget ---
//
// blockingSession's Run never returns on its own -- only its context
// expiring ends it. This is what a fixture "cleaner than reality" would
// never need: recordingSession's Run returns instantly regardless of
// context, so no test built only from it could ever tell "Joining runs on
// its own bounded context" apart from "Joining runs on whatever context
// Install happened to be called with".
type blockingSession struct {
	fakeSession
}

func (s *blockingSession) Run(ctx context.Context, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// Every other phase (Preparing, MediaAttached, Installing, Ready) is wrapped
// in its own context.WithTimeout(ctx, o.PhaseTimeout[phase]) -- see install.go.
// Joining must be too, per the design ("each phase carries its own
// timeout"), not left to run on whatever the caller's ambient context
// happens to be.
//
// This is deliberately not proven by a short outer context: an
// Install(shortCtx, ...) call would time out identically whether or not
// Joining has its own budget, and would prove nothing about this phase
// specifically. Instead the outer context is generous (2s) and
// PhaseTimeout[PhaseJoining] is tiny (10ms); only a Joining phase bounded by
// its own timeout can fail this fast, and it must report the failure as
// PhaseJoining, not as the outer context's doing.
func TestInstallJoiningHonorsItsOwnPhaseTimeout(t *testing.T) {
	d, _, _ := happyDeps()
	installSession := &recordingSession{files: map[string]string{
		markerPath: "b3f1c2d4-0000-4000-8000-000000000001",
	}}
	installSession.hostKey = "ssh-ed25519 AAAAhost"
	d.SSH = &fakeSSH{sessions: []Session{installSession, &blockingSession{}}}

	o := opts()
	o.PhaseTimeout[PhaseJoining] = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	res, err := Install(ctx, d, goodSpec(), o)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("want an error when Joining outlives its own budget, got nil")
	}
	if res.FailedPhase != PhaseJoining {
		t.Errorf("failed in %s, want Joining", res.FailedPhase)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("took %s; Joining's own 10ms budget should have ended this long before the outer 2s context did", elapsed)
	}
}
