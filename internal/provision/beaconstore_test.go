package provision

import (
	"sync"
	"testing"
	"time"
)

func TestBeaconStoreRecordsTheLatestCheckpointAndCountsSends(t *testing.T) {
	s := NewBeaconStore(8)
	t0 := time.Unix(1_700_000_000, 0)

	if ok := s.Record(testBeaconToken, CheckpointEarly, t0); !ok {
		t.Fatal("Record of a good beacon returned false")
	}
	s.Record(testBeaconToken, CheckpointEarly, t0.Add(30*time.Second))
	s.Record(testBeaconToken, CheckpointEarly, t0.Add(45*time.Second))

	got, ok := s.Get(testBeaconToken)
	if !ok {
		t.Fatal("Get after Record found nothing")
	}
	if got.LastCheckpoint != CheckpointEarly {
		t.Errorf("LastCheckpoint = %q, want %q", got.LastCheckpoint, CheckpointEarly)
	}
	if !got.LastSeen.Equal(t0.Add(45 * time.Second)) {
		t.Errorf("LastSeen = %v, want the most recent send", got.LastSeen)
	}
	if got.Count != 3 {
		t.Errorf("Count = %d, want 3", got.Count)
	}
}

// The write route is unauthenticated and on the LAN. Nothing a caller chose
// may reach the map: not the checkpoint, not the token.
func TestBeaconStoreRefusesWhatItWasNotToldToStore(t *testing.T) {
	s := NewBeaconStore(8)
	now := time.Unix(1_700_000_000, 0)

	if s.Record(testBeaconToken, "definitely-not-a-checkpoint", now) {
		t.Error("Record accepted an unknown checkpoint")
	}
	for _, badToken := range []string{"", "short", "../../etc/passwd", "0123456789ABCDEF0123456789abcdef"} {
		if s.Record(badToken, CheckpointEarly, now) {
			t.Errorf("Record accepted token %q", badToken)
		}
		if _, ok := s.Get(badToken); ok {
			t.Errorf("Get(%q) found something", badToken)
		}
	}
	if _, ok := s.Get(testBeaconToken); ok {
		t.Error("a refused Record still created an entry")
	}
}

func TestBeaconStoreForgetsAnImageThatIsGone(t *testing.T) {
	s := NewBeaconStore(8)
	s.Record(testBeaconToken, CheckpointEarly, time.Unix(1_700_000_000, 0))
	s.Forget(testBeaconToken)
	if _, ok := s.Get(testBeaconToken); ok {
		t.Error("Get found an entry after Forget")
	}
}

// TestBeaconStoreKeepsTheFurthestCheckpointAcrossAHeartbeat is the assertion
// the feature's value rests on. BeaconHeartbeat resends the checkpoint it
// was started at -- CheckpointEarly, per RenderAutoinstall -- every 15
// seconds for the life of the installer environment. If a heartbeat sent
// under CheckpointEarly regressed LastCheckpoint after the install had
// already reported CheckpointLate or CheckpointFailed, every install's progress
// signal would collapse to "early" within 15 seconds of leaving it,
// regardless of where the machine actually stopped.
func TestBeaconStoreKeepsTheFurthestCheckpointAcrossAHeartbeat(t *testing.T) {
	s := NewBeaconStore(8)
	t0 := time.Unix(1_700_000_000, 0)

	s.Record(testBeaconToken, CheckpointEarly, t0.Add(1*time.Second))
	s.Record(testBeaconToken, CheckpointLate, t0.Add(2*time.Second))

	// The heartbeat loop started back at CheckpointEarly is still firing --
	// this is exactly what it sends.
	heartbeatAt := t0.Add(17 * time.Second)
	s.Record(testBeaconToken, CheckpointEarly, heartbeatAt)

	got, ok := s.Get(testBeaconToken)
	if !ok {
		t.Fatal("Get after Record found nothing")
	}
	if got.LastCheckpoint != CheckpointLate {
		t.Errorf("LastCheckpoint = %q, want %q -- a heartbeat must not regress the furthest checkpoint reached", got.LastCheckpoint, CheckpointLate)
	}
	if !got.LastSeen.Equal(heartbeatAt) {
		t.Errorf("LastSeen = %v, want %v -- liveness must still move on a heartbeat that does not advance the checkpoint", got.LastSeen, heartbeatAt)
	}
	if got.Count != 3 {
		t.Errorf("Count = %d, want 3 -- every accepted beacon counts, whether or not it advances the checkpoint", got.Count)
	}
}

// Unbounded memory behind an unauthenticated route is a way to kill
// provisiond from the management network.
func TestBeaconStoreEvictsTheOldestWhenFull(t *testing.T) {
	s := NewBeaconStore(2)
	t0 := time.Unix(1_700_000_000, 0)
	a := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	b := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	c := "cccccccccccccccccccccccccccccccc"

	s.Record(a, CheckpointEarly, t0)
	s.Record(b, CheckpointEarly, t0.Add(time.Second))
	s.Record(c, CheckpointEarly, t0.Add(2*time.Second))

	if _, ok := s.Get(a); ok {
		t.Error("the oldest entry survived a full store")
	}
	for _, keep := range []string{b, c} {
		if _, ok := s.Get(keep); !ok {
			t.Errorf("entry %q was evicted although it is not the oldest", keep)
		}
	}
}

// Beacons arrive on one listener's goroutines and are read from another's.
// Run with -race.
func TestBeaconStoreIsSafeUnderConcurrentUse(t *testing.T) {
	s := NewBeaconStore(64)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(2)
		go func() { defer wg.Done(); s.Record(testBeaconToken, CheckpointEarly, time.Now()) }()
		go func() { defer wg.Done(); _, _ = s.Get(testBeaconToken) }()
	}
	wg.Wait()
}

// failed is terminal: error-commands send it once, while the heartbeat keeps
// sending early. If a later heartbeat hid it, a failed install would read as
// one still running until the heartbeat stopped, and then as HeartbeatLost.
func TestBeaconStoreKeepsFailedAcrossAHeartbeat(t *testing.T) {
	s := NewBeaconStore(8)
	t0 := time.Unix(1_700_000_000, 0)
	s.Record(testBeaconToken, CheckpointEarly, t0)
	s.Record(testBeaconToken, CheckpointFailed, t0.Add(time.Second))
	s.Record(testBeaconToken, CheckpointEarly, t0.Add(15*time.Second))

	got, _ := s.Get(testBeaconToken)
	if got.LastCheckpoint != CheckpointFailed {
		t.Errorf("LastCheckpoint = %q, want %q", got.LastCheckpoint, CheckpointFailed)
	}
}

// LastSeen moves with every heartbeat, including the ones after a failure, so
// it cannot say when the failure happened. FailedAt is set by the first
// `failed` and never moved: not by a heartbeat, not by a second `failed`.
func TestBeaconStoreRecordsWhenTheInstallFirstFailed(t *testing.T) {
	s := NewBeaconStore(8)
	t0 := time.Unix(1_700_000_000, 0)
	s.Record(testBeaconToken, CheckpointEarly, t0)
	if got, _ := s.Get(testBeaconToken); !got.FailedAt.IsZero() {
		t.Fatalf("FailedAt = %v before any failure", got.FailedAt)
	}
	failedAt := t0.Add(time.Minute)
	s.Record(testBeaconToken, CheckpointFailed, failedAt)
	s.Record(testBeaconToken, CheckpointEarly, t0.Add(2*time.Minute))
	s.Record(testBeaconToken, CheckpointFailed, t0.Add(3*time.Minute))
	s.Record(testBeaconToken, CheckpointEarly, t0.Add(4*time.Minute))

	got, _ := s.Get(testBeaconToken)
	if !got.FailedAt.Equal(failedAt) {
		t.Errorf("FailedAt = %v, want the first failure at %v", got.FailedAt, failedAt)
	}
	if !got.LastSeen.Equal(t0.Add(4 * time.Minute)) {
		t.Errorf("LastSeen = %v; it still tracks liveness", got.LastSeen)
	}
}
