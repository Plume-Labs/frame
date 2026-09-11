package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

type BaseSource struct {
	URL    string
	SHA256 string
}

// DefaultBase is the image this lot is built against. The checksum is the
// integrity control, verified against Debian's published SHA256SUMS on
// 2026-09-11; changing either field is a deliberate act.
func DefaultBase() BaseSource {
	return BaseSource{
		URL:    "https://cdimage.debian.org/cdimage/release/13.6.0/amd64/iso-cd/debian-13.6.0-amd64-netinst.iso",
		SHA256: "65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7",
	}
}

// FetchBase returns the path to a verified copy of src inside dir.
//
// A file that fails its checksum is removed rather than left in place: on the
// next run a leftover is indistinguishable from a cache hit, which turns one
// bad download into a permanently poisoned cache.
func FetchBase(ctx context.Context, dir string, src BaseSource) (string, error) {
	path := filepath.Join(dir, filepath.Base(src.URL))

	if got, err := fileSHA256(path); err == nil && got == src.SHA256 {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: HTTP %d", src.URL, resp.StatusCode)
	}

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("fetch %s: %w", src.URL, firstErr(copyErr, closeErr))
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != src.SHA256 {
		_ = os.Remove(path)
		return "", fmt.Errorf("fetch %s: sha256 %s, want %s", src.URL, got, src.SHA256)
	}
	return path, nil
}

// firstErr, not cmp: cmp is a standard library package name, and shadowing it
// in a file that may later want cmp.Or is a trap for whoever writes that line.
func firstErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
