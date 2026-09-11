package provision

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tok = "0123456789abcdef0123456789abcdef"

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

func TestBuildHandlerRejectsASpecItWouldRefuseToRender(t *testing.T) {
	h := BuildHandler(t.TempDir(), DefaultBase())
	body := `{"uid":"","hostname":"g9"}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/build", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rr.Code)
	}
}
