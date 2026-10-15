package provision

import (
	"fmt"
	"regexp"
	"strings"
)

// The three checkpoints an installation reports, and the only strings
// provisiond will ever store. The set is closed on purpose: the write route
// is unauthenticated and reachable from the management network, so a
// checkpoint that is not one of these must never reach memory.
//
// Each is emitted from exactly one autoinstall hook. `early` is sent from
// early-commands immediately AFTER the disk-size assertion, so a machine that
// was refused there never reports it -- the refusal does not read as
// progress. It does not stay silent either: a failing early-command is an
// install failure to subiquity, which then runs error-commands, so a size or
// disk-resolution refusal reports `failed` (InstallerFailed, not NeverSeen) as
// long as the live system has a network. `late` is the last late-command,
// just before subiquity reboots into the installed system. `failed` comes
// from error-commands, which subiquity runs only when the install has failed.
const (
	CheckpointEarly  = "early"  // the disk-size assertion passed; the installer is running
	CheckpointLate   = "late"   // target written, about to reboot
	CheckpointFailed = "failed" // subiquity reported the install failed
)

// beaconToken is the shape of a token this package produces -- the same 32
// hex characters imageName matches, without the extension. Matched before a
// caller-supplied token is used as a map key or a path element.
var beaconToken = regexp.MustCompile(`^[a-f0-9]{32}$`)

// ValidCheckpoint reports whether s is one of the three. Written as an
// explicit switch rather than a map so that adding a fourth checkpoint is a
// change a reviewer sees in the same diff as the code that emits it.
func ValidCheckpoint(s string) bool {
	switch s {
	case CheckpointEarly, CheckpointLate, CheckpointFailed:
		return true
	}
	return false
}

// checkpointRank orders the checkpoints so BeaconStore.Record keeps the
// furthest-progressed one rather than the most recent: BeaconHeartbeat keeps
// resending `early` for the life of the installer, so without this a heartbeat
// would overwrite `late` or `failed` within 15 seconds. `failed` ranks last:
// it is terminal, and a heartbeat arriving after it must not hide it.
var checkpointRank = map[string]int{
	CheckpointEarly:  0,
	CheckpointLate:   1,
	CheckpointFailed: 2,
}

// ValidateBeaconBase refuses a base URL that would break out of the shell word
// it is interpolated into, using the same guard every other interpolated
// value gets.
//
// An empty base is accepted and means "emit no beacons at all". That is how
// LocalImageStore (`frame bootstrap`) opts out: there is no controller in
// the cold-start path to read a beacon, and that path gets no new moving
// parts.
func ValidateBeaconBase(base string) error {
	if strings.TrimSpace(base) == "" {
		return nil
	}
	return checkShellValue("beacon base URL", base)
}

// BeaconURL is the one place the beacon path is built, so the URL the
// machine calls and the route MediaHandler serves cannot drift apart.
func BeaconURL(base, token, checkpoint string) string {
	return strings.TrimSuffix(base, "/") + "/beacon/" + token + "/" + checkpoint
}

// BeaconSend is one checkpoint report, as a shell command for the subiquity
// live environment, where wget is present (read off the 24.04.5 ISO).
//
// It ends in `|| true` because the alternative is an installation aborted by
// its own diagnostics: a failing early- or late-command aborts the install,
// and a beacon is never worth that.
//
// The URL is wrapped in single quotes to prevent shell metacharacters in the
// base from escaping the command. checkShellValue already forbids ' and \,
// so nothing that passes ValidateBeaconBase can close the quote.
func BeaconSend(base, token, checkpoint string) string {
	if strings.TrimSpace(base) == "" {
		return ""
	}
	return "wget -q -T 5 -O /dev/null '" + BeaconURL(base, token, checkpoint) + "' || true"
}

// BeaconHeartbeat is the liveness signal: a backgrounded loop that reports
// the checkpoint it was started at, every 15 seconds, for as long as the
// installer environment lives. It cannot outlive the kernel hosting it,
// which is the whole point -- it can only ever under-report liveness.
//
// 15 seconds against the 60-second "lost" threshold in the controller: four
// sends may be missed before anything is said.
//
// The loop's stdin, stdout and stderr are all redirected, not left to
// inherit the early-command's own. The loop outlives the shell that started
// it, and subiquity reads each command's output: a reader waiting for EOF on
// pipes this loop still holds would hang the install at early-commands. Not
// proven either way on hardware, and the redirection costs nothing -- which
// is why this line does not get simplified back to `&` later.
func BeaconHeartbeat(base, token, checkpoint string) string {
	if strings.TrimSpace(base) == "" {
		return ""
	}
	return fmt.Sprintf("(while true; do %s; sleep 15; done) </dev/null >/dev/null 2>&1 &", BeaconSend(base, token, checkpoint))
}
