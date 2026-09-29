package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const tok = "0123456789abcdef0123456789abcdef"

// testMediaURL stands in for the address the BMC, on the management
// network, would reach the media listener at. Only the seed tests actually
// fetch anything served by it; the
// other BuildHandler tests only need a non-empty value.
const testMediaURL = "http://192.0.2.50:8081"

// The listener the BMC reaches must not be able to make anything.
func TestMediaHandlerRefusesEverythingButReadingAnImage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, tok+".iso"), []byte("ISO"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := MediaHandler(dir, nil)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/iso/" + tok + ".iso", http.StatusOK},
		{http.MethodPost, "/build", http.StatusNotFound},
		{http.MethodPost, "/iso/" + tok + ".iso", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/iso/" + tok + ".iso", http.StatusMethodNotAllowed},
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
		if rr.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rr.Code, tc.want)
		}
	}
}

// Serving files by a name the caller supplies is how a media server becomes a
// way to read /etc/shadow.
func TestMediaHandlerRefusesPathsThatClimbOut(t *testing.T) {
	dir := t.TempDir()
	h := MediaHandler(dir, nil)
	for _, p := range []string{"/iso/../../etc/passwd", "/iso/..%2f..%2fetc%2fpasswd", "/iso/sub/dir.iso"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		if rr.Code == http.StatusOK {
			t.Errorf("%s was served", p)
		}
	}
}

// The positive control the cases above need: a well-formed 32-hex name that
// does not exist must still reach the filesystem and 404 there. Without
// this, "the traversal cases above returned 404" is indistinguishable from
// "the route never matched them" or "this handler 404s on everything" -- it
// is what separates the name check from the router and from the filesystem
// lookup the name check guards.
func TestMediaHandlerServesTheFilesystemsAnswerForAWellFormedNameThatIsAbsent(t *testing.T) {
	dir := t.TempDir()
	h := MediaHandler(dir, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/iso/"+tok+".iso", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404 (a well-formed but absent name should reach the filesystem and fail there, not be turned away earlier)", rr.Code)
	}
}

// The other half of the same pair, from the opposite direction: a name that
// is a single path segment (so the route matches it) and that DOES exist on
// disk under that exact name, but does not match imageName. This is what
// proves the name check itself refuses -- not a missing file, which would
// also 404, and not a route that failed to match a multi-segment path
// (TestMediaHandlerRefusesPathsThatClimbOut's "/iso/sub/dir.iso" case),
// which never reaches the name check at all.
func TestMediaHandlerRefusesAWellFormedRouteNameThatIsNotAnImageName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "not-an-image-name"), []byte("ISO"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := MediaHandler(dir, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/iso/not-an-image-name", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404 (the name check should have refused this before the filesystem was touched, even though a file exists under this exact name)", rr.Code)
	}
}

// The listener the BMC reaches must be able to read a seed the same way it
// reads an image, and be just as unable to write one.
func TestMediaHandlerServesTheSeedAndRefusesToWriteIt(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, tok), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"user-data", "meta-data", "vendor-data"} {
		if err := os.WriteFile(filepath.Join(dir, tok, f), []byte("SEED-"+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := MediaHandler(dir, nil)
	for _, f := range []string{"user-data", "meta-data", "vendor-data"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/autoinstall/"+tok+"/"+f, nil))
		if rr.Code != http.StatusOK || rr.Body.String() != "SEED-"+f {
			t.Errorf("GET %s = %d %q", f, rr.Code, rr.Body.String())
		}
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(m, "/autoinstall/"+tok+"/"+f, nil))
			if rr.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", m, f, rr.Code)
			}
		}
	}
}

// The closed list and the token shape each refuse on their own. Files exist
// under every refused name, so the guard -- not a missing file -- is what
// answers 404. Mutation-proved by replacing either guard with "true".
func TestMediaHandlerRefusesSeedNamesOutsideTheClosedList(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{tok + "/network-config", tok + "/user-data.bak", "not-a-token/user-data"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := MediaHandler(dir, nil)
	for _, p := range []string{
		"/autoinstall/" + tok + "/network-config",
		"/autoinstall/" + tok + "/user-data.bak",
		"/autoinstall/not-a-token/user-data",
		"/autoinstall/" + tok + "/../" + tok + ".iso",
		"/autoinstall/" + tok + "/sub/user-data",
		"/autoinstall/" + tok + "/",
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		if rr.Code == http.StatusOK {
			t.Errorf("GET %s = 200; it must be refused", p)
		}
	}
}

// buildOnce runs a real BuildHandler against a base served over HTTP and
// returns the directory it wrote into and the token it answered.
func buildOnce(t *testing.T, spec Spec, beacons *BeaconStore) (dir, token string) {
	t.Helper()
	requireXorriso(t)
	baseBytes, err := os.ReadFile(realBaseISO(t))
	if err != nil {
		t.Fatal(err)
	}
	srv := serveBytes(t, baseBytes)
	base := BaseSource{URL: srv.URL + "/base.iso", SHA256: sum(baseBytes)}
	dir = t.TempDir()
	reqBody, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	BuildHandler(dir, base, testMediaURL, beacons).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/build", bytes.NewReader(reqBody)))
	if rr.Code != http.StatusOK {
		t.Fatalf("build: code = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp buildResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return dir, resp.Token
}

// What BuildHandler writes must be exactly what RenderAutoinstall(spec)
// produces, and the seed address baked into the built image must be the one
// MediaHandler serves: a seed served at an address the image does not ask
// for is an installation that boots and waits with nothing to read.
func TestBuildHandlerWritesTheSeedTheImageAsksFor(t *testing.T) {
	spec := goodSpec()
	dir, token := buildOnce(t, spec, nil)

	want, err := RenderAutoinstall(spec, "", token)
	if err != nil {
		t.Fatal(err)
	}
	got := httptest.NewRecorder()
	MediaHandler(dir, nil).ServeHTTP(got, httptest.NewRequest(http.MethodGet, "/autoinstall/"+token+"/user-data", nil))
	if got.Code != http.StatusOK || got.Body.String() != want {
		t.Errorf("served user-data (%d) does not match RenderAutoinstall(spec) byte-for-byte", got.Code)
	}

	built := isoContains(t, filepath.Join(dir, token+".iso"), "/boot/grub/grub.cfg")
	wantURL := testMediaURL + "/autoinstall/" + token + "/"
	if !strings.Contains(built, `ds=nocloud\;s=`+wantURL) {
		t.Errorf("built image's boot args do not name %s:\n%s", wantURL, built)
	}
}

func TestBuildHandlerDeleteRemovesTheSeedWithTheImage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, tok+".iso"), []byte("ISO"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSeed(dir, tok, goodSpec(), ""); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	BuildHandler(dir, DefaultBase(), testMediaURL, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/iso/"+tok+".iso", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", rr.Code)
	}
	for _, name := range []string{tok + ".iso", tok} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived DELETE", name)
		}
	}
}

func TestLocalImageStoreRemoveTakesTheSeedToo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, tok+".iso"), []byte("ISO"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSeed(dir, tok, goodSpec(), ""); err != nil {
		t.Fatal(err)
	}
	if err := (&LocalImageStore{Dir: dir}).Remove(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{tok + ".iso", tok} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived Remove", name)
		}
	}
}

// I2. The build API is unauthenticated and in-cluster, which on this
// platform means reachable by every notebook and every coding sandbox --
// the recorded defect decision 3 of the design was written against. It has
// no use for the join token: RenderAutoinstall never reads Spec.Cluster. So the
// token must not be in the request at all.
//
// The assertion is on the raw request body, not on a decoded struct: a
// decode would only see the fields this package knows about, and the
// question here is what bytes crossed the wire.
func TestHTTPImageStoreNeverSendsTheJoinTokenToTheBuildAPI(t *testing.T) {
	const token = "K10deadbeef::server:supersecretpassword"

	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildResponse{Token: tok})
	}))
	defer srv.Close()

	spec := goodSpec()
	spec.Cluster = ClusterTarget{
		Mode:       ClusterJoin,
		ServerURL:  "https://192.0.2.201:6443",
		JoinToken:  token,
		K3sVersion: "v1.33.4+k3s1",
	}

	store := &HTTPImageStore{BuildURL: srv.URL, MediaURL: testMediaURL, Client: srv.Client()}
	if _, _, err := store.Build(context.Background(), spec); err != nil {
		t.Fatal(err)
	}

	// Positive control: the body really is the marshalled Spec, so "the
	// token is absent" is a statement about this request rather than about
	// an empty buffer nobody wrote to.
	if !strings.Contains(string(body), `"Hostname":"g9"`) {
		t.Fatalf("the recorded body is not the marshalled Spec at all:\n%s", body)
	}
	if strings.Contains(string(body), token) {
		t.Errorf("the k3s join token was POSTed in cleartext to the build API:\n%s", body)
	}
	if strings.Contains(string(body), "192.0.2.201:6443") {
		t.Errorf("the cluster target was POSTed to a build API that has no use for it:\n%s", body)
	}
}

// I10. The search for secret material belongs where the spec-derived
// content is, and that is the served seed, not the image. This reads the
// file BuildHandler actually wrote and serves -- dir/<token>/user-data --
// rather than RenderAutoinstall's return value, because the
// question is what is published on the unauthenticated media listener.
func TestBuildHandlerServesASeedWithNoSecretMaterial(t *testing.T) {
	requireXorriso(t)

	baseISOPath := realBaseISO(t)
	baseBytes, err := os.ReadFile(baseISOPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := serveBytes(t, baseBytes)
	base := BaseSource{URL: srv.URL + "/base.iso", SHA256: sum(baseBytes)}

	const joinToken = "K10deadbeef::server:supersecretpassword"
	spec := goodSpec()
	spec.SSHPublicKey = secretLadenPublicKey
	spec.Cluster = ClusterTarget{
		Mode:       ClusterJoin,
		ServerURL:  "https://192.0.2.201:6443",
		JoinToken:  joinToken,
		K3sVersion: "v1.33.4+k3s1",
	}

	dir := t.TempDir()
	reqBody, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	BuildHandler(dir, base, testMediaURL, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/build", bytes.NewReader(reqBody)))

	// The secret-laden key must be refused outright -- that is the primary
	// guard, and it is what keeps the seed clean. If it ever is not, the
	// rest of this test is what catches the consequence.
	if rr.Code == http.StatusBadRequest {
		return
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("build: code = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp buildResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	// Fetched through the media listener, not read off disk: that is the
	// surface the question is about.
	got := httptest.NewRecorder()
	MediaHandler(dir, nil).ServeHTTP(got, httptest.NewRequest(http.MethodGet, "/autoinstall/"+resp.Token+"/user-data", nil))
	if got.Code != http.StatusOK {
		t.Fatalf("GET the served seed = %d", got.Code)
	}
	served := got.Body.String()
	for what, v := range map[string]string{
		"PEM armour":          "PRIVATE KEY",
		"an OpenSSH key file": "BEGIN OPENSSH",
		"the k3s join token":  joinToken,
	} {
		if strings.Contains(served, v) {
			t.Errorf("the seed served on the unauthenticated media listener carries %s (%q)", what, v)
		}
	}
}

// secretLadenPublicKey is the shape a real leak takes: a Secret whose
// "id.pub" holds both halves, or a paste of `cat id_ed25519*`. Measured
// against golang.org/x/crypto/ssh, ParseAuthorizedKey finds the public line
// and returns no error for it.
const secretLadenPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILsytToxkJ2CiWuiv8BZ3hYpu7tFXn7Rwz+kc2gjbPSy frame-test-fixture\n" +
	"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\n"

// And the ordinary spec's served seed does carry the public key, which
// is the positive control for the search above: a served body that was
// empty, or a route answering with the wrong file, would satisfy every
// "does not contain" there.
func TestBuildHandlerServesASeedThatDoesCarryThePublicKey(t *testing.T) {
	requireXorriso(t)

	baseISOPath := realBaseISO(t)
	baseBytes, err := os.ReadFile(baseISOPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := serveBytes(t, baseBytes)
	base := BaseSource{URL: srv.URL + "/base.iso", SHA256: sum(baseBytes)}

	dir := t.TempDir()
	spec := goodSpec()
	reqBody, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	BuildHandler(dir, base, testMediaURL, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/build", bytes.NewReader(reqBody)))
	if rr.Code != http.StatusOK {
		t.Fatalf("build: code = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp buildResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	got := httptest.NewRecorder()
	MediaHandler(dir, nil).ServeHTTP(got, httptest.NewRequest(http.MethodGet, "/autoinstall/"+resp.Token+"/user-data", nil))
	if got.Code != http.StatusOK {
		t.Fatalf("GET the served seed = %d", got.Code)
	}
	if !strings.Contains(got.Body.String(), spec.SSHPublicKey) {
		t.Errorf("the served seed does not carry the public key at all:\n%s", got.Body.String())
	}
}

// The two halves of the media-URL check are deliberately different, and the
// difference is exactly one address: the placeholder both shipped manifests
// carry. Syntax must accept it (or the manager CrashLoops on `kubectl apply
// -k config/default`); the full check must refuse it (or the FrameInstall
// refusal never fires on that path).
func TestValidateMediaURLRefusesTheReservedPlaceholderButItsSyntaxDoesNot(t *testing.T) {
	const placeholder = "http://ci-placeholder.invalid:30581"

	if err := ValidateMediaURLSyntax(placeholder); err != nil {
		t.Errorf("syntax refused the shipped placeholder (%v); the manager would not start from config/default", err)
	}
	if err := ValidateMediaURL(placeholder); err == nil {
		t.Error("the full check accepted .invalid; RFC 2606 reserves it and it never resolves")
	}

	// Both forms of the reserved name, and case-insensitively.
	for _, raw := range []string{
		"http://invalid:30581",
		"http://anything.INVALID:30581",
		"https://a.b.invalid/",
	} {
		if err := ValidateMediaURL(raw); err == nil {
			t.Errorf("%q was accepted", raw)
		}
	}

	// Positive control: a real address passes both, so the refusals above
	// are about .invalid and not about the function refusing everything.
	for _, raw := range []string{"http://192.0.2.10:30581", "https://media.frame.internal"} {
		if err := ValidateMediaURLSyntax(raw); err != nil {
			t.Errorf("syntax refused %q: %v", raw, err)
		}
		if err := ValidateMediaURL(raw); err != nil {
			t.Errorf("the full check refused %q: %v", raw, err)
		}
	}

	// And a name that merely contains "invalid" is not the reserved one.
	if err := ValidateMediaURL("http://invalid-host.example.net:30581"); err != nil {
		t.Errorf("a host whose name merely contains \"invalid\" was refused: %v", err)
	}
}

func TestMediaHandlerRecordsABeacon(t *testing.T) {
	store := NewBeaconStore(8)
	dir := t.TempDir()
	// A beacon must name a real installation: see
	// TestMediaHandlerRefusesABeaconForATokenNamingNoImage. This test is
	// about the write succeeding, so the image the token names has to
	// exist.
	if err := os.WriteFile(filepath.Join(dir, testBeaconToken+".iso"), []byte("ISO"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := MediaHandler(dir, store)

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
	dir := t.TempDir()
	// The image exists here too: this test is about the checkpoint and
	// token shapes, not about whether the image exists, and it must not be
	// able to pass for the wrong reason (a 404 from the image check rather
	// than from the shape checks it actually names).
	if err := os.WriteFile(filepath.Join(dir, testBeaconToken+".iso"), []byte("ISO"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := MediaHandler(dir, store)

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

// TestMediaHandlerRefusesABeaconForATokenNamingNoImage is finding 5: Record
// shape-checks a token but never asks whether it names a real image, so
// every well-formed token is insertable -- and eviction is oldest-inserted,
// so enough well-formed-but-fake tokens evict a live install's own entry.
// A well-formed token that names no image on disk must 404 before the
// store is ever touched.
func TestMediaHandlerRefusesABeaconForATokenNamingNoImage(t *testing.T) {
	store := NewBeaconStore(8)
	// Deliberately empty: no <token>.iso exists here.
	h := MediaHandler(t.TempDir(), store)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/beacon/"+testBeaconToken+"/"+CheckpointEarly, nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d -- a token naming no image must not be recordable", rr.Code, http.StatusNotFound)
	}
	if _, ok := store.Get(testBeaconToken); ok {
		t.Error("a beacon for a token naming no image still created a store entry")
	}
}

// TestMediaHandlerRefusesEnoughFakeBeaconsToEvictALiveInstall is the shape
// of the actual attack finding 5 closes: before the fix, sixty-four
// well-formed but never-issued tokens would each pass Record's own shape
// checks, evicting the genuine, first-inserted entry (eviction is
// oldest-inserted). With the image-existence check in front of Record, none
// of them are ever recordable, so the real entry survives.
func TestMediaHandlerRefusesEnoughFakeBeaconsToEvictALiveInstall(t *testing.T) {
	store := NewBeaconStore(64)
	dir := t.TempDir()
	realToken := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := os.WriteFile(filepath.Join(dir, realToken+".iso"), []byte("ISO"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := MediaHandler(dir, store)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/beacon/"+realToken+"/"+CheckpointEarly, nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("the real install's own beacon was refused: status = %d", rr.Code)
	}

	for i := 0; i < 64; i++ {
		fake := fmt.Sprintf("%032x", i+1)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/beacon/"+fake+"/"+CheckpointEarly, nil))
		if rr.Code != http.StatusNotFound {
			t.Errorf("fake token %d: status = %d, want 404", i, rr.Code)
		}
	}

	if _, ok := store.Get(realToken); !ok {
		t.Error("the real install's entry was evicted by tokens naming no image")
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
//
// GET alone cannot tell those two apart: "never registered" and
// "registered but this handler always 404s" look identical on a GET. POST
// to the same path is the discriminator, because http.ServeMux itself
// answers 405, not 404, for a pattern that IS registered when the method
// doesn't match -- exactly what TestMediaHandlerRefusesEverythingButReading-
// AnImage's POST /iso/{name} case already relies on for that route. A 404
// on POST here is what proves the /beacon/{token}/{checkpoint} pattern is
// absent from the mux -- not merely that GET's handler refused this
// particular request.
func TestMediaHandlerWithoutAStoreHasNoBeaconRoute(t *testing.T) {
	h := MediaHandler(t.TempDir(), nil)
	path := "/beacon/" + testBeaconToken + "/" + CheckpointEarly

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET %s: status = %d, want 404", path, rr.Code)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("POST %s: status = %d, want 404 (405 would mean the route is registered)", path, rr.Code)
	}
}

func TestBuildHandlerServesBeaconStateBack(t *testing.T) {
	store := NewBeaconStore(8)
	seen := time.Unix(1_700_000_000, 0).UTC()
	store.Record(testBeaconToken, CheckpointEarly, seen)

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
	if got.LastCheckpoint != CheckpointEarly || !got.LastSeen.Equal(seen) || got.Count != 1 {
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

// The in-cluster listener reads. It must never be a second way to write --
// under either verb. GET is the shape MediaHandler's real write route uses
// (GET /beacon/{token}/{checkpoint}, Task 3), so it is the likely mistake:
// copying that route verbatim into BuildHandler. POST is the unlikely one,
// but http.ServeMux dispatches on method and pattern together, so a write
// route registered here as POST would 405 a GET without the handler ever
// running -- a GET-only version of this test would pass whether or not
// that write route existed.
func TestBuildHandlerDoesNotAcceptBeacons(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			store := NewBeaconStore(8)
			rr := httptest.NewRecorder()
			BuildHandler(t.TempDir(), DefaultBase(), testMediaURL, store).
				ServeHTTP(rr, httptest.NewRequest(method, "/beacon/"+testBeaconToken+"/"+CheckpointEarly, nil))
			if rr.Code >= 200 && rr.Code < 300 {
				t.Errorf("status = %d, want non-2xx: a write-shaped request must not succeed here", rr.Code)
			}
			if _, ok := store.Get(testBeaconToken); ok {
				t.Errorf("the build listener recorded a beacon (status %d)", rr.Code)
			}
		})
	}
}

// The URL a machine will call and the route that will answer it are both
// built from mediaURL here. This asserts they are the same address by
// construction -- the same property the seed URL already has.
func TestBuildHandlerBakesTheBeaconURLTheMediaListenerServes(t *testing.T) {
	requireXorriso(t)

	baseISOPath := realBaseISO(t)
	baseBytes, err := os.ReadFile(baseISOPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := serveBytes(t, baseBytes)
	base := BaseSource{URL: srv.URL + "/base.iso", SHA256: sum(baseBytes)}

	dir := t.TempDir()
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

	seed, err := os.ReadFile(filepath.Join(dir, resp.Token, "user-data"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{CheckpointEarly, CheckpointLate, CheckpointFailed} {
		if want := BeaconURL(testMediaURL, resp.Token, c); !strings.Contains(string(seed), want) {
			t.Errorf("the written seed does not carry %q", want)
		}
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

func TestHTTPImageStoreProgressReadsTheBuildListener(t *testing.T) {
	seen := time.Unix(1_700_000_000, 0).UTC()
	store := NewBeaconStore(8)
	store.Record(testBeaconToken, CheckpointEarly, seen)
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
	if got.LastCheckpoint != CheckpointEarly || !got.LastSeen.Equal(seen) {
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
