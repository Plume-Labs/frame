package provision

import (
	"strings"
	"testing"
)

const testBeaconBase = "http://192.168.2.10:30581"
const testBeaconToken = "0123456789abcdef0123456789abcdef"

func TestValidCheckpointAcceptsExactlyTheFourNames(t *testing.T) {
	for _, ok := range []string{CheckpointNetcfg, CheckpointEarly, CheckpointPartman, CheckpointLate} {
		if !ValidCheckpoint(ok) {
			t.Errorf("ValidCheckpoint(%q) = false; it is one of the four", ok)
		}
	}
	// Anything else reaches provisiond's memory if this is wrong, so the
	// refusals are asserted by name rather than by a single negative case.
	for _, bad := range []string{"", "Early", "early ", "netcfg/../..", "late;rm", strings.Repeat("a", 64)} {
		if ValidCheckpoint(bad) {
			t.Errorf("ValidCheckpoint(%q) = true; the set is closed", bad)
		}
	}
}

func TestBeaconURLPutsTokenAndCheckpointInThePath(t *testing.T) {
	got := BeaconURL(testBeaconBase+"/", testBeaconToken, CheckpointEarly)
	want := testBeaconBase + "/beacon/" + testBeaconToken + "/" + CheckpointEarly
	if got != want {
		t.Errorf("BeaconURL = %q, want %q (a trailing slash on the base must not double)", got, want)
	}
}

// A beacon that cannot be sent must not be able to stop an installation:
// the machine is mid-install and a failed wget is not a reason to abort.
func TestBeaconSendCannotFailTheInstall(t *testing.T) {
	got := BeaconSend(testBeaconBase, testBeaconToken, CheckpointEarly)
	if !strings.HasSuffix(strings.TrimSpace(got), "|| true") {
		t.Errorf("BeaconSend = %q; it must end in `|| true`", got)
	}
	if !strings.Contains(got, "wget") {
		t.Errorf("BeaconSend = %q; d-i has busybox wget, not curl", got)
	}
}

func TestBeaconHeartbeatRunsInTheBackgroundAndKeepsGoing(t *testing.T) {
	got := BeaconHeartbeat(testBeaconBase, testBeaconToken, CheckpointEarly)
	for _, want := range []string{"while", "sleep 15", "&"} {
		if !strings.Contains(got, want) {
			t.Errorf("BeaconHeartbeat = %q; it does not contain %q", got, want)
		}
	}
}

// The beacon base is interpolated into a preseed directive and into a
// single-quoted shell word, exactly like the run URL. It gets the same
// guard, and this asserts the refusal rather than the rendering.
func TestValidateBeaconBaseRefusesWhatWouldBreakOutOfThePreseed(t *testing.T) {
	for _, bad := range []string{
		"http://x\nd-i foo/bar string baz",
		"http://x'; poweroff -f; '",
		`http://x\`,
	} {
		if err := ValidateBeaconBase(bad); err == nil {
			t.Errorf("ValidateBeaconBase(%q) = nil; it must refuse", bad)
		}
	}
	if err := ValidateBeaconBase(testBeaconBase); err != nil {
		t.Errorf("ValidateBeaconBase(%q) = %v; it is a well-formed base", testBeaconBase, err)
	}
}

// An empty base is how the cold-start path says "no beacons". It is not an
// error, and it is not a URL either -- both facts have to hold.
func TestValidateBeaconBaseAcceptsEmptyAsMeaningNoBeacons(t *testing.T) {
	if err := ValidateBeaconBase(""); err != nil {
		t.Errorf("ValidateBeaconBase(\"\") = %v; empty means the caller wants no beacons", err)
	}
	if got := BeaconSend("", testBeaconToken, CheckpointEarly); got != "" {
		t.Errorf("BeaconSend with no base = %q, want \"\"", got)
	}
	if got := BeaconHeartbeat("", testBeaconToken, CheckpointEarly); got != "" {
		t.Errorf("BeaconHeartbeat with no base = %q, want \"\"", got)
	}
}

// Shell metacharacters in a validated base must be contained within the
// single-quoted word, not allowed to escape and create a second command.
// A base containing ; or $(...) or backticks should produce them inside
// quotes, so they cannot splice additional commands into preseed/early_command.
func TestBeaconSendContainsShellMetacharactersInQuotes(t *testing.T) {
	// This base would normally splice a second command: the semicolon closes
	// the wget and runs 'touch'. But it passes ValidateBeaconBase (which
	// forbids only \n, \r, ', and \), so the quoting must contain it.
	baseWithSemicolon := "http://192.168.2.10:30581; touch /tmp/pwned"
	if err := ValidateBeaconBase(baseWithSemicolon); err != nil {
		t.Fatalf("ValidateBeaconBase should accept %q; the quote guard must handle it", baseWithSemicolon)
	}
	cmd := BeaconSend(baseWithSemicolon, testBeaconToken, CheckpointEarly)
	// The semicolon must appear inside the single-quoted URL, between
	// the opening quote after /dev/null and the closing quote before || true.
	if !strings.Contains(cmd, "'http://192.168.2.10:30581; touch /tmp/pwned/beacon/") {
		t.Errorf("BeaconSend(%q, ...) = %q; the semicolon must be inside the quoted URL", baseWithSemicolon, cmd)
	}
	// The closing quote must come after the checkpoint, before the || true.
	quoteBeforeTrueIdx := strings.Index(cmd, "' || true")
	if quoteBeforeTrueIdx == -1 {
		t.Errorf("BeaconSend(%q, ...) = %q; no closing quote found before || true", baseWithSemicolon, cmd)
	}
	semiIdx := strings.Index(cmd, ";")
	if semiIdx > quoteBeforeTrueIdx {
		t.Errorf("BeaconSend(%q, ...) = %q; the semicolon appears after the closing quote (would escape)", baseWithSemicolon, cmd)
	}
}
