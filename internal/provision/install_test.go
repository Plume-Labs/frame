package provision

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeBMC struct {
	serial      string
	calls       []string
	inserted    bool
	insertedLie bool // report not-inserted even after a successful insert
	failInsert  error
}

func (b *fakeBMC) Serial(context.Context) (string, error) { return b.serial, nil }
func (b *fakeBMC) InsertMedia(_ context.Context, url string) error {
	b.calls = append(b.calls, "insert:"+url)
	if b.failInsert != nil {
		return b.failInsert
	}
	b.inserted = true
	return nil
}
func (b *fakeBMC) MediaInserted(context.Context) (bool, error) {
	if b.insertedLie {
		return false, nil
	}
	return b.inserted, nil
}
func (b *fakeBMC) EjectMedia(context.Context) error {
	b.calls = append(b.calls, "eject")
	b.inserted = false
	return nil
}
func (b *fakeBMC) SetBootOnce(_ context.Context, target, mode string) error {
	b.calls = append(b.calls, "boot:"+target+":"+mode)
	return nil
}
func (b *fakeBMC) ClearBootOverride(context.Context) error {
	b.calls = append(b.calls, "clearboot")
	return nil
}
func (b *fakeBMC) Reset(_ context.Context, t string) error {
	b.calls = append(b.calls, "reset:"+t)
	return nil
}

type fakeImages struct {
	built, removed int
	failBuild      error
}

func (i *fakeImages) Build(context.Context, Spec) (string, string, error) {
	if i.failBuild != nil {
		return "", "", i.failBuild
	}
	i.built++
	return "http://provisiond/iso/tok.iso", "tok", nil
}
func (i *fakeImages) Remove(context.Context, string) error { i.removed++; return nil }

type fakeNodes struct{ ready bool }

func (n *fakeNodes) NodeReady(context.Context, []byte, string) (bool, error) { return n.ready, nil }

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
			PhasePreparing: time.Second, PhaseMediaAttached: time.Second,
			PhaseInstalling: time.Second, PhaseJoining: time.Second, PhaseReady: time.Second,
		},
	}
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
		if c == "reset:ForceRestart" || c == "reset:On" {
			t.Error("the machine was booted with no media attached")
		}
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
// on a black screen and says nothing.
func TestInstallSetsTheBootModeExplicitly(t *testing.T) {
	d, b, _ := happyDeps()
	if _, err := Install(context.Background(), d, goodSpec(), opts()); err != nil {
		t.Fatal(err)
	}
	if !contains(b.calls, "boot:Cd:UEFI") {
		t.Errorf("boot mode was not set explicitly: %v", b.calls)
	}
}

// Replaying a destructive operation without a human asking is a fault.
func TestInstallNeverRetries(t *testing.T) {
	d, _, i := happyDeps()
	i.failBuild = errors.New("builder is down")
	if _, err := Install(context.Background(), d, goodSpec(), opts()); err == nil {
		t.Fatal("want an error, got nil")
	}
	if i.built != 0 {
		t.Errorf("builds = %d; a failed build must not be retried", i.built)
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
