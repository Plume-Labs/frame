package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func serveBytes(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// A netinst that does not match its published checksum is the supply-chain
// case this pin exists for. It must be deleted, not merely reported: a file
// left on disk is a file a later run treats as cached.
func TestFetchBaseDeletesAFileThatFailsItsChecksum(t *testing.T) {
	body := []byte("not the debian installer")
	srv := serveBytes(t, body)
	dir := t.TempDir()

	_, err := FetchBase(context.Background(), dir, BaseSource{URL: srv.URL + "/x.iso", SHA256: sum([]byte("something else"))})
	if err == nil {
		t.Fatal("want a checksum error, got nil")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("a file survived a failed checksum: %s", e.Name())
	}
}

func TestFetchBaseReusesAMatchingCachedFileWithoutDownloading(t *testing.T) {
	body := []byte("pretend netinst")
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	src := BaseSource{URL: srv.URL + "/x.iso", SHA256: sum(body)}

	for i := 0; i < 2; i++ {
		p, err := FetchBase(context.Background(), dir, src)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(p) != dir {
			t.Errorf("cached outside dir: %s", p)
		}
	}
	if hits != 1 {
		t.Errorf("downloads = %d, want 1", hits)
	}
}

// filepath.Base on a crafted URL like ".../foo/.." yields "..", and joining
// that under dir would write outside it. The request must never even be
// sent -- the handler fails the test if it's hit.
func TestFetchBaseRefusesAURLWhoseBaseNameEscapesTheDirectory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request sent for %s; the URL should have been refused before any network call", r.URL)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()

	_, err := FetchBase(context.Background(), dir, BaseSource{URL: srv.URL + "/x/..", SHA256: sum([]byte("irrelevant"))})
	if err == nil {
		t.Fatal("want an error for a URL whose base name is \"..\", got nil")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("a file was written for a refused URL: %s", e.Name())
	}
}

// The download is capped so an unbounded server (or a compromised mirror)
// can't run io.Copy to disk exhaustion before the checksum -- the whole
// point of the pin -- is ever checked. Shrinking maxBaseImageBytes for the
// test proves the cap actually truncates, without downloading gigabytes:
// a truncated body fails its checksum like any other corrupt download, and
// is deleted the same way.
func TestFetchBaseCapsTheDownloadSize(t *testing.T) {
	body := make([]byte, 4096)
	srv := serveBytes(t, body)
	dir := t.TempDir()

	orig := maxBaseImageBytes
	maxBaseImageBytes = 1024
	t.Cleanup(func() { maxBaseImageBytes = orig })

	_, err := FetchBase(context.Background(), dir, BaseSource{URL: srv.URL + "/x.iso", SHA256: sum(body)})
	if err == nil {
		t.Fatal("want a checksum error for a download truncated by the cap, got nil")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("a truncated file survived: %s", e.Name())
	}
}

// The value in Global Constraints, verified against the published SHA256SUMS
// on 2026-09-11. If this ever changes, the change is deliberate.
func TestDefaultBaseIsThePinnedDebianImage(t *testing.T) {
	b := DefaultBase()
	if b.SHA256 != "65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7" {
		t.Errorf("SHA256 = %s", b.SHA256)
	}
	if got := "debian-13.6.0-amd64-netinst.iso"; filepath.Base(b.URL) != got {
		t.Errorf("URL base = %s, want %s", filepath.Base(b.URL), got)
	}
}
