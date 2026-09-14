package provision

import (
	"sync"
	"time"
)

// defaultBeaconCapacity is how many installations provisiond remembers
// beacons for. Installs are driven one machine at a time and their images
// are removed when they end, so this is generous; it exists as a ceiling on
// memory reachable from an unauthenticated route, not as a working set.
const defaultBeaconCapacity = 64

// BeaconState is everything provisiond keeps about one installation. It is
// deliberately three fields: a diagnostic that is not allowed to decide
// anything does not need more, and every field here is one an unauthenticated
// caller can influence.
type BeaconState struct {
	LastCheckpoint string    `json:"lastCheckpoint"`
	LastSeen       time.Time `json:"lastSeen"`
	Count          int       `json:"count"`
}

// BeaconStore is provisiond's record of what installations have reported.
//
// In memory, and therefore lost on restart -- which is why the controller
// reports Unknown rather than NeverSeen when it cannot tell the two apart,
// and why frame-provisiond stays at replicas: 1. Both are stated in the
// design; neither is an accident.
type BeaconStore struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]BeaconState
	// order is insertion order of the keys currently in entries, oldest
	// first. A slice rather than a heap: capacity is small, eviction is
	// rare, and a reader can see at a glance what gets dropped.
	order []string
}

func NewBeaconStore(capacity int) *BeaconStore {
	if capacity <= 0 {
		capacity = defaultBeaconCapacity
	}
	return &BeaconStore{capacity: capacity, entries: map[string]BeaconState{}}
}

// Record stores one beacon and reports whether it was accepted. Both the
// token and the checkpoint are matched against their closed shapes first:
// nothing a caller chose becomes a map key or a stored value otherwise.
func (s *BeaconStore) Record(token, checkpoint string, now time.Time) bool {
	if !beaconToken.MatchString(token) || !ValidCheckpoint(checkpoint) {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	prev, existed := s.entries[token]
	if !existed {
		if len(s.order) >= s.capacity {
			oldest := s.order[0]
			s.order = s.order[1:]
			delete(s.entries, oldest)
		}
		s.order = append(s.order, token)
	}
	s.entries[token] = BeaconState{
		LastCheckpoint: checkpoint,
		LastSeen:       now,
		Count:          prev.Count + 1,
	}
	return true
}

func (s *BeaconStore) Get(token string) (BeaconState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.entries[token]
	return st, ok
}

// Forget drops an installation's beacons. Called when its image is removed,
// so beacon lifetime matches image lifetime rather than drifting past it.
func (s *BeaconStore) Forget(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[token]; !ok {
		return
	}
	delete(s.entries, token)
	for i, k := range s.order {
		if k == token {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}
