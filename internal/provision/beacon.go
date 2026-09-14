package provision

import (
	"fmt"
	"regexp"
	"strings"
)

// The four checkpoints an installation reports, and the only strings
// provisiond will ever store. The set is closed on purpose: the write route
// is unauthenticated and reachable from the management network, so a
// checkpoint that is not one of these must never reach memory.
//
// They are ordered by when they happen, and the order is what makes a
// failure legible: `netcfg` alone means the size guard in
// preseed/early_command powered the machine off (preseed.go's
// SizeAssertion), because `early` is emitted immediately after that
// assertion and would otherwise be here too.
const (
	CheckpointNetcfg  = "netcfg"  // the preseed/run script is running
	CheckpointEarly   = "early"   // the disk-size assertion passed
	CheckpointPartman = "partman" // about to partition
	CheckpointLate    = "late"    // base system installed, about to reboot
)

// beaconToken is the shape of a token this package produces -- the same 32
// hex characters imageName matches, without the extension. Matched before a
// caller-supplied token is used as a map key or a path element.
var beaconToken = regexp.MustCompile(`^[a-f0-9]{32}$`)

// ValidCheckpoint reports whether s is one of the four. Written as an
// explicit switch rather than a map so that adding a fifth checkpoint is a
// change a reviewer sees in the same diff as the code that emits it.
func ValidCheckpoint(s string) bool {
	switch s {
	case CheckpointNetcfg, CheckpointEarly, CheckpointPartman, CheckpointLate:
		return true
	}
	return false
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
func BeaconSend(base, token, checkpoint string) string {
	if strings.TrimSpace(base) == "" {
		return ""
	}
	return "wget -q -T 5 -O /dev/null " + BeaconURL(base, token, checkpoint) + " || true"
}

// BeaconHeartbeat is the liveness signal: a backgrounded loop that reports
// the checkpoint it was started at, every 15 seconds, for as long as the
// installer environment lives. It cannot outlive the kernel hosting it,
// which is the whole point -- it can only ever under-report liveness.
//
// 15 seconds against the 60-second "lost" threshold in the controller: four
// sends may be missed before anything is said.
func BeaconHeartbeat(base, token, checkpoint string) string {
	if strings.TrimSpace(base) == "" {
		return ""
	}
	return fmt.Sprintf("(while true; do %s; sleep 15; done) &", BeaconSend(base, token, checkpoint))
}
