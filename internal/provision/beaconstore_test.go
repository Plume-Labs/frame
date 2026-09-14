package provision

import (
	"sync"
	"testing"
	"time"
)

func TestBeaconStoreRecordsTheLatestCheckpointAndCountsSends(t *testing.T) {
	s := NewBeaconStore(8)
	t0 := time.Unix(1_700_000_000, 0)

	if ok := s.Record(testBeaconToken, CheckpointNetcfg, t0); !ok {
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
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); s.Record(testBeaconToken, CheckpointEarly, time.Now()) }()
		go func() { defer wg.Done(); _, _ = s.Get(testBeaconToken) }()
	}
	wg.Wait()
}
