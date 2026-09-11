package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tok = "0123456789abcdef0123456789abcdef"

// testMediaURL stands in for the address the BMC, on the management
// network, would reach the media listener at. Only TestBuildHandlerWrites-
// ThePreseedTheImageAsksFor actually fetches anything served by it; the
// other BuildHandler tests only need a non-empty value.
const testMediaURL = "http://192.168.2.50:8081"

// The listener the BMC reaches must not be able to make anything.
func TestMediaHandlerRefusesEverythingButReadingAnImage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, tok+".iso"), []byte("ISO"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := MediaHandler(dir)

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
	h := MediaHandler(dir)
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
	h := MediaHandler(dir)
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
	h := MediaHandler(dir)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/iso/not-an-image-name", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404 (the name check should have refused this before the filesystem was touched, even though a file exists under this exact name)", rr.Code)
	}
}

// The listener the BMC reaches must be able to read a preseed the same way
// it reads an image, and be just as unable to write one.
func TestMediaHandlerServesAPreseedAndRefusesToWriteOne(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, tok+".cfg"), []byte("PRESEED"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := MediaHandler(dir)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/preseed/" + tok + ".cfg", http.StatusOK},
		{http.MethodPost, "/preseed/" + tok + ".cfg", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/preseed/" + tok + ".cfg", http.StatusMethodNotAllowed},
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
		if rr.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rr.Code, tc.want)
		}
	}
}

// /iso/'s traversal coverage (TestMediaHandlerRefusesPathsThatClimbOut)
// never sends a single request at /preseed/; this is that same coverage for
// the new route.
func TestMediaHandlerRefusesPreseedPathsThatClimbOut(t *testing.T) {
	dir := t.TempDir()
	h := MediaHandler(dir)
	for _, p := range []string{"/preseed/../../etc/passwd", "/preseed/..%2f..%2fetc%2fpasswd", "/preseed/sub/dir.cfg"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		if rr.Code == http.StatusOK {
			t.Errorf("%s was served", p)
		}
	}
}

// This does NOT prove preseedName refuses anything -- it only separates "a
// well-formed name reached the filesystem and 404'd there" from "the route
// never matched at all". Keeping the comment honest about that after
// getting it wrong once already: an earlier version of this file paired
// this test with TestMediaHandlerServesAPreseedAndRefusesToWriteOne and
// called that the positive control for preseedName. It is not -- both of
// those tests stay fully green with preseedName's check deleted outright
// (measured; see the mutation proof in TestMediaHandlerRefusesAWellFormed-
// PreseedRouteNameThatIsNotAPreseedName's comment below, which is the
// actual proof).
func TestMediaHandlerServesTheFilesystemsAnswerForAWellFormedPreseedNameThatIsAbsent(t *testing.T) {
	dir := t.TempDir()
	h := MediaHandler(dir)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/preseed/"+tok+".cfg", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404 (a well-formed but absent preseed name should reach the filesystem and fail there, not be turned away earlier)", rr.Code)
	}
}

// The actual proof preseedName refuses anything: a name that is a single
// path segment (so the route matches it) and that DOES exist on disk under
// that exact name, but does not match preseedName. A missing file would
// also 404 here, and a multi-segment path
// (TestMediaHandlerRefusesPreseedPathsThatClimbOut's "/preseed/sub/dir.cfg"
// case) never reaches the name check at all -- only this shape, a
// well-formed route match against a real file the pattern still rejects,
// tells "the guard refused it" apart from "there was nothing there".
// Mutation-proved: replacing preseedName with regexp.MustCompile(`.*`)
// turns exactly this test red and none of the others in this file.
func TestMediaHandlerRefusesAWellFormedPreseedRouteNameThatIsNotAPreseedName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "not-a-preseed-name"), []byte("PRESEED"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := MediaHandler(dir)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/preseed/not-a-preseed-name", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404 (the name check should have refused this before the filesystem was touched, even though a file exists under this exact name)", rr.Code)
	}
}

func TestBuildHandlerRejectsASpecItWouldRefuseToRender(t *testing.T) {
	h := BuildHandler(t.TempDir(), DefaultBase(), testMediaURL)
	body := `{"uid":"","hostname":"g9"}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/build", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rr.Code)
	}
}

// The point of the whole HTTP-preseed change: what BuildHandler writes to
// dir/<token>.cfg must be exactly what RenderPreseed(spec) produces, and
// the URL baked into the built image's own boot arguments must be the one
// MediaHandler's /preseed/{name} route actually serves. A preseed served at
// an address the image does not ask for is an installation that hangs with
// nothing to read.
func TestBuildHandlerWritesThePreseedTheImageAsksFor(t *testing.T) {
	requireXorriso(t)

	baseISOPath := realBaseISO(t)
	baseBytes, err := os.ReadFile(baseISOPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := serveBytes(t, baseBytes)
	base := BaseSource{URL: srv.URL + "/base.iso", SHA256: sum(baseBytes)}

	dir := t.TempDir()
	h := BuildHandler(dir, base, testMediaURL)

	spec := goodSpec()
	reqBody, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/build", bytes.NewReader(reqBody)))
	if rr.Code != http.StatusOK {
		t.Fatalf("build: code = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp buildResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	want, err := RenderPreseed(spec, testMediaURL+"/preseed/"+resp.Token+".sh")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, resp.Token+".cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("dir/%s.cfg does not match RenderPreseed(spec) byte-for-byte", resp.Token)
	}

	wantURL := testMediaURL + "/preseed/" + resp.Token + ".cfg"
	built := isoContains(t, filepath.Join(dir, resp.Token+".iso"), "/isolinux/txt.cfg")
	if !strings.Contains(built, "url="+wantURL) {
		t.Errorf("built image's boot args do not carry url=%s:\n%s", wantURL, built)
	}
}

// C1's serving half. The preseed's preseed/run directive names a URL; if
// nothing answers it, netcfg never re-runs and the static network
// configuration is inert -- a failure indistinguishable, from the outside,
// from an install that is merely slow.
func TestMediaHandlerServesTheNetcfgRerunScriptBesideThePreseed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, tok+".sh"), []byte(NetcfgRerunScript), 0o644); err != nil {
		t.Fatal(err)
	}
	h := MediaHandler(dir)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/preseed/"+tok+".sh", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /preseed/%s.sh = %d, want 200", tok, rr.Code)
	}
	if rr.Body.String() != NetcfgRerunScript {
		t.Errorf("served body is not the run script:\n%s", rr.Body.String())
	}

	// Read-only, exactly like the preseed and the image beside it.
	for _, m := range []string{http.MethodPost, http.MethodDelete} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(m, "/preseed/"+tok+".sh", nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /preseed/%s.sh = %d, want 405", m, tok, rr.Code)
		}
	}
}

// The run script's own name guard, proved the same way preseedName's is: a
// single-segment name that DOES exist on disk under that exact name and
// still must not be served. A missing file would 404 too, so only a real
// file the pattern rejects separates "the guard refused it" from "there was
// nothing there".
//
// Mutation-proved: replacing runScriptName with regexp.MustCompile(`.*`)
// turns this red. Deleting the runScriptName clause from the route turns
// TestMediaHandlerServesTheNetcfgRerunScriptBesideThePreseed red instead --
// two different mutations, two different tests, neither able to stand in
// for the other.
func TestMediaHandlerRefusesARunScriptNameThatIsNotARunScriptName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"not-a-run-script.sh", tok + ".sh.txt", tok + ".bash"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(NetcfgRerunScript), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := MediaHandler(dir)
	for _, name := range []string{"not-a-run-script.sh", tok + ".sh.txt", tok + ".bash"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/preseed/"+name, nil))
		if rr.Code != http.StatusNotFound {
			t.Errorf("GET /preseed/%s = %d, want 404 (a file exists under this exact name; the guard has to be what refuses it)", name, rr.Code)
		}
	}
}

// The end-to-end version of C1 that this repo can actually run: build
// through the real BuildHandler, read the preseed it wrote, take the URL
// out of its own preseed/run directive, and fetch that URL through the
// media listener over the same directory. Nothing here is told what the
// address should be -- it is read back out of the artifact.
func TestBuildHandlerServesTheRunScriptAtTheAddressThePreseedNames(t *testing.T) {
	requireXorriso(t)

	baseISOPath := realBaseISO(t)
	baseBytes, err := os.ReadFile(baseISOPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := serveBytes(t, baseBytes)
	base := BaseSource{URL: srv.URL + "/base.iso", SHA256: sum(baseBytes)}

	dir := t.TempDir()
	reqBody, err := json.Marshal(goodSpec())
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	BuildHandler(dir, base, testMediaURL).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/build", bytes.NewReader(reqBody)))
	if rr.Code != http.StatusOK {
		t.Fatalf("build: code = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp buildResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	preseed, err := os.ReadFile(filepath.Join(dir, resp.Token+".cfg"))
	if err != nil {
		t.Fatal(err)
	}
	runURL := runURLFromPreseed(t, string(preseed))
	if !strings.HasPrefix(runURL, testMediaURL+"/") {
		t.Fatalf("preseed/run URL %q is not on the media listener the BMC reaches", runURL)
	}

	path := strings.TrimPrefix(runURL, testMediaURL)
	got := httptest.NewRecorder()
	MediaHandler(dir).ServeHTTP(got, httptest.NewRequest(http.MethodGet, path, nil))
	if got.Code != http.StatusOK {
		t.Fatalf("GET %s (the address the preseed itself names) = %d, want 200", path, got.Code)
	}
	if got.Body.String() != NetcfgRerunScript {
		t.Errorf("what is served at the preseed's own preseed/run address is not the run script:\n%s", got.Body.String())
	}
}

// runURLFromPreseed reads the value out of the rendered preseed's own
// preseed/run directive. Parsed rather than reconstructed on purpose: a
// test that rebuilds the URL from the token cannot tell the directive
// naming the right address from the directive being absent entirely.
func runURLFromPreseed(t *testing.T, preseed string) string {
	t.Helper()
	const prefix = "d-i preseed/run string "
	for _, line := range strings.Split(preseed, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("the rendered preseed carries no preseed/run directive at all:\n%s", preseed)
	return ""
}

// A built image's sidecars are removed with it. A .sh left behind is a live
// unauthenticated route pointing at nothing anyone owns.
func TestBuildHandlerDeleteRemovesThePreseedAndTheRunScriptWithTheImage(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{tok + ".iso", tok + ".cfg", tok + ".sh"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rr := httptest.NewRecorder()
	BuildHandler(dir, DefaultBase(), testMediaURL).ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/iso/"+tok+".iso", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204: %s", rr.Code, rr.Body.String())
	}
	for _, name := range []string{tok + ".iso", tok + ".cfg", tok + ".sh"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived the delete", name)
		}
	}
}

func TestLocalImageStoreRemoveTakesThePreseedAndTheRunScriptToo(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{tok + ".iso", tok + ".cfg", tok + ".sh"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := &LocalImageStore{Dir: dir, MediaURL: testMediaURL}
	if err := s.Remove(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{tok + ".iso", tok + ".cfg", tok + ".sh"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived Remove", name)
		}
	}
}
