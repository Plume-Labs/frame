package provision

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// imageName is the shape of a token this package ever produces: 32 hex
// characters from newToken, plus ".iso". It is matched before a caller-
// supplied name ever touches the filesystem, so a name that cannot be an
// image name is refused outright -- not cleaned, not joined-and-hoped-about.
// filepath.Join(dir, name) on an unchecked name is exactly how a media
// server becomes a way to read /etc/shadow.
var imageName = regexp.MustCompile(`^[a-f0-9]{32}\.iso$`)

// buildResponse is what the build API answers with. Only the token matters
// to a caller: HTTPImageStore derives the URL the BMC will use itself, from
// MediaURL, rather than trusting a URL the build API might hand back for its
// own (unreachable-by-the-BMC) address.
type buildResponse struct {
	Token string `json:"token"`
}

// newToken generates the per-image identifier: 16 random bytes, hex-encoded
// to the 32 lowercase hex characters imageName requires.
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating image token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// MediaHandler is the only listener a BMC reaches. It reads, and that is all
// it can do: the build API lives on a different port, on a Service nothing
// outside the cluster can reach.
//
// GET /iso/{name} is a single-segment wildcard: it never matches
// "/iso/sub/dir.iso", and any other method on the same path gets 405 from
// http.ServeMux itself, not from any check written here. A trailing-slash
// pattern is never used -- see the package-level note this lot has already
// paid a task to learn: in http.ServeMux a trailing-slash pattern is a
// subtree match that absorbs every deeper path, which is exactly how a
// predecessor lot's 404 tolerance went untested.
func MediaHandler(dir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /iso/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		// Matched before it touches the filesystem. A name that cannot be an
		// image name is refused outright, rather than cleaned and hoped
		// about.
		if !imageName.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, name))
	})
	return mux
}

// BuildHandler serves the build API. It must never be exposed outside the
// cluster: it accepts a Spec and writes a 700 MB file, and doing either of
// those on the LAN-facing listener is the mistake the two-listener design
// exists to make impossible.
//
// It decodes a Spec, calls RenderPreseed first -- so a bad spec is a 400
// before anything is fetched or built -- then fetches the base image and
// remasters it into dir/<token>.iso, where the token is 32 hex characters
// from crypto/rand. DELETE /iso/{name} removes a previously built image,
// guarded by the same imageName check as the read-only listener.
func BuildHandler(dir string, base BaseSource) http.Handler {
	mux := http.NewServeMux()
	baseDir := filepath.Join(dir, "base")

	mux.HandleFunc("POST /build", func(w http.ResponseWriter, r *http.Request) {
		var spec Spec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, fmt.Sprintf("decoding request body: %v", err), http.StatusBadRequest)
			return
		}

		// Rendered before anything is fetched or built: a spec this package
		// would refuse to turn into a preseed is refused here too, before a
		// single byte of a ~700 MB download happens.
		if _, err := RenderPreseed(spec); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		baseISO, err := FetchBase(r.Context(), baseDir, base)
		if err != nil {
			http.Error(w, fmt.Sprintf("fetching base image: %v", err), http.StatusInternalServerError)
			return
		}

		token, err := newToken()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			http.Error(w, fmt.Sprintf("preparing image directory: %v", err), http.StatusInternalServerError)
			return
		}

		out := filepath.Join(dir, token+".iso")
		if err := Remaster(r.Context(), baseISO, spec, out); err != nil {
			http.Error(w, fmt.Sprintf("building image: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildResponse{Token: token})
	})

	mux.HandleFunc("DELETE /iso/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !imageName.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			if os.IsNotExist(err) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}

// HTTPImageStore is the ImageStore the controller uses, over the build API.
//
// Build talks to BuildURL, the in-cluster address the manager reaches over
// the ClusterIP Service. The URL it returns is built from MediaURL instead
// -- the address the BMC, on the management network, will fetch from -- so
// the two listeners this design deliberately keeps separate are never
// confused with each other.
type HTTPImageStore struct {
	BuildURL string // the in-cluster build API
	MediaURL string // the base URL the BMC will fetch from
	Client   *http.Client
}

func (s *HTTPImageStore) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func (s *HTTPImageStore) Build(ctx context.Context, spec Spec) (url, token string, err error) {
	body, err := json.Marshal(spec)
	if err != nil {
		return "", "", fmt.Errorf("encoding spec: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(s.BuildURL, "/")+"/build", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client().Do(req)
	if err != nil {
		return "", "", fmt.Errorf("posting build request to %s: %w", s.BuildURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("build %s: HTTP %d: %s", s.BuildURL, resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var out buildResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", fmt.Errorf("decoding build response: %w", err)
	}
	// The response feeds directly into a URL handed to a BMC, unauthenticated,
	// on the management network. A build API that answered with a malformed
	// token would otherwise publish whatever that string is, verbatim.
	if !imageName.MatchString(out.Token + ".iso") {
		return "", "", fmt.Errorf("build %s: response token %q is not a valid image token", s.BuildURL, out.Token)
	}

	return strings.TrimSuffix(s.MediaURL, "/") + "/iso/" + out.Token + ".iso", out.Token, nil
}

func (s *HTTPImageStore) Remove(ctx context.Context, token string) error {
	if !imageName.MatchString(token + ".iso") {
		return fmt.Errorf("token %q is not a valid image token", token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimSuffix(s.BuildURL, "/")+"/iso/"+token+".iso", nil)
	if err != nil {
		return err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("posting remove request to %s: %w", s.BuildURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remove %s: HTTP %d: %s", token, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// LocalImageStore is the ImageStore `frame bootstrap` uses: it calls
// FetchBase and Remaster directly, because when no cluster exists there is
// nothing to host a build API.
type LocalImageStore struct {
	Dir      string
	Base     BaseSource
	MediaURL string // how the BMC reaches this machine, e.g. http://192.168.2.50:8081
}

func (s *LocalImageStore) Build(ctx context.Context, spec Spec) (url, token string, err error) {
	// Same order as BuildHandler, for the same reason: refuse before
	// fetching or building anything.
	if _, err := RenderPreseed(spec); err != nil {
		return "", "", err
	}

	baseISO, err := FetchBase(ctx, filepath.Join(s.Dir, "base"), s.Base)
	if err != nil {
		return "", "", fmt.Errorf("fetching base image: %w", err)
	}

	tok, err := newToken()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", "", fmt.Errorf("preparing image directory: %w", err)
	}

	out := filepath.Join(s.Dir, tok+".iso")
	if err := Remaster(ctx, baseISO, spec, out); err != nil {
		return "", "", fmt.Errorf("building image: %w", err)
	}

	return strings.TrimSuffix(s.MediaURL, "/") + "/iso/" + tok + ".iso", tok, nil
}

func (s *LocalImageStore) Remove(_ context.Context, token string) error {
	if !imageName.MatchString(token + ".iso") {
		return fmt.Errorf("token %q is not a valid image token", token)
	}
	if err := os.Remove(filepath.Join(s.Dir, token+".iso")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
