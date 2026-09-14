package provision

import (
	"fmt"
	"regexp"
	"strings"
)

// The five checkpoints an installation reports, and the only strings
// provisiond will ever store. The set is closed on purpose: the write route
// is unauthenticated and reachable from the management network, so a
// checkpoint that is not one of these must never reach memory.
//
// They are ordered by when they happen, and the order is what makes a
// failure legible. `netcfg` is emitted at the TOP of the run script,
// before kill-all-dhcp -- so on its own it means only "the preseed/run
// script started", which covers every outcome between there and the disk
// guard, above all a netcfg re-run that never takes the static address
// (preseed.go's own comment marks that UNPROVEN ON HARDWARE). `netcfg-done`
// is what actually discriminates: it is emitted after netcfg returns, so
// its absence -- `netcfg` with nothing past it -- is a machine that never
// got onto the static address, and its presence with nothing past it is a
// machine that reached preseed/early_command and was refused there
// (`early` is emitted immediately after the size assertion and would
// otherwise be here too).
const (
	CheckpointNetcfg     = "netcfg"      // the preseed/run script started
	CheckpointNetcfgDone = "netcfg-done" // netcfg returned; the machine is on its static address
	CheckpointEarly      = "early"       // the disk-size assertion passed
	CheckpointPartman    = "partman"     // about to partition
	CheckpointLate       = "late"        // base system installed, about to reboot
)

// beaconToken is the shape of a token this package produces -- the same 32
// hex characters imageName matches, without the extension. Matched before a
// caller-supplied token is used as a map key or a path element.
var beaconToken = regexp.MustCompile(`^[a-f0-9]{32}$`)

// ValidCheckpoint reports whether s is one of the five. Written as an
// explicit switch rather than a map so that adding a sixth checkpoint is a
// change a reviewer sees in the same diff as the code that emits it.
func ValidCheckpoint(s string) bool {
	switch s {
	case CheckpointNetcfg, CheckpointNetcfgDone, CheckpointEarly, CheckpointPartman, CheckpointLate:
		return true
	}
	return false
}

// checkpointRank gives the five checkpoints above a machine-readable form of
// the same order their own doc comment already states. BeaconStore.Record
// uses it to keep the furthest-progressed checkpoint rather than the most
// recently reported one: BeaconHeartbeat is a background loop that keeps
// resending the checkpoint it was started at (checkpoint, not `late`, is
// fixed for the life of that loop), so a later arrival is not always a
// later checkpoint, and without this a heartbeat sent under `early` -- the
// checkpoint every heartbeat happens to be rendered with -- would overwrite
// `partman` or `late` within 15 seconds of either firing, on every install.
var checkpointRank = map[string]int{
	CheckpointNetcfg:     0,
	CheckpointNetcfgDone: 1,
	CheckpointEarly:      2,
	CheckpointPartman:    3,
	CheckpointLate:       4,
}

// ValidateBeaconBase refuses a base URL that would break out of the two
// contexts it is interpolated into -- a preseed directive line and a shell
// word -- using the same guard every other interpolated value gets.
//
// An empty base is accepted and means "emit no beacons at all". That is how
// LocalImageStore (`frame bootstrap`) opts out: there is no controller in
// the cold-start path to read a beacon, and that path gets no new moving
// parts.
func ValidateBeaconBase(base string) error {
	if strings.TrimSpace(base) == "" {
		return nil
	}
	return checkPreseedValue("beacon base URL", base)
}

// BeaconURL is the one place the beacon path is built, so the URL the
// machine calls and the route MediaHandler serves cannot drift apart.
func BeaconURL(base, token, checkpoint string) string {
	return strings.TrimSuffix(base, "/") + "/beacon/" + token + "/" + checkpoint
}

// BeaconSend is one checkpoint report, as a shell command for d-i's busybox
// environment. `curl` is not there until pkgsel installs it (preseed.go's
// pkgsel/include), so this is wget.
//
// It ends in `|| true` because the alternative is an installation aborted by
// its own diagnostics: these run inside preseed commands, whose exit status
// d-i can act on, and a beacon is never worth a wiped disk.
//
// The URL is wrapped in single quotes to prevent shell metacharacters in the
// base from escaping the command. checkPreseedValue already forbids ' and \,
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
// inherit early_command's own. This loop is backgrounded inside the shell
// preseed/early_command runs, and it keeps running for the rest of the
// install -- long after that shell itself has exited. d-i runs preseed
// hooks under log-output, which reads the hook's output; if its reader
// waits for EOF on the hook's stdout/stderr pipes rather than only for the
// hook process's own exit, this loop holding an open copy of those pipes
// would block that reader for the rest of the install. Which behaviour
// log-output actually has on this d-i version cannot be proven without
// hardware, so the mitigation does not wait to find out: it costs nothing,
// and it is why this line does not get simplified back to `&` later.
func BeaconHeartbeat(base, token, checkpoint string) string {
	if strings.TrimSpace(base) == "" {
		return ""
	}
	return fmt.Sprintf("(while true; do %s; sleep 15; done) </dev/null >/dev/null 2>&1 &", BeaconSend(base, token, checkpoint))
}
