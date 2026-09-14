# Installing-phase liveness — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a `FrameInstall` stuck in `Installing` say whether the installer is alive and waiting on a question, dead, or never started — without giving that signal any power to end, advance, or fail a phase.

**Architecture:** The installed-to machine sends checkpoint and heartbeat beacons over HTTP to `frame-provisiond`'s LAN-facing media listener. provisiond keeps them in memory keyed by the image token and serves them back on its in-cluster build listener only. `FrameInstallReconciler` polls that read endpoint on the requeue it already performs every 15 seconds while an install is in flight, and writes one condition. `provision.Install` is given a write-only callback to announce its token and no way at all to read beacon state.

**Tech Stack:** Go 1.23, `net/http` (`http.ServeMux` path wildcards), controller-runtime, `apimachinery/pkg/api/meta` for conditions, busybox `wget` on the d-i side. No new dependency.

**Spec:** `docs/superpowers/specs/2026-09-14-installing-liveness-design.md`

## Global Constraints

- **A beacon may never end, advance, or fail a phase.** Only `WaitForOurSystem` ends `Installing`. Task 9 carries the test that proves it and that test must never be weakened.
- **Writes on the media listener, reads on the build listener.** The media listener is LAN-facing and reachable by any BMC; the build listener is in-cluster. No route crosses.
- **Beacons are keyed by the 32-hex image token**, matched against the existing `^[a-f0-9]{32}$` shape before anything is stored or looked up. Never by hostname, never by `Spec.UID`.
- **Checkpoint names are a closed set of exactly four**: `netcfg`, `early`, `partman`, `late`. Anything else is a 404 and is never stored.
- **A heartbeat is called lost after 60 seconds.** Sends are every 15 seconds; 60 s is four missed sends.
- **A beacon that fails to send must never break an install.** Every shell invocation ends `|| true`.
- **Every value interpolated into the preseed goes through `checkPreseedValue`** (`internal/provision/preseed.go:302`). The beacon base URL is such a value.
- **`frame-provisiond` stays at `replicas: 1`.** In-memory beacon state requires it.
- **The cold-start path (`LocalImageStore`, `frame bootstrap`) renders no beacons.** It passes an empty beacon base; an empty base means no beacon lines at all. There is no controller there to read them, and that path rebuilds node zero from a laptop — it gets no new moving parts in this plan.
- Commit messages in French, code and comments in English, matching the repository. **Never add a `Co-Authored-By` trailer.**
- Do not merge to `main` and do not push. This plan ends on the branch `feat/install-liveness`.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/provision/beacon.go` (new) | The shared vocabulary: checkpoint names, the token shape, the URL, and the shell fragments a preseed emits. Pure functions, no I/O. |
| `internal/provision/beaconstore.go` (new) | provisiond's in-memory record: `Record`, `Get`, `Forget`. Concurrency-safe, capped. |
| `internal/provision/server.go` (modify) | The write route on `MediaHandler`, the read route on `BuildHandler`, `Forget` on image deletion, `HTTPImageStore.Progress`, and the two build call sites that render beacon URLs. |
| `internal/provision/preseed.go` (modify) | `RenderPreseed` and the `preseed/run` script emit the four checkpoints and start the heartbeat. |
| `internal/provision/types.go` (modify) | `Deps.ReportToken`. |
| `internal/provision/install.go` (modify) | One call to `ReportToken` after `Images.Build` succeeds. |
| `internal/controller/frame/frameinstall_controller.go` (modify) | Remember the in-flight token, poll progress, write the `InstallerResponding` condition. |
| `cmd/provisiond/main.go` (modify) | Build the store, hand it to both listeners. |
| `docs/provisioning.md` (modify) | What the condition means and what each row of the table tells an operator. |

---

### Task 1: Beacon vocabulary and shell fragments

**Files:**
- Create: `internal/provision/beacon.go`
- Create: `internal/provision/beacon_test.go`

**Interfaces:**
- Consumes: `checkPreseedValue(field, v string) error` from `preseed.go`.
- Produces:
  - `const CheckpointNetcfg, CheckpointEarly, CheckpointPartman, CheckpointLate string`
  - `func ValidCheckpoint(s string) bool`
  - `func BeaconURL(base, token, checkpoint string) string`
  - `func BeaconSend(base, token, checkpoint string) string` — a single shell command
  - `func BeaconHeartbeat(base, token, checkpoint string) string` — a backgrounded loop
  - `func ValidateBeaconBase(base string) error`
  - `var beaconToken = regexp.MustCompile(...)` (unexported)

- [ ] **Step 1: Write the failing test**

Create `internal/provision/beacon_test.go`:

```go
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
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd /home/rmocq/frame-install-liveness && go test ./internal/provision/ -run 'Beacon|Checkpoint' -v`
Expected: FAIL to compile — `undefined: CheckpointNetcfg`, `undefined: ValidCheckpoint`, and the rest.

- [ ] **Step 3: Write the implementation**

Create `internal/provision/beacon.go`:

```go
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
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test ./internal/provision/ -run 'Beacon|Checkpoint' -v`
Expected: PASS, six tests.

- [ ] **Step 5: Commit**

```bash
cd /home/rmocq/frame-install-liveness
git add internal/provision/beacon.go internal/provision/beacon_test.go
git commit -m "feat(provision): le vocabulaire des balises et leurs fragments shell

Quatre jalons, un ensemble clos. Une base vide veut dire aucune balise --
c'est ainsi que le chemin d'amorcage a froid se retire."
```

---

### Task 2: The in-memory beacon store

**Files:**
- Create: `internal/provision/beaconstore.go`
- Create: `internal/provision/beaconstore_test.go`

**Interfaces:**
- Consumes: `ValidCheckpoint`, `beaconToken` from Task 1.
- Produces:
  - `type BeaconState struct { LastCheckpoint string; LastSeen time.Time; Count int }`
  - `func NewBeaconStore(capacity int) *BeaconStore`
  - `func (s *BeaconStore) Record(token, checkpoint string, now time.Time) bool`
  - `func (s *BeaconStore) Get(token string) (BeaconState, bool)`
  - `func (s *BeaconStore) Forget(token string)`

- [ ] **Step 1: Write the failing test**

Create `internal/provision/beaconstore_test.go`:

```go
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
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/provision/ -run BeaconStore -v`
Expected: FAIL to compile — `undefined: NewBeaconStore`.

- [ ] **Step 3: Write the implementation**

Create `internal/provision/beaconstore.go`:

```go
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
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test -race ./internal/provision/ -run BeaconStore -v`
Expected: PASS, five tests, no race reported.

- [ ] **Step 5: Commit**

```bash
git add internal/provision/beaconstore.go internal/provision/beaconstore_test.go
git commit -m "feat(provision): le registre en memoire des balises

Borne en capacite et clos en forme : ni le token ni le jalon choisis par
un appelant n'atteignent la carte sans avoir ete reconnus."
```

---

### Task 3: The write route, on the media listener only

**Files:**
- Modify: `internal/provision/server.go` (`MediaHandler`, around line 87)
- Modify: `internal/provision/server_test.go` (every `MediaHandler(dir)` call site gains a second argument)
- Modify: `cmd/bootstrap/main.go:154`

**Interfaces:**
- Consumes: `BeaconStore` (Task 2), `ValidCheckpoint`, `beaconToken` (Task 1).
- Produces: `func MediaHandler(dir string, beacons *BeaconStore) http.Handler` — a nil `beacons` means the route is not registered at all.

- [ ] **Step 1: Write the failing test**

Append to `internal/provision/server_test.go`:

```go
func TestMediaHandlerRecordsABeacon(t *testing.T) {
	store := NewBeaconStore(8)
	h := MediaHandler(t.TempDir(), store)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/beacon/"+testBeaconToken+"/"+CheckpointEarly, nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}
	got, ok := store.Get(testBeaconToken)
	if !ok || got.LastCheckpoint != CheckpointEarly {
		t.Errorf("store after a beacon = %+v, ok=%v; want the early checkpoint", got, ok)
	}
}

// The 404 is the point: a checkpoint outside the closed set must not be
// stored, and the store must be asked, not merely the status code.
func TestMediaHandlerRefusesABeaconItDoesNotRecognise(t *testing.T) {
	store := NewBeaconStore(8)
	h := MediaHandler(t.TempDir(), store)

	for _, path := range []string{
		"/beacon/" + testBeaconToken + "/bogus",
		"/beacon/not-a-token/" + CheckpointEarly,
		"/beacon/" + testBeaconToken,
		"/beacon/" + testBeaconToken + "/" + CheckpointEarly + "/extra",
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, rr.Code)
		}
	}
	if _, ok := store.Get(testBeaconToken); ok {
		t.Error("a refused beacon still created an entry")
	}
}

// The LAN-facing listener writes beacons. It must never be able to read
// them back: that would publish one install's progress to anything that can
// guess -- or observe -- its token.
func TestMediaHandlerDoesNotServeBeaconsBack(t *testing.T) {
	store := NewBeaconStore(8)
	store.Record(testBeaconToken, CheckpointEarly, time.Unix(1_700_000_000, 0))
	h := MediaHandler(t.TempDir(), store)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/beacon/"+testBeaconToken, nil))
	if rr.Code == http.StatusOK {
		t.Fatalf("the media listener answered a beacon read with 200:\n%s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), CheckpointEarly) {
		t.Errorf("the media listener leaked beacon state in a %d body:\n%s", rr.Code, rr.Body.String())
	}
}

// Without a store there is no route at all -- not a route that quietly
// accepts and discards.
func TestMediaHandlerWithoutAStoreHasNoBeaconRoute(t *testing.T) {
	rr := httptest.NewRecorder()
	MediaHandler(t.TempDir(), nil).ServeHTTP(rr,
		httptest.NewRequest(http.MethodGet, "/beacon/"+testBeaconToken+"/"+CheckpointEarly, nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/provision/ -run 'MediaHandler.*Beacon' -v`
Expected: FAIL to compile — `too many arguments in call to MediaHandler`.

- [ ] **Step 3: Change the signature and register the route**

In `internal/provision/server.go`, change the `MediaHandler` signature and add the route as the last registration in the function:

```go
func MediaHandler(dir string, beacons *BeaconStore) http.Handler {
	mux := http.NewServeMux()
	// ... the existing /iso/{name} and /preseed/{name} registrations are
	// unchanged ...

	// The write half of the beacon mechanism, and the only half that lives
	// here. Reads are on the build listener (BuildHandler): this listener is
	// reachable by anything on the management network, so it may deposit an
	// installation's progress and may never disclose it.
	//
	// Registered only when a store exists. A route that accepted beacons and
	// dropped them would be worse than no route: the machine's sends would
	// succeed and the absence of any record would read as "the installer
	// never spoke".
	//
	// Two path segments, both wildcards, both matched against a closed shape
	// before anything is stored -- the same rule /iso/{name} follows. The
	// response is 204 with no body: the machine ignores it, and there is
	// nothing to say.
	if beacons != nil {
		mux.HandleFunc("GET /beacon/{token}/{checkpoint}", func(w http.ResponseWriter, r *http.Request) {
			if !beacons.Record(r.PathValue("token"), r.PathValue("checkpoint"), time.Now()) {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	return mux
}
```

Add `"time"` to the file's imports if it is not already there.

- [ ] **Step 4: Fix the call sites so the tree compiles**

In `internal/provision/server_test.go`, every existing `MediaHandler(dir)` becomes `MediaHandler(dir, nil)`. In `cmd/bootstrap/main.go:154`, `provision.MediaHandler(imagesDir)` becomes `provision.MediaHandler(imagesDir, nil)` — the cold-start path renders no beacons (Global Constraints), so it needs no store.

Run: `go build ./... && go vet ./...`
Expected: no output.

- [ ] **Step 5: Run the tests and make sure they pass**

Run: `go test ./internal/provision/ ./cmd/... -v -run 'Media|Beacon'`
Expected: PASS, including the four new tests.

- [ ] **Step 6: Commit**

```bash
git add internal/provision/server.go internal/provision/server_test.go cmd/bootstrap/main.go
git commit -m "feat(provision): l'ecoute media enregistre les balises, sans jamais les rendre

Elle est joignable depuis le reseau de management : elle peut deposer
l'avancement d'une installation, jamais le divulguer."
```

---

### Task 4: The read route, on the build listener only

**Files:**
- Modify: `internal/provision/server.go` (`BuildHandler`, around line 129; the `DELETE /iso/{name}` handler around line 205)
- Modify: `internal/provision/server_test.go` (every `BuildHandler(dir, base, testMediaURL)` call site gains a fourth argument)

**Interfaces:**
- Consumes: `BeaconStore` (Task 2).
- Produces: `func BuildHandler(dir string, base BaseSource, mediaURL string, beacons *BeaconStore) http.Handler`, serving `GET /beacon/{token}` → `BeaconState` as JSON, 404 when unknown.

- [ ] **Step 1: Write the failing test**

Append to `internal/provision/server_test.go`:

```go
func TestBuildHandlerServesBeaconStateBack(t *testing.T) {
	store := NewBeaconStore(8)
	seen := time.Unix(1_700_000_000, 0).UTC()
	store.Record(testBeaconToken, CheckpointPartman, seen)

	rr := httptest.NewRecorder()
	BuildHandler(t.TempDir(), DefaultBase(), testMediaURL, store).
		ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/beacon/"+testBeaconToken, nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got BeaconState
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.LastCheckpoint != CheckpointPartman || !got.LastSeen.Equal(seen) || got.Count != 1 {
		t.Errorf("beacon state = %+v, want partman at %v with count 1", got, seen)
	}
}

// "Nothing has been heard" and "I cannot tell" are different answers, and
// the controller maps them to different reasons. A 404 is what carries the
// difference.
func TestBuildHandlerAnswers404ForAnInstallThatHasNotReported(t *testing.T) {
	rr := httptest.NewRecorder()
	BuildHandler(t.TempDir(), DefaultBase(), testMediaURL, NewBeaconStore(8)).
		ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/beacon/"+testBeaconToken, nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// Beacon lifetime must match image lifetime. Otherwise a token's state
// outlives the install it describes and occupies a slot until eviction.
func TestBuildHandlerForgetsBeaconsWhenTheImageIsRemoved(t *testing.T) {
	dir := t.TempDir()
	store := NewBeaconStore(8)
	store.Record(testBeaconToken, CheckpointLate, time.Unix(1_700_000_000, 0))
	if err := os.WriteFile(filepath.Join(dir, testBeaconToken+".iso"), []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	BuildHandler(dir, DefaultBase(), testMediaURL, store).
		ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/iso/"+testBeaconToken+".iso", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if _, ok := store.Get(testBeaconToken); ok {
		t.Error("beacon state survived the removal of its image")
	}
}

// The in-cluster listener reads. It must never be a second way to write.
func TestBuildHandlerDoesNotAcceptBeacons(t *testing.T) {
	store := NewBeaconStore(8)
	rr := httptest.NewRecorder()
	BuildHandler(t.TempDir(), DefaultBase(), testMediaURL, store).
		ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/beacon/"+testBeaconToken+"/"+CheckpointEarly, nil))
	if _, ok := store.Get(testBeaconToken); ok {
		t.Errorf("the build listener recorded a beacon (status %d)", rr.Code)
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/provision/ -run 'BuildHandler.*Beacon|Beacon.*Image' -v`
Expected: FAIL to compile — `too many arguments in call to BuildHandler`.

- [ ] **Step 3: Change the signature, add the read route, forget on delete**

In `internal/provision/server.go`:

```go
func BuildHandler(dir string, base BaseSource, mediaURL string, beacons *BeaconStore) http.Handler {
```

Add this registration alongside the existing ones:

```go
	// The read half. It lives here and nowhere else: this listener is the
	// in-cluster one, on a Service nothing outside the cluster reaches,
	// which is why it may disclose what the LAN-facing listener collected.
	//
	// A 404 means "nothing has been heard from this installation". The
	// controller maps that to NeverSeen, and maps a failure to reach this
	// endpoint at all to Unavailable -- two different things that must not
	// be collapsed, which is why "no beacons" is a status code rather than
	// a zero-valued 200.
	if beacons != nil {
		mux.HandleFunc("GET /beacon/{token}", func(w http.ResponseWriter, r *http.Request) {
			st, ok := beacons.Get(r.PathValue("token"))
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(st)
		})
	}
```

In the existing `DELETE /iso/{name}` handler, after the `.cfg`/`.sh` cleanup loop and before `w.WriteHeader(http.StatusNoContent)`:

```go
		// Beacon state is dropped with the image it belongs to, so its
		// lifetime cannot outlive the installation it describes.
		if beacons != nil {
			beacons.Forget(token)
		}
```

- [ ] **Step 4: Fix the call sites so the tree compiles**

In `internal/provision/server_test.go`, every `BuildHandler(dir, base, testMediaURL)` becomes `BuildHandler(dir, base, testMediaURL, nil)` except in the new tests above. Add `"encoding/json"`, `"os"`, `"path/filepath"`, `"time"` to the test file's imports if missing.

Run: `go build ./... && go vet ./...`
Expected: no output.

- [ ] **Step 5: Run the tests and make sure they pass**

Run: `go test ./internal/provision/ -v -run 'BuildHandler|Beacon'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/provision/server.go internal/provision/server_test.go
git commit -m "feat(provision): l'ecoute de build rend l'etat des balises, et l'oublie avec l'image

404 veut dire « rien entendu » -- distinct de « je n'ai pas pu demander »,
que le controleur rapporte autrement."
```

---

### Task 5: The preseed and the run script emit beacons

**Files:**
- Modify: `internal/provision/preseed.go` (the template around lines 53-127, `NetcfgRerunScript` at line 43, `RenderPreseed` at line 253)
- Modify: `internal/provision/preseed_test.go` (every `RenderPreseed(x, testRunURL)` call gains a third argument; `TestNetcfgRerunScriptKillsDHCPThenRunsNetcfg` moves to the rendered form)

**Interfaces:**
- Consumes: `BeaconSend`, `BeaconHeartbeat`, `ValidateBeaconBase`, the four checkpoint constants (Task 1).
- Produces:
  - `func RenderPreseed(s Spec, runURL, beaconBase, token string) (string, error)`
  - `func RenderRunScript(beaconBase, token string) string` — replaces the `NetcfgRerunScript` constant

- [ ] **Step 1: Write the failing test**

Append to `internal/provision/preseed_test.go`:

```go
// The four checkpoints have to be in the rendered file, at the right
// commands: a checkpoint emitted from the wrong hook reports a stage the
// installer has not reached.
func TestRenderPreseedEmitsEveryCheckpointAtItsOwnHook(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, testBeaconBase, testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ directive, checkpoint string }{
		{"preseed/early_command", CheckpointEarly},
		{"partman/early_command", CheckpointPartman},
		{"preseed/late_command", CheckpointLate},
	} {
		line := directiveLine(t, got, c.directive)
		if !strings.Contains(line, BeaconURL(testBeaconBase, testBeaconToken, c.checkpoint)) {
			t.Errorf("%s does not report checkpoint %q:\n%s", c.directive, c.checkpoint, line)
		}
	}
}

// This is what makes the size guard legible. `early` must be emitted after
// the assertion, so a machine that refused carries `netcfg` and nothing
// more -- an outcome no other failure produces.
func TestRenderPreseedReportsEarlyOnlyAfterTheDiskAssertion(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, testBeaconBase, testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	line := directiveLine(t, got, "preseed/early_command")
	assertion := strings.Index(line, "poweroff -f")
	beacon := strings.Index(line, BeaconURL(testBeaconBase, testBeaconToken, CheckpointEarly))
	if assertion < 0 || beacon < 0 {
		t.Fatalf("early_command is missing the assertion or the beacon:\n%s", line)
	}
	if beacon < assertion {
		t.Errorf("the early beacon is emitted before the disk assertion, so a refused machine would look like it passed:\n%s", line)
	}
}

func TestRenderPreseedStartsTheHeartbeat(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, testBeaconBase, testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	line := directiveLine(t, got, "preseed/early_command")
	if !strings.Contains(line, "sleep 15") || !strings.Contains(line, "while true") {
		t.Errorf("early_command does not start a heartbeat loop:\n%s", line)
	}
}

// A beacon must never be the reason an installation stops.
func TestRenderPreseedNeverLetsABeaconFailTheInstall(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, testBeaconBase, testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(got, "\n") {
		if !strings.Contains(line, "/beacon/") {
			continue
		}
		if !strings.Contains(line, "|| true") {
			t.Errorf("a beacon line has no `|| true`:\n%s", line)
		}
	}
}

// The cold-start path passes no base. Not "a base that goes nowhere": none.
func TestRenderPreseedWithNoBeaconBaseEmitsNoBeacons(t *testing.T) {
	got, err := RenderPreseed(goodSpec(), testRunURL, "", testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "/beacon/") {
		t.Errorf("a preseed rendered with no beacon base still carries beacon URLs:\n%s", got)
	}
}

func TestRenderPreseedRefusesABeaconBaseThatWouldBreakTheDirective(t *testing.T) {
	for _, bad := range []string{"http://x\nd-i foo/bar string baz", "http://x'; poweroff -f; '"} {
		if _, err := RenderPreseed(goodSpec(), testRunURL, bad, testBeaconToken); err == nil {
			t.Errorf("RenderPreseed accepted beacon base %q", bad)
		}
	}
}

func TestRenderRunScriptReportsNetcfgAndStillRerunsNetcfg(t *testing.T) {
	got := RenderRunScript(testBeaconBase, testBeaconToken)
	for _, want := range []string{"kill-all-dhcp", "\nnetcfg", BeaconURL(testBeaconBase, testBeaconToken, CheckpointNetcfg)} {
		if !strings.Contains(got, want) {
			t.Errorf("the preseed/run script does not contain %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "#!/bin/sh\n") {
		t.Errorf("the preseed/run script has no interpreter line:\n%s", got)
	}
	if strings.Index(got, "kill-all-dhcp") > strings.Index(got, "\nnetcfg") {
		t.Error("the script runs netcfg before killing the DHCP client")
	}
	if bare := RenderRunScript("", testBeaconToken); strings.Contains(bare, "/beacon/") {
		t.Errorf("a run script rendered with no beacon base carries a beacon URL:\n%s", bare)
	}
}

// directiveLine returns the logical preseed line for a directive, joining
// the backslash continuations d-i uses for multi-command hooks. Without the
// join, a test searching for two strings "on the same line" would pass or
// fail on where the template happens to wrap.
func directiveLine(t *testing.T, preseed, directive string) string {
	t.Helper()
	joined := strings.ReplaceAll(preseed, "\\\n", " ")
	for _, line := range strings.Split(joined, "\n") {
		if strings.Contains(line, directive) {
			return line
		}
	}
	t.Fatalf("no %s directive in the rendered preseed:\n%s", directive, preseed)
	return ""
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/provision/ -run 'RenderPreseed.*[Bb]eacon|Checkpoint|RunScript' -v`
Expected: FAIL to compile — `too many arguments in call to RenderPreseed`, `undefined: RenderRunScript`.

- [ ] **Step 3: Change the template and the two render functions**

In `internal/provision/preseed.go`:

Replace the `NetcfgRerunScript` constant with a template and a render function, keeping its whole existing doc comment (it explains why the re-run exists at all and is still true):

```go
const runScriptTemplate = `#!/bin/sh
# Generated by Frame. Do not edit on the machine.
#
# Debian's documented workaround for network-fetched preseeds: force the
# network configuration to run again now that the preconfiguration file has
# been loaded. Without this the static address above is never applied.
{{ if .Beacon }}{{ .Beacon }}
{{ end }}kill-all-dhcp
netcfg
`

var runScriptTmpl = template.Must(template.New("runscript").Parse(runScriptTemplate))

// RenderRunScript produces the preseed/run script, optionally reporting the
// netcfg checkpoint before it does its work.
//
// The beacon goes FIRST, before kill-all-dhcp: this script is about to tear
// the DHCP lease down and bring the interface back on a static address, and
// a report sent after that is a report that may never leave. Reaching this
// script at all is what `netcfg` means as a checkpoint.
func RenderRunScript(beaconBase, token string) string {
	var b strings.Builder
	// The template has no failure mode a caller can trigger: both fields are
	// strings this package produced, and Execute on a parsed template with a
	// struct of strings cannot fail. Errors are therefore not returned --
	// there is no caller decision to make about one.
	_ = runScriptTmpl.Execute(&b, struct{ Beacon string }{
		Beacon: BeaconSend(beaconBase, token, CheckpointNetcfg),
	})
	return b.String()
}
```

Add `"text/template"` to the imports if it is not already there (it is — `preseedTmpl` uses it).

In `preseedTemplate`, change the three hook directives. `early_command` becomes (keeping the existing comment block above it verbatim):

```
d-i preseed/early_command string {{ .SizeAssertion }} || { echo "FRAME: named disk is not the expected size, refusing" > /dev/console; poweroff -f; }{{ if .BeaconEarly }} ; {{ .BeaconEarly }} ; {{ .BeaconHeartbeat }}{{ end }}
```

Add a `partman/early_command` directive immediately after the `{{ .Partman }}` block, emitted only when there is a beacon to send — there is no other reason for this hook to exist:

```
{{ if .BeaconPartman }}d-i partman/early_command string {{ .BeaconPartman }}
{{ end }}
```

In `late_command`, append one more continuation line at the end of the existing chain:

```
  echo '127.0.1.1 {{ .Hostname }}' >> /target/etc/hosts{{ if .BeaconLate }} ; \
  {{ .BeaconLate }}{{ end }}
```

Change `RenderPreseed` to take and validate the beacon base, and to fill the four new template fields:

```go
// RenderPreseed turns a Spec into a preseed.cfg.
//
// ... (existing doc comment on runURL unchanged) ...
//
// beaconBase and token are where the machine reports progress. An empty
// beaconBase renders a preseed with no beacon lines at all -- the cold-start
// path (`frame bootstrap`) passes one, because nothing there reads a beacon.
// Rendering a base that goes nowhere instead would put a wget against a dead
// address in front of every install that path drives.
func RenderPreseed(s Spec, runURL, beaconBase, token string) (string, error) {
	addr, netmask, recipe, err := renderInputs(s)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(runURL) == "" {
		return "", fmt.Errorf("spec: no preseed/run URL, so netcfg would never re-run and the static network configuration would be inert")
	}
	if err := checkPreseedValue("preseed run URL", runURL); err != nil {
		return "", err
	}
	// The beacon base lands in the same two contexts every other
	// interpolated value does -- a preseed directive line, and a shell word
	// inside early_command and late_command. It gets the same guard.
	if err := ValidateBeaconBase(beaconBase); err != nil {
		return "", err
	}

	data := struct {
		IP, Netmask, Gateway, DNS, Hostname string
		SizeAssertion, Partman              string
		SSHPublicKey, UID, MarkerPath       string
		RunURL                              string
		BeaconEarly, BeaconPartman          string
		BeaconLate, BeaconHeartbeat         string
	}{
		IP:            addr.String(),
		Netmask:       netmask.String(),
		Gateway:       s.Network.Gateway,
		DNS:           strings.Join(s.Network.DNS, " "),
		Hostname:      s.Hostname,
		SizeAssertion: diskSizeAssertion(s.Layout),
		Partman:       recipe,
		SSHPublicKey:  strings.TrimSpace(s.SSHPublicKey),
		UID:           s.UID,
		MarkerPath:    markerPath,
		RunURL:        runURL,
		// Emitted after the size assertion above, never before: a machine
		// the assertion powers off must not have reported `early` first, or
		// a deliberate refusal becomes indistinguishable from a crash --
		// which is the failure this whole mechanism exists to separate.
		BeaconEarly:     BeaconSend(beaconBase, token, CheckpointEarly),
		BeaconPartman:   BeaconSend(beaconBase, token, CheckpointPartman),
		BeaconLate:      BeaconSend(beaconBase, token, CheckpointLate),
		BeaconHeartbeat: BeaconHeartbeat(beaconBase, token, CheckpointEarly),
	}

	var b strings.Builder
	if err := preseedTmpl.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}
```

- [ ] **Step 4: Fix the call sites so the tree compiles**

Four call sites, all mechanical:
- `internal/provision/preseed_test.go`: every `RenderPreseed(x, testRunURL)` → `RenderPreseed(x, testRunURL, "", testBeaconToken)`, except the new tests. Delete the old `TestNetcfgRerunScriptKillsDHCPThenRunsNetcfg` — `TestRenderRunScriptReportsNetcfgAndStillRerunsNetcfg` asserts everything it did plus the new behaviour.
- `internal/provision/server.go:175` and `:190` (`BuildHandler`) — Task 6.
- `internal/provision/server.go:435` and `:443` (`LocalImageStore.Build`) — pass `""` and `tok`: `RenderPreseed(spec, runURL, "", tok)` and `[]byte(RenderRunScript("", tok))`.
- `internal/provision/server_test.go`: `NetcfgRerunScript` → `RenderRunScript("", tok)` at each use, and `RenderPreseed(spec, testMediaURL+"/preseed/"+resp.Token+".sh")` gains its two new arguments (Task 6 sets what they are for `BuildHandler`; until then use `"", resp.Token`).

Run: `go build ./... && go vet ./...`
Expected: no output.

- [ ] **Step 5: Run the whole package's tests**

Run: `go test -race ./internal/provision/ -v`
Expected: PASS. Pay attention to `TestRenderPreseedCarriesTheStaticNetwork` and the disk-assertion tests — the template changed around both.

- [ ] **Step 6: Commit**

```bash
git add internal/provision/preseed.go internal/provision/preseed_test.go internal/provision/server.go internal/provision/server_test.go
git commit -m "feat(provision): le preseed rapporte ses quatre jalons et bat la mesure

`early` est emis apres l'assertion de taille, pas avant : une machine qui
refuse ne porte que `netcfg`, ce qu'aucune autre issue ne produit."
```

---

### Task 6: Wire the beacon base through the build path

**Files:**
- Modify: `internal/provision/server.go` (`BuildHandler` body, around lines 170-192)
- Modify: `internal/provision/server_test.go`
- Modify: `cmd/provisiond/main.go` (around lines 130-146)

**Interfaces:**
- Consumes: `RenderPreseed(s, runURL, beaconBase, token)`, `RenderRunScript(beaconBase, token)` (Task 5); `NewBeaconStore` (Task 2); `MediaHandler(dir, beacons)` (Task 3); `BuildHandler(dir, base, mediaURL, beacons)` (Task 4).
- Produces: a provisiond process whose two listeners share one store, and built images whose preseeds point at that store's write route.

- [ ] **Step 1: Write the failing test**

Append to `internal/provision/server_test.go`:

```go
// The URL a machine will call and the route that will answer it are both
// built from mediaURL here. This asserts they are the same address by
// construction -- the same property the preseed/run URL already has.
func TestBuildHandlerBakesTheBeaconURLTheMediaListenerServes(t *testing.T) {
	dir := t.TempDir()
	base := fakeBase(t)
	spec := goodSpec()
	reqBody, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	BuildHandler(dir, base, testMediaURL, NewBeaconStore(8)).
		ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/build", bytes.NewReader(reqBody)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var resp buildResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	cfg, err := os.ReadFile(filepath.Join(dir, resp.Token+".cfg"))
	if err != nil {
		t.Fatal(err)
	}
	want := BeaconURL(testMediaURL, resp.Token, CheckpointEarly)
	if !strings.Contains(string(cfg), want) {
		t.Errorf("the written preseed does not carry %q", want)
	}

	sh, err := os.ReadFile(filepath.Join(dir, resp.Token+".sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sh), BeaconURL(testMediaURL, resp.Token, CheckpointNetcfg)) {
		t.Errorf("the written preseed/run script does not report the netcfg checkpoint:\n%s", sh)
	}

	// And the route that URL names actually records, on a media listener
	// sharing the store.
	store := NewBeaconStore(8)
	got := httptest.NewRecorder()
	MediaHandler(dir, store).ServeHTTP(got,
		httptest.NewRequest(http.MethodGet, "/beacon/"+resp.Token+"/"+CheckpointEarly, nil))
	if got.Code != http.StatusNoContent {
		t.Errorf("the media listener answered the baked-in beacon URL with %d", got.Code)
	}
}
```

`fakeBase(t)` is the existing test helper `server_test.go` already uses for build tests — reuse it under whatever name it carries in that file rather than writing a second one.

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/provision/ -run BakesTheBeaconURL -v`
Expected: FAIL — the written preseed carries no beacon URL.

- [ ] **Step 3: Wire the build path**

In `BuildHandler`, beside the existing `preseedURL`/`runURL` construction:

```go
		// Built from mediaURL like the two above, and for the same reason:
		// the address baked into the image and the address this deployment
		// actually serves must be one construction, not two that happen to
		// agree. Empty when no store exists, which renders a preseed with
		// no beacons rather than one pointing at a route that is not there.
		beaconBase := mediaURL
		if beacons == nil {
			beaconBase = ""
		}

		preseed, err := RenderPreseed(spec, runURL, beaconBase, token)
```

and

```go
		if err := os.WriteFile(filepath.Join(dir, token+".sh"), []byte(RenderRunScript(beaconBase, token)), 0o644); err != nil {
```

In `cmd/provisiond/main.go`, build one store and give it to both listeners:

```go
	// One store, both listeners: the media listener writes to it, the build
	// listener reads from it. This is the only object the two share, and it
	// is why frame-provisiond stays at replicas: 1 -- a second replica would
	// answer the controller's reads from a memory the machine never wrote to.
	beacons := provision.NewBeaconStore(0)

	mediaMux := http.NewServeMux()
	mediaMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mediaMux.Handle("/", provision.MediaHandler(cfg.ImagesDir, beacons))

	buildSrv := &http.Server{
		Addr:              cfg.BuildAddr,
		Handler:           provision.BuildHandler(cfg.ImagesDir, provision.DefaultBase(), cfg.MediaURL, beacons),
		ReadHeaderTimeout: 10 * time.Second,
	}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test -race ./internal/provision/ ./cmd/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/provision/server.go internal/provision/server_test.go cmd/provisiond/main.go
git commit -m "feat(provisiond): un seul registre, deux ecoutes

L'URL cuite dans l'image et la route qui y repond viennent de la meme
construction -- pas de deux qui se trouvent d'accord."
```

---

### Task 7: The controller's way to read progress

**Files:**
- Modify: `internal/provision/server.go` (`HTTPImageStore`, around line 236)
- Modify: `internal/provision/server_test.go`

**Interfaces:**
- Consumes: `BeaconState` (Task 2), `beaconToken` (Task 1), `HTTPImageStore.BuildURL`.
- Produces:
  - `type ProgressReader interface { Progress(ctx context.Context, token string) (BeaconState, bool, error) }`
  - `func (s *HTTPImageStore) Progress(ctx context.Context, token string) (BeaconState, bool, error)` — `(state, true, nil)` when known, `(zero, false, nil)` on 404, an error otherwise.

- [ ] **Step 1: Write the failing test**

Append to `internal/provision/server_test.go`:

```go
func TestHTTPImageStoreProgressReadsTheBuildListener(t *testing.T) {
	seen := time.Unix(1_700_000_000, 0).UTC()
	store := NewBeaconStore(8)
	store.Record(testBeaconToken, CheckpointPartman, seen)
	srv := httptest.NewServer(BuildHandler(t.TempDir(), DefaultBase(), testMediaURL, store))
	defer srv.Close()

	s := &HTTPImageStore{BuildURL: srv.URL, MediaURL: testMediaURL}
	got, known, err := s.Progress(context.Background(), testBeaconToken)
	if err != nil {
		t.Fatal(err)
	}
	if !known {
		t.Fatal("known = false for an install that has reported")
	}
	if got.LastCheckpoint != CheckpointPartman || !got.LastSeen.Equal(seen) {
		t.Errorf("progress = %+v, want partman at %v", got, seen)
	}
}

// Silence is not an error, and an error is not silence. The controller
// reports them as different reasons, so this boundary must not blur.
func TestHTTPImageStoreProgressSeparatesSilenceFromFailure(t *testing.T) {
	srv := httptest.NewServer(BuildHandler(t.TempDir(), DefaultBase(), testMediaURL, NewBeaconStore(8)))
	defer srv.Close()

	s := &HTTPImageStore{BuildURL: srv.URL, MediaURL: testMediaURL}
	_, known, err := s.Progress(context.Background(), testBeaconToken)
	if err != nil {
		t.Errorf("err = %v; an install that has not reported is not an error", err)
	}
	if known {
		t.Error("known = true for an install that has not reported")
	}

	srv.Close()
	if _, _, err := s.Progress(context.Background(), testBeaconToken); err == nil {
		t.Error("err = nil although the build listener is unreachable")
	}
}

func TestHTTPImageStoreProgressRefusesAMalformedToken(t *testing.T) {
	s := &HTTPImageStore{BuildURL: "http://127.0.0.1:1", MediaURL: testMediaURL}
	if _, _, err := s.Progress(context.Background(), "../../secrets"); err == nil {
		t.Error("Progress accepted a token that is not a token")
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/provision/ -run ImageStoreProgress -v`
Expected: FAIL to compile — `s.Progress undefined`.

- [ ] **Step 3: Write the implementation**

In `internal/provision/server.go`:

```go
// ProgressReader is how the controller asks provisiond what an installation
// has reported. It is deliberately NOT part of Deps: provision.Install has
// no business reading beacon state, and the surest way to keep it that way
// is to give it no interface that could.
type ProgressReader interface {
	Progress(ctx context.Context, token string) (BeaconState, bool, error)
}

// Progress reads one installation's beacon state from the build API.
//
// The three outcomes are kept apart on purpose: known state, a 404 meaning
// nothing has been heard from that installation, and an error meaning this
// process could not ask. A caller that collapsed the last two would report
// a silent machine when the truth was a provisiond it could not reach.
func (s *HTTPImageStore) Progress(ctx context.Context, token string) (BeaconState, bool, error) {
	if !beaconToken.MatchString(token) {
		return BeaconState{}, false, fmt.Errorf("token %q is not a valid image token", token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(s.BuildURL, "/")+"/beacon/"+token, nil)
	if err != nil {
		return BeaconState{}, false, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return BeaconState{}, false, fmt.Errorf("reading progress from %s: %w", s.BuildURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return BeaconState{}, false, nil
	case http.StatusOK:
		var st BeaconState
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			return BeaconState{}, false, fmt.Errorf("decoding progress response: %w", err)
		}
		return st, true, nil
	default:
		b, _ := io.ReadAll(resp.Body)
		return BeaconState{}, false, fmt.Errorf("progress %s: HTTP %d: %s", s.BuildURL, resp.StatusCode, strings.TrimSpace(string(b)))
	}
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test -race ./internal/provision/ -v -run ImageStoreProgress`
Expected: PASS, three tests.

- [ ] **Step 5: Commit**

```bash
git add internal/provision/server.go internal/provision/server_test.go
git commit -m "feat(provision): lire l'avancement depuis l'API de build

Trois issues distinctes : etat connu, rien entendu, n'a pas pu demander.
ProgressReader n'entre pas dans Deps -- Install n'a rien a y lire."
```

---

### Task 8: Install announces the token it is using

**Files:**
- Modify: `internal/provision/types.go` (`Deps`, around line 120)
- Modify: `internal/provision/install.go` (just after `Images.Build`, around line 148)
- Modify: `internal/provision/install_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `Deps.ReportToken func(token string)` — called exactly once per `Install`, immediately after `Images.Build` returns successfully, never when it fails. May be nil.

- [ ] **Step 1: Write the failing test**

Append to `internal/provision/install_test.go`:

```go
// The controller cannot learn the token any other way: it is produced inside
// Install and is absent from Result, which the controller only sees once the
// install is over -- long after the diagnosis is needed.
func TestInstallAnnouncesItsTokenOnceTheImageExists(t *testing.T) {
	var got []string
	d, s, o := goodInstall(t)
	d.ReportToken = func(tok string) { got = append(got, tok) }

	if _, err := Install(context.Background(), d, s, o); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ReportToken called %d times, want exactly 1: %v", len(got), got)
	}
	if !beaconToken.MatchString(got[0]) {
		t.Errorf("reported token %q is not an image token", got[0])
	}
}

// Nothing was built, so there is no token and nothing to announce. A caller
// that heard one would poll provisiond for an installation that does not
// exist.
func TestInstallAnnouncesNoTokenWhenTheBuildFails(t *testing.T) {
	called := 0
	d, s, o := goodInstall(t)
	d.Images = failingImageStore{}
	d.ReportToken = func(string) { called++ }

	if _, err := Install(context.Background(), d, s, o); err == nil {
		t.Fatal("Install succeeded although the build failed")
	}
	if called != 0 {
		t.Errorf("ReportToken called %d times after a failed build, want 0", called)
	}
}
```

`goodInstall(t)` and `failingImageStore` are this file's existing helpers — reuse whatever names it already uses for "a Deps/Spec/Options triple that installs successfully" and "an ImageStore whose Build returns an error"; add the failing store only if the file has none.

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/provision/ -run InstallAnnounces -v`
Expected: FAIL to compile — `d.ReportToken undefined`.

- [ ] **Step 3: Write the implementation**

In `internal/provision/types.go`, add one field to `Deps`:

```go
type Deps struct {
	BMC    BMC
	Images ImageStore
	SSH    SSHClient
	Nodes  NodeChecker
	Report func(Phase) // called on entering each phase; may be nil
	// ReportToken is called once, with the image token, as soon as an image
	// exists -- and never if the build failed, because then there is
	// nothing to report about.
	//
	// It is the ONE thing this package tells a caller beyond the phase, and
	// it is write-only on purpose: the token is what a caller needs to ask
	// provisiond how the installation is reporting itself. There is no
	// matching read here, and there must not be one. Install must not be
	// able to see what a machine has claimed about its own progress; only
	// WaitForOurSystem, which proves identity, may end this phase.
	//
	// May be nil.
	ReportToken func(token string)
}
```

In `internal/provision/install.go`, immediately after the `buildErr` check:

```go
	url, token, buildErr := d.Images.Build(prepCtx, s)
	if buildErr != nil {
		return fail(PhasePreparing, phaseErr(prepCtx, fmt.Errorf("building the installer image: %w", buildErr)))
	}
	// Announced here and not earlier: before this line there is no token,
	// and announcing one that does not name a built image would have the
	// caller poll for an installation that was never created.
	if d.ReportToken != nil {
		d.ReportToken(token)
	}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test -race ./internal/provision/ -v`
Expected: PASS, whole package.

- [ ] **Step 5: Commit**

```bash
git add internal/provision/types.go internal/provision/install.go internal/provision/install_test.go
git commit -m "feat(provision): Install annonce son token, et rien de plus

Ecriture seule. Aucun lecteur en face : seul WaitForOurSystem, qui prouve
l'identite, peut terminer la phase Installing."
```

---

### Task 9: The condition an operator reads

**Files:**
- Modify: `internal/controller/frame/frameinstall_controller.go` (struct fields around line 100-160; `Reconcile`'s in-flight branch around line 198; `runInstall` around line 416; the `inFlight` helpers around lines 429-460)
- Modify: `internal/controller/frame/frameinstall_controller_test.go`

**Interfaces:**
- Consumes: `provision.ProgressReader`, `provision.BeaconState` (Task 7); `provision.Deps.ReportToken` (Task 8); the four checkpoint constants (Task 1).
- Produces: the condition `InstallerResponding` on `FrameInstall.status.conditions`, with reasons `Heartbeat`, `HeartbeatLost`, `NeverSeen`, `Unavailable`.

**Note for the implementer:** this controller writes no condition today — `status.Conditions` exists on the type and has never been set. This is the first, so there is no house pattern to follow; use `meta.SetStatusCondition` from `k8s.io/apimachinery/pkg/api/meta`, and mirror `reportPhase`'s Get-then-`Status().Patch` shape for the write.

- [ ] **Step 1: Write the failing test**

Append to `internal/controller/frame/frameinstall_controller_test.go`:

```go
func TestInstallerRespondingReportsEachStateDistinctly(t *testing.T) {
	lost := 61 * time.Second
	fresh := 5 * time.Second
	now := time.Unix(1_700_000_000, 0)

	for _, tc := range []struct {
		name       string
		state      provision.BeaconState
		known      bool
		err        error
		wantStatus metav1.ConditionStatus
		wantReason string
		wantInMsg  string
	}{
		{
			name:       "a heartbeat is arriving",
			state:      provision.BeaconState{LastCheckpoint: provision.CheckpointPartman, LastSeen: now.Add(-fresh), Count: 12},
			known:      true,
			wantStatus: metav1.ConditionTrue,
			wantReason: "Heartbeat",
			wantInMsg:  provision.CheckpointPartman,
		},
		{
			name:       "the heartbeat stopped mid-install",
			state:      provision.BeaconState{LastCheckpoint: provision.CheckpointPartman, LastSeen: now.Add(-lost), Count: 12},
			known:      true,
			wantStatus: metav1.ConditionFalse,
			wantReason: "HeartbeatLost",
			wantInMsg:  provision.CheckpointPartman,
		},
		{
			// The size guard powered the machine off: netcfg reported, the
			// heartbeat never started. The checkpoint in the message is what
			// tells this apart from a panic during partitioning.
			name:       "the disk-size guard refused",
			state:      provision.BeaconState{LastCheckpoint: provision.CheckpointNetcfg, LastSeen: now.Add(-lost), Count: 1},
			known:      true,
			wantStatus: metav1.ConditionFalse,
			wantReason: "HeartbeatLost",
			wantInMsg:  provision.CheckpointNetcfg,
		},
		{
			name:       "nothing was ever heard",
			known:      false,
			wantStatus: metav1.ConditionFalse,
			wantReason: "NeverSeen",
			wantInMsg:  "nothing",
		},
		{
			name:       "provisiond could not be asked",
			err:        errors.New("connection refused"),
			wantStatus: metav1.ConditionUnknown,
			wantReason: "Unavailable",
			wantInMsg:  "connection refused",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := installerRespondingCondition(tc.state, tc.known, tc.err, now)
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if !strings.Contains(got.Message, tc.wantInMsg) {
				t.Errorf("message = %q; it does not mention %q", got.Message, tc.wantInMsg)
			}
		})
	}
}

// The load-bearing test of the whole design. If someone later makes beacon
// state able to fail an install, this goes red.
func TestBeaconStateNeverEndsOrFailsAnInstall(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state provision.BeaconState
		known bool
		err   error
	}{
		{name: "never seen", known: false},
		{name: "heartbeat lost", known: true, state: provision.BeaconState{LastCheckpoint: provision.CheckpointNetcfg, LastSeen: time.Unix(0, 0)}},
		{name: "provisiond unreachable", err: errors.New("connection refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cond := installerRespondingCondition(tc.state, tc.known, tc.err, time.Now())
			// A condition, and nothing else. No phase, no failure, no
			// message that a caller could mistake for a terminal verdict.
			if cond.Type != "InstallerResponding" {
				t.Fatalf("condition type = %q", cond.Type)
			}
			for _, forbidden := range []string{string(provision.PhaseFailed), string(provision.PhaseReady), string(provision.PhaseInstalled)} {
				if strings.Contains(cond.Message, forbidden) {
					t.Errorf("the condition message names the phase %q; a diagnostic must not read as a verdict: %q", forbidden, cond.Message)
				}
			}
		})
	}
}
```

Add `"errors"`, `"strings"`, `"time"`, `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"` and `"github.com/rmocq/frame/internal/provision"` to the test file's imports if missing.

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/controller/frame/ -run 'InstallerResponding|BeaconStateNever' -v`
Expected: FAIL to compile — `undefined: installerRespondingCondition`.

- [ ] **Step 3: Write the pure decision function**

In `internal/controller/frame/frameinstall_controller.go`:

```go
// beaconLostAfter is how long without a beacon before the installer is
// called unresponsive. The machine sends every 15 seconds, so this is four
// missed sends.
//
// Not one or two: the machine is mid-installation on a network Frame just
// reconfigured under it, and the cost of calling a live installer dead is an
// operator sent to a machine that needed nothing. It is a guess with a
// reason, and the first number to revisit once real installs have run.
const beaconLostAfter = 60 * time.Second

// installerRespondingCondition turns what provisiond reported into the one
// condition an operator reads. It is a pure function of its arguments --
// no client, no clock of its own -- because its whole job is a judgment that
// must be testable at every boundary without standing up a cluster.
//
// It returns a condition and nothing else. It cannot fail an install, end a
// phase, or shorten a timeout, and the test beside it asserts exactly that.
func installerRespondingCondition(st provision.BeaconState, known bool, err error, now time.Time) metav1.Condition {
	cond := metav1.Condition{Type: "InstallerResponding"}
	switch {
	case err != nil:
		// Frame could not ask. Never conflated with "the machine is silent":
		// one is a fact about the machine, the other about this process.
		cond.Status = metav1.ConditionUnknown
		cond.Reason = "Unavailable"
		cond.Message = fmt.Sprintf("could not read installer progress: %v", err)
	case !known:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "NeverSeen"
		cond.Message = "nothing has been heard from the installer: it may not have booted the media, or the network may never have come up"
	case now.Sub(st.LastSeen) > beaconLostAfter:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "HeartbeatLost"
		cond.Message = fmt.Sprintf("last reached %s, %s ago, and has not reported since",
			st.LastCheckpoint, now.Sub(st.LastSeen).Round(time.Second))
	default:
		cond.Status = metav1.ConditionTrue
		cond.Reason = "Heartbeat"
		cond.Message = fmt.Sprintf("at %s, last reported %s ago; an installer that stays here is waiting on a question",
			st.LastCheckpoint, now.Sub(st.LastSeen).Round(time.Second))
	}
	return cond
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test ./internal/controller/frame/ -run 'InstallerResponding|BeaconStateNever' -v`
Expected: PASS, seven subtests.

- [ ] **Step 5: Remember the in-flight token and poll it**

Add to the `FrameInstallReconciler` struct, beside `Images`:

```go
	// Progress reads what an installation has reported to provisiond. It is
	// diagnosis only: nothing read through it may end, advance or fail a
	// phase. Left nil, the InstallerResponding condition is simply never
	// written and every install behaves exactly as it did before this
	// existed.
	Progress provision.ProgressReader
```

Beside `inFlight`, guarded by the same mutex:

```go
	// tokens is the image token of each in-flight install, by object UID.
	// provision.Install produces it (Deps.ReportToken) and it is absent from
	// Result, so this is the only way to know it while the install is still
	// running -- which is exactly when it is needed.
	//
	// Deliberately NOT written to status: the token is the unguessable
	// handle protecting both the beacon route and the preseed (which carries
	// the install UID) on an unauthenticated LAN listener. Putting it on an
	// object widens who can read it to everyone with get on frameinstalls.
	// The cost is that a manager restart loses it -- and the condition then
	// reports Unavailable, which is the honest answer.
	tokens map[types.UID]string
```

Two helpers beside `startInstall`/`finishInstall`:

```go
func (r *FrameInstallReconciler) rememberToken(uid types.UID, token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tokens == nil {
		r.tokens = map[types.UID]string{}
	}
	r.tokens[uid] = token
}

func (r *FrameInstallReconciler) tokenFor(uid types.UID) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tok, ok := r.tokens[uid]
	return tok, ok
}
```

`finishInstall` drops the token alongside the in-flight entry. It currently takes only `machineRef` and `runInstall` — its only caller of the defer — does not receive a UID at all:

```go
func (r *FrameInstallReconciler) runInstall(ctx context.Context, machineRef string, key client.ObjectKey, deps provision.Deps, spec provision.Spec, opts provision.Options) {
	defer r.finishInstall(machineRef)
```

So both gain the UID, and the one `runInstall` call site in `Reconcile` passes `fi.UID` (it already passes `fi.UID` to `startInstall`, so the value is in hand):

```go
func (r *FrameInstallReconciler) runInstall(ctx context.Context, machineRef string, uid types.UID, key client.ObjectKey, deps provision.Deps, spec provision.Spec, opts provision.Options) {
	defer r.finishInstall(machineRef, uid)
```

and `finishInstall` drops both entries under the one lock it already takes:

```go
func (r *FrameInstallReconciler) finishInstall(machineRef string, uid types.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, machineRef)
	delete(r.tokens, uid)
}
```

`provision.Deps` is built in `Reconcile` (line 326), not in `runInstall`, so the callback goes there, beside the existing `Report`. Capture the UID as a scalar rather than reading `fi.UID` from inside the closure: this closure runs on the goroutine spawned at line 337, after `Reconcile` has returned, and `&fi` is handed to `recordTask` in between — a closure reading through the struct would be reading an object someone else has had a pointer to.

```go
	uid := fi.UID
	deps := provision.Deps{
		// ... BMC, Images, SSH, Nodes, Report unchanged ...
		ReportToken: func(tok string) { r.rememberToken(uid, tok) },
	}
```

In `Reconcile`, the in-flight branch currently reads:

```go
	if r.running(fi.Spec.MachineRef) {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
```

It becomes — the poll and the condition write go ahead of the return, which is the whole of the controller change:

```go
	if r.running(fi.Spec.MachineRef) {
		r.reportInstallerLiveness(ctx, &fi)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
```

And the method itself:

```go
// reportInstallerLiveness refreshes the InstallerResponding condition on the
// requeue this reconciler already performs every 15 seconds while an install
// is in flight. It returns nothing: there is no outcome here a caller could
// act on, which is the point.
//
// Only during Installing. In every other phase Frame has a better signal than
// a beacon -- it is talking to the BMC, or to the machine itself -- and a
// liveness condition there would be noise competing with fact.
func (r *FrameInstallReconciler) reportInstallerLiveness(ctx context.Context, fi *framev1beta1.FrameInstall) {
	if r.Progress == nil || fi.Status.Phase != string(provision.PhaseInstalling) {
		return
	}
	token, ok := r.tokenFor(fi.UID)
	if !ok {
		// No token means this manager did not start this install (it
		// restarted), so it cannot ask about it. Said plainly rather than
		// reported as a silent machine.
		r.setCondition(ctx, fi, installerRespondingCondition(provision.BeaconState{}, false,
			fmt.Errorf("this manager did not start this installation, so it does not know its image token"), time.Now()))
		return
	}
	st, known, err := r.Progress.Progress(ctx, token)
	r.setCondition(ctx, fi, installerRespondingCondition(st, known, err, time.Now()))
}

// setCondition writes one condition, in the Get-then-Patch shape reportPhase
// uses. A failure to write is dropped on purpose: this is a diagnostic, and
// failing a reconcile because a diagnostic could not be recorded would let it
// affect the installation it only exists to describe.
func (r *FrameInstallReconciler) setCondition(ctx context.Context, fi *framev1beta1.FrameInstall, cond metav1.Condition) {
	var latest framev1beta1.FrameInstall
	if err := r.Get(ctx, client.ObjectKeyFromObject(fi), &latest); err != nil {
		return
	}
	patch := client.MergeFrom(latest.DeepCopy())
	cond.ObservedGeneration = latest.Generation
	meta.SetStatusCondition(&latest.Status.Conditions, cond)
	_ = r.Status().Patch(ctx, &latest, patch)
}
```

Add `"k8s.io/apimachinery/pkg/api/meta"` to the controller's imports.

In `cmd/main.go`, the `HTTPImageStore` already constructed for `Images` is also the `ProgressReader`. Build it once and use it twice rather than constructing a second:

```go
	provisiondImages := &provision.HTTPImageStore{
		BuildURL: provisiondBuildURL,
		MediaURL: provisiondMediaURL,
	}
	// ... in the reconciler literal:
		Images:   provisiondImages,
		Progress: provisiondImages,
```

- [ ] **Step 6: Run the whole suite**

Run: `cd /home/rmocq/frame-install-liveness && make test`
Expected: 0 failures. Read the output section by section — the envtest suite in this package is slow and its failures scroll past the summary.

- [ ] **Step 7: Commit**

```bash
git add internal/controller/frame/ cmd/main.go
git commit -m "feat(frameinstall): dire si l'installateur repond, sans jamais en decider

Une condition, rafraichie sur la requeue de 15 s qui existe deja. Elle ne
termine rien, n'echoue rien, ne raccourcit aucun budget -- et un test le
prouve pour chacun des etats."
```

---

### Task 10: Say it where an operator looks, and pin the replica

**Files:**
- Modify: `docs/provisioning.md`
- Modify: `charts/frame/templates/provisiond-deployment.yaml:17`
- Modify: `config/provisiond/deployment.yaml:21`

**Interfaces:**
- Consumes: the condition and reasons from Task 9.
- Produces: no code.

- [ ] **Step 1: Write the operator-facing section**

Append to `docs/provisioning.md` a section titled "Reading a stuck `Installing`", containing §2's table of the design doc rendered against what `kubectl` actually shows:

```markdown
## Reading a stuck `Installing`

`Installing` runs for up to twenty minutes and ends only when the machine
answers on SSH carrying this installation's UID. While it runs, the
`InstallerResponding` condition says whether the installer is speaking:

    kubectl get frameinstall <name> -o jsonpath='{.status.conditions[?(@.type=="InstallerResponding")]}'

| condition | last checkpoint in the message | what it means |
|---|---|---|
| `True` / `Heartbeat` | not advancing between reads | the installer is alive and waiting on a debconf question — go look at the console |
| `False` / `HeartbeatLost` | `partman` or `late` | it died during partitioning, `pkgsel`, or the base install |
| `False` / `HeartbeatLost` | `early` | it died between the disk-size guard and partitioning |
| `False` / `HeartbeatLost` | `netcfg` | **the machine refused**: a named disk was not the size the `FrameInstall` declared, and `preseed/early_command` powered it off. Check `spec.layout.disks[].sizeBytes` against the machine. |
| `False` / `NeverSeen` | none | it never booted the media, or the network never came up |
| `Unknown` / `Unavailable` | — | Frame could not ask `frame-provisiond`, or this manager restarted mid-install and no longer knows the image token. Says nothing about the machine. |

This condition is diagnosis only. It never ends the phase, never fails the
install and never shortens the twenty-minute budget: the phase still ends
only when the machine proves it is ours.
```

- [ ] **Step 2: Pin the replica count where someone would change it**

Add the same comment above `replicas: 1` in both files:

```yaml
  # Must stay 1. frame-provisiond keeps installer beacons in memory: the
  # media listener writes them and the build listener serves them back to
  # the manager, and a second replica would answer the manager's reads from
  # a process the machine never wrote to.
  replicas: 1
```

- [ ] **Step 3: Prove the chart and kustomize still agree**

Run: `make helm-parity`
Expected: green. Read every section of the output, not just the exit code — the script stops at the first red section, so a zero exit with a truncated run is possible.

- [ ] **Step 4: Run everything one last time**

Run: `make test && go vet ./... && npm run test`
Expected: 0 failures, no vet output, UI suite green (the UI lives at the repository root — `src/`, `vite.config.ts`; this plan touches none of it, so that run is a regression check).

- [ ] **Step 5: Commit**

```bash
git add docs/provisioning.md charts/frame/templates/provisiond-deployment.yaml config/provisiond/deployment.yaml
git commit -m "docs(provisioning): lire un Installing bloque, et epingler la replique

Le tableau dit ce que chaque jalon signifie, dont le refus du garde de
taille -- le cas qui etait jusqu'ici indiscernable d'une panne."
```

---

## Self-Review

**1. Spec coverage.** §1 (the hole) → Task 10's table. §2 (two signals) → Tasks 1, 5, 9. §3 (the boundary) → Tasks 7, 8, 9, with Task 9's `TestBeaconStateNeverEndsOrFailsAnInstall` as the enforcing test. §4 (keyed by token) → Tasks 1, 2, 8, 9. §5 (what the machine sends) → Tasks 1, 5. §6 (what provisiond keeps) → Tasks 2, 4, 6, 10. §7 (the condition) → Task 9. §8 (remote syslog) → **out of scope by design**, staged second and gated on measurement. §9 (testing) → the tests named in each task, with §9's four demands each landing in a named test. §10 (assumptions) → not code; assumption 1 is falsified by the first real install, and Task 10's table is what makes the answer readable.

**2. Placeholder scan.** No "TBD", no "add error handling", no "similar to Task N". Two tasks reference existing test helpers by description rather than by name (`fakeBase` in Task 6, `goodInstall`/`failingImageStore` in Task 8), because the plan should not guess at identifiers it has not read; both say to reuse whatever the file already carries.

**3. Type consistency.** `BeaconState{LastCheckpoint, LastSeen, Count}` is used identically in Tasks 2, 4, 7 and 9. `MediaHandler(dir, beacons)` (Task 3) and `BuildHandler(dir, base, mediaURL, beacons)` (Task 4) are called with those exact arities in Tasks 5, 6, 7. `RenderPreseed(s, runURL, beaconBase, token)` and `RenderRunScript(beaconBase, token)` (Task 5) are called with those arities in Task 6 and in `LocalImageStore`. `Progress(ctx, token) (BeaconState, bool, error)` is defined in Task 7 and consumed in Task 9 with the same three results. `installerRespondingCondition(st, known, err, now)` is defined and called identically in Task 9.

**4. Ordering.** Tasks 3, 4 and 5 each change a signature and each includes the call-site fixes that keep the tree compiling, so every task ends on a green build. Task 5's `server_test.go` edits are provisional (`"", resp.Token`) and Task 6 sets them to what the wiring actually produces — the only place in the plan where one task revises another's line, and it is called out in both.
